#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"
mkdir -p build/test-support
(cd server && go test -race ./internal/localcli ./internal/gitcheckout ./internal/config ./cmd/camp-local ./cmd/camp-test-provider && go build -o ../build/camp-test-provider ./cmd/camp-test-provider && go build -o ../build/camp-local ./cmd/camp-local)

# Compile the shared app code once; every suite exercises that same module.
swiftc -parse-as-library -emit-library -emit-module -enable-testing -D CAMP_TESTING \
  -swift-version 5 -module-name CampTestSupport \
  -emit-module-path build/test-support/CampTestSupport.swiftmodule \
  -framework SwiftUI -framework AppKit -framework Security -framework UserNotifications -lsqlite3 Sources/*.swift \
  -Xlinker -install_name -Xlinker @rpath/libCampTestSupport.dylib \
  -o build/test-support/libCampTestSupport.dylib
WORK="$(mktemp -d "$ROOT/build/.camp-tests.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
SUITES=(CampStateTests CampCloudTests CampSharingTests CampChildHistoryTests LocalConversationTests CampWorkerTests CampHandoffTests CampTransferTests CampWorkspaceExportTests CampTransferHistoryTests NativeTransferHistoryAPITests BridgeIntegrationTests SessionMentionsTests LocalPlanningTests LocalPlanImportTests CampPlanPromotionTests ExecutionApprovalTests ImplementationFlowTests CampNotificationTests CampPlanningTests CampPlanningStreamTests ImportedPlanningTests ImportedImplementationTests SharedImplementationTests SharedExecutionReviewTests SharedImplementationFollowupTests NativeImplementationAPITests CodexActivityTests CampProjectsTests PermissionSettingsTests CampOnboardingTests CampProviderAccountTests CampGitConnectionTests CampExecutionTests ExecutionDefaultsTests)
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
  if [ "$suite" = CampTransferTests ]; then cat Tests/TransferFixture.swift >> "$WORK/$suite.swift"; fi
  cat "Tests/$suite.swift" >> "$WORK/$suite.swift"
  swiftc -parse-as-library -swift-version 5 -I build/test-support -L build/test-support \
    -lCampTestSupport -framework SwiftUI -framework AppKit -framework Security -lsqlite3 \
    -Xlinker -rpath -Xlinker @executable_path/test-support \
    "$WORK/$suite.swift" -o "build/$suite"
  if [ "$suite" = BridgeIntegrationTests ]; then
    BUNDLE="$WORK/BridgeHarness.app"
    mkdir -p "$BUNDLE/Contents/MacOS" "$WORK/bridge-support"
    chmod 700 "$WORK/bridge-support"
    cp "build/$suite" "$BUNDLE/Contents/MacOS/$suite"
    cp build/camp-local "$BUNDLE/Contents/MacOS/camp-local"
    ln -s "$ROOT/build/test-support" "$BUNDLE/Contents/MacOS/test-support"
    plutil -create xml1 "$BUNDLE/Contents/Info.plist"
    plutil -insert CFBundleExecutable -string "$suite" "$BUNDLE/Contents/Info.plist"
    plutil -insert CFBundleIdentifier -string com.capybaracamp.bridge-tests "$BUNDLE/Contents/Info.plist"
    plutil -insert CFBundlePackageType -string APPL "$BUNDLE/Contents/Info.plist"
    CAPY_SUPPORT="$WORK/bridge-support" "$BUNDLE/Contents/MacOS/$suite"
  else
    "build/$suite"
  fi
done
