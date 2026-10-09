package s3

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/fujiwara/xk6-s3/internal/client"
	"github.com/fujiwara/xk6-s3/internal/data"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"github.com/grafana/sobek"
)

// Defaults of putObjectMultipart, following the aws-sdk-go-v2 upload manager.
const (
	defaultPartSize            = 5 << 20
	defaultMultipartConcurrent = 5
	maxParts                   = 10000
)

// PutObjectMultipart uploads an object with a multipart upload. Options:
//
//   - partSize: the part size (default: 5MiB)
//   - concurrency: the number of parts uploaded at once (default: 5)
//
// Like the aws-sdk-go-v2 upload manager, the checksum algorithm (CRC32
// unless configured) is set on CreateMultipartUpload and UploadPart when
// checksum is when_supported, and the part checksums are sent with
// CompleteMultipartUpload. If any request fails, the upload is aborted.
//
// The whole upload is measured as put_multipart and each part as
// upload_part. The timeout applies to each request.
func (c *Client) PutObjectMultipart(bucket, key string, size, options sobek.Value) *Result {
	rt := c.vu.Runtime()
	requireName(rt, "bucket", bucket)
	requireName(rt, "key", key)
	opts := c.options(options, "partSize", "concurrency")
	partSize := c.sizeOption(opts, "partSize", defaultPartSize)
	if partSize <= 0 {
		c.throw(errors.New("partSize must be positive"))
	}
	concurrency := c.intOption(opts, "concurrency", defaultMultipartConcurrent)
	if concurrency <= 0 {
		c.throw(errors.New("concurrency must be positive"))
	}
	n := c.sizer(size).Next(c.rng)
	parts := max((n+partSize-1)/partSize, 1)
	if parts > maxParts {
		c.throw(fmt.Errorf("too many parts: %d (size %d, partSize %d, max %d parts)", parts, n, partSize, maxParts))
	}
	body := c.newBody(n)
	env := c.env()
	sdk := c.parallelClient(env, int(min(concurrency, parts)))
	u := &multipartUpload{
		c: c, env: env, sdk: sdk, bucket: bucket, key: key, body: body,
		size: n, partSize: partSize, parts: int(parts), concurrency: int(concurrency),
		algorithm: c.multipartChecksumAlgorithm(),
	}
	return c.measure(env, op{name: metrics.OpPutMultipart, bucket: bucket, key: key, size: n, noTimeout: true}, u.run)
}

// multipartChecksumAlgorithm returns the checksum algorithm for multipart
// uploads, as the aws-sdk-go-v2 upload manager chooses it.
func (c *Client) multipartChecksumAlgorithm() types.ChecksumAlgorithm {
	if c.cfg.Checksum == client.ChecksumWhenRequired {
		return ""
	}
	if c.cfg.ChecksumAlgorithm != "" {
		return types.ChecksumAlgorithm(c.cfg.ChecksumAlgorithm)
	}
	return types.ChecksumAlgorithmCrc32
}

type multipartUpload struct {
	c           *Client
	env         vuEnv
	sdk         *awss3.Client
	bucket, key string
	body        *data.Reader
	size        int64
	partSize    int64
	parts       int
	concurrency int
	algorithm   types.ChecksumAlgorithm
}

func (u *multipartUpload) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, u.c.cfg.Timeout)
}

func (u *multipartUpload) run(ctx context.Context) (opOutput, error) {
	cctx, cancel := u.withTimeout(ctx)
	created, err := u.sdk.CreateMultipartUpload(cctx, &awss3.CreateMultipartUploadInput{
		Bucket:            aws.String(u.bucket),
		Key:               aws.String(u.key),
		ChecksumAlgorithm: u.algorithm,
	})
	cancel()
	if err != nil {
		return noBody, err
	}
	uploadID := created.UploadId

	completed, failed := u.uploadParts(ctx, uploadID)
	if failed != nil {
		u.abort(uploadID)
		res := &Result{}
		res.setError(failed)
		return opOutput{bytes: -1, size: -1, result: res}, errors.New(failed.Error)
	}

	cctx, cancel = u.withTimeout(ctx)
	defer cancel()
	out, err := u.sdk.CompleteMultipartUpload(cctx, &awss3.CompleteMultipartUploadInput{
		Bucket:          aws.String(u.bucket),
		Key:             aws.String(u.key),
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		u.abort(uploadID)
		return noBody, err
	}
	return opOutput{metadata: out.ResultMetadata, bytes: u.size, size: -1}, nil
}

// uploadParts uploads the parts concurrently. It stops at the first failure
// and returns its result.
func (u *multipartUpload) uploadParts(ctx context.Context, uploadID *string) ([]types.CompletedPart, *Result) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Parts canceled because another part failed are reported as canceled
	// and produce no samples.
	env := u.env
	env.ctx = ctx

	var (
		mu        sync.Mutex
		completed = make([]types.CompletedPart, 0, u.parts)
		failed    *Result
		wg        sync.WaitGroup
	)
	jobs := make(chan int)
	for range min(u.concurrency, u.parts) {
		wg.Go(func() {
			for i := range jobs {
				part, res := u.uploadPart(env, uploadID, i)
				mu.Lock()
				if res.OK {
					completed = append(completed, part)
				} else if failed == nil {
					failed = res
					cancel()
				}
				mu.Unlock()
			}
		})
	}
	for i := range u.parts {
		select {
		case jobs <- i:
			continue
		case <-ctx.Done():
		}
		break
	}
	close(jobs)
	wg.Wait()

	if failed == nil && len(completed) != u.parts {
		// The VU context was canceled before all parts were dispatched.
		failed = &Result{ErrorKind: client.KindCanceled, Error: context.Canceled.Error()}
	}
	sort.Slice(completed, func(i, j int) bool {
		return aws.ToInt32(completed[i].PartNumber) < aws.ToInt32(completed[j].PartNumber)
	})
	return completed, failed
}

func (u *multipartUpload) uploadPart(env vuEnv, uploadID *string, i int) (types.CompletedPart, *Result) {
	offset := int64(i) * u.partSize
	length := min(u.partSize, u.size-offset)
	body, err := data.NewReader(data.SharedBuffer(), u.body.Offset()+offset, length)
	if err != nil {
		return types.CompletedPart{}, &Result{ErrorKind: client.KindOther, Error: err.Error()}
	}
	partNumber := aws.Int32(int32(i + 1))
	var part types.CompletedPart
	res := u.c.measure(env, op{name: metrics.OpUploadPart, bucket: u.bucket, key: u.key, size: length}, func(ctx context.Context) (opOutput, error) {
		out, err := u.sdk.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket:            aws.String(u.bucket),
			Key:               aws.String(u.key),
			UploadId:          uploadID,
			PartNumber:        partNumber,
			Body:              body,
			ContentLength:     aws.Int64(length),
			ChecksumAlgorithm: u.algorithm,
		}, u.c.cfg.PayloadOptions()...)
		if err != nil {
			return noBody, err
		}
		part = types.CompletedPart{
			PartNumber:        partNumber,
			ETag:              out.ETag,
			ChecksumCRC32:     out.ChecksumCRC32,
			ChecksumCRC32C:    out.ChecksumCRC32C,
			ChecksumCRC64NVME: out.ChecksumCRC64NVME,
			ChecksumSHA1:      out.ChecksumSHA1,
			ChecksumSHA256:    out.ChecksumSHA256,
		}
		return opOutput{metadata: out.ResultMetadata, bytes: length, size: -1}, nil
	})
	return part, res
}

// abort aborts the upload. It runs even after the VU context is canceled,
// so that incomplete uploads are not left behind at the end of the test.
func (u *multipartUpload) abort(uploadID *string) {
	ctx, cancel := u.withTimeout(context.WithoutCancel(u.env.ctx))
	defer cancel()
	if _, err := u.sdk.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
		Bucket:   aws.String(u.bucket),
		Key:      aws.String(u.key),
		UploadId: uploadID,
	}); err != nil {
		u.env.state.Logger.WithField("bucket", u.bucket).WithField("key", u.key).
			WithField("uploadId", aws.ToString(uploadID)).
			Warnf("failed to abort the multipart upload: %s", err)
	}
}
