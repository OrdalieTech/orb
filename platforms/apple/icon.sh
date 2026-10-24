#!/bin/sh
# Orb's icon at 1024 px: the Android mark (Ubuntu Mono Bold "ORB", stretched) on a dark tile inset
# by <inset> px with rounded corners, as a Mac's; or, for 0, opaque and full bleed, as iOS wants it.
#   icon.sh <out.png> <inset>
set -eu
res=$(cd "$(dirname "$0")/../android/app/src/main/res" && pwd)
mark=$(sed -n 's/.*android:pathData="\([^"]*\)".*/\1/p' "$res/drawable/ic_orb_mark.xml")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
awk -v i="$2" -v d="$mark" 'BEGIN {
	k = (1024 - 2 * i) / 824
	printf "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"1024\" height=\"1024\">\n"
	printf "<rect x=\"%g\" y=\"%g\" width=\"%g\" height=\"%g\" rx=\"%g\" fill=\"#12151A\"/>\n", i, i, 1024 - 2 * i, 1024 - 2 * i, (i > 0 ? 185 * k : 0)
	printf "<path transform=\"translate(%g %g) scale(%g)\" fill=\"#FAF9F6\" d=\"%s\"/>\n</svg>\n", i + 185 * k, i + 168 * k, 3.3 * k, d
}' >"$tmp/icon.svg"
cat >"$tmp/render.swift" <<'EOF'
import AppKit
let svg = NSImage(contentsOf: URL(filePath: CommandLine.arguments[1]))!
let opaque = CommandLine.arguments[3] == "0"
let tile = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 1024, pixelsHigh: 1024, bitsPerSample: 8, samplesPerPixel: opaque ? 3 : 4,
                            hasAlpha: !opaque, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 32)!
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: tile)
svg.draw(in: NSRect(x: 0, y: 0, width: 1024, height: 1024))
try! tile.representation(using: .png, properties: [:])!.write(to: URL(filePath: CommandLine.arguments[2]))
EOF
swift "$tmp/render.swift" "$tmp/icon.svg" "$1" "$2"
