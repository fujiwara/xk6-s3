package client

import (
	"maps"
	"strings"
	"testing"
	"time"
)

func noEnv(string) (string, bool) { return "", false }

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{
		"endpoint":  "http://localhost:7070",
		"accessKey": "a",
		"secretKey": "s",
	}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Endpoint:       "http://localhost:7070",
		Region:         "us-east-1",
		AccessKey:      "a",
		SecretKey:      "s",
		PathStyle:      true,
		Checksum:       ChecksumWhenSupported,
		PayloadSigning: PayloadSigningAuto,
		Timeout:        60 * time.Second,
		MaxAttempts:    1,
	}
	if cfg.Endpoint != want.Endpoint || cfg.Region != want.Region || cfg.PathStyle != want.PathStyle ||
		cfg.Checksum != want.Checksum || cfg.ChecksumAlgorithm != "" || cfg.PayloadSigning != want.PayloadSigning ||
		cfg.Timeout != want.Timeout || cfg.MaxAttempts != want.MaxAttempts || len(cfg.Tags) != 0 {
		t.Errorf("ParseConfig() = %+v, want %+v", cfg, want)
	}
	if cfg.HTTPS() {
		t.Error("HTTPS() = true for http endpoint")
	}
}

func TestParseConfigValues(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{
		"endpoint":          "https://s3.example.com",
		"region":            "ap-northeast-1",
		"accessKey":         "a",
		"secretKey":         "s",
		"sessionToken":      "t",
		"pathStyle":         false,
		"checksumAlgorithm": "CRC64NVME",
		"payloadSigning":    "signed",
		"timeout":           "1m30s",
		"maxAttempts":       int64(3),
		"tags":              []any{"size_class", "bucket", "bucket"},
	}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HTTPS() || cfg.Region != "ap-northeast-1" || cfg.PathStyle || cfg.SessionToken != "t" ||
		cfg.ChecksumAlgorithm != "CRC64NVME" || cfg.PayloadSigning != "signed" ||
		cfg.Timeout != 90*time.Second || cfg.MaxAttempts != 3 {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if len(cfg.Tags) != 2 || !cfg.HasTag(TagBucket) || !cfg.HasTag(TagSizeClass) {
		t.Errorf("Tags = %v", cfg.Tags)
	}
}

func TestParseConfigTimeoutMilliseconds(t *testing.T) {
	cfg, err := ParseConfig(map[string]any{
		"endpoint": "http://localhost", "accessKey": "a", "secretKey": "s", "timeout": int64(1500),
	}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != 1500*time.Millisecond {
		t.Errorf("Timeout = %v", cfg.Timeout)
	}
}

func TestParseConfigCredentialsFromEnv(t *testing.T) {
	env := map[string]string{
		"AWS_ACCESS_KEY_ID":     "envkey",
		"AWS_SECRET_ACCESS_KEY": "envsecret",
		"AWS_SESSION_TOKEN":     "envtoken",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	cfg, err := ParseConfig(map[string]any{"endpoint": "http://localhost"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessKey != "envkey" || cfg.SecretKey != "envsecret" || cfg.SessionToken != "envtoken" {
		t.Errorf("credentials = %q %q %q", cfg.AccessKey, cfg.SecretKey, cfg.SessionToken)
	}

	cfg, err = ParseConfig(map[string]any{"endpoint": "http://localhost", "accessKey": "a", "secretKey": "s"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessKey != "a" || cfg.SecretKey != "s" || cfg.SessionToken != "" {
		t.Errorf("explicit credentials were overridden: %+v", cfg)
	}
}

func TestParseConfigEndpointFromEnv(t *testing.T) {
	base := map[string]any{"accessKey": "a", "secretKey": "s"}
	tests := []struct {
		env  map[string]string
		cfg  map[string]any
		want string
	}{
		{map[string]string{"AWS_ENDPOINT_URL_S3": "http://s3:7070"}, nil, "http://s3:7070"},
		{map[string]string{"AWS_ENDPOINT_URL": "http://all:7070"}, nil, "http://all:7070"},
		{map[string]string{"AWS_ENDPOINT_URL_S3": "http://s3:7070", "AWS_ENDPOINT_URL": "http://all:7070"}, nil, "http://s3:7070"},
		{map[string]string{"AWS_ENDPOINT_URL_S3": "", "AWS_ENDPOINT_URL": "http://all:7070"}, nil, "http://all:7070"},
		{map[string]string{"AWS_ENDPOINT_URL_S3": "http://s3:7070"}, map[string]any{"endpoint": "http://explicit:7070"}, "http://explicit:7070"},
	}
	for _, tt := range tests {
		m := maps.Clone(base)
		maps.Copy(m, tt.cfg)
		cfg, err := ParseConfig(m, func(k string) (string, bool) { v, ok := tt.env[k]; return v, ok })
		if err != nil {
			t.Errorf("env %v: %v", tt.env, err)
			continue
		}
		if cfg.Endpoint != tt.want {
			t.Errorf("env %v, config %v: Endpoint = %q, want %q", tt.env, tt.cfg, cfg.Endpoint, tt.want)
		}
	}

	// The endpoint from the environment is validated too.
	_, err := ParseConfig(maps.Clone(base), func(k string) (string, bool) {
		if k == "AWS_ENDPOINT_URL_S3" {
			return "localhost:7070", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "endpoint must be") {
		t.Errorf("error = %v", err)
	}
}

func TestParseConfigErrors(t *testing.T) {
	base := func(kv ...any) map[string]any {
		m := map[string]any{"endpoint": "http://localhost", "accessKey": "a", "secretKey": "s"}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(m, kv[i].(string))
			} else {
				m[kv[i].(string)] = kv[i+1]
			}
		}
		return m
	}
	tests := []struct {
		m       map[string]any
		wantErr string
	}{
		{base("endpoint", nil), "endpoint is required"},
		{base("endpoint", "localhost:9000"), "endpoint must be"},
		{base("endpoint", "ftp://localhost"), "endpoint must be"},
		{base("endpoint", int64(1)), "endpoint must be a string"},
		{base("accessKey", nil, "secretKey", nil), "accessKey and secretKey are required"},
		{base("secretKey", nil), "accessKey and secretKey are required"},
		{base("checksum", "always"), "checksum must be one of"},
		{base("checksumAlgorithm", "MD5"), "checksumAlgorithm must be one of"},
		{base("checksum", "when_required", "checksumAlgorithm", "CRC32"), "cannot be used with"},
		{base("payloadSigning", "none"), "payloadSigning must be one of"},
		{base("pathStyle", "yes"), "pathStyle must be a boolean"},
		{base("timeout", "soon"), "timeout"},
		{base("timeout", int64(0)), "timeout"},
		{base("maxAttempts", int64(0)), "maxAttempts"},
		{base("maxAttempts", 1.5), "maxAttempts"},
		{base("tags", "bucket"), "tags must be an array"},
		{base("tags", []any{"key"}), "tags must contain only"},
		{base("endpointUrl", "http://localhost"), "unknown config keys: [endpointUrl]"},
	}
	for _, tt := range tests {
		_, err := ParseConfig(tt.m, noEnv)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("ParseConfig(%v) error = %v, want %q", tt.m, err, tt.wantErr)
		}
	}
}
