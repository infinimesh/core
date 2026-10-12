#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

FUZZ_TIME="${FUZZ_TIME:-30s}"
mod=()
if [ -d vendor ]; then
  mod=(-mod=vendor)
fi

list=$(mktemp)
trap 'rm -f "$list"' EXIT
find . -name '*_test.go' \
  -not -path './vendor/*' \
  -not -path '*/node_modules/*' \
  -not -path './.tmp/*' \
  -print0 | xargs -0 grep -H -E '^func Fuzz' > "$list" || true

if [ ! -s "$list" ]; then
  echo "No fuzz tests."
  exit 0
fi

while IFS= read -r hit; do
  file=${hit%%:*}
  name=${hit##*func }
  name=${name%%(*}
  pkg=$(dirname "$file")
  if [ "$pkg" = "." ]; then
    target=.
  else
    target="./${pkg#./}"
  fi
  echo "==> fuzz: ${target} ${name}"
  go test "${mod[@]}" -run='^$' -fuzz="^${name}$" -fuzztime="${FUZZ_TIME}" "$target"
done < "$list"

echo ""
echo "Fuzz suite passed."
