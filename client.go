package s3

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

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
	"go.k6.io/k6/v2/lib"
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
	// Count is the number of objects listed, uploaded or deleted by
	// listObjects, preload and deletePrefix.
	Count int64 `js:"count"`
	// Failed is the number of failed objects in preload and deletePrefix.
	Failed int64 `js:"failed"`
}

// setError copies the error of r into res unless res already has one.
func (res *Result) setError(r *Result) {
	if res.ErrorKind != "" {
		return
	}
	res.Status = r.Status
	res.RequestID = r.RequestID
	res.ErrorKind = r.ErrorKind
	res.ErrorCode = r.ErrorCode
	res.Error = r.Error
}

// Client is the JavaScript s3.Client. Each VU has its own instance.
type Client struct {
	vu   modules.VU
	root *RootModule
	cfg  client.Config
	rng  *rand.Rand

	sdkOnce sync.Once
	sdk     *awss3.Client

	// parallel is the SDK client for operations that send requests
	// concurrently, with an HTTP transport sized for parallelConcurrency.
	parallel            *awss3.Client
	parallelTransport   *http.Transport
	parallelConcurrency int
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

// vuEnv is the VU state captured on the JavaScript goroutine at the start of
// an operation, so that it can be used from other goroutines.
type vuEnv struct {
	ctx   context.Context
	state *lib.State
	tags  k6metrics.TagsAndMeta
}

// env captures the VU state. It throws in the init context.
func (c *Client) env() vuEnv {
	state := c.vu.State()
	if state == nil {
		c.throw(errors.New("S3 operations cannot be called in the init context"))
	}
	return vuEnv{ctx: c.vu.Context(), state: state, tags: state.Tags.GetCurrentValues()}
}

// client returns the SDK client of the VU, creating it on the first call.
// The SDK client uses the VU's k6 HTTP transport, so that data_sent and
// data_received are recorded and the k6 TLS options apply.
func (c *Client) client(env vuEnv) *awss3.Client {
	c.sdkOnce.Do(func() {
		c.sdk = client.New(c.cfg, &http.Client{Transport: env.state.Transport})
	})
	return c.sdk
}

// parallelClient returns an SDK client for sending up to concurrency
// requests at once.
//
// The VU's k6 transport keeps at most batchPerHost idle connections per
// host, so concurrent requests beyond it would reconnect. The parallel
// client uses a clone of the VU's transport with larger idle connection
// limits. The clone keeps the k6 dialer, so data_sent and data_received are
// still recorded and the TLS settings apply. The clone is kept to reuse its
// connections and replaced when a higher concurrency is requested.
func (c *Client) parallelClient(env vuEnv, concurrency int) *awss3.Client {
	if concurrency <= 1 {
		return c.client(env)
	}
	if c.parallel != nil && concurrency <= c.parallelConcurrency {
		return c.parallel
	}
	base, ok := env.state.Transport.(*http.Transport)
	if !ok {
		env.state.Logger.Warnf("xk6-s3: the k6 transport is %T, not *http.Transport; "+
			"concurrent requests use the VU transport as is", env.state.Transport)
		c.parallel, c.parallelConcurrency = c.client(env), 1<<30
		return c.parallel
	}
	t := base.Clone()
	t.MaxIdleConnsPerHost = max(t.MaxIdleConnsPerHost, concurrency)
	if t.MaxIdleConns > 0 {
		t.MaxIdleConns = max(t.MaxIdleConns, concurrency)
	}
	if c.parallelTransport != nil {
		c.parallelTransport.CloseIdleConnections()
	}
	c.parallel = client.New(c.cfg, &http.Client{Transport: t})
	c.parallelTransport = t
	c.parallelConcurrency = concurrency
	return c.parallel
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
	// noTimeout disables the per-operation timeout for operations that
	// consist of multiple requests, which have their own timeouts.
	noTimeout bool
}

// opOutput is what an operation function reports to measure.
type opOutput struct {
	metadata middleware.Metadata
	// bytes is the number of body bytes sent or received, or -1 when the
	// operation has no body.
	bytes int64
	ttfb  time.Duration
	// size overrides op.size when it is known only from the response.
	size int64
	// result is the result of a composite operation that has already been
	// filled in (count, failed and the first error).
	result *Result
}

// noBody is the opOutput of a failed operation or an operation without body.
var noBody = opOutput{bytes: -1, size: -1}

// measure runs fn as one measured operation, emits its metrics and returns
// its result. It is safe to call from multiple goroutines.
func (c *Client) measure(env vuEnv, o op, fn func(ctx context.Context) (opOutput, error)) *Result {
	ctx := env.ctx
	if !o.noTimeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}

	start := time.Now()
	out, err := fn(ctx)
	duration := time.Since(start)

	if out.size >= 0 {
		o.size = out.size
	}
	res := out.result
	if res == nil {
		res = &Result{}
	}
	res.OK = err == nil
	res.Bytes = max(out.bytes, 0)
	mr := metrics.Result{
		Op: o.name, Bucket: o.bucket, Start: start, Duration: duration,
		TTFB: out.ttfb, Bytes: -1, Size: o.size,
	}
	if err == nil {
		res.Status = responseStatus(out.metadata)
		res.RequestID, _ = awsmiddleware.GetRequestIDMetadata(out.metadata)
		mr.Bytes = out.bytes
	} else {
		if res.ErrorKind == "" {
			info := client.Classify(err)
			if env.ctx.Err() != nil {
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
		}
		if env.ctx.Err() != nil {
			res.ErrorKind = client.KindCanceled
		}
		mr.ErrorKind, mr.ErrorCode, mr.Status = res.ErrorKind, res.ErrorCode, res.Status
		if res.ErrorKind != client.KindCanceled {
			c.warn(env, o, res)
		}
	}
	c.emit(env, mr)
	return res
}

func responseStatus(md middleware.Metadata) int {
	if resp, ok := awsmiddleware.GetRawResponse(md).(*smithyhttp.Response); ok && resp != nil {
		return resp.StatusCode
	}
	return 0
}

func (c *Client) emit(env vuEnv, r metrics.Result) {
	opts := metrics.TagOptions{
		Bucket:    c.cfg.HasTag(client.TagBucket),
		SizeClass: c.cfg.HasTag(client.TagSizeClass),
	}
	if samples := c.root.metrics.Samples(env.tags, opts, r); samples != nil {
		k6metrics.PushIfNotDone(env.ctx, env.state.Samples, samples)
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

func (c *Client) warn(env vuEnv, o op, res *Result) {
	n, ok := c.root.warnings.allow(o.name)
	if !ok {
		return
	}
	l := env.state.Logger.WithField("op", o.name).
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

// sizer parses a size argument. It throws on invalid sizes.
func (c *Client) sizer(v sobek.Value) data.Sizer {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		c.throw(errors.New("size is required"))
	}
	s, err := data.ParseSizer(v.Export())
	if err != nil {
		c.throw(fmt.Errorf("invalid size: %w", err))
	}
	return s
}

// newBody returns a body of size bytes from a random offset of the shared buffer.
func (c *Client) newBody(size int64) *data.Reader {
	r, err := data.NewReader(data.SharedBuffer(), c.rng.Int64N(data.BufferSize), size)
	if err != nil {
		c.throw(err)
	}
	return r
}

// options exports an optional options object. It throws if v is not an object.
func (c *Client) options(v sobek.Value, keys ...string) map[string]any {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return map[string]any{}
	}
	m, ok := v.Export().(map[string]any)
	if !ok {
		c.throw(errors.New("options must be an object"))
	}
	for k := range m {
		if !slices.Contains(keys, k) {
			c.throw(fmt.Errorf("unknown option %q (available: %v)", k, keys))
		}
	}
	return m
}

// intOption returns a positive integer option or def.
func (c *Client) intOption(m map[string]any, name string, def int64) int64 {
	v, ok := m[name]
	if !ok || v == nil {
		return def
	}
	var n int64
	switch t := v.(type) {
	case int64:
		n = t
	case float64:
		if t != float64(int64(t)) {
			c.throw(fmt.Errorf("%s must be an integer: %v", name, v))
		}
		n = int64(t)
	default:
		c.throw(fmt.Errorf("%s must be an integer: %v", name, v))
	}
	if n < 0 {
		c.throw(fmt.Errorf("%s must not be negative: %d", name, n))
	}
	return n
}

// sizeOption returns a byte size option or def.
func (c *Client) sizeOption(m map[string]any, name string, def int64) int64 {
	v, ok := m[name]
	if !ok || v == nil {
		return def
	}
	s, err := data.ParseSizer(v)
	if err != nil {
		c.throw(fmt.Errorf("%s: %w", name, err))
	}
	f, ok := s.(data.Fixed)
	if !ok {
		c.throw(fmt.Errorf("%s must be a fixed size", name))
	}
	return int64(f)
}
