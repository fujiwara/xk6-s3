# xk6-s3

A [k6](https://github.com/grafana/k6) extension for load testing S3-compatible object storage.

xk6-s3 is a thin wrapper around [aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2). Object bodies are generated and discarded on the Go side, so the load generator does not spend CPU and memory on large payloads in JavaScript. Results are recorded as k6 metrics, so thresholds, the end-of-test summary, and outputs such as `-o opentelemetry` work as usual.

> [!WARNING]
> This extension is under initial development. The JavaScript API is not stable until v1.0. See [docs/initial-design.md](docs/initial-design.md) for the planned design.

## Build

Build a k6 binary that includes this extension with [xk6](https://github.com/grafana/xk6).

### With Go

```console
$ go install go.k6.io/xk6/cmd/xk6@latest
$ xk6 build --with github.com/fujiwara/xk6-s3@latest
```

### With Docker

```console
$ docker run --rm -u "$(id -u):$(id -g)" -v "${PWD}:/xk6" \
    grafana/xk6 build --with github.com/fujiwara/xk6-s3@latest
```

### From a local checkout

```console
$ make build
```

## Usage

```js
import s3 from "k6/x/s3";
import { check } from "k6";

const client = new s3.Client({
  endpoint: "http://localhost:7070",
  // accessKey / secretKey default to AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
});

export const options = {
  thresholds: {
    s3_op_errors: ["rate<0.01"],
    "s3_op_duration{op:get}": ["p(95)<200"],
  },
};

export default function () {
  const key = `${s3.runId()}/data/${__VU}-${__ITER}`;
  const put = client.putObject("bucket", key, "1MiB");
  check(put, { "put ok": (r) => r.ok });
  const get = client.getObject("bucket", key);
  check(get, { "get ok": (r) => r.ok });
  client.deleteObject("bucket", key);
}
```

### `new s3.Client(config)`

Create clients in the init context. The configuration is validated there, and the underlying SDK client is created in each VU on its first operation. Requests are sent through the VU's k6 HTTP transport, so `data_sent` / `data_received` are recorded and the k6 TLS and connection options (`insecureSkipTLSVerify`, `tlsAuth`, `noConnectionReuse`, ...) apply.

| Key | Default | Description |
| --- | --- | --- |
| `endpoint` | (required) | Endpoint URL (`http://` or `https://`) |
| `region` | `us-east-1` | Region for signing |
| `accessKey` / `secretKey` / `sessionToken` | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | Static credentials |
| `pathStyle` | `true` | Use path-style addressing |
| `checksum` | `when_supported` | `when_supported` / `when_required`. Applied to both request checksum calculation and response checksum validation |
| `checksumAlgorithm` | (SDK default, CRC32) | `CRC32` / `CRC32C` / `CRC64NVME` / `SHA1` / `SHA256`. Cannot be used with `when_required` |
| `payloadSigning` | `auto` | `auto` / `unsigned` / `signed`. See [Request format](#request-format) |
| `timeout` | `60s` | Timeout per operation. A duration string or milliseconds |
| `maxAttempts` | `1` | Maximum attempts including SDK retries. Retries are disabled by default |
| `tags` | `[]` | Optional metric tags to enable: `bucket`, `size_class` |

Unknown keys are rejected.

### Operations

| Method | Description |
| --- | --- |
| `putObject(bucket, key, size)` | Upload an object with a body generated on the Go side |
| `putObjectMultipart(bucket, key, size, options?)` | Upload an object with a multipart upload. The upload is aborted on failure |
| `getObject(bucket, key)` | Download an object. The body is discarded on the Go side |
| `headObject(bucket, key)` | Get object metadata |
| `deleteObject(bucket, key)` | Delete an object |
| `listObjects(bucket, prefix, options?)` | List objects with ListObjectsV2 |
| `createBucket(bucket)` / `deleteBucket(bucket)` | Create or delete a bucket |
| `preload(bucket, prefix, count, size, options?)` | Upload `count` objects named `prefix + "0"`, ... concurrently, for `setup()` |
| `deletePrefix(bucket, prefix, options?)` | Delete all objects under a non-empty prefix concurrently, for `teardown()` |

Options:

- `putObjectMultipart`: `partSize` (default `"5MiB"`), `concurrency` (default `5`). The defaults follow the aws-sdk-go-v2 upload manager. As the upload manager does, the checksum algorithm is set on CreateMultipartUpload and UploadPart when `checksum` is `when_supported`, and the part checksums are sent with CompleteMultipartUpload. The `timeout` applies to each request.
- `listObjects`: `maxKeys` (default: server default), `maxPages` (default `1`, `0` for all pages). Each request is measured as a `list` operation.
- `preload` / `deletePrefix`: `concurrency` (default `16`). Each object operation is measured as `put` / `delete`.

Operations that send requests concurrently use a copy of the VU's k6 transport with idle connection limits raised to the concurrency, so that connections are reused beyond `batchPerHost`.

S3 and network errors do not throw. Each operation returns a result object, so that an error does not abort the iteration. Only script errors (invalid arguments, calls in the init context) throw.

| Field | Description |
| --- | --- |
| `ok` | Whether the operation succeeded |
| `status` | HTTP status code (0 for network errors) |
| `bytes` | Body bytes sent or received |
| `errorKind` | `s3`, `http`, `timeout`, `network`, `canceled` or `other`. Empty on success |
| `errorCode` | S3 error code such as `NoSuchKey` or `SlowDown` (for `s3` errors) |
| `error` | Error message. Empty on success |
| `requestId` | `x-amz-request-id` of the response |
| `count` | Objects listed, uploaded or deleted (`listObjects`, `preload`, `deletePrefix`) |
| `failed` | Objects that failed (`preload`, `deletePrefix`) |

`preload` and `deletePrefix` are `ok` when no object failed, and report the first error.

The first few failures of each operation type are logged as warnings with the request ID.

### Sizes

`size` accepts:

- a number of bytes, or a string with a unit: `"512KiB"`, `"1.5MiB"`, `"10MB"` (`KiB`, `MiB`, ... are powers of 1024, `KB`, `MB`, ... are powers of 1000)
- `{ dist: "uniform", min, max }`
- `{ dist: "lognormal", median, sigma }` (`sigma` of the underlying normal distribution, capped at 5TiB)
- `{ dist: "choice", values: [{ size, weight }, ...] }`

### `runId()`

Returns the run ID, which is intended to be used as a key prefix such as `<runId>/<dataset>/<n>`. All VUs in a k6 process share the same run ID. It is available in the init context.

The run ID is the value of the `XK6_S3_RUN_ID` environment variable if set, otherwise a random 8-character string generated at startup. To share the run ID across multiple k6 processes, or to correlate it with metrics, pass the same value to `XK6_S3_RUN_ID` and `--tag test-id=`.

```console
$ XK6_S3_RUN_ID=run-001 ./k6 run --tag test-id=run-001 script.js
```

## Metrics

| Metric | Type | Description |
| --- | --- | --- |
| `s3_op_duration` | Trend | Duration of an operation, including the body transfer |
| `s3_op_ttfb` | Trend | Time to the response headers (GET only) |
| `s3_op_bytes` | Counter | Body bytes sent (PUT) or received (GET) by successful operations |
| `s3_op_errors` | Rate | Failure rate of operations |
| `s3_errors` | Counter | Errors, tagged with `error_kind`, `error_code` and `status` |

All metrics are tagged with `op` (`put`, `get`, `head`, `delete`, `list`, `create_bucket`, `delete_bucket`, `put_multipart` for a whole multipart upload, `upload_part` for each part) and the VU tags such as `scenario` and `group`. `bucket` and `size_class` (`<4KiB`, `<64KiB`, `<1MiB`, `<16MiB`, `<128MiB`, `>=128MiB`) are added when enabled by the `tags` option. Operations canceled by the end of the test are not recorded.

`data_sent` / `data_received` are emitted by k6 at the end of each iteration. Use `s3_op_bytes` for throughput over time.

## Request format

The `checksum` and `payloadSigning` options change how PutObject and UploadPart requests are sent, which also changes how the server receives and verifies them. Choose them to reproduce the clients you expect, not to reduce the load generator's CPU usage. The defaults reproduce the current aws-sdk-go-v2 defaults.

| Scheme | `checksum` | `payloadSigning` | `x-amz-content-sha256` | Body | Checksum |
| --- | --- | --- | --- | --- | --- |
| HTTPS | `when_supported` | `auto` / `unsigned` | `STREAMING-UNSIGNED-PAYLOAD-TRAILER` | `aws-chunked` | trailer |
| HTTPS | `when_supported` | `signed` | SHA256 of the body | as is | header |
| HTTPS | `when_required` | `auto` / `unsigned` | `UNSIGNED-PAYLOAD` | as is | none |
| HTTPS | `when_required` | `signed` | SHA256 of the body | as is | none |
| HTTP | `when_supported` | `auto` / `signed` | SHA256 of the body | as is | header |
| HTTP | `when_supported` | `unsigned` | `UNSIGNED-PAYLOAD` | as is | header |
| HTTP | `when_required` | `auto` / `signed` | SHA256 of the body | as is | none |
| HTTP | `when_required` | `unsigned` | `UNSIGNED-PAYLOAD` | as is | none |

Chunk-signed uploads (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`) are not supported by aws-sdk-go-v2 and cannot be reproduced.

Computing signatures and checksums uses the load generator's CPU. Monitor the CPU usage of the load generator to make sure it is not saturated.

## Compatibility

| xk6-s3 | k6 |
| --- | --- |
| main | v2.3.0 |

## LICENSE

The source code of xk6-s3 is licensed under the [Apache License 2.0](LICENSE).

k6 is licensed under the [GNU AGPL v3](https://github.com/grafana/k6/blob/master/LICENSE.md). A k6 binary built with this extension is subject to the terms of the AGPL v3.

## Author

Copyright (c) 2026 FUJIWARA Shunichiro
