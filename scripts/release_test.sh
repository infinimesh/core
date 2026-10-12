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
out=$("$tags" v3.4.0-rc.1)
[ "$out" = "v3.4.0-rc.1" ]
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

cat > "$tmp" << 'EOF'
## [v3.4.0-rc.1] - 2026-10-12

### Added
- http-fs
EOF
out=$("$notes" v3.4.0-rc.1 "$tmp")
[ "$out" = "This release is for testing, not for production.

### Added
- http-fs" ]

ids=$(printf '%s\n' '[
  {"id":1,"created_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":["v3.4.0-rc.1"]}}},
  {"id":2,"created_at":"2026-10-01T00:00:00Z","metadata":{"container":{"tags":["v3.4.0-rc.2"]}}},
  {"id":3,"created_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":["v3.3.0","latest"]}}},
  {"id":4,"created_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":["v3.4.0-rc.1","latest"]}}},
  {"id":5,"created_at":"2020-01-01T00:00:00Z","metadata":{"container":{"tags":[]}}}
]' | sh "$root/scripts/prune-rc-images.sh" --select 2026-09-12T00:00:00Z)
[ "$ids" = "1" ]

grep -q 'v\[0-9\]+.\[0-9\]+.\[0-9\]+-rc.\[0-9\]+' "$root/.github/workflows/docker-build.yml" || {
  echo "docker-build must accept release candidate tags" >&2
  exit 1
}
grep -q 'v\[0-9\]+.\[0-9\]+.\[0-9\]+-rc.\[0-9\]+' "$root/.github/workflows/release.yml" || {
  echo "release workflow must accept release candidate tags" >&2
  exit 1
}
grep -q 'prerelease:' "$root/.github/workflows/release.yml" || {
  echo "release candidates must be GitHub pre-releases" >&2
  exit 1
}
if "$notes" v9.9.9 "$tmp" >/dev/null 2>&1; then
  echo "missing tag should fail" >&2
  exit 1
fi

wf="$root/.github/workflows/docker-build.yml"
grep -q "fail-fast: false" "$wf" || {
  echo "image builds must not cancel each other" >&2
  exit 1
}
awk '
  /^  build:/ { job="build"; next }
  /^  public:/ { job="public"; next }
  /^  [a-z]/ { job="" }
  job == "build" && /Make the package public/ { print "public check is inside the build job"; exit 1 }
  job == "public" && /Make the package public/ { found=1 }
  END { if (!found) { print "public check job missing"; exit 1 } }
' "$wf"

console_df="$root/Dockerfiles/console/Dockerfile"
grep -q "FROM node:20 " "$console_df" || {
  echo "console image must build on node 20" >&2
  exit 1
}
grep -q "pnpm@9" "$console_df" || {
  echo "console image must install pnpm 9 to match the lockfile" >&2
  exit 1
}
grep -q 'COPY ./pkg/observe ./pkg/observe' "$console_df" || {
  echo "console image must copy pkg/observe" >&2
  exit 1
}

sh -n "$root/scripts/buf_lint_pin.sh"
grep -q 'sh scripts/buf_lint_pin.sh' "$root/.github/workflows/go.yml" || {
  echo "go.yml must lint the pinned proto module" >&2
  exit 1
}
grep -q 'bufbuild/buf/releases/download/v1.55.1/' "$root/.github/workflows/go.yml" || {
  echo "go.yml must install buf 1.55.1" >&2
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
