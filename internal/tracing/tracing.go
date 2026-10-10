// Package tracing connects xk6-s3 and aws-sdk-go-v2 to the k6 tracer provider.
package tracing

import (
	"context"

	"github.com/aws/smithy-go/tracing"
	"github.com/aws/smithy-go/tracing/smithyoteltracing"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// Name is the instrumentation name of the xk6-s3 tracer.
const Name = "github.com/fujiwara/xk6-s3"

// K6TracerProvider is the tracer provider of a k6 VU (lib.TracerProvider).
type K6TracerProvider interface {
	Tracer(name string, options ...trace.TracerOption) trace.Tracer
}

// provider adapts a K6TracerProvider to an OpenTelemetry TracerProvider.
type provider struct {
	embedded.TracerProvider
	tp K6TracerProvider
}

func (p provider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return p.tp.Tracer(name, opts...)
}

// Adapt returns an OpenTelemetry TracerProvider backed by the k6 tracer provider.
func Adapt(tp K6TracerProvider) trace.TracerProvider {
	if otp, ok := tp.(trace.TracerProvider); ok {
		return otp
	}
	return provider{tp: tp}
}

// SDK returns a TracerProvider for aws-sdk-go-v2 that creates spans only
// under a recording span, that is, only for sampled xk6-s3 operations.
func SDK(tp trace.TracerProvider) tracing.TracerProvider {
	return gated{tp: smithyoteltracing.Adapt(tp)}
}

type gated struct {
	tp tracing.TracerProvider
}

func (g gated) Tracer(scope string, opts ...tracing.TracerOption) tracing.Tracer {
	return gatedTracer{t: g.tp.Tracer(scope, opts...)}
}

type gatedTracer struct {
	t tracing.Tracer
}

func (g gatedTracer) StartSpan(ctx context.Context, name string, opts ...tracing.SpanOption) (context.Context, tracing.Span) {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return tracing.NopTracerProvider{}.Tracer("").StartSpan(ctx, name, opts...)
	}
	return g.t.StartSpan(ctx, name, opts...)
}
