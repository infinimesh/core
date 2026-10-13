#!/usr/bin/env bash
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
tags=$'v3.2.0\nv3.4.0-rc.10\nv3.4.0'
echo 'INFINIMESH_VERSION=v3.4.0' > .env.example
CORE_TAGS=$tags bash "$here/check-pins.sh" >/dev/null
echo 'INFINIMESH_VERSION=v3.4.0-rc.10' > .env.example
if CORE_TAGS=$tags bash "$here/check-pins.sh" >/dev/null; then echo "rc behind stable must fail"; exit 1; fi
echo 'INFINIMESH_VERSION=v3.3.0' > .env.example
if CORE_TAGS=$tags bash "$here/check-pins.sh" >/dev/null; then echo "missing tag must fail"; exit 1; fi
echo 'INFINIMESH_VERSION=v3.4.0-rc.10' > .env.example
CORE_TAGS=$'v3.2.0\nv3.4.0-rc.10' bash "$here/check-pins.sh" >/dev/null
echo "check-pins tests passed"
