# xk6-s3

Load test S3-compatible object storage with [k6](https://github.com/grafana/k6) scenarios, with server measurements on par with [warp](https://github.com/minio/warp).

## Why xk6-s3

- **Write the workload, not just pick a benchmark.** Express the access patterns of your application as a k6 script: multiple workloads at once, any executor (`ramping-vus`, `constant-arrival-rate`, ...), and pass/fail thresholds for CI. For example, check that small reads keep their p99 latency while large uploads run in the background.
- **Server numbers on par with warp.** With the same request format, the latency measured by xk6-s3 is within about 0.1ms per request of warp ([details](#comparison-with-warp)). The standard warp benchmarks `put`, `get`, `mixed`, `multipart-put`, `stat`, `list` and `delete` are ready as [examples](#examples).
- **Send what real clients send.** By default, requests have the same format as the current AWS SDKs (for example, `aws-chunked` uploads with trailing checksums over HTTPS), and can be switched to the formats of other clients. Signature and checksum handling of the server is exercised as in production, and compatibility issues surface as errors. All formats are tested against a real S3-compatible server.
- **Large objects without a heavy load generator.** Object bodies are generated and discarded on the Go side, not in JavaScript, so large objects do not consume the CPU and memory of the load generator.
- **The k6 ecosystem.** Metrics go to the end-of-test summary, thresholds and k6 outputs (OpenTelemetry, Prometheus, JSON, ...). S3 tests can be combined with `k6/http` in the same script.

## Quick start

Build a k6 binary with this extension (requires Go) and run an example against your storage:

```console
$ git clone https://github.com/fujiwara/xk6-s3.git && cd xk6-s3
$ make build
$ export AWS_ENDPOINT_URL_S3=http://localhost:7070 AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
$ VUS=32 DURATION=1m SIZE=1MiB OBJECTS=1000 ./k6 run examples/get.js
```

The examples create the bucket `xk6-s3` if missing and delete the objects they upload. See [Build](#build) for other ways to build.

> [!WARNING]
> This extension is under initial development. The JavaScript API may change until v1.0.

## Write your own scenario

Does the p99 latency of small reads stay under 50ms while large multipart uploads run in the background? With k6 scenarios and thresholds:

```js
import s3 from "k6/x/s3";

const client = new s3.Client({}); // AWS_ENDPOINT_URL_S3, AWS_ACCESS_KEY_ID, ... from the environment
const hot = `${s3.runId()}/hot/`;

export const options = {
  scenarios: {
    // large uploads in the background
    uploads: { executor: "constant-vus", vus: 8, duration: "5m", exec: "upload" },
    // small reads at a fixed rate
    reads: {
      executor: "constant-arrival-rate", rate: 500, timeUnit: "1s",
      duration: "5m", preAllocatedVUs: 50, exec: "read",
    },
  },
  thresholds: {
    "s3_op_duration{scenario:reads}": ["p(99)<50"],
    "s3_op_errors{scenario:reads}": ["rate<0.001"],
  },
};

export function setup() {
  client.preload("bucket", hot, 1000, "64KiB");
}

export function upload() {
  client.putObjectMultipart("bucket", `${s3.runId()}/bulk/${__VU}-${__ITER}`, "256MiB");
}

export function read() {
  client.getObject("bucket", `${hot}${Math.floor(Math.random() * 1000)}`);
}

export function teardown() {
  client.deletePrefix("bucket", `${s3.runId()}/`);
}
```

k6 exits with a non-zero status when a threshold fails. A runnable version is [examples/slo.js](examples/slo.js).

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
  // endpoint defaults to AWS_ENDPOINT_URL_S3 or AWS_ENDPOINT_URL
  // region defaults to AWS_REGION, AWS_DEFAULT_REGION or us-east-1
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
| `endpoint` | `AWS_ENDPOINT_URL_S3`, then `AWS_ENDPOINT_URL` | Endpoint URL (`http://` or `https://`). Required unless set by the environment variables |
| `region` | `AWS_REGION`, then `AWS_DEFAULT_REGION`, then `us-east-1` | Region for signing. Also sent as the LocationConstraint of `createBucket` unless `us-east-1` |
| `accessKey` / `secretKey` / `sessionToken` | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | Static credentials |
| `pathStyle` | `true` | Use path-style addressing |
| `checksum` | `when_supported` | `when_supported` / `when_required`. Applied to both request checksum calculation and response checksum validation |
| `checksumAlgorithm` | (SDK default, CRC32) | `CRC32` / `CRC32C` / `CRC64NVME` / `SHA1` / `SHA256`. Cannot be used with `when_required` |
| `payloadSigning` | `auto` | `auto` / `unsigned` / `signed`. See [Request format](#request-format) |
| `timeout` | `60s` | Timeout per operation. A duration string or milliseconds |
| `maxAttempts` | `1` | Maximum attempts including SDK retries. Retries are disabled by default |
| `tags` | `[]` | Optional metric tags to enable: `bucket`, `size_class` |

Unknown keys are rejected. The default values from environment variables are read from the environment of the k6 process, not from `-e` / `__ENV`.

### Operations

| Method | Description |
| --- | --- |
| `putObject(bucket, key, size)` | Upload an object with a body generated on the Go side |
| `putObjectMultipart(bucket, key, size, options?)` | Upload an object with a multipart upload. The upload is aborted on failure |
| `getObject(bucket, key)` | Download an object. The body is discarded on the Go side |
| `headObject(bucket, key)` | Get object metadata |
| `deleteObject(bucket, key)` | Delete an object |
| `deleteObjects(bucket, keys, options?)` | Delete up to 1000 objects with one DeleteObjects request |
| `listObjects(bucket, prefix, options?)` | List objects with ListObjectsV2 |
| `createBucket(bucket)` / `deleteBucket(bucket)` | Create or delete a bucket |
| `preload(bucket, prefix, count, size, options?)` | Upload `count` objects named `prefix + "0"`, ... concurrently, for `setup()` |
| `deletePrefix(bucket, prefix, options?)` | Delete all objects under a non-empty prefix concurrently, for `teardown()` |

Options:

- `putObjectMultipart`: `partSize` (default `"5MiB"`), `concurrency` (default `5`). The defaults follow the aws-sdk-go-v2 upload manager. As the upload manager does, the checksum algorithm is set on CreateMultipartUpload and UploadPart when `checksum` is `when_supported`, and the part checksums are sent with CompleteMultipartUpload. The `timeout` applies to each request.
- `deleteObjects`: `quiet` (default `false`, as the S3 API and minio-go). The result is not `ok` when any object was not deleted, and has the first per-object error.
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
| `count` | Objects listed, uploaded or deleted (`listObjects`, `deleteObjects`, `preload`, `deletePrefix`) |
| `failed` | Objects that failed (`deleteObjects`, `preload`, `deletePrefix`) |

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

All metrics are tagged with `op` (`put`, `get`, `head`, `delete`, `delete_objects`, `list`, `create_bucket`, `delete_bucket`, `put_multipart` for a whole multipart upload, `upload_part` for each part) and the VU tags such as `scenario` and `group`. `bucket` and `size_class` (`<4KiB`, `<64KiB`, `<1MiB`, `<16MiB`, `<128MiB`, `>=128MiB`) are added when enabled by the `tags` option. Operations canceled by the end of the test are not recorded.

`data_sent` / `data_received` are emitted by k6 at the end of each iteration. Use `s3_op_bytes` for throughput over time.

### OpenTelemetry output

With `-o opentelemetry`, Trend metrics (`s3_op_duration`, `s3_op_ttfb`) are exported as histograms. By default they use the OpenTelemetry SDK default bucket boundaries (0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000 ms), which are too coarse for low-latency operations. If your backend supports exponential histograms, enable them with the standard OpenTelemetry environment variable:

```console
$ OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION=base2_exponential_bucket_histogram \
    K6_OTEL_EXPORTER_PROTOCOL=http/protobuf K6_OTEL_HTTP_EXPORTER_ENDPOINT=localhost:4318 \
    ./k6 run -o opentelemetry script.js
```

The Rate metric `s3_op_errors` is exported as counters by k6 (e.g. `s3_op_errors.total`).

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

## Examples

[examples/](examples/) has benchmarks comparable to the [warp](https://github.com/minio/warp) benchmarks `put`, `get`, `mixed`, `multipart-put`, `stat`, `list` and `delete`, and a scenario that warp cannot express. They are configured by environment variables (see [examples/common.js](examples/common.js) and each script).

| Script | Description | Defaults |
| --- | --- | --- |
| [put.js](examples/put.js) | Upload objects with PutObject | 20 VUs, 10MiB |
| [get.js](examples/get.js) | Download random objects preloaded in `setup()` | 20 VUs, 2500 objects of 10MiB |
| [mixed.js](examples/mixed.js) | GET, HEAD, PUT and DELETE with weights 45:30:15:10 | 20 VUs, 2500 objects of 10MiB |
| [multipart.js](examples/multipart.js) | Upload objects with multipart uploads (like `warp multipart-put`) | 4 VUs, 100MiB in 5MiB parts, 5 parts at a time (the aws-sdk-go-v2 upload manager defaults) |
| [stat.js](examples/stat.js) | HeadObject on random objects preloaded in `setup()` | 20 VUs, 10000 objects of 1KiB |
| [list.js](examples/list.js) | List all objects under a prefix per VU with ListObjectsV2 | 20 VUs, 10000 objects of 1KiB |
| [delete.js](examples/delete.js) | Delete objects preloaded in `setup()` with DeleteObjects | 20 VUs, 25000 objects of 1KiB, 100 objects per request |
| [slo.js](examples/slo.js) | Small reads at a fixed rate with large multipart uploads in the background, with a p99 latency threshold | 500 reads/s of 64KiB, 8 VUs uploading 256MiB |

```console
$ AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... \
    AWS_ENDPOINT_URL_S3=http://localhost:7070 VUS=32 DURATION=5m SIZE=1MiB \
    ./k6 run examples/get.js
```

The examples create the bucket if missing and delete the objects of the run in `teardown()`. Operations in `setup()` and `teardown()` are also recorded, so the thresholds are filtered by the `scenario` tag.

## Comparison with warp

There is no practical difference in measuring server performance. In a comparison against versitygw on the same machine, with the same request format and keep-alive connections, the latency measured by xk6-s3 was within about 0.1ms per request of warp, which is the processing cost of aws-sdk-go-v2 compared with minio-go. Note the following when comparing the numbers with other tools:

- The `checksum` and `payloadSigning` options change the work of the server (see [Request format](#request-format)). warp sends the same requests as `checksum: when_required` over HTTPS.
- Against a server that closes the connection after every response, such as versitygw without `--keep-alive`, warp resumes TLS sessions while k6 performs a full TLS handshake for every request, as the default aws-sdk-go-v2 client does. Enable keep-alive on the server for comparisons.
- With a fixed number of VUs, the k6 iteration overhead (less than 0.1ms) slightly lowers the throughput of small objects when the server is not saturated. Add VUs, or use `constant-arrival-rate`.

## Development

```console
$ make test   # unit tests
$ make build  # build ./k6 with this extension
$ make e2e    # E2E tests and examples against versitygw (posix backend) over HTTP and HTTPS; requires docker
```

## Compatibility

| xk6-s3 | k6 |
| --- | --- |
| main | v2.3.0 |

Only k6 v2 is supported. CI builds the extension with the k6 versions above and the latest k6 every week.

Tested S3-compatible implementations:

- [versitygw](https://github.com/versity/versitygw) v1.8.0 (posix backend)

## LICENSE

The source code of xk6-s3 is licensed under the [Apache License 2.0](LICENSE).

k6 is licensed under the [GNU AGPL v3](https://github.com/grafana/k6/blob/master/LICENSE.md). A k6 binary built with this extension is subject to the terms of the AGPL v3.

## Author

Copyright (c) 2026 FUJIWARA Shunichiro
