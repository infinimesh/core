#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
cover=$here/cover.sh

pkgs=$("$cover" --packages)
if echo "$pkgs" | grep -q '/mocks/'; then
	echo "coverage package list includes mocks"
	exit 1
fi
if ! echo "$pkgs" | grep -qx 'github.com/infinimesh/core/v3/pkg/graph'; then
	echo "coverage package list dropped pkg/graph"
	exit 1
fi

if "$cover" --meets 84.9; then
	echo "84.9 must fail the 85 gate"
	exit 1
fi
"$cover" --meets 85
"$cover" --meets 85.0
"$cover" --meets 100
echo "cover tests passed"
