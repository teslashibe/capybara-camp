#!/bin/bash
set -euo pipefail
umask 077
unset COPYFILE_DISABLE DITTONORSRC DITTO_TEST_OPTIONS
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
usage() { echo "Usage: tools/package-release.sh --development | --release | --resume SUBMISSION_ID" >&2; exit 2; }
fail() { echo "$*" >&2; exit 1; }
MODE="${1:-}"
case "$MODE:$#" in --development:1|--release:1|--resume:2) ;; *) usage ;; esac
VERSION="$(cat VERSION)"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "Invalid VERSION"
mkdir -p build dist
WORK="$(mktemp -d "$ROOT/build/.camp-package.XXXXXX")"
LOCK=""
cleanup() {
  rm -rf "$WORK"
  if [ -n "$LOCK" ]; then rm -f "$LOCK/pid"; rmdir "$LOCK"; fi
}
trap cleanup EXIT
acquire_lock() {
  local candidate="$ROOT/build/$1.lock"
  mkdir "$candidate" 2>/dev/null || fail "Packaging is already running or its lock needs review: $candidate"
  LOCK="$candidate"
  printf '%s\n' "$$" > "$LOCK/pid"
}
APP="$WORK/Capybara Camp.app"

if [ "$MODE" = --development ]; then
  acquire_lock "development-$VERSION"
  OUTPUT="$ROOT/dist/development-$VERSION"
  [ ! -e "$OUTPUT" ] || fail "Development package already exists: $OUTPUT"
  CAMP_APP_PATH="$APP" ./build.sh --universal
  mkdir "$WORK/package"
  ZIP="Capybara-Camp-$VERSION-macos-universal-DEVELOPMENT.zip"
  ditto -c -k --norsrc --keepParent "$APP" "$WORK/package/$ZIP"
  printf '%s\n' 'Development build: not notarized and not a public release artifact.' > "$WORK/package/DEVELOPMENT.txt"
  (cd "$WORK/package" && shasum -a 256 "$ZIP" DEVELOPMENT.txt > SHA256SUMS)
  mv "$WORK/package" "$OUTPUT"
  echo "Packaged development build: $OUTPUT"
  exit 0
fi

TEAM="${CAMP_SIGN_TEAM_ID:-}"
PROFILE="${CAMP_NOTARY_PROFILE:-}"
[[ "$TEAM" =~ ^[A-Z0-9]{10}$ ]] || fail "Set the expected CAMP_SIGN_TEAM_ID"
[ -n "$PROFILE" ] || fail "Set CAMP_NOTARY_PROFILE to a saved notarytool credential profile"
acquire_lock "release-$VERSION"
[ -z "$(git status --porcelain --untracked-files=normal)" ] || fail "Commit the complete release candidate before packaging"
COMMIT="$(git rev-parse HEAD)"
[[ "$COMMIT" =~ ^[a-f0-9]{40}$ ]] || fail "Invalid source commit"
check_source() {
  [ "$(git rev-parse HEAD)" = "$COMMIT" ] && [ -z "$(git status --porcelain --untracked-files=normal)" ] || fail "The source changed during packaging; restore and review the original candidate"
}
NOTES="releases/v$VERSION.md"
git ls-files --error-unmatch "$NOTES" >/dev/null 2>&1 || fail "Commit $NOTES before packaging"
OUTPUT="$ROOT/dist/v$VERSION"
[ ! -e "$OUTPUT" ] || fail "Release artifacts already exist: $OUTPUT (never overwrite published artifacts)"
STAGE="$ROOT/build/release-$VERSION-$COMMIT"
UPLOAD="$STAGE/Capybara-Camp-$VERSION-$COMMIT-upload.zip"

if [ "$MODE" = --release ]; then
  [ -n "${CAMP_SIGN_IDENTITY:-}" ] && [ "$CAMP_SIGN_IDENTITY" != - ] || fail "Set CAMP_SIGN_IDENTITY to the Developer ID Application identity"
  [ ! -e "$STAGE" ] || fail "A preparation already exists. Inspect it and resume its submission; do not submit again blindly: $STAGE"
  CAMP_APP_PATH="$APP" ./build.sh --release
  check_source
  mkdir "$STAGE"
  # This immutable upload is retained locally. Recovery extracts it again,
  # never a possibly changed app bundle from a later build.
  ditto -c -k --keepParent "$APP" "$UPLOAD"
  plutil -create xml1 "$STAGE/candidate.plist"
  plutil -insert commit -string "$COMMIT" "$STAGE/candidate.plist"
  plutil -insert version -string "$VERSION" "$STAGE/candidate.plist"
  plutil -insert team -string "$TEAM" "$STAGE/candidate.plist"
  plutil -insert sha256 -string "$(shasum -a 256 "$UPLOAD" | cut -d ' ' -f 1)" "$STAGE/candidate.plist"
  if ! xcrun notarytool submit "$UPLOAD" --keychain-profile "$PROFILE" --no-wait --output-format plist > "$STAGE/submission.plist"; then
    fail "Notarization submission was not confirmed. Keep $STAGE; inspect notarytool history before using --resume with its submission ID"
  fi
  SUBMISSION="$(plutil -extract id raw "$STAGE/submission.plist")"
else
  SUBMISSION="$2"
fi
[[ "$SUBMISSION" =~ ^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$ ]] || fail "Invalid notarization submission ID"
[ -f "$STAGE/candidate.plist" ] && [ -f "$UPLOAD" ] || fail "The retained candidate for this source commit is missing"
for key in commit version team; do
  case "$key" in commit) expected="$COMMIT" ;; version) expected="$VERSION" ;; team) expected="$TEAM" ;; esac
  [ "$(plutil -extract "$key" raw "$STAGE/candidate.plist")" = "$expected" ] || fail "Retained candidate $key changed"
done
[ "$(shasum -a 256 "$UPLOAD" | cut -d ' ' -f 1)" = "$(plutil -extract sha256 raw "$STAGE/candidate.plist")" ] || fail "Retained upload changed"
if [ -f "$STAGE/submission-id" ]; then
  [ "$(cat "$STAGE/submission-id")" = "$SUBMISSION" ] || fail "This candidate already has a different notarization submission"
fi
xcrun notarytool info "$SUBMISSION" --keychain-profile "$PROFILE" --output-format plist > "$STAGE/status.plist"
[ "$(plutil -extract id raw "$STAGE/status.plist")" = "$SUBMISSION" ] || fail "Notarization returned a different submission"
[ "$(plutil -extract name raw "$STAGE/status.plist")" = "$(basename "$UPLOAD")" ] || fail "Notarization belongs to a different upload"
printf '%s\n' "$SUBMISSION" > "$STAGE/submission-id"
STATUS="$(plutil -extract status raw "$STAGE/status.plist")"
case "$STATUS" in
  Accepted) ;;
  'In Progress') echo "Notarization is processing. Resume this exact candidate: tools/package-release.sh --resume $SUBMISSION"; exit 0 ;;
  *) fail "Notarization status: $STATUS. Inspect the submission log; no release archive was created" ;;
esac

ditto -x -k "$UPLOAD" "$WORK"
"$ROOT/tools/verify-macos-release.sh" "$APP"
xcrun stapler staple "$APP"
"$ROOT/tools/verify-macos-release.sh" "$APP" --notarized
mkdir "$WORK/package"
ZIP="Capybara-Camp-$VERSION-macos-universal.zip"
# Preserve the stapled ticket and resource metadata in the final archive.
ditto -c -k --keepParent "$APP" "$WORK/package/$ZIP"
mkdir "$WORK/extracted"
ditto -x -k "$WORK/package/$ZIP" "$WORK/extracted"
"$ROOT/tools/verify-macos-release.sh" "$WORK/extracted/Capybara Camp.app" --notarized
cp docs/index.html "$WORK/package/Capybara-Camp-Getting-Started.html"
cp "$NOTES" "$WORK/package/RELEASE-NOTES.md"
{
  printf 'Version: %s\nSource commit: %s\nSigning team: %s\nArchitectures: arm64 x86_64\nNotarization submission: %s\n' "$VERSION" "$COMMIT" "$TEAM" "$SUBMISSION"
  swiftc --version
  go version
} > "$WORK/package/BUILD-INFO.txt"
(cd "$WORK/package" && shasum -a 256 "$ZIP" Capybara-Camp-Getting-Started.html RELEASE-NOTES.md BUILD-INFO.txt > SHA256SUMS && shasum -a 256 -c SHA256SUMS)
check_source
chmod 755 "$WORK/package"
chmod 644 "$WORK/package/"*
[ ! -e "$OUTPUT" ] || fail "Release artifacts appeared during packaging; they were not overwritten"
mv "$WORK/package" "$OUTPUT"
echo "Verified release artifacts: $OUTPUT"
echo "No Git push, tag, GitHub release or website deployment was performed"
