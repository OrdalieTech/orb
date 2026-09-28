#!/bin/sh
# Builds and signs the Android release: release.sh <version> <keystore> <password-file> <out-dir>
# Writes orb_<version>_android_arm64.apk and its .sha256 — the files the app updates from.
# The release key rotated from the original debug key (signing/lineage), so a phone that ran
# a debug build installs the release over it and keeps its sessions, keys and pairings.
set -eu
version=$1 keystore=$2 password=$(cat "$3") out=$4
here=$(cd "$(dirname "$0")" && pwd)
sdk=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}
tools=$(ls -d "$sdk"/build-tools/* | sort -V | tail -1)
name="orb_${version}_android_arm64.apk"

"$here/gradlew" -p "$here" assembleRelease -PorbVersion="$version" --console=plain -q
mkdir -p "$out"
# v3 only (Android 9+; the app needs 10): v1 and v2 would have to be signed by the old key too,
# and v4 is a separate .idsig file for incremental installs from a store, which this is not.
"$tools/apksigner" sign --v1-signing-enabled false --v2-signing-enabled false --v4-signing-enabled false --ks "$keystore" --ks-pass "pass:$password" --ks-key-alias orb \
	--lineage "$here/signing/lineage" --rotation-min-sdk-version 28 \
	--out "$out/$name" "$here/app/build/outputs/apk/release/app-release-unsigned.apk"
"$tools/apksigner" verify --print-certs "$out/$name" | grep -q "CN=Orb, O=Ordalie"
(cd "$out" && if command -v sha256sum >/dev/null; then sha256sum "$name"; else shasum -a 256 "$name"; fi > "$name.sha256")
echo "$out/$name"
