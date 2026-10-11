#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tags=$root/scripts/image-tags.sh
notes=$root/scripts/changelog-notes.sh

out=$("$tags" v3.1.0-r1)
[ "$out" = "v3.1.0-r1
v3.1.0
latest" ]
out=$("$tags" v3.1.0)
[ "$out" = "v3.1.0
latest" ]
out=$("$tags" main)
[ "$out" = "latest" ]

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
cat > "$tmp" << 'EOF'
## [v3.1.0-r1] - 2026-10-11

### Fixed
- rebuild

## [v3.1.0] - 2026-10-11

### Changed
- images

## v3.0.0

- old
EOF

out=$("$notes" v3.1.0-r1 "$tmp")
[ "$out" = "
### Fixed
- rebuild" ]
out=$("$notes" v3.1.0 "$tmp")
[ "$out" = "
### Changed
- images" ]
out=$("$notes" v3.0.0 "$tmp")
[ "$out" = "
- old" ]
if "$notes" v9.9.9 "$tmp" >/dev/null 2>&1; then
  echo "missing tag should fail" >&2
  exit 1
fi

console_df="$root/Dockerfiles/console/Dockerfile"
grep -q "FROM node:20 " "$console_df" || {
  echo "console image must build on node 20" >&2
  exit 1
}
grep -q "pnpm@9" "$console_df" || {
  echo "console image must install pnpm 9 to match the lockfile" >&2
  exit 1
}

go_ver=$(awk '/^go / { print $2; exit }' "$root/go.mod")
major=${go_ver%.*}
for f in "$root"/Dockerfiles/*/Dockerfile; do
  grep -q "FROM golang:${major}-alpine" "$f" || {
    echo "$f does not use golang:${major}-alpine" >&2
    exit 1
  }
done
