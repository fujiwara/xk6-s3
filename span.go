package s3

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/fujiwara/xk6-s3/internal/client"
	"github.com/fujiwara/xk6-s3/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// nestedKey marks a context of an operation that is part of another
// measured operation, such as a part of a multipart upload.
type nestedKey struct{}

// startSpan starts the span of an operation.
//
// A top-level operation is traced with the probability of the sample rate.
// An operation inside another operation is traced when its parent is. The
// returned context is marked as nested and carries the span, if any, so that
// the SDK spans and the traceparent header follow it.
func (c *Client) startSpan(env vuEnv, ctx context.Context, o op, start time.Time) (context.Context, trace.Span) {
	nested := ctx.Value(nestedKey{}) != nil
	ctx = context.WithValue(ctx, nestedKey{}, true)
	sampled := trace.SpanFromContext(ctx).IsRecording()
	if !nested {
		sampled = c.cfg.Tracing.SampleRate > 0 && rand.Float64() < c.cfg.Tracing.SampleRate
	}
	if !sampled {
		return ctx, trace.SpanFromContext(context.Background())
	}
	return env.tracer.Tracer(tracing.Name).Start(ctx, spanName(o), trace.WithTimestamp(start),
		trace.WithAttributes(c.spanAttributes(env, o)...))
}

// endSpan ends the span of an operation and returns it. A failed top-level
// operation that was not sampled is recorded with a span created afterwards,
// so that failures are always traced.
func (c *Client) endSpan(env vuEnv, span trace.Span, o op, res *Result, start, end time.Time) trace.Span {
	if !span.IsRecording() {
		failed := !res.OK && res.ErrorKind != client.KindCanceled
		if !failed || env.ctx.Value(nestedKey{}) != nil {
			// the parent operation records the failure
			return span
		}
		_, span = env.tracer.Tracer(tracing.Name).Start(context.WithoutCancel(env.ctx), spanName(o),
			trace.WithTimestamp(start), trace.WithAttributes(c.spanAttributes(env, o)...))
	}
	attrs := []attribute.KeyValue{attribute.Int64("xk6.s3.bytes", res.Bytes)}
	if o.size >= 0 {
		attrs = append(attrs, attribute.Int64("xk6.s3.size", o.size))
	}
	if res.Status > 0 {
		attrs = append(attrs, attribute.Int("http.response.status_code", res.Status))
	}
	if res.RequestID != "" {
		attrs = append(attrs, attribute.String("aws.request_id", res.RequestID))
	}
	if res.Count > 0 || res.Failed > 0 {
		attrs = append(attrs, attribute.Int64("xk6.s3.count", res.Count), attribute.Int64("xk6.s3.failed", res.Failed))
	}
	if !res.OK {
		attrs = append(attrs, attribute.String("error.type", res.ErrorKind))
		if res.ErrorCode != "" {
			attrs = append(attrs, attribute.String("aws.s3.error_code", res.ErrorCode))
		}
		if res.ErrorKind != client.KindCanceled {
			span.SetStatus(codes.Error, res.Error)
		}
	}
	span.SetAttributes(attrs...)
	span.End(trace.WithTimestamp(end))
	return span
}

func spanName(o op) string {
	return "s3." + o.name
}

func (c *Client) spanAttributes(env vuEnv, o op) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("xk6.s3.op", o.name),
		attribute.String("aws.s3.bucket", o.bucket),
	}
	if o.key != "" {
		attrs = append(attrs, attribute.String("aws.s3.key", o.key))
	}
	if v, ok := env.tags.Tags.Get("scenario"); ok {
		attrs = append(attrs, attribute.String("k6.scenario", v))
	}
	if env.state.VUID > 0 {
		attrs = append(attrs, attribute.Int64("k6.vu", int64(env.state.VUID)))
	}
	return attrs
}
