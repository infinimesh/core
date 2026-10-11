#!/bin/sh
# Image tags for a git ref, one per line.
# main and master publish latest. A revision vX.Y.Z-rN also publishes vX.Y.Z.
set -eu
ref=${1:?ref}
case "$ref" in
  main|master)
    printf '%s\n' latest
    ;;
  v*-r*)
    base=${ref%-r*}
    case "$base" in
      v*.*.*)
        printf '%s\n' "$ref" "$base" latest
        ;;
      *)
        printf '%s\n' "$ref" latest
        ;;
    esac
    ;;
  *)
    printf '%s\n' "$ref" latest
    ;;
esac
