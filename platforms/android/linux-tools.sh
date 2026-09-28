#!/bin/sh
# Fetches the programs that open Orb's Linux: proot, its loader and their two libraries, from
# Termux's package repository. Android starts programs only from the app's own lib directory, so
# they ship in the APK; everything they run (Termux's base system) is downloaded by the app.
# linux-tools.sh <out-dir>: writes libproot.so, libprootloader.so, libtalloc.so, libandroid-shmem.so.
set -eu
out=$1
repo=https://packages.termux.dev/apt/termux-main
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL "$repo/dists/stable/main/binary-aarch64/Packages.gz" | gunzip > "$work/Packages"

# The index names each package's file and sha256; a download that does not match stops the build.
fetch() {
	entry=$(awk -v p="$1" '$0 == "Package: " p {f=1} f && /^Filename:/ {n=$2} f && /^SHA256:/ {print n, $2; exit}' "$work/Packages")
	name=${entry% *} sum=${entry#* }
	curl -fsSL -o "$work/$1.deb" "$repo/$name"
	echo "$sum  $work/$1.deb" | (sha256sum -c - 2>/dev/null || shasum -a 256 -c -) >/dev/null
	mkdir -p "$work/$1"
	if tar --version 2>/dev/null | grep -q bsdtar; then tar -xf "$work/$1.deb" -C "$work/$1"; else (cd "$work/$1" && ar x "../$1.deb"); fi
	tar -xf "$work/$1"/data.tar.* -C "$work"
}
fetch proot
fetch libtalloc
fetch libandroid-shmem

usr="$work/data/data/com.termux/files/usr"
mkdir -p "$out"
# Android packs only lib*.so: proot looks for libtalloc.so.2, so its name is shortened in place.
perl -0777 -pe 's/libtalloc\.so\.2\0/libtalloc.so\0\0\0/' "$usr/bin/proot" > "$out/libproot.so"
cp "$usr/libexec/proot/loader" "$out/libprootloader.so"
cp "$(ls "$usr"/lib/libtalloc.so.2.* | head -1)" "$out/libtalloc.so"
cp "$usr/lib/libandroid-shmem.so" "$out/libandroid-shmem.so"
chmod 755 "$out"/lib*.so
