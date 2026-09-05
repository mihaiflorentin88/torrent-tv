#!/bin/sh
set -eu
: "${TIZEN_PROFILE:?Set TIZEN_PROFILE to the certificate profile name}"
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
version=$(tr -d '[:space:]' < "$root/../../VERSION")
stage="$root/.build/torrent-tv"
rm -rf -- "$stage"
mkdir -p "$stage"
cp -R "$root/../tv/dist/." "$stage/"
cp "$root/config.xml" "$stage/config.xml"
cp "$root/icon.png" "$stage/icon.png"
tizen package -t wgt -s "$TIZEN_PROFILE" -- "$stage"
mkdir -p "$root/.build/artifacts"
mv "$stage"/*.wgt "$root/.build/artifacts/torrent-tv-$version-signed.wgt"
sha256sum "$root/.build/artifacts/torrent-tv-$version-signed.wgt" > "$root/.build/artifacts/torrent-tv-$version-signed.wgt.sha256"
