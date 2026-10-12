#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! command -v codeql >/dev/null 2>&1; then
  echo "ERROR: codeql CLI not found in PATH"
  echo "Install from: https://docs.github.com/en/code-security/codeql-cli/getting-started-with-the-codeql-cli"
  exit 1
fi

mkdir -p .tmp/codeql

CODEQL_GO_RAM_MB="${CODEQL_GO_RAM_MB:-2048}"
CODEQL_GO_CGO_ENABLED="${CODEQL_GO_CGO_ENABLED:-0}"
CODEQL_JS_RAM_MB="${CODEQL_JS_RAM_MB:-2048}"
CODEQL_JS_THREADS="${CODEQL_JS_THREADS:-1}"
CODEQL_ACTIONS_RAM_MB="${CODEQL_ACTIONS_RAM_MB:-1024}"
CODEQL_QUERY_STRATEGY="${CODEQL_QUERY_STRATEGY:-github}"

config_args=(--codescanning-config="$ROOT_DIR/.github/codeql/codeql-config.yml")

go_build=(go build -buildvcs=false -ldflags=-w ./...)
if [ -d vendor ]; then
  go_build=(go build -mod=vendor -buildvcs=false -ldflags=-w ./...)
fi

has_js=0
if [ -f console/package.json ]; then
  has_js=1
else
  js_file=$(find . \( -name '*.js' -o -name '*.vue' -o -name '*.ts' \) \
    -not -path '*/node_modules/*' -not -path './vendor/*' -not -path './.tmp/*' \
    -print -quit || true)
  if [ -n "$js_file" ]; then
    has_js=1
  fi
fi

echo "==> Ensuring CodeQL query packs are available"
packs=(codeql/go-queries)
if [ -d .github/workflows ]; then
  packs+=(codeql/actions-queries)
fi
if [ "$has_js" -eq 1 ]; then
  packs+=(codeql/javascript-queries)
fi
codeql pack download "${packs[@]}"

run_go() {
  echo "==> CodeQL (Go)"
  echo "    using --ram=${CODEQL_GO_RAM_MB}MB CGO_ENABLED=${CODEQL_GO_CGO_ENABLED}"
  rm -rf .tmp/codeql/go-db
  codeql database create .tmp/codeql/go-db \
    --language=go \
    --ram="$CODEQL_GO_RAM_MB" \
    "${config_args[@]}" \
    --command="env CGO_ENABLED=${CODEQL_GO_CGO_ENABLED} ${go_build[*]}"

  if [ "$CODEQL_QUERY_STRATEGY" = "security-and-quality" ]; then
    codeql database analyze .tmp/codeql/go-db \
      codeql/go-queries:codeql-suites/go-security-and-quality.qls \
      --download \
      --ram="$CODEQL_GO_RAM_MB" \
      --format=sarifv2.1.0 \
      --sarif-category="/language:go" \
      --output .tmp/codeql/go.sarif
  else
    codeql database analyze .tmp/codeql/go-db \
      codeql/go-queries \
      --download \
      --ram="$CODEQL_GO_RAM_MB" \
      --format=sarifv2.1.0 \
      --sarif-category="/language:go" \
      --output .tmp/codeql/go.sarif
  fi
}

run_js() {
  echo "==> CodeQL (JavaScript/TypeScript)"
  echo "    using --ram=${CODEQL_JS_RAM_MB}MB --threads=${CODEQL_JS_THREADS}"
  rm -rf .tmp/codeql/js-db
  codeql database create .tmp/codeql/js-db \
    --language=javascript-typescript \
    --build-mode=none \
    --ram="$CODEQL_JS_RAM_MB" \
    --threads="$CODEQL_JS_THREADS" \
    "${config_args[@]}"

  if [ "$CODEQL_QUERY_STRATEGY" = "security-and-quality" ]; then
    codeql database analyze .tmp/codeql/js-db \
      codeql/javascript-queries:codeql-suites/javascript-security-and-quality.qls \
      --download \
      --ram="$CODEQL_JS_RAM_MB" \
      --threads="$CODEQL_JS_THREADS" \
      --format=sarifv2.1.0 \
      --sarif-category="/language:javascript-typescript" \
      --output .tmp/codeql/javascript.sarif
  else
    codeql database analyze .tmp/codeql/js-db \
      codeql/javascript-queries \
      --download \
      --ram="$CODEQL_JS_RAM_MB" \
      --threads="$CODEQL_JS_THREADS" \
      --format=sarifv2.1.0 \
      --sarif-category="/language:javascript-typescript" \
      --output .tmp/codeql/javascript.sarif
  fi
}

run_actions() {
  echo "==> CodeQL (Actions)"
  echo "    using --ram=${CODEQL_ACTIONS_RAM_MB}MB"
  rm -rf .tmp/codeql/actions-db
  codeql database create .tmp/codeql/actions-db \
    --language=actions \
    --build-mode=none \
    --ram="$CODEQL_ACTIONS_RAM_MB" \
    "${config_args[@]}"

  codeql database analyze .tmp/codeql/actions-db \
    codeql/actions-queries \
    --download \
    --ram="$CODEQL_ACTIONS_RAM_MB" \
    --format=sarifv2.1.0 \
    --sarif-category="/language:actions" \
    --output .tmp/codeql/actions.sarif
}

run_go
if [ "$has_js" -eq 1 ]; then
  run_js
fi
if [ -d .github/workflows ]; then
  run_actions
fi

echo ""
echo "CodeQL local run complete."
echo "SARIF outputs:"
find .tmp/codeql -name '*.sarif' -print
