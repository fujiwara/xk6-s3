// Package metrics defines the xk6-s3 metrics and builds their samples.
package metrics

import (
	"errors"
	"strconv"
	"time"

	"github.com/fujiwara/xk6-s3/internal/data"
	k6metrics "go.k6.io/k6/v2/metrics"
)

// Metric names.
const (
	OpDurationName = "s3_op_duration"
	OpTTFBName     = "s3_op_ttfb"
	OpBytesName    = "s3_op_bytes"
	OpErrorsName   = "s3_op_errors"
	ErrorsName     = "s3_errors"
)

// Operation names used as the op tag.
const (
	OpPut          = "put"
	OpGet          = "get"
	OpHead         = "head"
	OpDelete       = "delete"
	OpList         = "list"
	OpCreateBucket = "create_bucket"
	OpDeleteBucket = "delete_bucket"
	OpPutMultipart = "put_multipart"
	OpUploadPart   = "upload_part"
)

// Tag names.
const (
	TagOp        = "op"
	TagBucket    = "bucket"
	TagSizeClass = "size_class"
	TagErrorKind = "error_kind"
	TagErrorCode = "error_code"
	TagStatus    = "status"
)

// ErrorKindCanceled is the error kind of operations canceled by the end of
// the test. Such operations produce no samples.
const ErrorKindCanceled = "canceled"

// Metrics holds the registered xk6-s3 metrics.
type Metrics struct {
	OpDuration *k6metrics.Metric
	OpTTFB     *k6metrics.Metric
	OpBytes    *k6metrics.Metric
	OpErrors   *k6metrics.Metric
	Errors     *k6metrics.Metric
}

// Register registers the xk6-s3 metrics in the registry.
func Register(r *k6metrics.Registry) (*Metrics, error) {
	var errs []error
	newMetric := func(name string, typ k6metrics.MetricType, vt ...k6metrics.ValueType) *k6metrics.Metric {
		m, err := r.NewMetric(name, typ, vt...)
		errs = append(errs, err)
		return m
	}
	m := &Metrics{
		OpDuration: newMetric(OpDurationName, k6metrics.Trend, k6metrics.Time),
		OpTTFB:     newMetric(OpTTFBName, k6metrics.Trend, k6metrics.Time),
		OpBytes:    newMetric(OpBytesName, k6metrics.Counter, k6metrics.Data),
		OpErrors:   newMetric(OpErrorsName, k6metrics.Rate),
		Errors:     newMetric(ErrorsName, k6metrics.Counter),
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return m, nil
}

// TagOptions enables optional tags.
type TagOptions struct {
	Bucket    bool
	SizeClass bool
}

// Result is the outcome of a single operation.
type Result struct {
	Op       string
	Bucket   string
	Start    time.Time
	Duration time.Duration
	// TTFB is the time to the response headers. Zero means not measured.
	TTFB time.Duration
	// Bytes is the number of body bytes sent or received. Negative means the
	// operation has no body and no s3_op_bytes sample is emitted.
	Bytes int64
	// Size is the object size used for the size_class tag. Negative means
	// unknown and the tag is omitted.
	Size int64
	// ErrorKind is empty on success.
	ErrorKind string
	ErrorCode string
	Status    int
}

// Samples builds the samples for an operation result on top of the VU tags
// and metadata. It returns nil for canceled operations.
func (m *Metrics) Samples(base k6metrics.TagsAndMeta, opts TagOptions, r Result) k6metrics.SampleContainer {
	if r.ErrorKind == ErrorKindCanceled {
		return nil
	}
	tags := base.Tags.With(TagOp, r.Op)
	if opts.Bucket && r.Bucket != "" {
		tags = tags.With(TagBucket, r.Bucket)
	}
	if opts.SizeClass && r.Size >= 0 {
		tags = tags.With(TagSizeClass, data.SizeClass(r.Size))
	}
	t := r.Start.Add(r.Duration)
	sample := func(metric *k6metrics.Metric, tags *k6metrics.TagSet, value float64) k6metrics.Sample {
		return k6metrics.Sample{
			TimeSeries: k6metrics.TimeSeries{Metric: metric, Tags: tags},
			Time:       t,
			Value:      value,
			Metadata:   base.Metadata,
		}
	}

	failed := 0.0
	if r.ErrorKind != "" {
		failed = 1
	}
	samples := []k6metrics.Sample{
		sample(m.OpDuration, tags, k6metrics.D(r.Duration)),
		sample(m.OpErrors, tags, failed),
	}
	if r.TTFB > 0 {
		samples = append(samples, sample(m.OpTTFB, tags, k6metrics.D(r.TTFB)))
	}
	if r.Bytes >= 0 {
		samples = append(samples, sample(m.OpBytes, tags, float64(r.Bytes)))
	}
	if r.ErrorKind != "" {
		errTags := tags.With(TagErrorKind, r.ErrorKind).
			With(TagErrorCode, r.ErrorCode).
			With(TagStatus, strconv.Itoa(r.Status))
		samples = append(samples, sample(m.Errors, errTags, 1))
	}
	return k6metrics.ConnectedSamples{Samples: samples, Tags: tags, Time: t}
}
