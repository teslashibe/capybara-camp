#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"
export PYTHONDONTWRITEBYTECODE=1
python3 -c 'import sys; sys.exit("Python 3.11 or newer is required") if sys.version_info < (3, 11) else None'
mkdir -p build/test-support
python3 -m unittest discover -s Tests -v

# Compile the shared app code once; every suite exercises that same module.
swiftc -parse-as-library -emit-library -emit-module -enable-testing -D CAMP_TESTING \
  -swift-version 5 -module-name CampTestSupport \
  -emit-module-path build/test-support/CampTestSupport.swiftmodule \
  -framework SwiftUI -framework AppKit -framework Security -lsqlite3 Sources/*.swift \
  -Xlinker -install_name -Xlinker @rpath/libCampTestSupport.dylib \
  -o build/test-support/libCampTestSupport.dylib
WORK="$(mktemp -d "$ROOT/build/.camp-tests.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
for suite in CampStateTests CampCloudTests CampSharingTests LocalConversationTests CampWorkerTests SessionMentionsTests LocalPlanningTests; do
  printf '%s\n' '@testable import CampTestSupport' > "$WORK/$suite.swift"
  cat "Tests/$suite.swift" >> "$WORK/$suite.swift"
  swiftc -parse-as-library -swift-version 5 -I build/test-support -L build/test-support \
    -lCampTestSupport -framework SwiftUI -framework AppKit -framework Security -lsqlite3 \
    -Xlinker -rpath -Xlinker @executable_path/test-support \
    "$WORK/$suite.swift" -o "build/$suite"
  "build/$suite"
done
