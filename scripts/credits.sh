#!/bin/bash
# Generates CREDITS, the licenses of the modules in the xk6-s3 binary
# (cmd/xk6-s3), with gocredits (https://github.com/Songmu/gocredits).
set -euo pipefail

cd "$(dirname "$0")/.."

GOCREDITS=${GOCREDITS:-go run github.com/Songmu/gocredits/cmd/gocredits@v1.1.0}

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

# Ignore cmd/xk6-s3/vendor, which goreleaser creates for the source archive
# and may be stale.
if ! (cd cmd/xk6-s3 && GOFLAGS=-mod=mod $GOCREDITS -skip-missing .) >"$out/CREDITS" 2>"$out/missing.log"; then
  cat "$out/missing.log" >&2
  exit 1
fi

# Modules without a license file. Their license is taken from the source headers.
declare -A known=(
  # generated from the Prometheus protobuf definitions:
  # "Copyright 2017 Prometheus Team, Licensed under the Apache License, Version 2.0"
  ["buf.build/gen/go/prometheus/prometheus/protocolbuffers/go"]="Copyright 2017 Prometheus Team"
)
while read -r line; do
  mod=$(sed -n 's/^could not find the license for "\(.*\)"$/\1/p' <<<"$line")
  if [[ -z $mod ]]; then
    # other messages, such as "go: downloading ..." of go run
    echo "$line" >&2
    continue
  fi
  if [[ -z ${known[$mod]:-} ]]; then
    echo "license not found: $mod" >&2
    exit 1
  fi
  {
    echo
    echo "================================================================"
    echo
    echo "$mod"
    echo "https://$mod"
    echo "----------------------------------------------------------------"
    echo "${known[$mod]}"
    echo
    echo "Licensed under the Apache License, Version 2.0 (the \"License\");"
    echo "you may not use this file except in compliance with the License."
    echo "You may obtain a copy of the License at"
    echo
    echo "    http://www.apache.org/licenses/LICENSE-2.0"
    echo
    echo "(The full text of the Apache License, Version 2.0 is in the LICENSE file of xk6-s3.)"
  } >>"$out/CREDITS"
done <"$out/missing.log"

cp "$out/CREDITS" CREDITS
echo "wrote CREDITS" >&2
