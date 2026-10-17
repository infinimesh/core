#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$root"

# Product packages only. Generated mockery trees under mocks/ are imported by
# tests and Go 1.27 records them at 0% when they are in the test list.
packages() {
	go list ./... | grep -v '/mocks/'
}

meets() {
	awk -v t="$1" 'BEGIN { exit !(t+0 >= 85) }'
}

case "${1:-}" in
--packages)
	packages
	exit 0
	;;
--meets)
	meets "$2"
	exit 0
	;;
esac

# shellcheck disable=SC2046
go test $(packages) -coverprofile=cover.out
total=$(go tool cover -func=cover.out | awk '/^total:/ {sub("%", "", $3); print $3}')
echo "Total coverage: ${total}%"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	echo "Total coverage: ${total}%" >> "$GITHUB_STEP_SUMMARY"
fi
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	echo "total=${total}" >> "$GITHUB_OUTPUT"
fi
meets "$total"
