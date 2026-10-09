.PHONY: clean test

xk6-s3: go.* *.go
	go build -o $@ ./cmd/xk6-s3

clean:
	rm -rf xk6-s3 dist/

test:
	go test -v ./...

install:
	go install github.com/fujiwara/xk6-s3/cmd/xk6-s3

dist:
	goreleaser build --snapshot --clean
