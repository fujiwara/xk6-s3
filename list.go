package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"github.com/grafana/sobek"
)

// ListObjectsOptions are the options of listObjects.
type ListObjectsOptions struct {
	// MaxKeys is the MaxKeys of each request. The server default (1000 on
	// S3) if 0 or not set. It limits the objects and the common prefixes
	// together.
	MaxKeys int64 `js:"maxKeys"`
	// MaxPages is the number of pages to fetch, 0 for all pages.
	MaxPages int64 `js:"maxPages" default:"1"`
	// Delimiter is the Delimiter of each request. Without it, all objects
	// under the prefix are listed. With it, the objects in the next levels
	// are grouped into common prefixes.
	Delimiter string `js:"delimiter"`
	// Keys returns the listed keys in keys and the common prefixes in
	// prefixes of the result.
	Keys bool `js:"keys"`
}

// ListObjects lists objects under the prefix with ListObjectsV2. Each
// request (page) is measured as a list operation.
//
// The result has the total number of listed objects in count, and the
// status, request ID and error of the last request. Common prefixes are not
// counted.
//
// For example, to get the listed objects in order:
//
//	const r = client.listObjects(bucket, prefix, { maxPages: 0, keys: true });
//	for (const key of r.keys) {
//	  client.getObject(bucket, key);
//	}
func (c *Client) ListObjects(bucket, prefix string, options sobek.Value) *Result {
	requireName(c.vu.Runtime(), "bucket", bucket)
	var opts ListObjectsOptions
	c.decodeOptions(options, &opts)
	maxKeys, maxPages, delimiter, withKeys := opts.MaxKeys, opts.MaxPages, opts.Delimiter, opts.Keys
	env := c.env()
	sdk := c.client(env)

	total := &Result{}
	var keys, prefixes []string
	if withKeys {
		keys, prefixes = []string{}, []string{}
	}
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
			if delimiter != "" {
				in.Delimiter = aws.String(delimiter)
			}
			if maxKeys > 0 {
				in.MaxKeys = aws.Int32(int32(min(maxKeys, 1<<31-1)))
			}
			out, err := sdk.ListObjectsV2(ctx, in)
			if err != nil {
				return noBody, err
			}
			count = int64(len(out.Contents))
			if withKeys {
				for _, obj := range out.Contents {
					keys = append(keys, aws.ToString(obj.Key))
				}
				for _, p := range out.CommonPrefixes {
					prefixes = append(prefixes, aws.ToString(p.Prefix))
				}
			}
			if aws.ToBool(out.IsTruncated) {
				next = out.NextContinuationToken
			}
			return opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1}, nil
		})
		count += total.Count
		*total = *res
		total.Count = count
		if withKeys {
			total.Keys = keys
			total.Prefixes = prefixes
		}
		if !res.OK || next == nil {
			break
		}
		token = next
	}
	return total
}
