package tracing

import (
	"context"
	"testing"

	"github.com/aws/smithy-go/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// k6Provider has only the Tracer method, like lib.TracerProvider.
type k6Provider struct {
	tp *sdktrace.TracerProvider
}

func (p k6Provider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return p.tp.Tracer(name, opts...)
}

func TestSDKSpansOnlyUnderRecordingSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otp := Adapt(k6Provider{tp})
	sdk := SDK(otp).Tracer("sdk")

	// without a parent span: no SDK span
	_, span := sdk.StartSpan(context.Background(), "S3.GetObject")
	span.End()
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("%d spans without a parent", n)
	}

	// under a recording span: the SDK span is its child
	ctx, parent := otp.Tracer(Name).Start(context.Background(), "s3.get")
	_, span = sdk.StartSpan(ctx, "S3.GetObject", func(o *tracing.SpanOptions) {})
	span.End()
	parent.End()
	spans := rec.Ended()
	if len(spans) != 2 || spans[0].Name() != "S3.GetObject" || spans[0].Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("spans = %v", spans)
	}
}

func TestAdaptKeepsOTelProvider(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	if Adapt(tp) != trace.TracerProvider(tp) {
		t.Error("an OpenTelemetry provider is wrapped")
	}
}
