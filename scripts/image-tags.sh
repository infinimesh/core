#!/bin/sh
# Image tags for a git ref, one per line.
# main and master publish latest.
# vX.Y.Z and vX.Y.Z-rN also publish latest. A revision also publishes vX.Y.Z.
# vX.Y.Z-rc.N publishes only that tag.
set -eu
ref=${1:?ref}
if printf '%s\n' "$ref" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'; then
  printf '%s\n' "$ref"
  exit 0
fi
case "$ref" in
  main|master)
    printf '%s\n' latest
    ;;
  v[0-9]*.[0-9]*.[0-9]*-r[0-9]*)
    base=${ref%-r*}
    printf '%s\n' "$ref" "$base" latest
    ;;
  *)
    printf '%s\n' "$ref" latest
    ;;
esac
