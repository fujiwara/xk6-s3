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

export default function () {
  console.log(s3.runId());
}
```

### `runId()`

Returns the run ID, which is intended to be used as a key prefix such as `<runId>/<dataset>/<n>`. All VUs in a k6 process share the same run ID. It is available in the init context.

The run ID is the value of the `XK6_S3_RUN_ID` environment variable if set, otherwise a random 8-character string generated at startup. To share the run ID across multiple k6 processes, or to correlate it with metrics, pass the same value to `XK6_S3_RUN_ID` and `--tag test-id=`.

```console
$ XK6_S3_RUN_ID=run-001 ./k6 run --tag test-id=run-001 script.js
```

## Compatibility

| xk6-s3 | k6 |
| --- | --- |
| main | v2.3.0 |

## LICENSE

The source code of xk6-s3 is licensed under the [Apache License 2.0](LICENSE).

k6 is licensed under the [GNU AGPL v3](https://github.com/grafana/k6/blob/master/LICENSE.md). A k6 binary built with this extension is subject to the terms of the AGPL v3.

## Author

Copyright (c) 2026 FUJIWARA Shunichiro
