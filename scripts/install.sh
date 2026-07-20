#!/bin/sh
# pi-go installer: downloads the latest release binary for this platform.
set -eu

REPO="OrdalieTech/pi-go"
INSTALL_DIR="${PI_INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux | darwin) ;;
  *) echo "unsupported OS: $os (Windows is a later parity wave)" >&2; exit 1 ;;
esac

tag=${PI_VERSION:-$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')}
[ -n "$tag" ] || { echo "could not resolve the latest release tag" >&2; exit 1; }
version=${tag#v}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="pi_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

curl -fsSL -o "$tmp/$archive" "$base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  checksum="sha256sum -c"
elif command -v shasum >/dev/null 2>&1; then
  checksum="shasum -a 256 -c"
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi
(cd "$tmp" && grep " $archive\$" checksums.txt | $checksum - >/dev/null)
tar -xzf "$tmp/$archive" -C "$tmp"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/pi" "$INSTALL_DIR/pi"
echo "pi $tag installed to $INSTALL_DIR/pi"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: add $INSTALL_DIR to your PATH" ;;
esac
