#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"
mkdir -p build/test-support
(cd server && go test -race ./internal/localcli ./internal/config ./cmd/camp-local ./cmd/camp-test-provider && go build -o ../build/camp-test-provider ./cmd/camp-test-provider)

# Compile the shared app code once; every suite exercises that same module.
swiftc -parse-as-library -emit-library -emit-module -enable-testing -D CAMP_TESTING \
  -swift-version 5 -module-name CampTestSupport \
  -emit-module-path build/test-support/CampTestSupport.swiftmodule \
  -framework SwiftUI -framework AppKit -framework Security -framework UserNotifications -lsqlite3 Sources/*.swift \
  -Xlinker -install_name -Xlinker @rpath/libCampTestSupport.dylib \
  -o build/test-support/libCampTestSupport.dylib
WORK="$(mktemp -d "$ROOT/build/.camp-tests.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
SUITES=(CampStateTests CampCloudTests CampSharingTests LocalConversationTests CampWorkerTests SessionMentionsTests LocalPlanningTests ExecutionApprovalTests ImplementationFlowTests CampNotificationTests CampPlanningTests CodexActivityTests CampProjectsTests PermissionSettingsTests CampOnboardingTests CampProviderAccountTests CampGitConnectionTests CampExecutionTests)
if [ "$#" -gt 0 ]; then
  for requested in "$@"; do
    found=false
    for known in "${SUITES[@]}"; do if [ "$requested" = "$known" ]; then found=true; break; fi; done
    if [ "$found" != true ]; then echo "Unknown test suite: $requested" >&2; exit 2; fi
  done
  SUITES=("$@")
fi
for suite in "${SUITES[@]}"; do
  printf '%s\n' '@testable import CampTestSupport' > "$WORK/$suite.swift"
  cat Tests/FixtureProcess.swift >> "$WORK/$suite.swift"
  cat "Tests/$suite.swift" >> "$WORK/$suite.swift"
  swiftc -parse-as-library -swift-version 5 -I build/test-support -L build/test-support \
    -lCampTestSupport -framework SwiftUI -framework AppKit -framework Security -lsqlite3 \
    -Xlinker -rpath -Xlinker @executable_path/test-support \
    "$WORK/$suite.swift" -o "build/$suite"
  "build/$suite"
done
