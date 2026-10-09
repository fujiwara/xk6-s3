package s3

import (
	"context"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fujiwara/xk6-s3/internal/data"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"github.com/grafana/sobek"
)

// PutObject uploads an object of the given size (a number, a string with a
// unit or a size distribution) with a body generated on the Go side.
func (c *Client) PutObject(bucket, key string, size sobek.Value) *Result {
	rt := c.vu.Runtime()
	requireName(rt, "bucket", bucket)
	requireName(rt, "key", key)
	n := c.sizer(size).Next(c.rng)
	env := c.env()
	return c.putObject(env, c.client(env), bucket, key, c.newBody(n))
}

// putObject is safe to call from multiple goroutines.
func (c *Client) putObject(env vuEnv, sdk *awss3.Client, bucket, key string, body *data.Reader) *Result {
	n := body.Size()
	return c.measure(env, op{name: metrics.OpPut, bucket: bucket, key: key, size: n}, func(ctx context.Context) (opOutput, error) {
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
	env := c.env()
	sdk := c.client(env)
	return c.measure(env, op{name: metrics.OpGet, bucket: bucket, key: key, size: -1}, func(ctx context.Context) (opOutput, error) {
		start := time.Now()
		out, err := sdk.GetObject(ctx, &awss3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return noBody, err
		}
		defer out.Body.Close()
		res := opOutput{metadata: out.ResultMetadata, ttfb: time.Since(start), size: -1}
		if out.ContentLength != nil {
			res.size = *out.ContentLength
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
	env := c.env()
	sdk := c.client(env)
	return c.measure(env, op{name: metrics.OpHead, bucket: bucket, key: key, size: -1}, func(ctx context.Context) (opOutput, error) {
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
	env := c.env()
	return c.deleteObject(env, c.client(env), bucket, key)
}

// deleteObject is safe to call from multiple goroutines.
func (c *Client) deleteObject(env vuEnv, sdk *awss3.Client, bucket, key string) *Result {
	return c.measure(env, op{name: metrics.OpDelete, bucket: bucket, key: key, size: -1}, func(ctx context.Context) (opOutput, error) {
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
