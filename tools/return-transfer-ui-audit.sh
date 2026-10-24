#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
./test.sh CampReturnTransferTests CampWorkspaceExportTests CampStateTests
WORK="$(mktemp -d "$ROOT/build/.return-ui.XXXXXX")"
mkdir -p "$WORK/state"
chmod 700 "$WORK" "$WORK/state"
touch "$WORK/.capybara-ui-fixtures"
APP="$ROOT/build/Capybara Camp Return Audit.app"
mkdir -p "$APP/Contents/MacOS"
cp build/test-support/libCampTestSupport.dylib build/camp-local "$APP/Contents/MacOS/"
printf '%s\n' '@testable import CampTestSupport' > "$WORK/Audit.swift"
cat Tests/DestinationBridgeFixture.swift Tests/WorkspaceExportFixture.swift Tests/ReturnScreenFixture.swift Tests/ReturnTransferAudit.swift >> "$WORK/Audit.swift"
swiftc -parse-as-library -swift-version 5 -I build/test-support -L build/test-support \
  -lCampTestSupport -framework SwiftUI -framework AppKit \
  -Xlinker -rpath -Xlinker @executable_path "$WORK/Audit.swift" \
  -o "$APP/Contents/MacOS/ReturnAudit"
PLIST="$APP/Contents/Info.plist"
cat > "$PLIST" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>ReturnAudit</string>
<key>CFBundleIdentifier</key><string>com.capybaracamp.return-audit</string>
<key>CFBundleName</key><string>Capybara Camp Return Audit</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>NSPrincipalClass</key><string>NSApplication</string>
<key>LSMinimumSystemVersion</key><string>14.0</string>
</dict></plist>
PLIST
plutil -insert LSEnvironment -json "{\"CAMP_RETURN_AUDIT\":\"$WORK\",\"CAPY_SUPPORT\":\"$WORK/state\"}" "$PLIST"
IDENTITY="${CAMP_SIGN_IDENTITY:--}"
for binary in libCampTestSupport.dylib camp-local ReturnAudit; do
  codesign --force --sign "$IDENTITY" --timestamp=none "$APP/Contents/MacOS/$binary"
done
codesign --force --sign "$IDENTITY" --timestamp=none "$APP"
codesign --verify --strict "$APP"
printf 'Built %s\nSynthetic files: %s\n' "$APP" "$WORK"
