#!/bin/sh
# Point core's pinned defaults at a final release. Usage: scripts/bump-pins.sh vX.Y.Z
set -eu
v=${1:?version}
sed -i.bak -E "s/^INFINIMESH_VERSION=v[^ ]+/INFINIMESH_VERSION=$v/" .env.example
sed -i.bak -E "s/^Current version: .*/Current version: \`$v\`. See [CHANGELOG.md](CHANGELOG.md) and [Releases](https:\/\/github.com\/infinimesh\/core\/releases)./" README.md
sed -i.bak -E "s/Image tags default to \`v[^\`]+\`/Image tags default to \`$v\`/" docs/production-on-kubernetes.md
sed -i.bak -E "s/The chart image tag is \`[^\`]+\`( until [^,]+, then the release moves it to \`[^\`]+\`)?\./The chart image tag is \`$v\`./" docs/production-on-kubernetes.md
rm -f .env.example.bak README.md.bak docs/production-on-kubernetes.md.bak
