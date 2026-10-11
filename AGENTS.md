# AGENTS.md

Development guide of xk6-s3 for contributors and coding agents. See [README.md](README.md) for the usage.

xk6-s3 is a k6 extension (`k6/x/s3`) for load testing S3-compatible object storage, built on aws-sdk-go-v2. Only k6 v2 (`go.k6.io/k6/v2`) is supported.

## Layout

| Path | Description |
| --- | --- |
| `*.go` (root, package `s3`) | The k6 module: `module.go` (exports), `client.go` (`Client`, `Result`, measurement), one file per group of operations (`object.go`, `list.go`, `multipart.go`, ...) |
| `internal/client` | The client config (`Config`, `ParseConfig`), the SDK client and error classification |
| `internal/jsobj` | Decoder of JavaScript objects into structs by struct tags |
| `internal/data` | Sizes, size distributions and the generated object bodies |
| `internal/metrics` | k6 metrics and tags |
| `internal/tracing` | Adapter of the k6 tracer provider |
| `internal/cmd/apidoc` | Generator of `docs/api.md` and `index.d.ts` |
| `cmd/xk6-s3` | The released binary (k6 with this extension). A separate module that replaces `github.com/fujiwara/xk6-s3` with `../..` |
| `examples/` | Benchmark scripts, also run by the E2E tests |
| `e2e/` | E2E tests against versitygw |
| `docs/initial-design.md` | The initial design (in Japanese) |

## Commands

```console
$ make test      # unit tests (go test -race ./...)
$ make build     # build ./k6 with xk6 (K6_VERSION in Makefile)
$ make e2e       # E2E tests and examples against versitygw (posix backend) over HTTP and HTTPS; requires docker
$ make generate  # regenerate docs/api.md and index.d.ts
$ make credits   # regenerate CREDITS
$ go -C cmd/xk6-s3 build -o ../../xk6-s3 .  # build the released binary
```

Run `go fmt ./...` and `go fix ./...` before committing.

## JavaScript API

`docs/api.md` and `index.d.ts` are generated from the Go source by `make generate`, and must be committed with the change. `go test` fails when they are out of date, and the generator fails on a method or a field without a doc comment.

The API is defined by:

- the exported methods of `Client`, exposed with the first letter lowercased (`ListObjects` → `listObjects`). Every exported method of `Client` is visible to JavaScript.
- `<Method>Options`, the options struct of a method with an `options sobek.Value` parameter, decoded with `c.decodeOptions(options, &opts)`.
- `Result`, returned by every operation.
- `internal/client.Config`, the argument of `new s3.Client(config)`.

The struct tags of the options and the config define both the decoding and the documented defaults and accepted values (see [internal/jsobj](internal/jsobj/jsobj.go)): `js`, `default`, `env`, `enum`, `min` / `max`, and `ts` for a field whose TypeScript type cannot be derived from the Go type. Do not validate in code what a tag can express. Cross-field checks stay in code.

Doc comments are the documentation, written for JavaScript users:

- The leading Go name is removed: "Count is the number of ..." becomes "The number of ...", and "PutObject uploads ..." becomes "Uploads ...".
- "the X section of README" becomes a link to the README section in `docs/api.md`.
- Tab-indented code blocks become JavaScript code blocks.
- Keep Go-specific notes out of doc comments, for example in a line comment after the field.

The layout of the generated files is in `internal/cmd/apidoc/templates/`.

When adding or changing the API, also:

- add unit tests (`ops_test.go` and others run operations against gofakes3), and E2E tests in `e2e/` for behavior that depends on a real server
- update README for concepts that are not part of the API reference (sizes, metrics, tracing, request format)

## Conventions

- S3 and network errors are not thrown; they are reported in `Result` so that an error does not abort the iteration. Only script errors (invalid arguments, calls in the init context) throw, with `c.throw`.
- Each request is measured with `c.measure`, which emits the metrics and the span. Operations that send requests concurrently use `c.parallelClient`.
- The examples must keep working against versitygw; `make e2e` runs them.
- Performance comparisons on localhost are meaningful only with headroom (not saturated). Do not draw conclusions from saturation benchmarks on localhost.

## Dependencies and releases

- When dependencies change, keep `cmd/xk6-s3/go.mod` in sync (`go -C cmd/xk6-s3 mod tidy`) and run `make credits`. CI fails when `CREDITS` is out of date. `CREDITS` lists the licenses of the modules in the released binary, generated with [gocredits](https://github.com/Songmu/gocredits).
- Releases are managed by [tagpr](https://github.com/Songmu/tagpr): it opens a release PR that updates `version.go` and `CHANGELOG.md`, and merging it tags the release and runs GoReleaser. Do not edit them by hand.
- The release archives include the source with the vendored dependencies (`cmd/xk6-s3/vendor`, created by GoReleaser), as the corresponding source of the binaries under the AGPL v3.
- GitHub Actions are pinned by SHA (use `pinact run`).
