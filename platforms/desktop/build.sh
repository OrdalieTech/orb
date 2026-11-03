#!/bin/sh
# Builds Orb for the desktop with MyGo: this app, the orb it runs (built from this checkout for each
# platform), the face it shares with the Android app and the mark on its icon. On macOS the app is
# signed with mygo.json's identity, ad hoc without one, and put in a disk image.
#   build.sh <version> [platforms]   dist/<os>-<arch>/…; platforms as `mygo build -platform` takes
#                                    them, comma separated (default: this machine's)
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
version=$1 platforms=${2:-$(go env GOOS)/$(go env GOARCH)}

# The orb each app runs sits in its platform's resources (bin/), where
# the app looks first.
rm -rf "$here/resources"
for p in $(echo "$platforms" | tr , ' '); do
	os=${p%/*} arch=${p#*/} exe=
	[ "$os" = windows ] && exe=.exe
	[ "$arch" = universal ] && { echo "build.sh: one platform per architecture (darwin/arm64, darwin/amd64)" >&2; exit 2; }
	(cd "$repo" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" \
		-o "$here/resources/$os-$arch/bin/orb$exe" ./cmd/orb)
done
"$repo/platforms/apple/icon.sh" "$here/icon.png" 100

# The version is mygo.json's while the build runs.
cp "$here/mygo.json" "$here/mygo.json.orig"
trap 'mv "$here/mygo.json.orig" "$here/mygo.json"' EXIT
sed "s/\"version\": \"[^\"]*\"/\"version\": \"$version\"/" "$here/mygo.json.orig" >"$here/mygo.json"
cd "$here" && go tool mygo build -platform "$platforms"
