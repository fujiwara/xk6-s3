package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/fujiwara/xk6-s3/internal/client"
	"github.com/fujiwara/xk6-s3/internal/metrics"
)

// CreateBucket creates a bucket. For regions other than us-east-1, the
// region is sent as the LocationConstraint.
func (c *Client) CreateBucket(bucket string) *Result {
	requireName(c.vu.Runtime(), "bucket", bucket)
	env := c.env()
	sdk := c.client(env)
	return c.measure(env, op{name: metrics.OpCreateBucket, bucket: bucket, size: -1}, func(ctx context.Context) (opOutput, error) {
		in := &awss3.CreateBucketInput{Bucket: aws.String(bucket)}
		// us-east-1 must not be sent as a LocationConstraint.
		if c.cfg.Region != client.DefaultRegion {
			in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
				LocationConstraint: types.BucketLocationConstraint(c.cfg.Region),
			}
		}
		out, err := sdk.CreateBucket(ctx, in)
		if err != nil {
			return noBody, err
		}
		return opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1}, nil
	})
}

// DeleteBucket deletes an empty bucket.
func (c *Client) DeleteBucket(bucket string) *Result {
	requireName(c.vu.Runtime(), "bucket", bucket)
	env := c.env()
	sdk := c.client(env)
	return c.measure(env, op{name: metrics.OpDeleteBucket, bucket: bucket, size: -1}, func(ctx context.Context) (opOutput, error) {
		out, err := sdk.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(bucket)})
		if err != nil {
			return noBody, err
		}
		return opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1}, nil
	})
}
