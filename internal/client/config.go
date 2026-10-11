// Package client builds aws-sdk-go-v2 S3 clients and classifies their errors.
package client

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/fujiwara/xk6-s3/internal/jsobj"
)

// Checksum modes. They are applied to both RequestChecksumCalculation and
// ResponseChecksumValidation.
const (
	ChecksumWhenSupported = "when_supported"
	ChecksumWhenRequired  = "when_required"
)

// Payload signing modes.
const (
	PayloadSigningAuto     = "auto"
	PayloadSigningUnsigned = "unsigned"
	PayloadSigningSigned   = "signed"
)

// DefaultRegion is the region used when neither the config nor the
// environment specifies one.
const DefaultRegion = "us-east-1"

// Optional tags that can be enabled by the tags setting.
const (
	TagBucket    = "bucket"
	TagSizeClass = "size_class"
)

// ChecksumAlgorithms lists the supported checksum algorithms.
// The first one is the SDK default.
var ChecksumAlgorithms = []string{"CRC32", "CRC32C", "CRC64NVME", "SHA1", "SHA256"}

// Tracing is the tracing configuration. See the Tracing section of README.
type Tracing struct {
	// SampleRate is the ratio of operations traced with spans. Failed
	// operations are always traced.
	SampleRate float64 `js:"sampleRate" default:"0.01" min:"0" max:"1"`
	// Propagate adds the W3C `traceparent` header to the requests of traced
	// operations.
	Propagate bool `js:"propagate"`
}

// Config is the configuration of a client. Unknown keys are rejected. The
// environment variables are read from the environment of the k6 process,
// not from `-e` / `__ENV`.
type Config struct {
	// Endpoint is the endpoint URL (`http://` or `https://`). Required unless
	// set by the environment variables.
	Endpoint string `js:"endpoint" env:"AWS_ENDPOINT_URL_S3,AWS_ENDPOINT_URL"`
	// Region is the region for signing. It is also sent as the
	// LocationConstraint of createBucket unless us-east-1.
	Region string `js:"region" env:"AWS_REGION,AWS_DEFAULT_REGION" default:"us-east-1"`
	// AccessKey is the access key ID. When neither accessKey nor secretKey
	// is set, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN
	// are used.
	AccessKey string `js:"accessKey"`
	// SecretKey is the secret access key.
	SecretKey string `js:"secretKey"`
	// SessionToken is the session token.
	SessionToken string `js:"sessionToken"`
	// PathStyle uses path-style addressing.
	PathStyle bool `js:"pathStyle" default:"true"`
	// Checksum is applied to both request checksum calculation and response
	// checksum validation. See the Request format section of README.
	Checksum string `js:"checksum" enum:"when_supported,when_required" default:"when_supported"`
	// ChecksumAlgorithm is the request checksum algorithm. The SDK default
	// (CRC32) if not set. Cannot be used with checksum when_required.
	ChecksumAlgorithm string `js:"checksumAlgorithm" enum:"CRC32,CRC32C,CRC64NVME,SHA1,SHA256"`
	// PayloadSigning selects how the payload is signed. See the Request
	// format section of README.
	PayloadSigning string `js:"payloadSigning" enum:"auto,unsigned,signed" default:"auto"`
	// Timeout is the timeout of an operation.
	Timeout time.Duration `js:"timeout" default:"60s"`
	// MaxAttempts is the maximum number of attempts including SDK retries.
	// Retries are disabled by default.
	MaxAttempts int `js:"maxAttempts" default:"1" min:"1"`
	// Tags are the optional metric tags to enable.
	Tags []string `js:"tags" enum:"bucket,size_class"`
	// Tracing is the tracing of operations.
	Tracing Tracing `js:"tracing"`
}

// HTTPS reports whether the endpoint uses TLS.
func (c Config) HTTPS() bool {
	u, err := url.Parse(c.Endpoint)
	return err == nil && u.Scheme == "https"
}

// HasTag reports whether the optional tag is enabled.
func (c Config) HasTag(name string) bool {
	return slices.Contains(c.Tags, name)
}

// ParseConfig validates a configuration object exported from JavaScript and
// fills in the defaults. lookupEnv is used for the default endpoint, region
// and credentials.
func ParseConfig(m map[string]any, lookupEnv func(string) (string, bool)) (Config, error) {
	var cfg Config
	errs := []error{jsobj.Decode(m, &cfg, lookupEnv)}
	var tags []string
	for _, tag := range cfg.Tags {
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	cfg.Tags = tags

	if cfg.Endpoint == "" {
		errs = append(errs, errors.New("endpoint is required (or set AWS_ENDPOINT_URL_S3 or AWS_ENDPOINT_URL)"))
	} else if u, err := url.Parse(cfg.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("endpoint must be an http or https URL: %q", cfg.Endpoint))
	}
	if cfg.ChecksumAlgorithm != "" && cfg.Checksum == ChecksumWhenRequired {
		errs = append(errs, errors.New("checksumAlgorithm cannot be used with checksum: when_required"))
	}

	if cfg.AccessKey == "" && cfg.SecretKey == "" {
		cfg.AccessKey, _ = lookupEnv("AWS_ACCESS_KEY_ID")
		cfg.SecretKey, _ = lookupEnv("AWS_SECRET_ACCESS_KEY")
		if cfg.SessionToken == "" {
			cfg.SessionToken, _ = lookupEnv("AWS_SESSION_TOKEN")
		}
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		errs = append(errs, errors.New("accessKey and secretKey are required (or set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY)"))
	}

	return cfg, errors.Join(errs...)
}
