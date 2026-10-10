package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type recorded struct {
	header http.Header
	body   []byte
}

type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	h := req.Header.Clone()
	if len(req.Trailer) > 0 {
		for k, v := range req.Trailer {
			h["Trailer-"+k] = v
		}
	}
	h.Set("X-Test-Content-Length", req.Header.Get("Content-Length"))
	r.mu.Lock()
	r.reqs = append(r.reqs, recorded{header: h, body: body})
	r.mu.Unlock()
	w.Header().Set("ETag", `"0"`)
	w.Header().Set("x-amz-request-id", "req-1")
}

func (r *recorder) last() recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}

func newTestServer(t *testing.T, https bool) (*recorder, string, *http.Client) {
	t.Helper()
	rec := &recorder{}
	var srv *httptest.Server
	if https {
		srv = httptest.NewTLSServer(rec)
	} else {
		srv = httptest.NewServer(rec)
	}
	t.Cleanup(srv.Close)
	return rec, srv.URL, srv.Client()
}

func testConfig(endpoint string, m map[string]any) Config {
	m["endpoint"] = endpoint
	m["accessKey"] = "AKID"
	m["secretKey"] = "SECRET"
	cfg, err := ParseConfig(m, func(string) (string, bool) { return "", false })
	if err != nil {
		panic(err)
	}
	return cfg
}

func putObject(t *testing.T, cfg Config, hc *http.Client, body []byte) {
	t.Helper()
	c := New(cfg, hc, nil)
	_, err := c.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:            aws.String("bucket"),
		Key:               aws.String("key"),
		Body:              bytes.NewReader(body),
		ContentLength:     aws.Int64(int64(len(body))),
		ChecksumAlgorithm: cfg.ChecksumAlgorithmType(),
	}, cfg.PayloadOptions()...)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWireFormat(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1000)
	const bodySHA256 = "44f8354494a5ba03ba1792a8d3e9c534c47a9181980fde7a3f44b06ef2ae7c7f"
	tests := []struct {
		https          bool
		checksum       string
		signing        string
		algorithm      string
		contentSHA256  string
		chunked        bool
		trailer        string // x-amz-trailer
		checksumHeader string // x-amz-checksum-* header sent before the body
	}{
		// SDK defaults
		{true, "when_supported", "auto", "", "STREAMING-UNSIGNED-PAYLOAD-TRAILER", true, "x-amz-checksum-crc32", ""},
		{true, "when_required", "auto", "", "UNSIGNED-PAYLOAD", false, "", ""},
		{false, "when_supported", "auto", "", bodySHA256, false, "", "X-Amz-Checksum-Crc32"},
		{false, "when_required", "auto", "", bodySHA256, false, "", ""},
		// overrides
		{true, "when_supported", "unsigned", "", "STREAMING-UNSIGNED-PAYLOAD-TRAILER", true, "x-amz-checksum-crc32", ""},
		{true, "when_supported", "signed", "", bodySHA256, false, "", "X-Amz-Checksum-Crc32"},
		{true, "when_required", "unsigned", "", "UNSIGNED-PAYLOAD", false, "", ""},
		{true, "when_required", "signed", "", bodySHA256, false, "", ""},
		{false, "when_supported", "unsigned", "", "UNSIGNED-PAYLOAD", false, "", "X-Amz-Checksum-Crc32"},
		{false, "when_supported", "signed", "", bodySHA256, false, "", "X-Amz-Checksum-Crc32"},
		{false, "when_required", "unsigned", "", "UNSIGNED-PAYLOAD", false, "", ""},
		{false, "when_required", "signed", "", bodySHA256, false, "", ""},
		// algorithms
		{true, "when_supported", "auto", "CRC64NVME", "STREAMING-UNSIGNED-PAYLOAD-TRAILER", true, "x-amz-checksum-crc64nvme", ""},
		{false, "when_supported", "auto", "CRC32C", bodySHA256, false, "", "X-Amz-Checksum-Crc32c"},
		{true, "when_supported", "signed", "SHA256", bodySHA256, false, "", "X-Amz-Checksum-Sha256"},
		{true, "when_supported", "signed", "CRC64NVME", bodySHA256, false, "", "X-Amz-Checksum-Crc64nvme"},
		{true, "when_supported", "signed", "SHA1", bodySHA256, false, "", "X-Amz-Checksum-Sha1"},
	}
	for _, tt := range tests {
		name := fmt.Sprintf("https=%v/%s/%s/%s", tt.https, tt.checksum, tt.signing, tt.algorithm)
		t.Run(name, func(t *testing.T) {
			rec, url, hc := newTestServer(t, tt.https)
			m := map[string]any{"checksum": tt.checksum, "payloadSigning": tt.signing}
			if tt.algorithm != "" {
				m["checksumAlgorithm"] = tt.algorithm
			}
			putObject(t, testConfig(url, m), hc, body)
			r := rec.last()

			if got := r.header.Get("X-Amz-Content-Sha256"); got != tt.contentSHA256 {
				t.Errorf("x-amz-content-sha256 = %q, want %q", got, tt.contentSHA256)
			}
			if got := r.header.Get("Content-Encoding") == "aws-chunked"; got != tt.chunked {
				t.Errorf("aws-chunked = %v, want %v", got, tt.chunked)
			}
			if got := r.header.Get("X-Amz-Trailer"); got != tt.trailer {
				t.Errorf("x-amz-trailer = %q, want %q", got, tt.trailer)
			}
			var checksumHeaders []string
			for k := range r.header {
				if strings.HasPrefix(k, "X-Amz-Checksum-") {
					checksumHeaders = append(checksumHeaders, k)
				}
			}
			var want []string
			if tt.checksumHeader != "" {
				want = []string{tt.checksumHeader}
			}
			if !slices.Equal(checksumHeaders, want) {
				t.Errorf("checksum headers = %v, want %v", checksumHeaders, want)
			}
			if tt.chunked {
				if !bytes.Contains(r.body, []byte(tt.trailer+":")) {
					t.Errorf("trailer %s not found in body %q", tt.trailer, r.body[len(r.body)-60:])
				}
			} else if !bytes.Equal(r.body, body) {
				t.Errorf("body was modified: %d bytes", len(r.body))
			}
		})
	}
}

func TestSignedChecksumMatchesSDK(t *testing.T) {
	body := bytes.Repeat([]byte("checksum"), 4096)
	for _, alg := range ChecksumAlgorithms {
		// Over HTTP the SDK computes the checksum header itself.
		rec, url, hc := newTestServer(t, false)
		putObject(t, testConfig(url, map[string]any{"checksumAlgorithm": alg}), hc, body)
		h := "X-Amz-Checksum-" + strings.ToUpper(alg[:1]) + strings.ToLower(alg[1:])
		want := rec.last().header.Get(h)

		rec, url, hc = newTestServer(t, true)
		putObject(t, testConfig(url, map[string]any{"checksumAlgorithm": alg, "payloadSigning": "signed"}), hc, body)
		if got := rec.last().header.Get(h); got == "" || got != want {
			t.Errorf("%s: signed checksum %q, SDK computed %q", alg, got, want)
		}
	}
}
