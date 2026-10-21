#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FIXTURE_DIR="${1:?Usage: tools/native-fixture.sh /absolute/fixture/directory}"
case "$FIXTURE_DIR" in /*) ;; *) echo "Use an absolute fixture directory" >&2; exit 2 ;; esac
mkdir -p "$FIXTURE_DIR" "$ROOT/build"
chmod 700 "$FIXTURE_DIR"
FIXTURE_CONTAINER="capybara-native-fixture-$$"
docker run --detach --rm --name "$FIXTURE_CONTAINER" --publish 127.0.0.1::5432 \
  --env POSTGRES_DB=camp_test --env POSTGRES_PASSWORD=camp_fixture_only \
  postgres:17@sha256:a63c1ea3f6a3a5a1621f97d2aba3b30faecfab4224c1b0000cc85ced57bb1939 >/dev/null
FIXTURE_PID=""
cleanup() {
  if [ -n "$FIXTURE_PID" ]; then kill -INT "$FIXTURE_PID" 2>/dev/null || true; wait "$FIXTURE_PID" 2>/dev/null || true; fi
  docker stop "$FIXTURE_CONTAINER" >/dev/null
}
trap cleanup EXIT INT TERM
for attempt in {1..40}; do
  if docker exec "$FIXTURE_CONTAINER" pg_isready -U postgres -d camp_test >/dev/null 2>&1; then break; fi
  sleep 0.25
done
FIXTURE_PORT="$(docker port "$FIXTURE_CONTAINER" 5432/tcp | sed -n 's/^127\.0\.0\.1://p')"
if ! [[ "$FIXTURE_PORT" =~ ^[0-9]+$ ]]; then echo "Could not find loopback PostgreSQL port" >&2; exit 1; fi
cd "$ROOT/server"
go test -c -o "$ROOT/build/native-fixture.test" ./internal/httpapi
CAMP_NATIVE_FIXTURE_DIR="$FIXTURE_DIR" \
CAMP_TEST_DATABASE_URL="postgres://postgres:camp_fixture_only@127.0.0.1:$FIXTURE_PORT/camp_test?sslmode=disable" \
  "$ROOT/build/native-fixture.test" -test.run '^TestNativeFixtureServer$' -test.v -test.timeout 1h &
FIXTURE_PID=$!
wait "$FIXTURE_PID"
