package s3

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grafana/sobek"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"go.k6.io/k6/v2/js/modulestest"
	"go.k6.io/k6/v2/lib"
	k6metrics "go.k6.io/k6/v2/metrics"
)

const testBucket = "bucket"

type testEnv struct {
	t       *testing.T
	rt      *modulestest.Runtime
	samples chan k6metrics.SampleContainer
	logs    *logtest.Hook
	url     string
}

// newTestEnv creates a VU with the s3 module and a fake S3 server, runs
// initScript in the init context and moves the VU to the VU context.
func newTestEnv(t *testing.T, initScript string) *testEnv {
	t.Helper()
	return newTestEnvWith(t, initScript, nil)
}

// newTestEnvWith is newTestEnv with a middleware in front of the fake S3 server.
func newTestEnvWith(t *testing.T, initScript string, wrap func(http.Handler) http.Handler) *testEnv {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(testBucket); err != nil {
		t.Fatal(err)
	}
	var handler http.Handler = gofakes3.New(backend).Server()
	if wrap != nil {
		handler = wrap(handler)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	rt := modulestest.NewRuntime(t)
	rt.VU.InitEnvField.LookupEnv = func(key string) (string, bool) {
		switch key {
		case "AWS_ACCESS_KEY_ID":
			return "key", true
		case "AWS_SECRET_ACCESS_KEY":
			return "secret", true
		case "S3_ENDPOINT":
			return srv.URL, true
		}
		return "", false
	}
	mi := New().NewModuleInstance(rt.VU)
	if err := rt.VU.Runtime().Set("s3", mi.Exports().Named); err != nil {
		t.Fatal(err)
	}
	if err := rt.VU.Runtime().Set("endpoint", srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RunOnEventLoop(initScript); err != nil {
		t.Fatal(err)
	}

	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	samples := make(chan k6metrics.SampleContainer, 1000)
	registry := rt.VU.InitEnvField.Registry
	rt.MoveToVUContext(&lib.State{
		Samples:        samples,
		Tags:           lib.NewVUStateTags(registry.RootTagSet().With("scenario", "default")),
		Transport:      srv.Client().Transport,
		Logger:         logger,
		BuiltinMetrics: rt.BuiltinMetrics,
	})
	return &testEnv{t: t, rt: rt, samples: samples, logs: hook, url: srv.URL}
}

const defaultInit = `globalThis.client = new s3.Client({ endpoint });`

func (e *testEnv) run(code string) sobek.Value {
	e.t.Helper()
	v, err := e.rt.RunOnEventLoop(code)
	if err != nil {
		e.t.Fatalf("%s: %v", code, err)
	}
	return v
}

// result runs code and returns the result object as seen from JavaScript.
// Integral numbers are converted to int64.
func (e *testEnv) result(code string) map[string]any {
	e.t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(e.run("JSON.stringify("+code+")").String()), &m); err != nil {
		e.t.Fatal(err)
	}
	for k, v := range m {
		if f, ok := v.(float64); ok && f == math.Trunc(f) {
			m[k] = int64(f)
		}
	}
	return m
}

func (e *testEnv) drain() []k6metrics.Sample {
	var out []k6metrics.Sample
	for {
		select {
		case c := <-e.samples:
			out = append(out, c.GetSamples()...)
		default:
			return out
		}
	}
}

func find(samples []k6metrics.Sample, metric, op string) []k6metrics.Sample {
	var out []k6metrics.Sample
	for _, s := range samples {
		if v, _ := s.Tags.Get("op"); s.Metric.Name == metric && v == op {
			out = append(out, s)
		}
	}
	return out
}

func TestObjectRoundTrip(t *testing.T) {
	e := newTestEnv(t, defaultInit)

	put := e.result(`client.putObject("bucket", "a/1", "64KiB")`)
	if put["ok"] != true || put["status"] != int64(200) || put["bytes"] != int64(64<<10) || put["errorKind"] != "" {
		t.Errorf("putObject = %v", put)
	}
	head := e.result(`client.headObject("bucket", "a/1")`)
	if head["ok"] != true || head["status"] != int64(200) || head["bytes"] != int64(0) {
		t.Errorf("headObject = %v", head)
	}
	get := e.result(`client.getObject("bucket", "a/1")`)
	if get["ok"] != true || get["status"] != int64(200) || get["bytes"] != int64(64<<10) {
		t.Errorf("getObject = %v", get)
	}
	del := e.result(`client.deleteObject("bucket", "a/1")`)
	if del["ok"] != true || del["status"] != int64(204) {
		t.Errorf("deleteObject = %v", del)
	}
	get = e.result(`client.getObject("bucket", "a/1")`)
	if get["ok"] != false || get["status"] != int64(404) || get["errorKind"] != "s3" || get["errorCode"] != "NoSuchKey" || get["error"] == "" {
		t.Errorf("getObject after delete = %v", get)
	}

	samples := e.drain()
	for _, o := range []string{"put", "head", "get", "delete"} {
		if len(find(samples, "s3_op_duration", o)) == 0 {
			t.Errorf("no s3_op_duration for %s", o)
		}
	}
	if s := find(samples, "s3_op_bytes", "put"); len(s) != 1 || s[0].Value != 64<<10 {
		t.Errorf("s3_op_bytes put = %v", s)
	}
	if s := find(samples, "s3_op_bytes", "get"); len(s) != 1 || s[0].Value != 64<<10 {
		t.Errorf("s3_op_bytes get = %v (only successful GETs count)", s)
	}
	if s := find(samples, "s3_op_ttfb", "get"); len(s) != 1 {
		t.Errorf("s3_op_ttfb get = %v", s)
	}
	if s := find(samples, "s3_op_ttfb", "put"); len(s) != 0 {
		t.Errorf("s3_op_ttfb emitted for put")
	}
	if s := find(samples, "s3_op_errors", "get"); len(s) != 2 || s[0].Value != 0 || s[1].Value != 1 {
		t.Errorf("s3_op_errors get = %v", s)
	}
	errs := find(samples, "s3_errors", "get")
	if len(errs) != 1 {
		t.Fatalf("s3_errors = %v", errs)
	}
	for k, want := range map[string]string{"error_kind": "s3", "error_code": "NoSuchKey", "status": "404", "scenario": "default"} {
		if v, _ := errs[0].Tags.Get(k); v != want {
			t.Errorf("s3_errors tag %s = %q, want %q", k, v, want)
		}
	}
}

func TestSizeDistribution(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	v := e.run(`
		const sizes = [];
		for (let i = 0; i < 20; i++) {
			sizes.push(client.putObject("bucket", "d/" + i, { dist: "uniform", min: 100, max: 200 }).bytes);
		}
		sizes;`)
	for _, n := range v.Export().([]any) {
		if n := n.(int64); n < 100 || n > 200 {
			t.Errorf("size out of range: %d", n)
		}
	}
}

func TestOptionalTags(t *testing.T) {
	e := newTestEnv(t, `globalThis.client = new s3.Client({ endpoint, tags: ["bucket", "size_class"] });`)
	e.run(`client.putObject("bucket", "k", 1024); client.getObject("bucket", "k"); client.headObject("bucket", "k");`)
	samples := e.drain()
	for _, tt := range []struct{ op, sizeClass string }{{"put", "<4KiB"}, {"get", "<4KiB"}, {"head", ""}} {
		s := find(samples, "s3_op_duration", tt.op)
		if len(s) != 1 {
			t.Fatalf("%s: samples = %v", tt.op, s)
		}
		if v, _ := s[0].Tags.Get("bucket"); v != "bucket" {
			t.Errorf("%s: bucket tag = %q", tt.op, v)
		}
		if v, _ := s[0].Tags.Get("size_class"); v != tt.sizeClass {
			t.Errorf("%s: size_class tag = %q, want %q", tt.op, v, tt.sizeClass)
		}
	}
}

func TestScriptErrorsThrow(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	for _, code := range []string{
		`client.putObject("", "k", 1)`,
		`client.putObject("bucket", "", 1)`,
		`client.putObject("bucket", "k")`,
		`client.putObject("bucket", "k", "1XB")`,
		`client.putObject("bucket", "k", { dist: "zipf" })`,
		`client.getObject("bucket", "")`,
		`client.headObject("", "k")`,
		`client.deleteObject("bucket", "")`,
	} {
		if _, err := e.rt.RunOnEventLoop(code); err == nil {
			t.Errorf("%s did not throw", code)
		}
	}
}

func TestInvalidConfigThrows(t *testing.T) {
	rt := modulestest.NewRuntime(t)
	mi := New().NewModuleInstance(rt.VU)
	if err := rt.VU.Runtime().Set("s3", mi.Exports().Named); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{
		`new s3.Client()`,
		`new s3.Client({ endpoint: "http://localhost", accessKey: "a", secretKey: "s", payloadSigning: "x" })`,
		`new s3.Client({ accessKey: "a", secretKey: "s" })`,
	} {
		_, err := rt.RunOnEventLoop(code)
		if err == nil || !strings.Contains(err.Error(), "s3.Client") {
			t.Errorf("%s: error = %v", code, err)
		}
	}
}

func TestOperationInInitContextThrows(t *testing.T) {
	rt := modulestest.NewRuntime(t)
	mi := New().NewModuleInstance(rt.VU)
	if err := rt.VU.Runtime().Set("s3", mi.Exports().Named); err != nil {
		t.Fatal(err)
	}
	_, err := rt.RunOnEventLoop(`
		const c = new s3.Client({ endpoint: "http://localhost", accessKey: "a", secretKey: "s" });
		c.getObject("bucket", "k");`)
	if err == nil || !strings.Contains(err.Error(), "init context") {
		t.Errorf("error = %v", err)
	}
}

func TestWarningsAreLimited(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	e.run(`for (let i = 0; i < 8; i++) { client.getObject("bucket", "missing"); client.headObject("bucket", "missing"); }`)
	counts := map[string]int{}
	for _, entry := range e.logs.AllEntries() {
		if entry.Level == logrus.WarnLevel {
			counts[fmt.Sprint(entry.Data["op"])]++
		}
	}
	if counts["get"] != maxWarningsPerOp || counts["head"] != maxWarningsPerOp {
		t.Errorf("warnings = %v, want %d per op", counts, maxWarningsPerOp)
	}
	last := e.logs.AllEntries()[len(e.logs.AllEntries())-1]
	if last.Data["key"] != "missing" || last.Data["errorKind"] == nil {
		t.Errorf("log fields = %v", last.Data)
	}
}

func TestCanceledOperation(t *testing.T) {
	e := newTestEnv(t, defaultInit)
	e.rt.CancelContext()
	res := e.result(`client.getObject("bucket", "k")`)
	if res["ok"] != false || res["errorKind"] != "canceled" {
		t.Errorf("getObject after cancel = %v", res)
	}
	if s := e.drain(); len(s) != 0 {
		t.Errorf("samples emitted for a canceled operation: %d", len(s))
	}
	for _, entry := range e.logs.AllEntries() {
		if entry.Level == logrus.WarnLevel {
			t.Errorf("warning logged for a canceled operation: %s", entry.Message)
		}
	}
}
