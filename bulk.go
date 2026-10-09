package s3

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fujiwara/xk6-s3/internal/client"
	"github.com/fujiwara/xk6-s3/internal/data"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"github.com/grafana/sobek"
)

// defaultBulkConcurrency is the default concurrency of preload and deletePrefix.
const defaultBulkConcurrency = 16

// bulk runs object operations concurrently and aggregates their results.
type bulk struct {
	mu  sync.Mutex
	res Result
}

func (b *bulk) add(r *Result) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.OK {
		b.res.Count++
		b.res.Bytes += r.Bytes
		return
	}
	b.res.Failed++
	b.res.setError(r)
}

// result returns the aggregated result. It is OK when no operation failed
// and the whole run was not canceled.
func (b *bulk) result(ctx context.Context) *Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := b.res
	if ctx.Err() != nil && res.ErrorKind == "" {
		res.ErrorKind = client.KindCanceled
		res.Error = ctx.Err().Error()
	}
	res.OK = res.ErrorKind == ""
	return &res
}

// workers starts n workers calling fn for each job sent to the returned
// channel. Close the channel and call wait to finish.
func workers[T any](n int, fn func(T)) (jobs chan<- T, wait func()) {
	ch := make(chan T)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			for j := range ch {
				fn(j)
			}
		})
	}
	return ch, func() {
		close(ch)
		wg.Wait()
	}
}

// send sends j to jobs unless ctx is done.
func send[T any](ctx context.Context, jobs chan<- T, j T) bool {
	select {
	case jobs <- j:
		return true
	case <-ctx.Done():
		return false
	}
}

// Preload uploads count objects named prefix + "0" ... prefix + (count-1)
// with the given size concurrently, for use in setup(). Each upload is
// measured as a put operation. Options:
//
//   - concurrency: the number of concurrent uploads (default: 16)
//
// The result has the number of uploaded objects in count, the number of
// failed objects in failed, the total bytes in bytes and the first error.
func (c *Client) Preload(bucket, prefix string, count int64, size, options sobek.Value) *Result {
	requireName(c.vu.Runtime(), "bucket", bucket)
	if count < 0 {
		c.throw(errors.New("count must not be negative"))
	}
	opts := c.options(options, "concurrency")
	concurrency := c.intOption(opts, "concurrency", defaultBulkConcurrency)
	if concurrency <= 0 {
		c.throw(errors.New("concurrency must be positive"))
	}
	sizer := c.sizer(size)
	env := c.env()
	sdk := c.parallelClient(env, int(concurrency))

	type job struct {
		key  string
		body *data.Reader
	}
	var b bulk
	jobs, wait := workers(int(min(concurrency, max(count, 1))), func(j job) {
		b.add(c.putObject(env, sdk, bucket, j.key, j.body))
	})
	for i := range count {
		// Sizes and offsets are sampled here because c.rng is not goroutine safe.
		j := job{key: prefix + strconv.FormatInt(i, 10), body: c.newBody(sizer.Next(c.rng))}
		if !send(env.ctx, jobs, j) {
			break
		}
	}
	wait()
	return b.result(env.ctx)
}

// DeletePrefix deletes all objects whose keys start with prefix
// concurrently, for use in teardown(). The prefix must not be empty. Each
// ListObjectsV2 request is measured as a list operation and each deletion
// as a delete operation. Options:
//
//   - concurrency: the number of concurrent deletions (default: 16)
//
// The result has the number of deleted objects in count, the number of
// failed objects in failed and the first error.
func (c *Client) DeletePrefix(bucket, prefix string, options sobek.Value) *Result {
	requireName(c.vu.Runtime(), "bucket", bucket)
	requireName(c.vu.Runtime(), "prefix", prefix)
	opts := c.options(options, "concurrency")
	concurrency := c.intOption(opts, "concurrency", defaultBulkConcurrency)
	if concurrency <= 0 {
		c.throw(errors.New("concurrency must be positive"))
	}
	env := c.env()
	sdk := c.parallelClient(env, int(concurrency))

	var b bulk
	jobs, wait := workers(int(concurrency), func(key string) {
		b.add(c.deleteObject(env, sdk, bucket, key))
	})
	var token *string
	for {
		var keys []string
		var next *string
		res := c.measure(env, op{name: metrics.OpList, bucket: bucket, size: -1}, func(ctx context.Context) (opOutput, error) {
			out, err := sdk.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
				Bucket:            aws.String(bucket),
				Prefix:            aws.String(prefix),
				ContinuationToken: token,
			})
			if err != nil {
				return noBody, err
			}
			for _, o := range out.Contents {
				keys = append(keys, aws.ToString(o.Key))
			}
			if aws.ToBool(out.IsTruncated) {
				next = out.NextContinuationToken
			}
			return opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1}, nil
		})
		if !res.OK {
			b.mu.Lock()
			b.res.setError(res)
			b.mu.Unlock()
			break
		}
		canceled := false
		for _, key := range keys {
			if !send(env.ctx, jobs, key) {
				canceled = true
				break
			}
		}
		if canceled || next == nil {
			break
		}
		token = next
	}
	wait()
	return b.result(env.ctx)
}
