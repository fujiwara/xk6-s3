package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestClassifyNil(t *testing.T) {
	if got := Classify(nil); got != (ErrorInfo{}) {
		t.Errorf("Classify(nil) = %+v", got)
	}
}

func errorServer(t *testing.T, status int, body string, hits *atomic.Int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("x-amz-request-id", "req-123")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func getObject(ctx context.Context, cfg Config, hc *http.Client) error {
	out, err := New(cfg, hc, nil).GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("key"),
	})
	if err == nil {
		out.Body.Close()
	}
	return err
}

func TestClassifyResponseErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   ErrorInfo
	}{
		{
			"NoSuchKey", 404,
			`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>no</Message><RequestId>req-123</RequestId></Error>`,
			ErrorInfo{Kind: KindS3, Code: "NoSuchKey", Status: 404, RequestID: "req-123"},
		},
		{
			"SlowDown", 503,
			`<Error><Code>SlowDown</Code><Message>slow</Message></Error>`,
			ErrorInfo{Kind: KindS3, Code: "SlowDown", Status: 503, RequestID: "req-123"},
		},
		{
			"unparsable body", 502, "<html>bad gateway</html>",
			ErrorInfo{Kind: KindHTTP, Status: 502, RequestID: "req-123"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := errorServer(t, tt.status, tt.body, nil)
			err := getObject(context.Background(), testConfig(url, map[string]any{}), http.DefaultClient)
			if got := Classify(err); got != tt.want {
				t.Errorf("Classify() = %+v, want %+v (err: %v)", got, tt.want, err)
			}
		})
	}
}

func TestClassifyHeadNotFound(t *testing.T) {
	url := errorServer(t, 404, "", nil)
	_, err := New(testConfig(url, map[string]any{}), http.DefaultClient, nil).HeadObject(context.Background(),
		&s3.HeadObjectInput{Bucket: aws.String("bucket"), Key: aws.String("key")})
	want := ErrorInfo{Kind: KindHTTP, Status: 404, RequestID: "req-123"}
	if got := Classify(err); got != want {
		t.Errorf("Classify() = %+v, want %+v (err: %v)", got, want, err)
	}
}

func TestClassifyTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := getObject(ctx, testConfig(srv.URL, map[string]any{}), http.DefaultClient)
	if got := Classify(err); got.Kind != KindTimeout {
		t.Errorf("Classify() = %+v, want timeout (err: %v)", got, err)
	}
}

func TestClassifyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	url := errorServer(t, 200, "", nil)
	err := getObject(ctx, testConfig(url, map[string]any{}), http.DefaultClient)
	if got := Classify(err); got.Kind != KindCanceled {
		t.Errorf("Classify() = %+v, want canceled (err: %v)", got, err)
	}
}

func TestClassifyConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	err = getObject(context.Background(), testConfig("http://"+addr, map[string]any{}), http.DefaultClient)
	if got := Classify(err); got != (ErrorInfo{Kind: KindNetwork}) {
		t.Errorf("Classify() = %+v, want network (err: %v)", got, err)
	}
}

func TestClassifyTLSError(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	// http.DefaultClient does not trust the test server certificate.
	err := getObject(context.Background(), testConfig(srv.URL, map[string]any{}), http.DefaultClient)
	if got := Classify(err); got.Kind != KindNetwork {
		t.Errorf("Classify() = %+v, want network (err: %v)", got, err)
	}
}

func TestClassifyOther(t *testing.T) {
	if got := Classify(errors.New("boom")); got.Kind != KindOther {
		t.Errorf("Classify() = %+v, want other", got)
	}
}

func TestMaxAttempts(t *testing.T) {
	for _, tt := range []struct {
		maxAttempts int64
		wantHits    int64
	}{{1, 1}, {2, 2}} {
		var hits atomic.Int64
		url := errorServer(t, 503, `<Error><Code>SlowDown</Code></Error>`, &hits)
		cfg := testConfig(url, map[string]any{"maxAttempts": tt.maxAttempts})
		if err := getObject(context.Background(), cfg, http.DefaultClient); err == nil {
			t.Fatal("expected error")
		}
		if got := hits.Load(); got != tt.wantHits {
			t.Errorf("maxAttempts %d: %d requests, want %d", tt.maxAttempts, got, tt.wantHits)
		}
	}
}
