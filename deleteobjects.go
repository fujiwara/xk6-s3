package s3

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/fujiwara/xk6-s3/internal/client"
	"github.com/fujiwara/xk6-s3/internal/metrics"
	"github.com/grafana/sobek"
)

// maxDeleteObjects is the maximum number of keys in a DeleteObjects request.
const maxDeleteObjects = 1000

// DeleteObjects deletes up to 1000 objects with one DeleteObjects request,
// measured as a delete_objects operation. Options:
//
//   - quiet: request the quiet mode, in which the response lists only the
//     failed keys (default: false, as the S3 API and minio-go)
//
// The result has the number of deleted objects in count and the number of
// failed objects in failed. When any object fails, the result is not ok and
// has the first per-object error.
func (c *Client) DeleteObjects(bucket string, keys []string, options sobek.Value) *Result {
	rt := c.vu.Runtime()
	requireName(rt, "bucket", bucket)
	if len(keys) == 0 || len(keys) > maxDeleteObjects {
		c.throw(fmt.Errorf("keys must have 1 to %d keys: %d", maxDeleteObjects, len(keys)))
	}
	objects := make([]types.ObjectIdentifier, len(keys))
	for i, key := range keys {
		requireName(rt, fmt.Sprintf("keys[%d]", i), key)
		objects[i] = types.ObjectIdentifier{Key: aws.String(key)}
	}
	opts := c.options(options, "quiet")
	quiet := false
	if v, ok := opts["quiet"]; ok && v != nil {
		b, ok := v.(bool)
		if !ok {
			c.throw(errors.New("quiet must be a boolean"))
		}
		quiet = b
	}
	env := c.env()
	sdk := c.client(env)
	return c.measure(env, op{name: metrics.OpDeleteObjects, bucket: bucket, size: -1}, func(ctx context.Context) (opOutput, error) {
		out, err := sdk.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(quiet)},
		})
		if err != nil {
			return noBody, err
		}
		res := &Result{Failed: int64(len(out.Errors))}
		if quiet {
			res.Count = int64(len(keys) - len(out.Errors))
		} else {
			res.Count = int64(len(out.Deleted))
		}
		output := opOutput{metadata: out.ResultMetadata, bytes: -1, size: -1, result: res}
		if len(out.Errors) == 0 {
			return output, nil
		}
		// The request succeeded but some objects were not deleted.
		first := out.Errors[0]
		res.Status = responseStatus(out.ResultMetadata)
		res.RequestID, _ = awsmiddleware.GetRequestIDMetadata(out.ResultMetadata)
		res.ErrorKind = client.KindS3
		res.ErrorCode = aws.ToString(first.Code)
		res.Error = fmt.Sprintf("%d of %d objects were not deleted: %s: %s (key %s)",
			len(out.Errors), len(keys), aws.ToString(first.Code), aws.ToString(first.Message), aws.ToString(first.Key))
		return output, errors.New(res.Error)
	})
}
