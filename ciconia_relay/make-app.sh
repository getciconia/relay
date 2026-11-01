#!/bin/sh
# Puts the Home Assistant app together in dist/ciconia_relay: its files plus
# the relay's source in src/. CI builds the published image from it; copied
# into Home Assistant's local_apps folder, it's built on the device (the
# image: line is left out for that).
set -e
here="$(cd "$(dirname "$0")" && pwd)"
relay="$(dirname "$here")"
out="${1:-$relay/dist/ciconia_relay}"

rm -rf "$out"
mkdir -p "$out/src"
cp -R "$here/Dockerfile" "$here/run.sh" "$here/apparmor.txt" "$here/CHANGELOG.md" \
      "$here/icon.png" "$here/logo.png" "$here/translations" "$out/"
for doc in README.md DOCS.md; do
  if [ -f "$here/$doc" ]; then cp "$here/$doc" "$out/"; fi
done
grep -v '^image:' "$here/config.yaml" > "$out/config.yaml"
cp -R "$relay/go.mod" "$relay/go.sum" "$relay/cmd" "$relay/internal" "$out/src/"
chmod a+x "$out/run.sh"
echo "App ready in $out"
