# xk6 reads the k6 version from K6_VERSION.
export K6_VERSION ?= v2.3.0
XK6 ?= go run go.k6.io/xk6/cmd/xk6@v1.4.14

.PHONY: build test e2e clean

build: k6

k6: go.* *.go
	$(XK6) build --with github.com/fujiwara/xk6-s3=. --output $@

test:
	go test -race ./...

e2e: k6
	./e2e/run.sh

clean:
	rm -f k6
