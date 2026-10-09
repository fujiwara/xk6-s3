package client

import (
	"context"
	"crypto/sha1" //nolint:gosec // SHA1 is an S3 checksum algorithm
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// computeInputChecksumID is the ID of the SDK middleware that computes the
// request checksum and decides whether to use a trailing checksum.
const computeInputChecksumID = "AWSChecksum:ComputeInputPayloadChecksum"

const unsignedPayload = "UNSIGNED-PAYLOAD"

// crc64NVME is the reversed CRC-64/NVME polynomial, as used by the SDK.
const crc64NVME = 0x9a6c_9329_ac4b_c9b5

// New returns an S3 client for cfg that sends requests with httpClient.
func New(cfg Config, httpClient aws.HTTPClient) *s3.Client {
	awsCfg := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken),
		HTTPClient:  httpClient,
	}
	if cfg.Checksum == ChecksumWhenRequired {
		awsCfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		awsCfg.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	} else {
		awsCfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenSupported
		awsCfg.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenSupported
	}
	if cfg.MaxAttempts <= 1 {
		awsCfg.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
	} else {
		awsCfg.RetryMaxAttempts = cfg.MaxAttempts
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = cfg.PathStyle
	})
}

// ChecksumAlgorithmType returns the value for the ChecksumAlgorithm field of
// PutObject and UploadPart inputs. It is empty unless an algorithm is
// explicitly configured, so that the SDK default request is reproduced.
func (c Config) ChecksumAlgorithmType() types.ChecksumAlgorithm {
	return types.ChecksumAlgorithm(c.ChecksumAlgorithm)
}

// PayloadOptions returns per-operation options for operations with a request
// body (PutObject and UploadPart) that apply the payloadSigning setting.
func (c Config) PayloadOptions() []func(*s3.Options) {
	if c.PayloadSigning == PayloadSigningAuto {
		return nil
	}
	m := &payloadSigning{mode: c.PayloadSigning}
	if c.PayloadSigning == PayloadSigningSigned && c.Checksum == ChecksumWhenSupported {
		m.checksumAlgorithm = c.ChecksumAlgorithm
		if m.checksumAlgorithm == "" {
			m.checksumAlgorithm = ChecksumAlgorithms[0]
		}
	}
	return []func(*s3.Options){
		func(o *s3.Options) {
			o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
				if _, ok := stack.Finalize.Get(computeInputChecksumID); ok {
					return stack.Finalize.Insert(m, computeInputChecksumID, middleware.Before)
				}
				return stack.Finalize.Add(m, middleware.Before)
			})
		},
	}
}

// payloadSigning overrides how the SDK signs the request payload.
//
// The SDK signs PutObject and UploadPart with UNSIGNED-PAYLOAD over HTTPS
// and with the SHA256 of the body over HTTP. It runs before the SDK checksum
// middleware and stores the payload hash in the context, which the SDK
// signing middlewares respect.
//
// For "signed", it also computes the checksum header itself when checksums
// are enabled, so that the SDK sends the checksum as a header instead of an
// aws-chunked trailer, which cannot be combined with a signed payload hash.
type payloadSigning struct {
	mode              string
	checksumAlgorithm string
}

func (*payloadSigning) ID() string { return "XK6S3:PayloadSigning" }

func (m *payloadSigning) HandleFinalize(
	ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler,
) (middleware.FinalizeOutput, middleware.Metadata, error) {
	if m.mode == PayloadSigningUnsigned {
		return next.HandleFinalize(v4.SetPayloadHash(ctx, unsignedPayload), in)
	}

	req, ok := in.Request.(*smithyhttp.Request)
	if !ok {
		return middleware.FinalizeOutput{}, middleware.Metadata{}, fmt.Errorf("unknown request type %T", in.Request)
	}
	payloadHash := sha256.New()
	writers := []io.Writer{payloadHash}
	var checksumHash hash.Hash
	if m.checksumAlgorithm != "" && !hasChecksumHeader(req) {
		checksumHash = newChecksumHash(m.checksumAlgorithm)
		writers = append(writers, checksumHash)
	}
	if stream := req.GetStream(); stream != nil {
		if !req.IsStreamSeekable() {
			return middleware.FinalizeOutput{}, middleware.Metadata{}, fmt.Errorf("signed payload requires a seekable body")
		}
		if _, err := io.Copy(io.MultiWriter(writers...), stream); err != nil {
			return middleware.FinalizeOutput{}, middleware.Metadata{}, fmt.Errorf("failed to hash payload: %w", err)
		}
		if err := req.RewindStream(); err != nil {
			return middleware.FinalizeOutput{}, middleware.Metadata{}, fmt.Errorf("failed to rewind payload: %w", err)
		}
	}
	if checksumHash != nil {
		req.Header.Set("X-Amz-Checksum-"+m.checksumAlgorithm,
			base64.StdEncoding.EncodeToString(checksumHash.Sum(nil)))
	}
	ctx = v4.SetPayloadHash(ctx, hex.EncodeToString(payloadHash.Sum(nil)))
	return next.HandleFinalize(ctx, in)
}

func hasChecksumHeader(req *smithyhttp.Request) bool {
	for h := range req.Header {
		if strings.HasPrefix(strings.ToLower(h), "x-amz-checksum-") {
			return true
		}
	}
	return false
}

func newChecksumHash(algorithm string) hash.Hash {
	switch algorithm {
	case "CRC32C":
		return crc32.New(crc32.MakeTable(crc32.Castagnoli))
	case "CRC64NVME":
		return crc64.New(crc64.MakeTable(crc64NVME))
	case "SHA1":
		return sha1.New() //nolint:gosec
	case "SHA256":
		return sha256.New()
	default:
		return crc32.NewIEEE()
	}
}
