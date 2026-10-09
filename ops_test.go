package s3

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestBucketOperations(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	if r := e.result(`client.createBucket("new-bucket")`); r["ok"] != true || r["status"] != int64(200) {
		t.Errorf("createBucket = %v", r)
	}
	if r := e.result(`client.createBucket("new-bucket")`); r["ok"] != false || r["errorKind"] != "s3" {
		t.Errorf("createBucket (exists) = %v", r)
	}
	if r := e.result(`client.deleteBucket("new-bucket")`); r["ok"] != true || r["status"] != int64(204) {
		t.Errorf("deleteBucket = %v", r)
	}
	if r := e.result(`client.deleteBucket("new-bucket")`); r["ok"] != false || r["errorCode"] != "NoSuchBucket" {
		t.Errorf("deleteBucket (missing) = %v", r)
	}
	samples := e.drain()
	if n := len(find(samples, "s3_op_duration", "create_bucket")); n != 2 {
		t.Errorf("create_bucket samples = %d", n)
	}
	if n := len(find(samples, "s3_op_duration", "delete_bucket")); n != 2 {
		t.Errorf("delete_bucket samples = %d", n)
	}
}

func TestListObjects(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	e.run(`for (let i = 0; i < 5; i++) client.putObject("bucket", "list/" + i, 10); client.putObject("bucket", "other", 10);`)
	e.drain()

	tests := []struct {
		code  string
		count int64
		pages int
	}{
		{`client.listObjects("bucket", "list/")`, 5, 1},
		{`client.listObjects("bucket", "")`, 6, 1},
		{`client.listObjects("bucket", "list/", { maxKeys: 2 })`, 2, 1},
		{`client.listObjects("bucket", "list/", { maxKeys: 2, maxPages: 2 })`, 4, 2},
		{`client.listObjects("bucket", "list/", { maxKeys: 2, maxPages: 0 })`, 5, 3},
		{`client.listObjects("bucket", "none/")`, 0, 1},
	}
	for _, tt := range tests {
		r := e.result(tt.code)
		if r["ok"] != true || r["count"] != tt.count {
			t.Errorf("%s = %v, want count %d", tt.code, r, tt.count)
		}
		if n := len(find(e.drain(), "s3_op_duration", "list")); n != tt.pages {
			t.Errorf("%s: %d list samples, want %d", tt.code, n, tt.pages)
		}
	}
	if r := e.result(`client.listObjects("missing", "")`); r["ok"] != false || r["errorCode"] != "NoSuchBucket" {
		t.Errorf("listObjects (missing bucket) = %v", r)
	}
	for _, code := range []string{
		`client.listObjects("bucket", "", { maxKey: 1 })`,
		`client.listObjects("bucket", "", { maxKeys: -1 })`,
		`client.listObjects("bucket", "", 1)`,
	} {
		if _, err := e.rt.RunOnEventLoop(code); err == nil {
			t.Errorf("%s did not throw", code)
		}
	}
}

func TestPutObjectMultipart(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	r := e.result(`client.putObjectMultipart("bucket", "mp", "12MiB", { partSize: "5MiB", concurrency: 2 })`)
	if r["ok"] != true || r["bytes"] != int64(12<<20) || r["status"] != int64(200) {
		t.Fatalf("putObjectMultipart = %v", r)
	}
	samples := e.drain()
	parts := find(samples, "s3_op_bytes", "upload_part")
	if len(parts) != 3 {
		t.Fatalf("upload_part samples = %d, want 3", len(parts))
	}
	var total float64
	for _, p := range parts {
		total += p.Value
	}
	if total != 12<<20 {
		t.Errorf("upload_part bytes = %v", total)
	}
	if s := find(samples, "s3_op_bytes", "put_multipart"); len(s) != 1 || s[0].Value != 12<<20 {
		t.Errorf("put_multipart bytes = %v", s)
	}
	if g := e.result(`client.getObject("bucket", "mp")`); g["bytes"] != int64(12<<20) {
		t.Errorf("getObject = %v", g)
	}

	// The upload uses a transport sized for the concurrency.
	c := e.run(`client`).Export().(*Client)
	if c.parallelTransport == nil || c.parallelTransport.MaxIdleConnsPerHost < 2 {
		t.Errorf("parallel transport = %+v", c.parallelTransport)
	}

	// An object smaller than a part is uploaded as one part.
	if r := e.result(`client.putObjectMultipart("bucket", "small", 100)`); r["ok"] != true || r["bytes"] != int64(100) {
		t.Errorf("small putObjectMultipart = %v", r)
	}
}

func TestPutObjectMultipartArguments(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	for _, code := range []string{
		`client.putObjectMultipart("bucket", "k", "1MiB", { partSize: 0 })`,
		`client.putObjectMultipart("bucket", "k", "1MiB", { partSize: { dist: "uniform", min: 1, max: 2 } })`,
		`client.putObjectMultipart("bucket", "k", "1MiB", { concurrency: 0 })`,
		`client.putObjectMultipart("bucket", "k", "1MiB", { parts: 2 })`,
		`client.putObjectMultipart("bucket", "k", "1GiB", { partSize: "100KiB" })`,
	} {
		if _, err := e.rt.RunOnEventLoop(code); err == nil {
			t.Errorf("%s did not throw", code)
		}
	}
}

// failingPart makes UploadPart of the given part number fail with 500 and
// records the methods of multipart requests.
type failingPart struct {
	mu       sync.Mutex
	requests []string
}

func (f *failingPart) wrap(partNumber string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Has("uploadId") || q.Has("uploads") {
				f.mu.Lock()
				f.requests = append(f.requests, r.Method+" "+q.Get("partNumber"))
				f.mu.Unlock()
			}
			if r.Method == http.MethodPut && q.Get("partNumber") == partNumber {
				// Read the body as a real server does, so that the client
				// receives the response instead of a connection reset.
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<Error><Code>InternalError</Code><Message>injected</Message></Error>`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func TestPutObjectMultipartAbortsOnFailure(t *testing.T) {
	f := &failingPart{}
	e := newTestEnvWith(t, defaultInit, f.wrap("2"))
	r := e.result(`client.putObjectMultipart("bucket", "mp", "15MiB", { partSize: "5MiB", concurrency: 1 })`)
	if r["ok"] != false || r["errorKind"] != "s3" || r["errorCode"] != "InternalError" || r["status"] != int64(500) {
		t.Errorf("putObjectMultipart = %v", r)
	}
	f.mu.Lock()
	requests := strings.Join(f.requests, ",")
	f.mu.Unlock()
	// create, part 1, part 2 (fails), abort. Part 3 is not sent.
	if requests != "POST ,PUT 1,PUT 2,DELETE " {
		t.Errorf("requests = %s", requests)
	}
	if g := e.result(`client.headObject("bucket", "mp")`); g["ok"] != false {
		t.Errorf("object exists after a failed upload: %v", g)
	}
	samples := e.drain()
	if s := find(samples, "s3_op_errors", "put_multipart"); len(s) != 1 || s[0].Value != 1 {
		t.Errorf("put_multipart errors = %v", s)
	}
	if s := find(samples, "s3_errors", "upload_part"); len(s) != 1 {
		t.Errorf("upload_part errors = %v", s)
	}
}

func TestPreloadAndDeletePrefix(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	r := e.result(`client.preload("bucket", "pre/", 25, { dist: "uniform", min: 10, max: 20 }, { concurrency: 4 })`)
	if r["ok"] != true || r["count"] != int64(25) || r["failed"] != int64(0) {
		t.Fatalf("preload = %v", r)
	}
	if b := r["bytes"].(int64); b < 250 || b > 500 {
		t.Errorf("preload bytes = %d", b)
	}
	if n := len(find(e.drain(), "s3_op_duration", "put")); n != 25 {
		t.Errorf("put samples = %d", n)
	}
	if l := e.result(`client.listObjects("bucket", "pre/", { maxPages: 0 })`); l["count"] != int64(25) {
		t.Errorf("listObjects = %v", l)
	}
	if h := e.result(`client.headObject("bucket", "pre/24")`); h["ok"] != true {
		t.Errorf("pre/24 = %v", h)
	}
	e.run(`client.putObject("bucket", "keep", 1)`)

	r = e.result(`client.deletePrefix("bucket", "pre/", { concurrency: 3 })`)
	if r["ok"] != true || r["count"] != int64(25) || r["failed"] != int64(0) {
		t.Fatalf("deletePrefix = %v", r)
	}
	if l := e.result(`client.listObjects("bucket", "", { maxPages: 0 })`); l["count"] != int64(1) {
		t.Errorf("listObjects after deletePrefix = %v", l)
	}

	for _, code := range []string{
		`client.deletePrefix("bucket", "")`,
		`client.preload("bucket", "p/", -1, 1)`,
		`client.preload("bucket", "p/", 1, 1, { concurrency: 0 })`,
	} {
		if _, err := e.rt.RunOnEventLoop(code); err == nil {
			t.Errorf("%s did not throw", code)
		}
	}
}

func TestPreloadReportsFailures(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	r := e.result(`client.preload("missing", "p/", 3, 1)`)
	if r["ok"] != false || r["count"] != int64(0) || r["failed"] != int64(3) || r["errorCode"] != "NoSuchBucket" {
		t.Errorf("preload = %v", r)
	}
}
