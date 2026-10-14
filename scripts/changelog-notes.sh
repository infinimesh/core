#!/bin/sh
# Print the keep-a-changelog section for a tag. Headings are "## [tag]" or "## tag".
set -eu
tag=${1:?tag}
file=${2:?changelog}
body=$(awk -v tag="$tag" '
  function is_heading(line, t) {
    if (line == "## " t) return 1
    prefix = "## [" t "]"
    if (index(line, prefix) != 1) return 0
    rest = substr(line, length(prefix) + 1)
    return rest == "" || substr(rest, 1, 1) == " " || substr(rest, 1, 1) == "\t"
  }
  $0 ~ /^## / {
    if (found) exit
    found = is_heading($0, tag)
    next
  }
  found { print }
  END { if (!found) exit 1 }
' "$file")
note='This release is for testing, not for production.'
if printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'; then
  rest=$(printf '%s\n' "$body" | sed '/./,$!d')
  case $rest in
    "$note"*) printf '%s\n' "$rest" ;;
    *) printf '%s\n%s\n' "$note" "$body" ;;
  esac
  exit 0
fi
printf '%s\n' "$body"
