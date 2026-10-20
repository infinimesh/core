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

cat > "$tmp" << 'EOF'
## [v3.4.1-rc.1] - 2026-10-14

This release is for testing, not for production.

### Added
- rotate
EOF
out=$("$notes" v3.4.1-rc.1 "$tmp")
[ "$out" = "This release is for testing, not for production.

### Added
- rotate" ]

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
grep -q 'scripts/check-stable.sh' "$root/.github/workflows/release.yml" || {
  echo "release workflow must check stable prerequisites" >&2
  exit 1
}
grep -q 'scripts/check-stable.sh' "$root/.github/workflows/go.yml" || {
  echo "go.yml must check stable prerequisites" >&2
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
grep -q "pnpm@9.15.9" "$console_df" || {
  echo "console image must install pnpm 9.15.9 to match the lockfile" >&2
  exit 1
}
grep -q '"packageManager": "pnpm@9.15.9"' "$root/console/package.json" || {
  echo "console packageManager must pin pnpm 9.15.9 so Dependabot updates the lockfile with that pnpm" >&2
  exit 1
}
grep -q 'version: 9$' "$root/.github/workflows/go.yml" || {
  echo "console test job must install pnpm 9" >&2
  exit 1
}
grep -q 'COPY ./pkg/observe ./pkg/observe' "$console_df" || {
  echo "console image must copy pkg/observe" >&2
  exit 1
}
if grep -q goreportcard "$root/README.md"; then
  echo "README must not link goreportcard; that service is retired" >&2
  exit 1
fi

compose="$root/docker-compose.yaml"
grep -q 'image: traefik:2.11.58' "$compose" || {
  echo "compose must run Traefik 2.11.58" >&2
  exit 1
}
api_routers=$(grep -c 'traefik.http.routers.api.rule=' "$compose" || true)
if [ "$api_routers" != 1 ]; then
  echo "compose must name one router api, found $api_routers" >&2
  exit 1
fi
grep -q 'traefik.http.routers.traefik.rule=' "$compose" || {
  echo "the dashboard router must be named traefik" >&2
  exit 1
}
if grep -q '/v1/handsfree' "$root/api.swagger.json"; then
  echo "api.swagger.json still lists /v1/handsfree" >&2
  exit 1
fi

sh -n "$root/scripts/buf_lint_pin.sh"
grep -q 'sh scripts/buf_lint_pin.sh' "$root/.github/workflows/go.yml" || {
  echo "go.yml must lint the pinned proto module" >&2
  exit 1
}
grep -q 'bufbuild/buf/releases/download/v1.55.1/' "$root/.github/workflows/go.yml" || {
  echo "go.yml must install buf 1.55.1" >&2
  exit 1
}

stable=$root/scripts/check-stable.sh
fix=$(mktemp -d)
trap 'rm -f "$tmp"; rm -rf "$fix"' EXIT
cat > "$fix/CHANGELOG.md" << 'EOF'
## [v9.9.9] - 2026-10-14

### Fixed
- kept

## [v9.9.9-rc.1] - 2026-10-14

This release is for testing, not for production.

### Fixed
- draft
EOF
printf 'module example\n\ngo 1.27.2\n\nrequire github.com/infinimesh/proto v1.2.0\n' > "$fix/go.mod"
STABLE_SKIP_REMOTE=1 sh "$stable" "$fix" v9.9.9
printf 'module example\n\ngo 1.27.2\n\nrequire github.com/infinimesh/proto v1.2.3-0.20261014081529-3817bce516c2\n' > "$fix/go.mod"
if STABLE_SKIP_REMOTE=1 sh "$stable" "$fix" v9.9.9 >/dev/null 2>&1; then
  echo "a pseudo-version must fail a stable release" >&2
  exit 1
fi
printf 'module example\n\ngo 1.27.2\n\nrequire github.com/infinimesh/proto v1.2.0-rc.1\n' > "$fix/go.mod"
if STABLE_SKIP_REMOTE=1 sh "$stable" "$fix" v9.9.9 >/dev/null 2>&1; then
  echo "a prerelease module must fail a stable release" >&2
  exit 1
fi
printf 'module example\n\ngo 1.27.2\n\nrequire github.com/infinimesh/proto v1.2.0\n\nreplace github.com/infinimesh/proto => ../proto\n' > "$fix/go.mod"
if STABLE_SKIP_REMOTE=1 sh "$stable" "$fix" v9.9.9 >/dev/null 2>&1; then
  echo "a replace directive must fail a stable release" >&2
  exit 1
fi
printf 'module example\n\ngo 1.27.2\n\nrequire github.com/infinimesh/proto v1.2.3-0.20261014081529-3817bce516c2\n' > "$fix/go.mod"
STABLE_SKIP_REMOTE=1 sh "$stable" "$fix" v9.9.9-rc.1
cat > "$fix/CHANGELOG.md" << 'EOF'
## [v9.9.9] - 2026-10-14

This release is for testing, not for production.

### Fixed
- kept
EOF
printf 'module example\n\ngo 1.27.2\n\nrequire github.com/infinimesh/proto v1.2.0\n' > "$fix/go.mod"
if STABLE_SKIP_REMOTE=1 sh "$stable" "$fix" v9.9.9 >/dev/null 2>&1; then
  echo "stable notes must not say the release is for testing" >&2
  exit 1
fi

go_ver=$(awk '/^go / { print $2; exit }' "$root/go.mod")
major=${go_ver%.*}
for f in "$root"/Dockerfiles/*/Dockerfile; do
  grep -q "FROM golang:${major}-alpine" "$f" || {
    echo "$f does not use golang:${major}-alpine" >&2
    exit 1
  }
done
