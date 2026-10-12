#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

export PATH="$(go env GOPATH)/bin:${PATH}"

go mod download
dir="$(go list -m -f '{{.Dir}}' github.com/infinimesh/proto)"
buf lint --config "${dir}/buf.yaml" "${dir}"
