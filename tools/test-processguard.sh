#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p build
ARCH="$(go env GOARCH)"
GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go -C server test -c -o "$ROOT/build/processguard-linux.test" ./internal/processguard
docker run --rm --platform "linux/$ARCH" --network none --user 65534:65534 \
  --read-only --cap-drop ALL --security-opt no-new-privileges -e CAMP_REQUIRE_PROCESS_GUARD=1 \
  --tmpfs /tmp:mode=1777 \
  --mount "type=bind,source=$ROOT/build/processguard-linux.test,target=/guard.test,readonly" \
  node:24-bookworm-slim@sha256:55fb5f5a527ea2c5c4a95f963e48ef90aa38c7b346054fdc8ee9957a2b55d205 \
  /guard.test -test.v
