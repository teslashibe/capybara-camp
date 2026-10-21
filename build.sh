#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

case "${1:-}" in
  --universal) ARCHS="arm64 x86_64" ;;
  "") ARCHS="$(uname -m)" ;;
  *) echo "Usage: ./build.sh [--universal]" >&2; exit 2 ;;
esac

python3 -c 'import sys; sys.exit("Python 3.11 or newer is required") if sys.version_info < (3, 11) else None'
VERSION="$(cat VERSION)"
OUTPUT="$ROOT/build"
APP="${CAMP_APP_PATH:-$OUTPUT/Capybara Camp.app}"
IDENTITY="${CAMP_SIGN_IDENTITY:--}"
IDENTIFIER="${CAMP_BUNDLE_ID:-com.capybaracamp.app}"
mkdir -p "$OUTPUT"
WORK="$(mktemp -d "$OUTPUT/.camp-build.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
STAGED="$WORK/Capybara Camp.app"
mkdir -p "$STAGED/Contents/MacOS" "$STAGED/Contents/Resources" "$WORK/AppIcon.iconset"

BINARIES=()
for arch in $ARCHS; do
  binary="$WORK/CapybaraCamp-$arch"
  swiftc -parse-as-library -swift-version 5 -module-name CapybaraCamp \
    -target "$arch-apple-macosx14.0" -O \
    -file-prefix-map "$ROOT=." -debug-prefix-map "$ROOT=." \
    -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift -o "$binary"
  BINARIES+=("$binary")
done
if [ "${#BINARIES[@]}" -eq 1 ]; then
  cp "${BINARIES[0]}" "$STAGED/Contents/MacOS/CapybaraCamp"
else
  lipo -create "${BINARIES[@]}" -output "$STAGED/Contents/MacOS/CapybaraCamp"
fi
strip -S "$STAGED/Contents/MacOS/CapybaraCamp"
cp -X Resources/bridge.py Resources/activity.py Resources/conversation.py Resources/capybara.png "$STAGED/Contents/Resources/"
for size in 16 32 128 256 512; do
  sips -s format png -z "$size" "$size" Resources/capybara.png --out "$WORK/AppIcon.iconset/icon_${size}x${size}.png" >/dev/null
  twice=$((size * 2))
  sips -s format png -z "$twice" "$twice" Resources/capybara.png --out "$WORK/AppIcon.iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$WORK/AppIcon.iconset" -o "$STAGED/Contents/Resources/AppIcon.icns"
python3 - "$STAGED" "$VERSION" "$IDENTIFIER" <<'PY'
import plistlib, sys
from pathlib import Path
app, version, identifier = sys.argv[1:]
info = {
    "CFBundleExecutable": "CapybaraCamp", "CFBundleIdentifier": identifier,
    "CFBundleName": "Capybara Camp", "CFBundleDisplayName": "Capybara Camp",
    "CFBundlePackageType": "APPL", "CFBundleShortVersionString": version,
    "CFBundleVersion": "1", "CFBundleIconFile": "AppIcon",
    "LSMinimumSystemVersion": "14.0", "NSHighResolutionCapable": True,
    "NSPrincipalClass": "NSApplication", "LSApplicationCategoryType": "public.app-category.developer-tools",
}
with (Path(app) / "Contents/Info.plist").open("wb") as f:
    plistlib.dump(info, f)
PY
codesign --force --sign "$IDENTITY" --timestamp=none "$STAGED"
codesign --verify --strict "$STAGED"
# Only replace the exact requested app bundle after compilation and signing pass.
case "$APP" in *.app) ;; *) echo "CAMP_APP_PATH must end in .app" >&2; exit 2 ;; esac
mkdir -p "$(dirname "$APP")"
if [ -e "$APP" ]; then rm -rf "$APP"; fi
ditto --norsrc "$STAGED" "$APP"
echo "Built $APP ($VERSION; $ARCHS)"
