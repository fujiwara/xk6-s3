package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/fujiwara/xk6-s3/internal/client"
	"github.com/fujiwara/xk6-s3/internal/data"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"github.com/grafana/sobek"
	"go.k6.io/k6/v2/js/common"
	"go.k6.io/k6/v2/js/modules"
	k6metrics "go.k6.io/k6/v2/metrics"
)

// Result is the result of an operation returned to JavaScript.
// S3 and network errors are reported here instead of being thrown.
type Result struct {
	OK        bool   `js:"ok"`
	Status    int    `js:"status"`
	Bytes     int64  `js:"bytes"`
	ErrorKind string `js:"errorKind"`
	ErrorCode string `js:"errorCode"`
	Error     string `js:"error"`
	RequestID string `js:"requestId"`
}

// Client is the JavaScript s3.Client. Each VU has its own instance.
type Client struct {
	vu      modules.VU
	root    *RootModule
	cfg     client.Config
	rng     *rand.Rand
	sdkOnce sync.Once
	sdk     *awss3.Client
}

// newClient implements `new s3.Client(config)`. It only validates the
// configuration; the SDK client is created on the first operation in the VU.
func (mi *ModuleInstance) newClient(call sobek.ConstructorCall) *sobek.Object {
	rt := mi.vu.Runtime()
	if mi.root.metricsErr != nil {
		common.Throw(rt, mi.root.metricsErr)
	}
	m, ok := call.Argument(0).Export().(map[string]any)
	if !ok {
		common.Throw(rt, errors.New("s3.Client requires a config object"))
	}
	cfg, err := client.ParseConfig(m, lookupEnvFunc(mi.vu))
	if err != nil {
		common.Throw(rt, fmt.Errorf("invalid s3.Client config: %w", err))
	}
	c := &Client{
		vu:   mi.vu,
		root: mi.root,
		cfg:  cfg,
		rng:  rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())),
	}
	return rt.ToValue(c).ToObject(rt)
}

func (c *Client) throw(err error) {
	common.Throw(c.vu.Runtime(), err)
}

// client returns the SDK client of the VU, creating it on the first call.
// The SDK client uses the VU's k6 HTTP transport, so that data_sent and
// data_received are recorded and the k6 TLS options apply.
func (c *Client) client() *awss3.Client {
	state := c.vu.State()
	if state == nil {
		c.throw(errors.New("S3 operations cannot be called in the init context"))
	}
	c.sdkOnce.Do(func() {
		c.sdk = client.New(c.cfg, &http.Client{Transport: state.Transport})
	})
	return c.sdk
}

func requireName(rt *sobek.Runtime, what, v string) {
	if v == "" {
		common.Throw(rt, fmt.Errorf("%s must not be empty", what))
	}
}

// op describes an operation for metrics and logs.
type op struct {
	name   string
	bucket string
	key    string
	// size of the object for the size_class tag, or -1 if unknown
	size int64
}

// opOutput is what an operation function reports to run.
type opOutput struct {
	metadata middleware.Metadata
	// bytes is the number of body bytes sent or received, or -1 when the
	// operation has no body.
	bytes int64
	ttfb  time.Duration
	// size overrides op.size when it is known only from the response.
	size int64
}

// noBody is the opOutput of a failed operation or an operation without body.
var noBody = opOutput{bytes: -1, size: -1}

// run runs fn as one measured operation with the operation timeout, emits
// its metrics and returns its result.
func (c *Client) run(o op, fn func(ctx context.Context) (opOutput, error)) *Result {
	vuCtx := c.vu.Context()
	ctx, cancel := context.WithTimeout(vuCtx, c.cfg.Timeout)
	defer cancel()

	start := time.Now()
	out, err := fn(ctx)
	duration := time.Since(start)

	if out.size >= 0 {
		o.size = out.size
	}
	res := &Result{OK: err == nil, Bytes: max(out.bytes, 0)}
	mr := metrics.Result{
		Op: o.name, Bucket: o.bucket, Start: start, Duration: duration,
		TTFB: out.ttfb, Bytes: -1, Size: o.size,
	}
	if err == nil {
		res.Status = responseStatus(out.metadata)
		res.RequestID, _ = awsmiddleware.GetRequestIDMetadata(out.metadata)
		mr.Bytes = out.bytes
	} else {
		info := client.Classify(err)
		if vuCtx.Err() != nil {
			info.Kind = client.KindCanceled
		}
		if info.RequestID == "" {
			info.RequestID, _ = awsmiddleware.GetRequestIDMetadata(out.metadata)
		}
		if info.Status == 0 {
			info.Status = responseStatus(out.metadata)
		}
		res.Status = info.Status
		res.RequestID = info.RequestID
		res.ErrorKind = info.Kind
		res.ErrorCode = info.Code
		res.Error = err.Error()
		mr.ErrorKind, mr.ErrorCode, mr.Status = info.Kind, info.Code, info.Status
		if info.Kind != client.KindCanceled {
			c.warn(o, res)
		}
	}
	c.emit(mr)
	return res
}

func responseStatus(md middleware.Metadata) int {
	if resp, ok := awsmiddleware.GetRawResponse(md).(*smithyhttp.Response); ok && resp != nil {
		return resp.StatusCode
	}
	return 0
}

func (c *Client) emit(r metrics.Result) {
	state := c.vu.State()
	if state == nil {
		return
	}
	opts := metrics.TagOptions{
		Bucket:    c.cfg.HasTag(client.TagBucket),
		SizeClass: c.cfg.HasTag(client.TagSizeClass),
	}
	if samples := c.root.metrics.Samples(state.Tags.GetCurrentValues(), opts, r); samples != nil {
		k6metrics.PushIfNotDone(c.vu.Context(), state.Samples, samples)
	}
}

// maxWarningsPerOp is the number of warning logs per operation type in a process.
const maxWarningsPerOp = 5

type warningLimiter struct {
	counts sync.Map // op name -> *atomic.Int64
}

func (w *warningLimiter) allow(op string) (n int64, ok bool) {
	v, _ := w.counts.LoadOrStore(op, new(atomic.Int64))
	n = v.(*atomic.Int64).Add(1)
	return n, n <= maxWarningsPerOp
}

func (c *Client) warn(o op, res *Result) {
	state := c.vu.State()
	if state == nil {
		return
	}
	n, ok := c.root.warnings.allow(o.name)
	if !ok {
		return
	}
	l := state.Logger.WithField("op", o.name).
		WithField("bucket", o.bucket).
		WithField("errorKind", res.ErrorKind).
		WithField("status", res.Status)
	if o.key != "" {
		l = l.WithField("key", o.key)
	}
	if res.ErrorCode != "" {
		l = l.WithField("errorCode", res.ErrorCode)
	}
	if res.RequestID != "" {
		l = l.WithField("requestId", res.RequestID)
	}
	msg := "S3 operation failed: " + res.Error
	if n == maxWarningsPerOp {
		msg += fmt.Sprintf(" (further warnings for %s are suppressed)", o.name)
	}
	l.Warn(msg)
}

// PutObject uploads an object of the given size (a number, a string with a
// unit or a size distribution) with a body generated on the Go side.
func (c *Client) PutObject(bucket, key string, size sobek.Value) *Result {
	rt := c.vu.Runtime()
	requireName(rt, "bucket", bucket)
	requireName(rt, "key", key)
	n := c.sampleSize(size)
	body, err := data.NewReader(data.SharedBuffer(), c.rng.Int64N(data.BufferSize), n)
	if err != nil {
		c.throw(err)
	}
	sdk := c.client()
	return c.run(op{name: metrics.OpPut, bucket: bucket, key: key, size: n}, func(ctx context.Context) (opOutput, error) {
		out, err := sdk.PutObject(ctx, &awss3.PutObjectInput{
			Bucket:            aws.String(bucket),
			Key:               aws.String(key),
			Body:              body,
			ContentLength:     aws.Int64(n),
			ChecksumAlgorithm: c.cfg.ChecksumAlgorithmType(),
		}, c.cfg.PayloadOptions()...)
		if err != nil {
			return noBody, err
		}
		return opOutput{metadata: out.ResultMetadata, bytes: n, size: -1}, nil
	})
}

// GetObject downloads an object and discards the body.
func (c *Client) GetObject(bucket, key string) *Result {
	rt := c.vu.Runtime()
	requireName(rt, "bucket", bucket)
	requireName(rt, "key", key)
	sdk := c.client()
	return c.run(op{name: metrics.OpGet, bucket: bucket, key: key, size: -1}, func(ctx context.Context) (opOutput, error) {
		start := time.Now()
		out, err := sdk.GetObject(ctx, &awss3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return noBody, err
		}
		defer out.Body.Close()
		res := opOutput{metadata: out.ResultMetadata, ttfb: time.Since(start), size: aws.ToInt64(out.ContentLength)}
		if out.ContentLength == nil {
			res.size = -1
		}
		res.bytes, err = io.Copy(io.Discard, out.Body)
		return res, err
	})
}

// HeadObject retrieves the object metadata.
func (c *Client) HeadObject(bucket, key string) *Result {
	rt := c.vu.Runtime()
	requireName(rt, "bucket", bucket)
	requireName(rt, "key", key)
	sdk := c.client()
	return c.run(op{name: metrics.OpHead, bucket: bucket, key: key, size: -1}, func(ctx context.Context) (opOutput, error) {
		out, err := sdk.HeadObject(ctx, &awss3.HeadObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return noBody, err
		}
		return opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1}, nil
	})
}

// DeleteObject deletes an object.
func (c *Client) DeleteObject(bucket, key string) *Result {
	rt := c.vu.Runtime()
	requireName(rt, "bucket", bucket)
	requireName(rt, "key", key)
	sdk := c.client()
	return c.run(op{name: metrics.OpDelete, bucket: bucket, key: key, size: -1}, func(ctx context.Context) (opOutput, error) {
		out, err := sdk.DeleteObject(ctx, &awss3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return noBody, err
		}
		return opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1}, nil
	})
}

func (c *Client) sampleSize(v sobek.Value) int64 {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		c.throw(errors.New("size is required"))
	}
	sizer, err := data.ParseSizer(v.Export())
	if err != nil {
		c.throw(fmt.Errorf("invalid size: %w", err))
	}
	return sizer.Next(c.rng)
}
