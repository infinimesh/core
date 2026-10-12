#!/bin/sh
# Delete GHCR versions whose tags are only vX.Y.Z-rc.N and that are older than 30 days.
# --select reads a versions JSON array on stdin and prints ids. The cutoff is $2.
set -eu
select_ids() {
  cutoff=$1
  jq -r --arg cutoff "$cutoff" '
    def tags: (.metadata.container.tags // []);
    def is_rc: test("^v[0-9]+\\.[0-9]+\\.[0-9]+-rc\\.[0-9]+$");
    .[]
    | select((tags | length) > 0 and all(tags[]; is_rc))
    | select(.created_at < $cutoff)
    | .id
  '
}
if [ "${1:-}" = "--select" ]; then
  select_ids "${2:?cutoff}"
  exit 0
fi

package=${1:?package name, for example core/api}
days=${2:-30}
org=${3:-infinimesh}
cutoff=$(date -u -d "${days} days ago" +%Y-%m-%dT%H:%M:%SZ)
enc=$(printf '%s' "$package" | jq -sRr @uri)
err=$(mktemp)
trap 'rm -f "$err"' EXIT
if ! json=$(gh api --paginate "/orgs/${org}/packages/container/${enc}/versions?per_page=100" 2>"$err"); then
  if grep -q 'HTTP 404' "$err"; then
    exit 0
  fi
  cat "$err" >&2
  exit 1
fi
printf '%s\n' "$json" | jq -s 'add // []' | select_ids "$cutoff" | while read -r id; do
  [ -n "$id" ] || continue
  gh api --method DELETE "/orgs/${org}/packages/container/${enc}/versions/${id}"
  printf 'deleted %s %s\n' "$package" "$id"
done
