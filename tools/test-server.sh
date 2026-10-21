#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ -n "${CAMP_TEST_DATABASE_URL:-}" ]; then
  cd "$ROOT/server"
  go test -race -count=1 ./...
  go vet ./...
  exit
fi

TEST_CONTAINER="capybara-server-tests-$$"
docker run --detach --rm --name "$TEST_CONTAINER" \
  --publish 127.0.0.1::5432 \
  --env POSTGRES_DB=camp_test --env POSTGRES_PASSWORD=camp_fixture_only \
  postgres:17@sha256:a63c1ea3f6a3a5a1621f97d2aba3b30faecfab4224c1b0000cc85ced57bb1939 >/dev/null
trap 'docker stop "$TEST_CONTAINER" >/dev/null' EXIT
for attempt in {1..40}; do
  if docker exec "$TEST_CONTAINER" pg_isready -U postgres -d camp_test >/dev/null 2>&1; then break; fi
  sleep 0.25
done
TEST_PORT="$(docker port "$TEST_CONTAINER" 5432/tcp | sed -n 's/^127\.0\.0\.1://p')"
if ! [[ "$TEST_PORT" =~ ^[0-9]+$ ]]; then
  echo "Could not find the loopback PostgreSQL test port" >&2
  exit 1
fi
cd "$ROOT/server"
CAMP_TEST_DATABASE_URL="postgres://postgres:camp_fixture_only@127.0.0.1:$TEST_PORT/camp_test?sslmode=disable" go test -race -count=1 ./...
go vet ./...
