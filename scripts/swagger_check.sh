#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
go mod download
dir="$(go list -m -f '{{.Dir}}' github.com/infinimesh/proto)"
if ! diff -q api.swagger.json "${dir}/api.swagger.json" >/dev/null; then
  echo "core/api.swagger.json does not match github.com/infinimesh/proto ${dir}"
  echo "Copy api.swagger.json from that module. Do not edit it by hand."
  exit 1
fi
