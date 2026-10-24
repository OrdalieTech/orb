#!/bin/sh
# Builds Orb.app for iOS, with Xcode: the SwiftUI app (platforms/apple, shared with macOS), the orb
# linked into it (cmd/orb as a static library, its entry core/app.go), and the face and mark it
# shares with the Android app. A simulator build is signed ad hoc; a device build is signed, and
# packed for App Store Connect, when an Apple Distribution identity and its profile are named.
#   build.sh <version> [simulator|device]                build/<sdk>/Orb.app
#   build.sh <version> device <identity> <profile>       build/Orb.ipa
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
res="$repo/platforms/android/app/src/main/res"
version=$1 minimum=18.0
case ${2:-simulator} in
simulator) sdk=iphonesimulator triple=arm64-apple-ios$minimum-simulator platform=iPhoneSimulator ;;
device) sdk=iphoneos triple=arm64-apple-ios$minimum platform=iPhoneOS ;;
*) echo "build.sh <version> [simulator|device]" >&2 && exit 2 ;;
esac
out="$here/build/$sdk" app="$here/build/$sdk/Orb.app"
sysroot=$(xcrun --sdk $sdk --show-sdk-path)
rm -rf "$app"
mkdir -p "$app"

# The orb: cmd/orb with core/app.go laid in, as a static library for the app to link.
printf '{"Replace":{"%s/cmd/orb/ios_app.go":"%s/core/app.go"}}' "$repo" "$here" >"$out/overlay.json"
(cd "$repo" && GOOS=ios GOARCH=arm64 CGO_ENABLED=1 CC="$(xcrun --sdk $sdk -f clang) -target $triple -isysroot $sysroot" \
	go build -overlay "$out/overlay.json" -tags appshell -trimpath -ldflags "-s -w -X main.version=$version" -buildmode=c-archive -o "$out/liborb.a" ./cmd/orb)

swift build -c release --package-path "$repo/platforms/apple" --scratch-path "$out/swift" --triple $triple --sdk "$sysroot" \
	-Xlinker "$out/liborb.a" -Xlinker -framework -Xlinker CoreFoundation -Xlinker -framework -Xlinker Security -Xlinker -lresolv
cp "$out/swift/release/Orb" "$app/Orb"
cp "$res/font/ubuntu_sans_mono.ttf" "$here/PrivacyInfo.xcprivacy" "$app/"

# The icon, opaque and full bleed: iOS rounds it.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/Assets.xcassets/AppIcon.appiconset"
"$repo/platforms/apple/icon.sh" "$tmp/Assets.xcassets/AppIcon.appiconset/icon.png" 0
echo '{"images":[{"filename":"icon.png","idiom":"universal","platform":"ios","size":"1024x1024"}],"info":{"author":"xcode","version":1}}' \
	>"$tmp/Assets.xcassets/AppIcon.appiconset/Contents.json"
xcrun actool "$tmp/Assets.xcassets" --compile "$app" --platform $sdk --minimum-deployment-target $minimum --app-icon AppIcon \
	--target-device iphone --output-partial-info-plist "$tmp/icon.plist" >/dev/null

# Store builds carry the toolchain they were built with, as Xcode records it.
xcode=$(xcodebuild -version)
short=${version%%-*}
cat >"$app/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>tech.ordalie.orb</string>
	<key>CFBundleName</key><string>Orb</string>
	<key>CFBundleDisplayName</key><string>Orb</string>
	<key>CFBundleExecutable</key><string>Orb</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$short</string>
	<key>CFBundleVersion</key><string>$short</string>
	<key>CFBundleSupportedPlatforms</key><array><string>$platform</string></array>
	<key>CFBundleDevelopmentRegion</key><string>en</string>
	<key>MinimumOSVersion</key><string>$minimum</string>
	<key>UIDeviceFamily</key><array><integer>1</integer></array>
	<key>UIRequiredDeviceCapabilities</key><array><string>arm64</string></array>
	<key>UILaunchScreen</key><dict/>
	<key>UISupportedInterfaceOrientations</key><array><string>UIInterfaceOrientationPortrait</string></array>
	<key>UIAppFonts</key><array><string>ubuntu_sans_mono.ttf</string></array>
	<key>CFBundleURLTypes</key><array><dict><key>CFBundleURLName</key><string>tech.ordalie.orb.pair</string><key>CFBundleURLSchemes</key><array><string>orb-bridge</string></array></dict></array>
	<key>NSCameraUsageDescription</key><string>Orb reads the pairing code another of your devices shows.</string>
	<key>NSLocalNetworkUsageDescription</key><string>Orb reaches your paired devices directly when they are on this network.</string>
	<key>ITSAppUsesNonExemptEncryption</key><true/>
	<key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
	<key>DTPlatformName</key><string>$sdk</string>
	<key>DTPlatformVersion</key><string>$(xcrun --sdk $sdk --show-sdk-version)</string>
	<key>DTPlatformBuild</key><string>$(xcrun --sdk $sdk --show-sdk-build-version)</string>
	<key>DTSDKName</key><string>$sdk$(xcrun --sdk $sdk --show-sdk-version)</string>
	<key>DTSDKBuild</key><string>$(xcrun --sdk $sdk --show-sdk-build-version)</string>
	<key>DTXcode</key><string>$(echo "$xcode" | awk 'NR == 1 { split($2, v, "."); printf "%d%d%d%d", v[1] / 10, v[1] % 10, v[2], v[3] }')</string>
	<key>DTXcodeBuild</key><string>$(echo "$xcode" | awk 'NR == 2 { print $3 }')</string>
	<key>DTCompiler</key><string>com.apple.compilers.llvm.clang.1_0</string>
	<key>BuildMachineOSBuild</key><string>$(sw_vers -buildVersion)</string>
</dict>
</plist>
EOF
/usr/libexec/PlistBuddy -c "Merge $tmp/icon.plist" "$app/Info.plist" >/dev/null
plutil -lint "$app/Info.plist" >/dev/null
if [ $sdk = iphonesimulator ]; then
	codesign --force --sign - --timestamp=none "$app"
elif [ $# -eq 4 ]; then
	# The entitlements are the profile's own: the team's app id, nothing more.
	cp "$4" "$app/embedded.mobileprovision"
	security cms -D -i "$4" | plutil -extract Entitlements xml1 -o "$tmp/entitlements.plist" -
	codesign --force --sign "$3" --entitlements "$tmp/entitlements.plist" "$app"
	mkdir "$tmp/Payload"
	cp -R "$app" "$tmp/Payload/"
	rm -f "$here/build/Orb.ipa"
	(cd "$tmp" && zip -qry "$here/build/Orb.ipa" Payload)
	app="$here/build/Orb.ipa"
fi
echo "$app"
