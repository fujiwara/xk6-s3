package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"github.com/grafana/sobek"
)

// ListObjects lists objects with ListObjectsV2. Each request (page) is
// measured as a list operation. Options:
//
//   - maxKeys: MaxKeys of each request (default: server default, 1000 on S3)
//   - maxPages: the number of pages to fetch, 0 for all (default: 1)
//
// The result has the total number of listed objects in count, and the
// status, request ID and error of the last request.
func (c *Client) ListObjects(bucket, prefix string, options sobek.Value) *Result {
	requireName(c.vu.Runtime(), "bucket", bucket)
	opts := c.options(options, "maxKeys", "maxPages")
	maxKeys := c.intOption(opts, "maxKeys", 0)
	maxPages := c.intOption(opts, "maxPages", 1)
	env := c.env()
	sdk := c.client(env)

	total := &Result{}
	var token *string
	for page := int64(0); maxPages == 0 || page < maxPages; page++ {
		var next *string
		var count int64
		res := c.measure(env, op{name: metrics.OpList, bucket: bucket, size: -1}, func(ctx context.Context) (opOutput, error) {
			in := &awss3.ListObjectsV2Input{
				Bucket:            aws.String(bucket),
				ContinuationToken: token,
			}
			if prefix != "" {
				in.Prefix = aws.String(prefix)
			}
			if maxKeys > 0 {
				in.MaxKeys = aws.Int32(int32(min(maxKeys, 1<<31-1)))
			}
			out, err := sdk.ListObjectsV2(ctx, in)
			if err != nil {
				return noBody, err
			}
			count = int64(len(out.Contents))
			if aws.ToBool(out.IsTruncated) {
				next = out.NextContinuationToken
			}
			return opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1}, nil
		})
		count += total.Count
		*total = *res
		total.Count = count
		if !res.OK || next == nil {
			break
		}
		token = next
	}
	return total
}
