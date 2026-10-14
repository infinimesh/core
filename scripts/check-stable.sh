#!/bin/sh
# Fail a final release that still requires an untagged or prerelease
# github.com/infinimesh module. Release candidates return success.
# Usage: check-stable.sh <repo-root> [version]
# STABLE_SKIP_REMOTE=1 skips the tag lookup. Tests set it.
set -eu

root=${1:?repo root}
version=${2:-}

if [ -z "$version" ]; then
  version=$(awk '
    /^## \[/ {
      line = $0
      sub(/^## \[/, "", line)
      sub(/\].*/, "", line)
      print line
      exit
    }
  ' "$root/CHANGELOG.md")
fi

case "$version" in
  v*.*.*-rc.*) exit 0 ;;
esac

if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-r[0-9]+)?$'; then
  echo "stable check: $version is not a final release" >&2
  exit 1
fi

here=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
notes=$("$here/changelog-notes.sh" "$version" "$root/CHANGELOG.md") || {
  echo "stable check: CHANGELOG.md has no section for $version" >&2
  exit 1
}
case "$notes" in
  *"This release is for testing, not for production."*)
    echo "stable check: $version notes say the release is for testing" >&2
    exit 1
    ;;
esac

gomod=$root/go.mod
[ -f "$gomod" ] || {
  echo "stable check: $gomod is missing" >&2
  exit 1
}

fail=0
check_mod() {
  mod=$1
  ver=$2
  case "$ver" in
    v[0-9]*.[0-9]*.[0-9]*+incompatible) base=${ver%%+incompatible} ;;
    *) base=$ver ;;
  esac
  if ! printf '%s\n' "$base" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "stable check: $mod $ver is untagged or a prerelease" >&2
    fail=1
    return
  fi
  if [ "${STABLE_SKIP_REMOTE:-}" = 1 ]; then
    return
  fi
  path=${mod#github.com/}
  owner=${path%%/*}
  rest=${path#*/}
  name=${rest%%/*}
  if ! git ls-remote --tags "https://github.com/${owner}/${name}.git" "refs/tags/${base}" | grep -q .; then
    echo "stable check: $mod tag $base does not exist" >&2
    fail=1
  fi
}

list=$(mktemp)
trap 'rm -f "$list"' EXIT
awk '
  /^replace[[:space:]]+github.com\/infinimesh\// { print "replace", $2; next }
  /^require[[:space:]]+github.com\/infinimesh\// && $3 != "" { print "require", $2, $3; next }
  /^require[[:space:]]*\(/ { req = 1; next }
  req && /^\)/ { req = 0; next }
  req && /^[[:space:]]*github.com\/infinimesh\// { print "require", $1, $2; next }
' "$gomod" > "$list"

while read -r kind mod ver; do
  [ -n "${kind:-}" ] || continue
  if [ "$kind" = replace ]; then
    echo "stable check: replace $mod" >&2
    fail=1
    continue
  fi
  check_mod "$mod" "$ver"
done < "$list"

exit "$fail"
