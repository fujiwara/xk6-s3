#!/bin/bash
# Runs the E2E tests and the examples against versitygw with the posix
# backend, over HTTP and HTTPS. Requires docker and a k6 binary built with
# this extension (make build).
set -euo pipefail

cd "$(dirname "$0")/.."

K6=${K6:-./k6}
IMAGE=${VERSITYGW_IMAGE:-versity/versitygw:v1.8.0}
HTTP_PORT=${HTTP_PORT:-17070}
HTTPS_PORT=${HTTPS_PORT:-17443}
NAME=xk6-s3-e2e-$$

export AWS_ACCESS_KEY_ID=e2e-access
export AWS_SECRET_ACCESS_KEY=e2e-secret
export S3_BUCKET=xk6-s3-e2e

workdir=$(mktemp -d)
cleanup() {
  docker rm -f "$NAME-http" "$NAME-https" >/dev/null 2>&1 || true
  docker volume rm -f "$NAME" >/dev/null 2>&1 || true
  rm -rf "$workdir"
}
trap cleanup EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -keyout "$workdir/key.pem" -out "$workdir/cert.pem" 2>/dev/null
chmod 644 "$workdir"/*.pem

# Both gateways serve the same posix directory.
docker volume create "$NAME" >/dev/null
start() {
  local name=$1 port=$2
  shift 2
  docker run -d --name "$name" -p "127.0.0.1:$port:7070" \
    -v "$NAME:/data" -v "$workdir:/certs:ro" \
    "$IMAGE" --access "$AWS_ACCESS_KEY_ID" --secret "$AWS_SECRET_ACCESS_KEY" "$@" posix /data >/dev/null
}
start "$NAME-http" "$HTTP_PORT"
start "$NAME-https" "$HTTPS_PORT" --cert /certs/cert.pem --key /certs/key.pem

for url in "http://127.0.0.1:$HTTP_PORT" "https://127.0.0.1:$HTTPS_PORT"; do
  for _ in $(seq 1 30); do
    if curl -ks -o /dev/null "$url"; then
      break
    fi
    sleep 1
  done
done

run() {
  echo "=== $*"
  "$K6" run --quiet --no-usage-report "$@"
}

S3_ENDPOINT_HTTP="http://127.0.0.1:$HTTP_PORT" S3_ENDPOINT_HTTPS="https://127.0.0.1:$HTTPS_PORT" \
  run e2e/formats.js

for endpoint in "http://127.0.0.1:$HTTP_PORT" "https://127.0.0.1:$HTTPS_PORT"; do
  export AWS_ENDPOINT_URL_S3=$endpoint
  export VUS=4 DURATION=3s OBJECTS=50 SIZE=256KiB
  run --insecure-skip-tls-verify examples/put.js
  run --insecure-skip-tls-verify examples/get.js
  run --insecure-skip-tls-verify examples/mixed.js
  SIZE=12MiB PART_SIZE=5MiB run --insecure-skip-tls-verify examples/multipart.js
done

echo "=== all E2E tests passed"
