#!/bin/sh
# pigo installer: downloads the latest release binary for this platform.
set -eu

REPO="OrdalieTech/pigo"
INSTALL_DIR="${PIGO_INSTALL_DIR:-$HOME/.local/bin}"

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

resolve_tag_api() {
  curl -fsSL --retry 3 --retry-delay 1 --retry-all-errors \
    "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1
}
# Fallback: the releases/latest redirect carries the tag and avoids API gateways/rate limits.
resolve_tag_redirect() {
  curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/$REPO/releases/latest" 2>/dev/null |
    sed -n 's#.*/tag/\([^/?]*\).*#\1#p'
}
tag=${PIGO_VERSION:-$(resolve_tag_api)}
[ -n "$tag" ] || tag=$(resolve_tag_redirect)
[ -n "$tag" ] || { echo "could not resolve the latest release tag (github.com unreachable?)" >&2; exit 1; }
version=${tag#v}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="pigo_${version}_${os}_${arch}.tar.gz"
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
install -m 0755 "$tmp/pigo" "$INSTALL_DIR/pigo"
echo "pigo $tag is ready at $INSTALL_DIR/pigo"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo "note: add $INSTALL_DIR to your PATH:"
    printf '  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    ;;
esac
