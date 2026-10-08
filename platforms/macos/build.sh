#!/bin/sh
# Builds Orb.app: the SwiftUI app, the orb it runs (`orb app`), and the face and mark it shares
# with the Android app; signed with a Developer ID when one is named, ad hoc otherwise.
#   build.sh <version> [identity]              build/Orb.app
#   build.sh notarize <key.p8> <key-id> <issuer>   notarizes build/Orb.app and staples the ticket
# The notary key is read from its file, never put on a command line or in the bundle.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
res="$repo/platforms/android/app/src/main/res"
app="$here/build/Orb.app"

if [ "$1" = notarize ]; then
	zip="$here/build/Orb.zip"
	ditto -c -k --keepParent "$app" "$zip"
	xcrun notarytool submit "$zip" --key "$2" --key-id "$3" --issuer "$4" --wait
	xcrun stapler staple "$app"
	rm "$zip"
	exit
fi

version=$1 identity=${2:--}
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Helpers" "$app/Contents/Resources"
swift build -c release --package-path "$here"
cp "$(swift build -c release --package-path "$here" --show-bin-path)/Orb" "$app/Contents/MacOS/Orb"
(cd "$repo" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$app/Contents/Helpers/orb" ./cmd/orb)
cp "$res/font/ubuntu_sans_mono.ttf" "$app/Contents/Resources/"

# The icon: the Android mark (Ubuntu Mono Bold "ORB", stretched) on a Mac app tile.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mark=$(sed -n 's/.*android:pathData="\([^"]*\)".*/\1/p' "$res/drawable/ic_orb_mark.xml")
cat >"$tmp/icon.svg" <<EOF
<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024">
<rect x="100" y="100" width="824" height="824" rx="185" fill="#12151A"/>
<path transform="translate(285 268) scale(3.3)" fill="#FAF9F6" d="$mark"/>
</svg>
EOF
cat >"$tmp/render.swift" <<'EOF'
import AppKit
let svg = NSImage(contentsOf: URL(filePath: CommandLine.arguments[1]))!
let tile = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 1024, pixelsHigh: 1024, bitsPerSample: 8, samplesPerPixel: 4,
                            hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: tile)
svg.draw(in: NSRect(x: 0, y: 0, width: 1024, height: 1024))
try! tile.representation(using: .png, properties: [:])!.write(to: URL(filePath: CommandLine.arguments[2]))
EOF
swift "$tmp/render.swift" "$tmp/icon.svg" "$tmp/icon.png"
mkdir "$tmp/AppIcon.iconset"
for size in 16 32 128 256 512; do
	sips -z $size $size "$tmp/icon.png" --out "$tmp/AppIcon.iconset/icon_${size}x${size}.png" >/dev/null
	sips -z $((size * 2)) $((size * 2)) "$tmp/icon.png" --out "$tmp/AppIcon.iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$tmp/AppIcon.iconset" -o "$app/Contents/Resources/AppIcon.icns"

cat >"$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>tech.ordalie.orb</string>
	<key>CFBundleName</key><string>Orb</string>
	<key>CFBundleExecutable</key><string>Orb</string>
	<key>CFBundleIconFile</key><string>AppIcon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>LSMinimumSystemVersion</key><string>15.0</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
	<key>ATSApplicationFontsPath</key><string>.</string>
	<key>NSHumanReadableCopyright</key><string>Ordalie</string>
</dict>
</plist>
EOF

# Inside out: the orb it runs, then the app; hardened, with a secure timestamp for a Developer ID.
stamp=--timestamp
[ "$identity" = - ] && stamp=--timestamp=none
codesign --force --options runtime $stamp --sign "$identity" "$app/Contents/Helpers/orb"
codesign --force --options runtime $stamp --sign "$identity" "$app"
codesign --verify --strict "$app"
echo "$app"
