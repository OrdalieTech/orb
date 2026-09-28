#!/bin/sh
# orb installer: downloads the latest release binary for this platform.
set -eu

REPO="OrdalieTech/orb"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
# Termux calls itself Linux but is Android: its build keeps Android's DNS, and it installs to $PREFIX.
if [ -n "${TERMUX_VERSION:-}" ]; then
  os=android
  INSTALL_DIR="${ORB_INSTALL_DIR:-$PREFIX/bin}"
fi
INSTALL_DIR="${ORB_INSTALL_DIR:-${INSTALL_DIR:-$HOME/.local/bin}}"

arch=$(uname -m)
case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux | darwin | android) ;;
  *) echo "unsupported OS: $os (Windows binaries are not released yet; see docs/deployments.md)" >&2; exit 1 ;;
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
tag=${ORB_VERSION:-$(resolve_tag_api)}
[ -n "$tag" ] || tag=$(resolve_tag_redirect)
[ -n "$tag" ] || { echo "could not resolve the latest release tag (github.com unreachable?)" >&2; exit 1; }
version=${tag#v}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="orb_${version}_${os}_${arch}.tar.gz"
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
install -m 0755 "$tmp/orb" "$INSTALL_DIR/orb"
echo "orb $tag is ready at $INSTALL_DIR/orb"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo "note: add $INSTALL_DIR to your PATH:"
    printf '  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    ;;
esac
