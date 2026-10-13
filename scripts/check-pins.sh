#!/usr/bin/env bash
# Fail if a pinned core version in this repo does not exist as a core tag,
# or is older than the newest stable core release.
# Run from the root of core, inf-cli, platform-operator, k8s or website.
# CORE_TAGS can hold a newline list of tags (for tests); otherwise they are read from GitHub.
set -euo pipefail

if [ -z "${CORE_TAGS:-}" ]; then
  CORE_TAGS=$(git ls-remote --tags --refs https://github.com/infinimesh/core | sed 's#.*refs/tags/##')
fi

# key prints a sortable key: rc < final < -rN revision.
key() {
  local v=${1#v} base suf rank n
  base=${v%%-*}
  suf=${v#"$base"}
  case "$suf" in
    "") rank=2 n=0 ;;
    -rc.*) rank=1 n=${suf#-rc.} ;;
    -r*) rank=3 n=${suf#-r} ;;
    *) rank=0 n=0 ;;
  esac
  IFS=. read -r a b c <<<"$base"
  printf '%05d%05d%05d%d%05d\n' "$a" "$b" "$c" "$rank" "$n"
}

stable=""
while read -r t; do
  printf '%s\n' "$t" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-r[0-9]+)?$' || continue
  if [ -z "$stable" ] || [[ $(key "$t") > $(key "$stable") ]]; then stable=$t; fi
done <<<"$CORE_TAGS"
echo "newest stable core release: ${stable:-none}"

# file glob|extended regex whose first v-tag is the pin
rules=(
  '.env.example|^INFINIMESH_VERSION=v[0-9][^ ]*'
  'charts/*/values.yaml|^  tag: v[0-9][^ ]*'
  'charts/*/Chart.yaml|^appVersion: "?v[0-9][^ "]*'
  'pkg/controller/platform/release.go|defaultCoreVersion *= *"v[0-9][^"]*'
  'config/samples/*.yaml|^  version: v[0-9][^ ]*'
  'README.md|^Current version: `v[0-9][^`]*'
  'README.md|core/<service>:v[0-9][^`]*'
  'llms-full.txt|^Current version: `v[0-9][^`]*'
)

fail=0 seen=0
shopt -s nullglob
for r in "${rules[@]}"; do
  glob=${r%%|*} re=${r#*|}
  for f in $glob; do
    [ -f "$f" ] || continue
    while IFS= read -r line; do
      pin=$(printf '%s\n' "$line" | grep -Eo 'v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-r[0-9]+)?' | head -1)
      [ -n "$pin" ] || continue
      seen=$((seen + 1))
      if ! grep -qxF "$pin" <<<"$CORE_TAGS"; then
        echo "::error file=$f::$pin is pinned but is not a core tag"; fail=1; continue
      fi
      if [ -n "$stable" ] && [[ $(key "$pin") < $(key "$stable") ]]; then
        echo "::error file=$f::$pin is older than the newest stable core release $stable"; fail=1; continue
      fi
      echo "ok $f $pin"
    done < <(grep -E "$re" "$f" || true)
  done
done
echo "checked $seen pin(s)"
exit $fail
