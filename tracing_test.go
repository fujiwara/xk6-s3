package s3

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// traceRecorder records the traceparent header of each request.
type traceRecorder struct {
	mu      sync.Mutex
	headers []string
}

func (r *traceRecorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.headers = append(r.headers, req.Header.Get("traceparent"))
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

func (r *traceRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.headers
	r.headers = nil
	return h
}

// withTracing sets a recording tracer provider to the VU.
func withTracing(t *testing.T, e *testEnv) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	e.rt.VU.StateField.TracerProvider = tp
	return rec
}

func spansByName(spans []sdktrace.ReadOnlySpan) map[string][]sdktrace.ReadOnlySpan {
	m := map[string][]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		m[s.Name()] = append(m[s.Name()], s)
	}
	return m
}

func attr(s sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

func TestTracingSampled(t *testing.T) {
	e := newTestEnv(t, `globalThis.client = new s3.Client({ endpoint, tracing: { sampleRate: 1 } });`)
	rec := withTracing(t, e)
	e.run(`client.putObject("bucket", "k", 1024)`)

	spans := spansByName(rec.Ended())
	op := spans["s3.put"]
	if len(op) != 1 {
		t.Fatalf("spans = %v", spans)
	}
	root := op[0]
	if root.Parent().IsValid() {
		t.Error("s3.put has a parent")
	}
	for k, want := range map[string]string{
		"xk6.s3.op": "put", "aws.s3.bucket": "bucket", "aws.s3.key": "k",
		"xk6.s3.size": "1024", "xk6.s3.bytes": "1024", "http.response.status_code": "200", "k6.scenario": "default",
	} {
		if got := attr(root, k); got != want {
			t.Errorf("attribute %s = %q, want %q", k, got, want)
		}
	}
	// The SDK spans are children of the operation span.
	var sdkChildren int
	for name, ss := range spans {
		for _, s := range ss {
			if s.Parent().SpanID() == root.SpanContext().SpanID() {
				sdkChildren++
				t.Logf("child span: %s", name)
			}
		}
	}
	if sdkChildren == 0 {
		t.Errorf("no SDK spans under s3.put: %v", spans)
	}
	// Samples are linked to the trace.
	for _, s := range find(e.drain(), "s3_op_duration", "put") {
		if s.Metadata["trace_id"] != root.SpanContext().TraceID().String() {
			t.Errorf("trace_id metadata = %q", s.Metadata["trace_id"])
		}
	}
}

func TestTracingNotSampled(t *testing.T) {
	e := newTestEnv(t, `globalThis.client = new s3.Client({ endpoint, tracing: { sampleRate: 0 } });`)
	rec := withTracing(t, e)
	e.run(`client.putObject("bucket", "k", 1); client.getObject("bucket", "k");`)
	if spans := rec.Ended(); len(spans) != 0 {
		t.Errorf("spans for successful operations: %v", spansByName(spans))
	}
	for _, s := range e.drain() {
		if _, ok := s.Metadata["trace_id"]; ok {
			t.Errorf("trace_id metadata on %s without a span", s.Metadata)
		}
	}

	// A failure is traced regardless of the sample rate, without SDK spans.
	e.run(`client.getObject("bucket", "missing")`)
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "s3.get" {
		t.Fatalf("spans = %v", spansByName(spans))
	}
	s := spans[0]
	if s.Status().Code.String() != "Error" || attr(s, "aws.s3.error_code") != "NoSuchKey" || attr(s, "error.type") != "s3" {
		t.Errorf("status = %v, attributes = %v", s.Status(), s.Attributes())
	}
	if !s.EndTime().After(s.StartTime()) {
		t.Errorf("span times: %v - %v", s.StartTime(), s.EndTime())
	}
	errs := find(e.drain(), "s3_errors", "get")
	if len(errs) != 1 || errs[0].Metadata["trace_id"] != s.SpanContext().TraceID().String() {
		t.Errorf("s3_errors = %v", errs)
	}
}

func TestTracingPropagation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string
		want   bool
	}{
		{"propagate", `{ sampleRate: 1, propagate: true }`, true},
		{"not propagated by default", `{ sampleRate: 1 }`, false},
		{"not sampled", `{ sampleRate: 0, propagate: true }`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tr := &traceRecorder{}
			e := newTestEnvWith(t, `globalThis.client = new s3.Client({ endpoint, tracing: `+tt.config+` });`, tr.wrap)
			rec := withTracing(t, e)
			e.run(`client.putObject("bucket", "k", 1)`)
			headers := tr.take()
			if len(headers) != 1 {
				t.Fatalf("requests = %d", len(headers))
			}
			if got := headers[0] != ""; got != tt.want {
				t.Fatalf("traceparent = %q, want present: %v", headers[0], tt.want)
			}
			if tt.want {
				spans := spansByName(rec.Ended())
				traceID := spans["s3.put"][0].SpanContext().TraceID().String()
				if !strings.Contains(headers[0], traceID) {
					t.Errorf("traceparent %q does not have the trace ID %s", headers[0], traceID)
				}
			}
		})
	}
}

func TestTracingMultipart(t *testing.T) {
	e := newTestEnv(t, `globalThis.client = new s3.Client({ endpoint, tracing: { sampleRate: 1 } });`)
	rec := withTracing(t, e)
	e.run(`client.putObjectMultipart("bucket", "mp", "12MiB", { partSize: "5MiB", concurrency: 2 })`)
	spans := spansByName(rec.Ended())
	if len(spans["s3.put_multipart"]) != 1 || len(spans["s3.upload_part"]) != 3 {
		t.Fatalf("spans = %v", spans)
	}
	parent := spans["s3.put_multipart"][0].SpanContext()
	for _, p := range spans["s3.upload_part"] {
		if p.Parent().SpanID() != parent.SpanID() || p.SpanContext().TraceID() != parent.TraceID() {
			t.Errorf("upload_part parent = %v, want %v", p.Parent(), parent)
		}
	}
}

func TestTracingMultipartFailureNotSampled(t *testing.T) {
	f := &failingPart{}
	e := newTestEnvWith(t, `globalThis.client = new s3.Client({ endpoint, tracing: { sampleRate: 0 } });`, f.wrap("2"))
	rec := withTracing(t, e)
	e.run(`client.putObjectMultipart("bucket", "mp", "15MiB", { partSize: "5MiB", concurrency: 1 })`)
	// Only the failed upload is traced, not its parts.
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "s3.put_multipart" || spans[0].Parent().IsValid() {
		t.Errorf("spans = %v", spansByName(spans))
	}
}

func TestTracingWithoutProvider(t *testing.T) {
	// The k6 tracer provider is nil in tests and noop without --traces-output.
	e := newTestEnv(t, `globalThis.client = new s3.Client({ endpoint, tracing: { sampleRate: 1, propagate: true } });`)
	e.rt.VU.StateField.TracerProvider = nil
	if r := e.result(`client.getObject("bucket", "missing")`); r["errorCode"] != "NoSuchKey" {
		t.Errorf("getObject = %v", r)
	}
	if r := e.result(`client.putObject("bucket", "k", 1)`); r["ok"] != true {
		t.Errorf("putObject = %v", r)
	}
}
