#!/usr/bin/env bash
# Owned, network-none PostgreSQL 16/17; no workstation DSN or user attachments.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TASK_DIR="$(mktemp -d)"
CONTAINERS=()
cleanup() { for name in "${CONTAINERS[@]}"; do docker rm -f -v "$name" >/dev/null; done; rm -rf "$TASK_DIR"; }
trap cleanup EXIT
(cd "$ROOT/services/file-service" && CGO_ENABLED=0 go test -c -covermode=atomic ./internal/storage -o "$TASK_DIR/storage.test")
for version in 16 17; do
 if [[ "$version" == 16 ]]; then
  image=postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54
 else
  image=postgres@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f
 fi
 name="nchat-scan-queue-test-$version-$$"
 CONTAINERS+=("$name")
 docker run -d --name "$name" --network none --label nchat.test=scan-queue \
  -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=nchat_scan_test \
  --mount "type=bind,src=$ROOT/migrations,dst=/repo/migrations,readonly" \
  --mount "type=bind,src=$ROOT/migrations,dst=$ROOT/migrations,readonly" \
  --mount "type=bind,src=$ROOT/scripts/db/grant-runtime.sql,dst=/repo/scripts/db/grant-runtime.sql,readonly" \
  --mount "type=bind,src=$ROOT/scripts/testing/scan-queue-plan.sql,dst=/repo/scripts/testing/scan-queue-plan.sql,readonly" "$image" >/dev/null
 for attempt in {1..60}; do
  if docker exec "$name" pg_isready -h 127.0.0.1 -U postgres -d nchat_scan_test >/dev/null 2>&1; then break; fi
  sleep 1

 done
 docker exec "$name" psql -q -U postgres -d nchat_scan_test -v ON_ERROR_STOP=1 -c \
  'CREATE ROLE nchat_migrator LOGIN; CREATE ROLE nchat_app LOGIN; ALTER DATABASE nchat_scan_test OWNER TO nchat_migrator;'
 docker exec "$name" bash -c 'set -e; shopt -s nullglob; for schema in auth chat files notifications; do
  for f in /repo/migrations/$schema/*.up.sql; do psql -q -U nchat_migrator -d nchat_scan_test -v ON_ERROR_STOP=1 -f "$f"; done; done' >/dev/null
 docker exec "$name" psql -q -U postgres -d nchat_scan_test -v ON_ERROR_STOP=1 -f /repo/scripts/db/grant-runtime.sql
 docker cp "$TASK_DIR/storage.test" "$name:/tmp/storage.test" >/dev/null
 echo "==> owned PostgreSQL $version"
 docker exec "$name" psql -U nchat_migrator -d nchat_scan_test -v ON_ERROR_STOP=1 -c 'SELECT version();'
 docker exec "$name" psql -U nchat_migrator -d nchat_scan_test -v ON_ERROR_STOP=1 -f /repo/scripts/testing/scan-queue-plan.sql
 docker exec -w /tmp -e NCHAT_SCAN_QUEUE_TEST=owned-network-none \
  "$name" /tmp/storage.test -test.run '^TestScanQueueFencingPostgreSQL$' -test.v -test.timeout 120s -test.coverprofile /tmp/scan-queue.out
 docker cp "$name:/tmp/scan-queue.out" "$TASK_DIR/postgres-$version.out" >/dev/null
 awk 'NR>1 && $3>0 {found=1} END {exit found ? 0 : 1}' "$TASK_DIR/postgres-$version.out"
 if [[ -n "${SCAN_QUEUE_COVERAGE_DIR:-}" ]]; then
  mkdir -p "$SCAN_QUEUE_COVERAGE_DIR"
  cp "$TASK_DIR/postgres-$version.out" "$SCAN_QUEUE_COVERAGE_DIR/postgres-$version.out"
 fi
 done
