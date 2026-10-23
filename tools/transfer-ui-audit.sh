#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
./test.sh CampTransferTests BridgeIntegrationTests
WORK="$(mktemp -d "$ROOT/build/.transfer-ui.XXXXXX")"
mkdir -p "$WORK/repository" "$WORK/state"
chmod 700 "$WORK" "$WORK/repository" "$WORK/state"
touch "$WORK/.capybara-ui-fixtures"
git -C "$WORK/repository" init -q
printf 'A small synthetic project\n' > "$WORK/repository/README.md"
git -C "$WORK/repository" add README.md
git -C "$WORK/repository" -c user.name=Fixture -c user.email=fixture@example.com commit -qm Fixture
git -C "$WORK/repository" remote add origin https://github.com/fixture/repo.git
git -C "$WORK/repository" update-ref refs/remotes/origin/main HEAD
printf 'Unpublished implementation from this Mac\n' > "$WORK/repository/implementation.txt"
git -C "$WORK/repository" add implementation.txt
git -C "$WORK/repository" -c user.name=Fixture -c user.email=fixture@example.com commit -qm 'Unpublished fixture implementation'
printf 'A small synthetic project\nReviewed change for the cloud workspace\n' > "$WORK/repository/README.md"
printf 'EXAMPLE_VALUE=not-a-real-credential\n' > "$WORK/repository/.env"
APP="$ROOT/build/Capybara Camp Transfer Audit.app"
mkdir -p "$APP/Contents/MacOS"
cp build/test-support/libCampTestSupport.dylib build/camp-local "$APP/Contents/MacOS/"
printf '%s\n' '@testable import CampTestSupport' > "$WORK/Audit.swift"
cat Tests/TransferFixture.swift Tests/TransferSourceFixture.swift Tests/TransferReviewAudit.swift >> "$WORK/Audit.swift"
swiftc -parse-as-library -swift-version 5 -I build/test-support -L build/test-support \
  -lCampTestSupport -framework SwiftUI -framework AppKit \
  -Xlinker -rpath -Xlinker @executable_path "$WORK/Audit.swift" \
  -o "$APP/Contents/MacOS/TransferAudit"
PLIST="$APP/Contents/Info.plist"
cat > "$PLIST" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>TransferAudit</string>
<key>CFBundleIdentifier</key><string>com.capybaracamp.transfer-audit</string>
<key>CFBundleName</key><string>Capybara Camp Transfer Audit</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>NSPrincipalClass</key><string>NSApplication</string>
<key>LSMinimumSystemVersion</key><string>14.0</string>
</dict></plist>
PLIST
plutil -insert LSEnvironment -json "{\"CAMP_TRANSFER_AUDIT\":\"$WORK\",\"CAPY_SUPPORT\":\"$WORK/state\"}" "$PLIST"
IDENTITY="${CAMP_SIGN_IDENTITY:--}"
for binary in libCampTestSupport.dylib camp-local TransferAudit; do
  codesign --force --sign "$IDENTITY" --timestamp=none "$APP/Contents/MacOS/$binary"
done
codesign --force --sign "$IDENTITY" --timestamp=none "$APP"
codesign --verify --strict "$APP"
printf 'Built %s\nSynthetic files: %s\n' "$APP" "$WORK"
