#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

SWIFT_FLAGS=()
GO_LDFLAGS='-s -w'
RELEASE=false
case "${1:-}" in
  --universal) ARCHS="arm64 x86_64" ;;
  --release) ARCHS="arm64 x86_64"; RELEASE=true ;;
  --development) ARCHS="$(uname -m)"; SWIFT_FLAGS=(-D CAMP_DEVELOPMENT); GO_LDFLAGS="$GO_LDFLAGS -X github.com/teslashibe/capybara-camp/server/internal/config.developmentBuild=1" ;;
  "") ARCHS="$(uname -m)" ;;
  *) echo "Usage: ./build.sh [--universal | --development | --release]" >&2; exit 2 ;;
esac

VERSION="$(cat VERSION)"
OUTPUT="$ROOT/build"
APP="${CAMP_APP_PATH:-$OUTPUT/Capybara Camp.app}"
IDENTITY="${CAMP_SIGN_IDENTITY:--}"
IDENTIFIER="${CAMP_BUNDLE_ID:-com.capybaracamp.app}"
SIGN_FLAGS=(--timestamp=none)
if [ "$RELEASE" = true ]; then
  if [ "$IDENTITY" = - ] || [ -z "$IDENTITY" ] || [[ ! "${CAMP_SIGN_TEAM_ID:-}" =~ ^[A-Z0-9]{10}$ ]]; then
    echo "Release builds require CAMP_SIGN_IDENTITY and the expected CAMP_SIGN_TEAM_ID" >&2; exit 2
  fi
  if [ "$IDENTIFIER" != com.capybaracamp.app ]; then
    echo "Release builds must keep the stable com.capybaracamp.app identifier" >&2; exit 2
  fi
  SIGN_FLAGS=(--timestamp --options runtime)
fi
mkdir -p "$OUTPUT"
WORK="$(mktemp -d "$OUTPUT/.camp-build.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
STAGED="$WORK/Capybara Camp.app"
mkdir -p "$STAGED/Contents/MacOS" "$STAGED/Contents/Resources" "$WORK/AppIcon.iconset"

BINARIES=()
HELPERS=()
for arch in $ARCHS; do
  binary="$WORK/CapybaraCamp-$arch"
  swiftc -parse-as-library -swift-version 5 -module-name CapybaraCamp \
    -target "$arch-apple-macosx14.0" -O \
    -file-prefix-map "$ROOT=." -debug-prefix-map "$ROOT=." \
    -framework SwiftUI -framework AppKit -framework Security -framework UserNotifications -lsqlite3 ${SWIFT_FLAGS[@]+"${SWIFT_FLAGS[@]}"} Sources/*.swift -o "$binary"
  BINARIES+=("$binary")
  case "$arch" in arm64) goarch=arm64 ;; x86_64) goarch=amd64 ;; *) echo "Unsupported architecture: $arch" >&2; exit 1 ;; esac
  helper="$WORK/camp-local-$arch"
  GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 go -C "$ROOT/server" build -trimpath -buildvcs=false -ldflags="$GO_LDFLAGS" -o "$helper" ./cmd/camp-local
  HELPERS+=("$helper")
done
if [ "${#BINARIES[@]}" -eq 1 ]; then
  cp "${BINARIES[0]}" "$STAGED/Contents/MacOS/CapybaraCamp"
  cp "${HELPERS[0]}" "$STAGED/Contents/MacOS/camp-local"
else
  lipo -create "${BINARIES[@]}" -output "$STAGED/Contents/MacOS/CapybaraCamp"
  lipo -create "${HELPERS[@]}" -output "$STAGED/Contents/MacOS/camp-local"
fi
strip -S "$STAGED/Contents/MacOS/CapybaraCamp"
cp -X Resources/capybara.png "$STAGED/Contents/Resources/"
for size in 16 32 128 256 512; do
  sips -s format png -z "$size" "$size" Resources/capybara.png --out "$WORK/AppIcon.iconset/icon_${size}x${size}.png" >/dev/null
  twice=$((size * 2))
  sips -s format png -z "$twice" "$twice" Resources/capybara.png --out "$WORK/AppIcon.iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$WORK/AppIcon.iconset" -o "$STAGED/Contents/Resources/AppIcon.icns"
PLIST="$STAGED/Contents/Info.plist"
plutil -create xml1 "$PLIST"
plutil -insert CFBundleExecutable -string CapybaraCamp "$PLIST"
plutil -insert CFBundleIdentifier -string "$IDENTIFIER" "$PLIST"
plutil -insert CFBundleName -string 'Capybara Camp' "$PLIST"
plutil -insert CFBundleDisplayName -string 'Capybara Camp' "$PLIST"
plutil -insert CFBundlePackageType -string APPL "$PLIST"
plutil -insert CFBundleShortVersionString -string "$VERSION" "$PLIST"
plutil -insert CFBundleVersion -string 1 "$PLIST"
plutil -insert CFBundleIconFile -string AppIcon "$PLIST"
plutil -insert LSMinimumSystemVersion -string 14.0 "$PLIST"
plutil -insert NSHighResolutionCapable -bool YES "$PLIST"
plutil -insert NSPrincipalClass -string NSApplication "$PLIST"
plutil -insert LSApplicationCategoryType -string public.app-category.developer-tools "$PLIST"
plutil -insert CFBundleURLTypes -json '[{"CFBundleURLName":"Camp source links","CFBundleURLSchemes":["capybara"]}]' "$PLIST"
codesign --force --sign "$IDENTITY" "${SIGN_FLAGS[@]}" --identifier "$IDENTIFIER.camp-local" "$STAGED/Contents/MacOS/camp-local"
codesign --force --sign "$IDENTITY" "${SIGN_FLAGS[@]}" "$STAGED"
codesign --verify --strict "$STAGED"
if [ "$RELEASE" = true ]; then "$ROOT/tools/verify-macos-release.sh" "$STAGED"; fi
# Only replace the exact requested app bundle after compilation and signing pass.
case "$APP" in *.app) ;; *) echo "CAMP_APP_PATH must end in .app" >&2; exit 2 ;; esac
mkdir -p "$(dirname "$APP")"
if [ -e "$APP" ]; then rm -rf "$APP"; fi
ditto --norsrc "$STAGED" "$APP"
echo "Built $APP ($VERSION; $ARCHS)"
