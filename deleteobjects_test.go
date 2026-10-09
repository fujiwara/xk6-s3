package s3

import (
	"io"
	"net/http"
	"testing"
)

func TestDeleteObjects(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	e.run(`for (let i = 0; i < 5; i++) client.putObject("bucket", "del/" + i, 10);`)
	e.drain()

	// Deleting a missing key succeeds in S3.
	r := e.result(`client.deleteObjects("bucket", ["del/0", "del/1", "del/missing"])`)
	if r["ok"] != true || r["count"] != int64(3) || r["failed"] != int64(0) || r["status"] != int64(200) {
		t.Errorf("deleteObjects = %v", r)
	}
	r = e.result(`client.deleteObjects("bucket", ["del/2", "del/3"], { quiet: true })`)
	if r["ok"] != true || r["count"] != int64(2) || r["failed"] != int64(0) {
		t.Errorf("deleteObjects quiet = %v", r)
	}
	if l := e.result(`client.listObjects("bucket", "del/")`); l["count"] != int64(1) {
		t.Errorf("listObjects after deleteObjects = %v", l)
	}
	samples := e.drain()
	if s := find(samples, "s3_op_duration", "delete_objects"); len(s) != 2 {
		t.Errorf("delete_objects samples = %d", len(s))
	}
	if s := find(samples, "s3_op_errors", "delete_objects"); len(s) != 2 || s[0].Value != 0 {
		t.Errorf("delete_objects errors = %v", s)
	}

	if r := e.result(`client.deleteObjects("missing", ["k"])`); r["ok"] != false || r["errorCode"] != "NoSuchBucket" {
		t.Errorf("deleteObjects (missing bucket) = %v", r)
	}

	for _, code := range []string{
		`client.deleteObjects("bucket", [])`,
		`client.deleteObjects("bucket", Array.from({ length: 1001 }, (_, i) => "k" + i))`,
		`client.deleteObjects("bucket", ["k", ""])`,
		`client.deleteObjects("", ["k"])`,
		`client.deleteObjects("bucket", ["k"], { quiet: "yes" })`,
		`client.deleteObjects("bucket", ["k"], { batch: 1 })`,
	} {
		if _, err := e.rt.RunOnEventLoop(code); err == nil {
			t.Errorf("%s did not throw", code)
		}
	}
}

// partialDeleteFailure answers DeleteObjects with one deleted key and one error.
func partialDeleteFailure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Query().Has("delete") {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("x-amz-request-id", "req-del")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
<Deleted><Key>a</Key></Deleted>
<Error><Key>b</Key><Code>AccessDenied</Code><Message>Access Denied</Message></Error>
</DeleteResult>`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func TestDeleteObjectsPartialFailure(t *testing.T) {
	e := newTestEnvWith(t, defaultInit, partialDeleteFailure)
	r := e.result(`client.deleteObjects("bucket", ["a", "b"])`)
	if r["ok"] != false || r["count"] != int64(1) || r["failed"] != int64(1) ||
		r["errorKind"] != "s3" || r["errorCode"] != "AccessDenied" || r["status"] != int64(200) || r["requestId"] != "req-del" {
		t.Errorf("deleteObjects = %v", r)
	}
	errs := find(e.drain(), "s3_errors", "delete_objects")
	if len(errs) != 1 {
		t.Fatalf("s3_errors = %v", errs)
	}
	if v, _ := errs[0].Tags.Get("error_code"); v != "AccessDenied" {
		t.Errorf("error_code = %q", v)
	}
}
