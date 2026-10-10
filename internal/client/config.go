// Package client builds aws-sdk-go-v2 S3 clients and classifies their errors.
package client

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"time"
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

// DefaultSampleRate is the default ratio of traced operations.
const DefaultSampleRate = 0.01

// Tracing is the tracing configuration.
type Tracing struct {
	// SampleRate is the ratio of operations traced with spans, in [0, 1].
	// Failed operations are always traced.
	SampleRate float64
	// Propagate adds the W3C traceparent header to the requests of traced
	// operations.
	Propagate bool
}

// Config is the validated client configuration.
type Config struct {
	Endpoint     string
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	PathStyle    bool
	Checksum     string
	// ChecksumAlgorithm is empty when not specified (the SDK default, CRC32).
	ChecksumAlgorithm string
	PayloadSigning    string
	Timeout           time.Duration
	MaxAttempts       int
	Tags              []string
	Tracing           Tracing
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

var configKeys = []string{
	"endpoint", "region", "accessKey", "secretKey", "sessionToken", "pathStyle",
	"checksum", "checksumAlgorithm", "payloadSigning", "timeout", "maxAttempts", "tags",
	"tracing",
}

// ParseConfig validates a configuration object exported from JavaScript and
// fills in the defaults. lookupEnv is used for the default endpoint, region
// and credentials.
func ParseConfig(m map[string]any, lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := Config{
		PathStyle:      true,
		Checksum:       ChecksumWhenSupported,
		PayloadSigning: PayloadSigningAuto,
		Timeout:        60 * time.Second,
		MaxAttempts:    1,
		Tracing:        Tracing{SampleRate: DefaultSampleRate},
	}
	var unknown []string
	for k := range m {
		if !slices.Contains(configKeys, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return cfg, fmt.Errorf("unknown config keys: %v", unknown)
	}

	var errs []error
	str := func(key string, dst *string) {
		if v, ok := m[key]; ok && v != nil {
			s, ok := v.(string)
			if !ok {
				errs = append(errs, fmt.Errorf("%s must be a string", key))
				return
			}
			*dst = s
		}
	}
	oneOf := func(key string, dst *string, values []string) {
		str(key, dst)
		if _, ok := m[key]; ok && !slices.Contains(values, *dst) {
			errs = append(errs, fmt.Errorf("%s must be one of %v: %q", key, values, *dst))
		}
	}

	str("endpoint", &cfg.Endpoint)
	str("region", &cfg.Region)
	if cfg.Region == "" {
		cfg.Region = envOr(lookupEnv, DefaultRegion, "AWS_REGION", "AWS_DEFAULT_REGION")
	}
	str("accessKey", &cfg.AccessKey)
	str("secretKey", &cfg.SecretKey)
	str("sessionToken", &cfg.SessionToken)
	oneOf("checksum", &cfg.Checksum, []string{ChecksumWhenSupported, ChecksumWhenRequired})
	oneOf("checksumAlgorithm", &cfg.ChecksumAlgorithm, ChecksumAlgorithms)
	oneOf("payloadSigning", &cfg.PayloadSigning,
		[]string{PayloadSigningAuto, PayloadSigningUnsigned, PayloadSigningSigned})

	if v, ok := m["pathStyle"]; ok && v != nil {
		b, ok := v.(bool)
		if !ok {
			errs = append(errs, errors.New("pathStyle must be a boolean"))
		}
		cfg.PathStyle = b
	}
	if v, ok := m["timeout"]; ok && v != nil {
		d, err := parseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("timeout: %w", err))
		}
		cfg.Timeout = d
	}
	if v, ok := m["maxAttempts"]; ok && v != nil {
		n, err := toInt(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("maxAttempts must be a positive integer: %v", v))
		}
		cfg.MaxAttempts = n
	}
	if v, ok := m["tracing"]; ok && v != nil {
		t, err := parseTracing(v)
		if err != nil {
			errs = append(errs, err)
		}
		cfg.Tracing = t
	}
	if v, ok := m["tags"]; ok && v != nil {
		tags, err := parseTags(v)
		if err != nil {
			errs = append(errs, err)
		}
		cfg.Tags = tags
	}

	if cfg.Endpoint == "" {
		// The same precedence as the AWS SDKs: the service-specific variable first.
		cfg.Endpoint = envOr(lookupEnv, "", "AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL")
	}
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

// envOr returns the first non-empty value of the environment variables, or def.
func envOr(lookupEnv func(string) (string, bool), def string, names ...string) string {
	for _, name := range names {
		if v, ok := lookupEnv(name); ok && v != "" {
			return v
		}
	}
	return def
}

// parseDuration accepts a duration string such as "30s" or a number of
// milliseconds, following the k6 convention.
func parseDuration(v any) (time.Duration, error) {
	var d time.Duration
	switch t := v.(type) {
	case string:
		var err error
		if d, err = time.ParseDuration(t); err != nil {
			return 0, err
		}
	case int64, int, float64:
		f, _ := toFloat(t)
		d = time.Duration(f * float64(time.Millisecond))
	default:
		return 0, fmt.Errorf("must be a duration string or milliseconds: %v", v)
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive: %v", v)
	}
	return d, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func toInt(v any) (int, error) {
	f, ok := toFloat(v)
	if !ok || f != math.Trunc(f) || f > math.MaxInt32 || f < math.MinInt32 {
		return 0, fmt.Errorf("not an integer: %v", v)
	}
	return int(f), nil
}

func parseTracing(v any) (Tracing, error) {
	t := Tracing{SampleRate: DefaultSampleRate}
	m, ok := v.(map[string]any)
	if !ok {
		return t, errors.New("tracing must be an object")
	}
	for k, v := range m {
		switch k {
		case "sampleRate":
			f, ok := toFloat(v)
			if !ok || f < 0 || f > 1 || math.IsNaN(f) {
				return t, fmt.Errorf("tracing.sampleRate must be a number in [0, 1]: %v", v)
			}
			t.SampleRate = f
		case "propagate":
			b, ok := v.(bool)
			if !ok {
				return t, fmt.Errorf("tracing.propagate must be a boolean: %v", v)
			}
			t.Propagate = b
		default:
			return t, fmt.Errorf("unknown tracing key: %q", k)
		}
	}
	return t, nil
}

func parseTags(v any) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("tags must be an array of strings")
	}
	tags := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok || (s != TagBucket && s != TagSizeClass) {
			return nil, fmt.Errorf("tags must contain only %q or %q: %v", TagBucket, TagSizeClass, e)
		}
		if !slices.Contains(tags, s) {
			tags = append(tags, s)
		}
	}
	return tags, nil
}
