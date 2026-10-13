#!/bin/sh
# Point core's pinned defaults at a final release. Usage: scripts/bump-pins.sh vX.Y.Z
set -eu
v=${1:?version}
sed -i.bak -E "s/^INFINIMESH_VERSION=v[^ ]+/INFINIMESH_VERSION=$v/" .env.example
# Any rc pin in the README (current version line, compose example) moves to the final.
sed -i.bak -E -e "s/ \(release candidate for v[0-9.]+\)//" -e "s/^(Current version: \`)v[^\`]+\`/\1$v\`/" -e "s/\`v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+\`/\`$v\`/g" README.md
sed -i.bak -E "s/Image tags default to \`v[^\`]+\`/Image tags default to \`$v\`/" docs/production-on-kubernetes.md
sed -i.bak -E "s/The chart image tag is \`[^\`]+\`( until [^,]+, then the release moves it to \`[^\`]+\`)?\./The chart image tag is \`$v\`./" docs/production-on-kubernetes.md
rm -f .env.example.bak README.md.bak docs/production-on-kubernetes.md.bak
