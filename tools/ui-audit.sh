#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Rebuild the same UI module used by native tests; the harness changes only
# the conversation transport. Each build gets a fresh synthetic support folder.
./test.sh LocalConversationTests ExecutionApprovalTests
APP="$ROOT/build/Capybara Camp Interaction Audit.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp Resources/capybara.png "$APP/Contents/Resources/"
cp build/test-support/libCampTestSupport.dylib "$APP/Contents/MacOS/"
cp build/camp-test-provider "$APP/Contents/MacOS/"
GOOS=darwin CGO_ENABLED=0 go -C server build -trimpath -buildvcs=false \
  -ldflags='-s -w -X github.com/teslashibe/capybara-camp/server/internal/config.developmentBuild=1' \
  -o "$APP/Contents/MacOS/camp-local" ./cmd/camp-local
swiftc -parse-as-library -swift-version 5 -I build/test-support -L build/test-support \
  -lCampTestSupport -framework SwiftUI -framework AppKit \
  -Xlinker -rpath -Xlinker @executable_path Tests/UIAuditApp.swift \
  -o "$APP/Contents/MacOS/CapybaraAudit"
PLIST="$APP/Contents/Info.plist"
cat > "$PLIST" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>CapybaraAudit</string>
<key>CFBundleIdentifier</key><string>com.capybaracamp.interaction-audit</string>
<key>CFBundleName</key><string>Capybara Camp Interaction Audit</string>
<key>CFBundleDisplayName</key><string>Capybara Camp Interaction Audit</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>NSPrincipalClass</key><string>NSApplication</string>
<key>LSMinimumSystemVersion</key><string>14.0</string>
</dict></plist>
PLIST
PROFILES="${CAMP_DEV_PROFILE_ROOT:-$ROOT/build/ui-audit/profiles}"
if [ ! -f "$PROFILES/.capybara-ui-fixtures" ]; then
  echo "CAMP_DEV_PROFILE_ROOT must name an existing synthetic fixture folder" >&2; exit 2
fi
mkdir -p "$ROOT/build/ui-audit"
STATE="$(mktemp -d "$ROOT/build/ui-audit/state-isolated.XXXXXX")"
touch "$STATE/.capybara-ui-fixtures"
plutil -insert LSEnvironment -json '{}' "$PLIST"
plutil -insert LSEnvironment.CAMP_UI_AUDIT -string 1 "$PLIST"
plutil -insert LSEnvironment.CAMP_DEV_PROFILE_ROOT -string "$PROFILES" "$PLIST"
plutil -insert LSEnvironment.CAPY_SUPPORT -string "$STATE" "$PLIST"
CAMP_UI_AUDIT=1 CAMP_DEV_PROFILE_ROOT="$PROFILES" CAPY_SUPPORT="$STATE" \
  "$APP/Contents/MacOS/camp-local" audit-config
IDENTITY="${CAMP_SIGN_IDENTITY:--}"
for binary in libCampTestSupport.dylib camp-test-provider camp-local CapybaraAudit; do
  codesign --force --sign "$IDENTITY" --timestamp=none "$APP/Contents/MacOS/$binary"
done
codesign --force --sign "$IDENTITY" --timestamp=none "$APP"
codesign --verify --strict "$APP"
printf 'Built %s\n' "$APP"
