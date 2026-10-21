#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
./build.sh --universal
VERSION="$(cat VERSION)"
mkdir -p dist
ZIP="Capybara-Camp-$VERSION-macos-universal.zip"
COPYFILE_DISABLE=1 ditto -c -k --norsrc --keepParent "build/Capybara Camp.app" "dist/$ZIP"
(
  cd dist
  cp "$ROOT/docs/index.html" Capybara-Camp-Getting-Started.html
  shasum -a 256 "$ZIP" Capybara-Camp-Getting-Started.html > SHA256SUMS
)
echo "Packaged dist/$ZIP"
