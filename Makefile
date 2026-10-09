K6_VERSION ?= v2.3.0
XK6 ?= go run go.k6.io/xk6/cmd/xk6@latest

.PHONY: build test clean

build: k6

k6: go.* *.go
	$(XK6) build $(K6_VERSION) --with github.com/fujiwara/xk6-s3=. --output $@

test:
	go test -race ./...

clean:
	rm -f k6
