#!/bin/bash
# Verify a distribution candidate without changing it or contacting a provider.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ] || { [ "$#" -eq 2 ] && [ "$2" != --notarized ]; }; then
  echo "Usage: CAMP_SIGN_TEAM_ID=TEAM tools/verify-macos-release.sh APP [--notarized]" >&2; exit 2
fi
APP="$1"
TEAM="${CAMP_SIGN_TEAM_ID:-}"
[[ "$TEAM" =~ ^[A-Z0-9]{10}$ ]] || { echo "Set the expected CAMP_SIGN_TEAM_ID" >&2; exit 2; }
VERSION="$(cat "$ROOT/VERSION")"
fail() { echo "Release verification failed: $*" >&2; exit 1; }
[ -d "$APP" ] && [ ! -L "$APP" ] || fail "missing app bundle"
[ -z "$(find "$APP" ! -type d ! -type f -print -quit)" ] || fail "unexpected symlink or special file in app"
PLIST="$APP/Contents/Info.plist"
[ "$(plutil -extract CFBundleIdentifier raw "$PLIST")" = com.capybaracamp.app ] || fail "bundle identifier"
[ "$(plutil -extract CFBundleExecutable raw "$PLIST")" = CapybaraCamp ] || fail "executable name"
[ "$(plutil -extract CFBundleShortVersionString raw "$PLIST")" = "$VERSION" ] || fail "version"
# An allowlist prevents test harnesses, credential files and private fixtures
# from entering a public archive, even when copied into an unexpected folder.
while IFS= read -r -d '' file; do
  case "${file#"$APP/"}" in
    Contents/Info.plist|Contents/MacOS/CapybaraCamp|Contents/MacOS/camp-local|Contents/Resources/AppIcon.icns|Contents/Resources/capybara.png|Contents/_CodeSignature/CodeResources|Contents/CodeResources) ;;
    *) fail "unexpected bundle file" ;;
  esac
done < <(find "$APP" -type f -print0)
for resource in AppIcon.icns capybara.png; do [ -s "$APP/Contents/Resources/$resource" ] || fail "missing resource"; done
for binary in CapybaraCamp camp-local; do
  [ -x "$APP/Contents/MacOS/$binary" ] || fail "missing executable"
  lipo "$APP/Contents/MacOS/$binary" -verify_arch arm64 x86_64
done
for target in "$APP/Contents/MacOS/camp-local" "$APP"; do
  identifier=com.capybaracamp.app
  if [ "$target" != "$APP" ]; then identifier="$identifier.camp-local"; fi
  requirement="=anchor apple generic and identifier \"$identifier\" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \"$TEAM\""
  codesign --verify --strict --all-architectures -R "$requirement" "$target"
  # Check each slice; a universal file must not hide an ad-hoc/untimestamped
  # architecture behind a correctly signed native slice.
  for arch in arm64 x86_64; do
    details="$(codesign --display --verbose=4 --arch "$arch" "$target" 2>&1)"
    [[ "$details" == *"(runtime)"* ]] || fail "hardened runtime is missing"
    [[ "$details" == *$'\nTimestamp='* ]] || fail "secure timestamp is missing"
  done
done
if [ "${2:-}" = --notarized ]; then
  xcrun stapler validate "$APP"
  spctl --assess --type execute --verbose=2 "$APP"
fi
echo "Verified universal Developer ID app${2:+ and notarization}"
