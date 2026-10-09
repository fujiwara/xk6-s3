package metrics

import (
	"testing"
	"time"

	k6metrics "go.k6.io/k6/v2/metrics"
)

func setup(t *testing.T) (*Metrics, k6metrics.TagsAndMeta) {
	t.Helper()
	r := k6metrics.NewRegistry()
	m, err := Register(r)
	if err != nil {
		t.Fatal(err)
	}
	base := k6metrics.TagsAndMeta{
		Tags:     r.RootTagSet().With("scenario", "default").With("group", ""),
		Metadata: map[string]string{"vu": "1"},
	}
	return m, base
}

type got struct {
	value float64
	tags  map[string]string
}

func collect(t *testing.T, c k6metrics.SampleContainer) map[string][]got {
	t.Helper()
	out := map[string][]got{}
	for _, s := range c.GetSamples() {
		out[s.Metric.Name] = append(out[s.Metric.Name], got{s.Value, s.Tags.Map()})
		if s.Metadata["vu"] != "1" {
			t.Errorf("%s: metadata not propagated: %v", s.Metric.Name, s.Metadata)
		}
	}
	return out
}

func TestRegister(t *testing.T) {
	r := k6metrics.NewRegistry()
	m, err := Register(r)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]k6metrics.MetricType{
		OpDurationName: k6metrics.Trend,
		OpTTFBName:     k6metrics.Trend,
		OpBytesName:    k6metrics.Counter,
		OpErrorsName:   k6metrics.Rate,
		ErrorsName:     k6metrics.Counter,
	} {
		if got := r.Get(name); got == nil || got.Type != want {
			t.Errorf("metric %s = %v, want type %v", name, got, want)
		}
	}
	if m.OpDuration.Contains != k6metrics.Time || m.OpBytes.Contains != k6metrics.Data {
		t.Error("unexpected value types")
	}
	// Registering again in the same registry returns the same metrics.
	if m2, err := Register(r); err != nil || m2.OpDuration != m.OpDuration {
		t.Errorf("second Register() = %v, %v", m2, err)
	}
}

func TestSamplesSuccess(t *testing.T) {
	m, base := setup(t)
	start := time.Unix(1000, 0)
	c := m.Samples(base, TagOptions{}, Result{
		Op: OpGet, Bucket: "b", Start: start, Duration: 250 * time.Millisecond,
		TTFB: 20 * time.Millisecond, Bytes: 4096, Size: 4096,
	})
	s := collect(t, c)
	if len(s) != 4 {
		t.Fatalf("metrics = %v", s)
	}
	if v := s[OpDurationName][0].value; v != 250 {
		t.Errorf("duration = %v", v)
	}
	if v := s[OpTTFBName][0].value; v != 20 {
		t.Errorf("ttfb = %v", v)
	}
	if v := s[OpBytesName][0].value; v != 4096 {
		t.Errorf("bytes = %v", v)
	}
	if v := s[OpErrorsName][0].value; v != 0 {
		t.Errorf("errors rate = %v", v)
	}
	tags := s[OpDurationName][0].tags
	if tags[TagOp] != OpGet || tags["scenario"] != "default" {
		t.Errorf("tags = %v", tags)
	}
	if _, ok := tags[TagBucket]; ok {
		t.Error("bucket tag is set without the option")
	}
	if _, ok := tags[TagSizeClass]; ok {
		t.Error("size_class tag is set without the option")
	}
	if cs := c.(k6metrics.ConnectedSamples); !cs.Time.Equal(start.Add(250 * time.Millisecond)) {
		t.Errorf("time = %v", cs.Time)
	}
}

func TestSamplesOptionalTags(t *testing.T) {
	m, base := setup(t)
	opts := TagOptions{Bucket: true, SizeClass: true}

	s := collect(t, m.Samples(base, opts, Result{Op: OpPut, Bucket: "b", Bytes: 1 << 20, Size: 1 << 20}))
	tags := s[OpDurationName][0].tags
	if tags[TagBucket] != "b" || tags[TagSizeClass] != "<16MiB" {
		t.Errorf("tags = %v", tags)
	}

	s = collect(t, m.Samples(base, opts, Result{Op: OpList, Bucket: "b", Bytes: -1, Size: -1}))
	tags = s[OpDurationName][0].tags
	if _, ok := tags[TagSizeClass]; ok {
		t.Errorf("size_class tag set for unknown size: %v", tags)
	}
	if _, ok := s[OpBytesName]; ok {
		t.Error("s3_op_bytes emitted for an operation without body")
	}
	if _, ok := s[OpTTFBName]; ok {
		t.Error("s3_op_ttfb emitted without TTFB")
	}
}

func TestSamplesError(t *testing.T) {
	m, base := setup(t)
	s := collect(t, m.Samples(base, TagOptions{}, Result{
		Op: OpGet, Duration: time.Millisecond, Bytes: -1, Size: -1,
		ErrorKind: "s3", ErrorCode: "NoSuchKey", Status: 404,
	}))
	if v := s[OpErrorsName][0].value; v != 1 {
		t.Errorf("errors rate = %v", v)
	}
	e := s[ErrorsName]
	if len(e) != 1 || e[0].value != 1 {
		t.Fatalf("s3_errors = %v", e)
	}
	want := map[string]string{TagErrorKind: "s3", TagErrorCode: "NoSuchKey", TagStatus: "404", TagOp: OpGet}
	for k, v := range want {
		if e[0].tags[k] != v {
			t.Errorf("s3_errors tag %s = %q, want %q", k, e[0].tags[k], v)
		}
	}
	if _, ok := s[OpDurationName][0].tags[TagErrorKind]; ok {
		t.Error("error tags leaked into s3_op_duration")
	}
}

func TestSamplesCanceled(t *testing.T) {
	m, base := setup(t)
	if c := m.Samples(base, TagOptions{}, Result{Op: OpGet, ErrorKind: ErrorKindCanceled}); c != nil {
		t.Errorf("Samples() = %v, want nil", c)
	}
}
