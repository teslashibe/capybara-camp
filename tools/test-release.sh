#!/bin/bash
# No Apple login, certificate, provider or network is used by these fixtures.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$ROOT/build"
WORK="$(mktemp -d "$ROOT/build/.release-tests.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
REPO="$WORK/repo"
BIN="$WORK/bin"
mkdir -p "$REPO/tools" "$REPO/docs" "$REPO/releases" "$BIN"
cp "$ROOT/tools/package-release.sh" "$ROOT/tools/verify-macos-release.sh" "$REPO/tools/"
printf '1.2.3\n' > "$REPO/VERSION"
printf '<html>Fixture setup</html>\n' > "$REPO/docs/index.html"
printf '# Fixture release\n' > "$REPO/releases/v1.2.3.md"
printf '/build/\n/dist/\n' > "$REPO/.gitignore"
export CAMP_RELEASE_TEST_LOG="$WORK/calls"
export CAMP_RELEASE_TEST_STATUS="$WORK/status"
export CAMP_RELEASE_TEST_FAIL=""
export CAMP_SIGN_IDENTITY="Fixture Developer ID"
export CAMP_SIGN_TEAM_ID=TESTTEAM01
export CAMP_NOTARY_PROFILE="Fixture notary profile"
printf 'In Progress\n' > "$CAMP_RELEASE_TEST_STATUS"
cat > "$REPO/build.sh" <<'SH'
#!/bin/bash
set -euo pipefail
echo "build:$1" >> "$CAMP_RELEASE_TEST_LOG"
if [ "$CAMP_RELEASE_TEST_FAIL" = source ]; then printf 'changed\n' > changed-source; fi
mkdir -p "$CAMP_APP_PATH/Contents/MacOS" "$CAMP_APP_PATH/Contents/Resources"
for file in CapybaraCamp camp-local; do printf 'fixture executable\n' > "$CAMP_APP_PATH/Contents/MacOS/$file"; chmod +x "$CAMP_APP_PATH/Contents/MacOS/$file"; done
for file in AppIcon.icns capybara.png; do printf 'fixture resource\n' > "$CAMP_APP_PATH/Contents/Resources/$file"; done
plutil -create xml1 "$CAMP_APP_PATH/Contents/Info.plist"
plutil -insert CFBundleIdentifier -string com.capybaracamp.app "$CAMP_APP_PATH/Contents/Info.plist"
plutil -insert CFBundleExecutable -string CapybaraCamp "$CAMP_APP_PATH/Contents/Info.plist"
plutil -insert CFBundleShortVersionString -string 1.2.3 "$CAMP_APP_PATH/Contents/Info.plist"
SH
cat > "$BIN/codesign" <<'SH'
#!/bin/bash
set -euo pipefail
if [ "$1" = --display ]; then
  echo "$*" >> "$CAMP_RELEASE_TEST_LOG"
  echo 'CodeDirectory flags=0x10000(runtime)'
  if [ "$CAMP_RELEASE_TEST_FAIL" != timestamp ] && { [ "$CAMP_RELEASE_TEST_FAIL" != timestamp_x86 ] || [[ "$*" != *'--arch x86_64'* ]]; }; then echo 'Timestamp=fixture'; fi
else
  [[ "$*" == *--all-architectures* && "$*" == *1.2.840.113635.100.6.1.13* && "$*" == *TESTTEAM01* ]] || exit 1
  [ "$CAMP_RELEASE_TEST_FAIL" != signature ] || exit 1
fi
SH
cat > "$BIN/lipo" <<'SH'
#!/bin/bash
set -euo pipefail
[[ "$*" == *'-verify_arch arm64 x86_64'* ]] && [ "$CAMP_RELEASE_TEST_FAIL" != architecture ]
SH
cat > "$BIN/xcrun" <<'SH'
#!/bin/bash
set -euo pipefail
echo "$1:$2" >> "$CAMP_RELEASE_TEST_LOG"
if [ "$1" = stapler ]; then
  target="${!#}"
  case "$2" in
    staple) [ "$CAMP_RELEASE_TEST_FAIL" != staple ] || exit 1; printf 'fixture ticket\n' > "$target/Contents/CodeResources" ;;
    validate) [ -f "$target/Contents/CodeResources" ] || exit 1 ;;
    *) exit 1 ;;
  esac
  exit 0
fi
[ "$1" = notarytool ] || exit 1
case "$2" in
  submit)
    [ "$CAMP_RELEASE_TEST_FAIL" != submission ] || exit 1
    status='In Progress' ;;
  info) status="$(cat "$CAMP_RELEASE_TEST_STATUS")" ;;
  *) exit 1 ;;
esac
name="$(basename "$(find build -name '*-upload.zip' -print -quit)")"
if [ "$CAMP_RELEASE_TEST_FAIL" = receipt ]; then name=wrong.zip; fi
printf '<?xml version="1.0"?><plist version="1.0"><dict><key>id</key><string>12345678-1234-1234-1234-123456789abc</string><key>name</key><string>%s</string><key>status</key><string>%s</string></dict></plist>\n' "$name" "$status"
SH
cat > "$BIN/spctl" <<'SH'
#!/bin/bash
set -euo pipefail
[ -f "${!#}/Contents/CodeResources" ] && [ "$CAMP_RELEASE_TEST_FAIL" != gatekeeper ]
SH
cat > "$BIN/swiftc" <<'SH'
#!/bin/bash
echo 'Swift fixture'
SH
cat > "$BIN/go" <<'SH'
#!/bin/bash
echo 'Go fixture'
SH
chmod +x "$REPO/build.sh" "$BIN/"*
export PATH="$BIN:$PATH"
git -C "$REPO" init -q
git -C "$REPO" add .
git -C "$REPO" -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm fixture
COMMIT="$(git -C "$REPO" rev-parse HEAD)"
STAGE="$REPO/build/release-1.2.3-$COMMIT"
SUBMISSION=12345678-1234-1234-1234-123456789abc
expect_failure() {
  if "$@" > "$WORK/failure.log" 2>&1; then echo "Expected failure: $*" >&2; exit 1; fi
}
expect_failure "$REPO/tools/package-release.sh"
expect_failure env CAMP_SIGN_TEAM_ID=bad "$REPO/tools/package-release.sh" --release
mkdir -p "$REPO/build/release-1.2.3.lock"
expect_failure "$REPO/tools/package-release.sh" --release
rmdir "$REPO/build/release-1.2.3.lock"
printf 'dirty\n' > "$REPO/untracked"
expect_failure "$REPO/tools/package-release.sh" --release
rm "$REPO/untracked"
expect_failure env CAMP_RELEASE_TEST_FAIL=source "$REPO/tools/package-release.sh" --release
[ ! -e "$STAGE" ]
rm "$REPO/changed-source"
"$REPO/tools/package-release.sh" --release > "$WORK/pending.log"
[ ! -e "$REPO/dist/v1.2.3" ]
[ "$(awk '/^notarytool:submit$/ {n++} END {print n+0}' "$CAMP_RELEASE_TEST_LOG")" = 1 ]
expect_failure "$REPO/tools/package-release.sh" --release
expect_failure "$REPO/tools/package-release.sh" --resume 87654321-1234-1234-1234-123456789abc
"$REPO/tools/package-release.sh" --resume "$SUBMISSION" > "$WORK/pending-again.log"
[ "$(awk '/^notarytool:submit$/ {n++} END {print n+0}' "$CAMP_RELEASE_TEST_LOG")" = 1 ]
printf 'Invalid\n' > "$CAMP_RELEASE_TEST_STATUS"
expect_failure "$REPO/tools/package-release.sh" --resume "$SUBMISSION"
[ ! -e "$REPO/dist/v1.2.3" ]
printf 'Accepted\n' > "$CAMP_RELEASE_TEST_STATUS"
for failure in architecture signature timestamp timestamp_x86 staple gatekeeper receipt; do
  expect_failure env CAMP_RELEASE_TEST_FAIL="$failure" "$REPO/tools/package-release.sh" --resume "$SUBMISSION"
  [ ! -e "$REPO/dist/v1.2.3" ]
done
"$REPO/tools/package-release.sh" --resume "$SUBMISSION" > "$WORK/accepted.log"
[ "$(awk '/^notarytool:submit$/ {n++} END {print n+0}' "$CAMP_RELEASE_TEST_LOG")" = 1 ]
(cd "$REPO/dist/v1.2.3" && shasum -a 256 -c SHA256SUMS)
expect_failure "$REPO/tools/package-release.sh" --resume "$SUBMISSION"
mkdir "$WORK/extracted"
ditto -x -k "$REPO/dist/v1.2.3/Capybara-Camp-1.2.3-macos-universal.zip" "$WORK/extracted"
APP="$WORK/extracted/Capybara Camp.app"
"$REPO/tools/verify-macos-release.sh" "$APP" --notarized
printf 'secret fixture\n' > "$APP/Contents/Resources/auth.json"
expect_failure "$REPO/tools/verify-macos-release.sh" "$APP"
rm "$APP/Contents/Resources/auth.json"
ln -s /tmp "$APP/Contents/Resources/escape"
expect_failure "$REPO/tools/verify-macos-release.sh" "$APP"
rm "$APP/Contents/Resources/escape"
rm "$APP/Contents/Resources/capybara.png"
expect_failure "$REPO/tools/verify-macos-release.sh" "$APP"

# A lost upload response never causes an automatic second submission. The
# operator can recover the ID, then finish from the retained, hashed archive.
rm -rf "$STAGE" "$REPO/dist/v1.2.3"
expect_failure env CAMP_RELEASE_TEST_FAIL=submission "$REPO/tools/package-release.sh" --release
expect_failure "$REPO/tools/package-release.sh" --release
expect_failure env CAMP_RELEASE_TEST_FAIL=receipt "$REPO/tools/package-release.sh" --resume "$SUBMISSION"
[ ! -e "$STAGE/submission-id" ]
UPLOAD="$STAGE/Capybara-Camp-1.2.3-$COMMIT-upload.zip"
cp "$UPLOAD" "$WORK/upload-backup.zip"
printf 'tampered\n' >> "$UPLOAD"
expect_failure "$REPO/tools/package-release.sh" --resume "$SUBMISSION"
cp "$WORK/upload-backup.zip" "$UPLOAD"
"$REPO/tools/package-release.sh" --resume "$SUBMISSION" > "$WORK/recovered.log"
[ "$(awk '/^notarytool:submit$/ {n++} END {print n+0}' "$CAMP_RELEASE_TEST_LOG")" = 2 ]
"$REPO/tools/package-release.sh" --development > "$WORK/development.log"
[ -f "$REPO/dist/development-1.2.3/Capybara-Camp-1.2.3-macos-universal-DEVELOPMENT.zip" ]
expect_failure "$REPO/tools/package-release.sh" --development
echo 'PASS: release gates, all architecture checks, lost-submit recovery, artifact preservation and checksums (synthetic Apple tools)'
