# xk6 reads the k6 version from K6_VERSION.
export K6_VERSION ?= v2.3.0
XK6 ?= go run go.k6.io/xk6/cmd/xk6@v1.4.14

.PHONY: build test e2e credits dist clean

build: k6

k6: go.* *.go
	$(XK6) build --with github.com/fujiwara/xk6-s3=. --output $@

test:
	go test -race ./...

e2e: k6
	./e2e/run.sh

# CREDITS lists the licenses of the modules in the k6 binary built with xk6.
credits:
	./scripts/credits.sh

# Build the release archives locally (without publishing).
dist:
	goreleaser release --snapshot --clean

clean:
	rm -rf k6 dist/ cmd/xk6-s3/vendor/
