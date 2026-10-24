#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p build
FIXTURE_DIR="$(mktemp -d "$ROOT/build/native-api.XXXXXX")"
chmod 700 "$FIXTURE_DIR"
touch "$FIXTURE_DIR/.capybara-api-fixture"
FIXTURE_PID=""
cleanup() {
  if [ -n "$FIXTURE_PID" ]; then
    kill -TERM "$FIXTURE_PID" 2>/dev/null || true
    wait "$FIXTURE_PID" 2>/dev/null || true
  fi
  # Remove synthetic credentials and cached conversations after the run.
  rm -rf "$FIXTURE_DIR"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
bash tools/native-fixture.sh "$FIXTURE_DIR" > "$FIXTURE_DIR/server.log" 2>&1 &
FIXTURE_PID=$!
for ((attempt=0; attempt<240; attempt++)); do
  if [ -f "$FIXTURE_DIR/fixture.json" ]; then break; fi
  if ! kill -0 "$FIXTURE_PID" 2>/dev/null; then
    cat "$FIXTURE_DIR/server.log" >&2
    exit 1
  fi
  sleep 0.25
done
if [ ! -f "$FIXTURE_DIR/fixture.json" ]; then
  echo "Timed out starting the native API fixture" >&2
  exit 1
fi
if [ "$#" -eq 0 ]; then set -- NativeImplementationAPITests; fi
CAMP_NATIVE_API_FIXTURE="$FIXTURE_DIR" bash test.sh "$@"
