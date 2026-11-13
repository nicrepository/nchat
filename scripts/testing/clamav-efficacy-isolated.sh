#!/usr/bin/env bash
# Dedicated network-none PG16/17 + real official ClamAV. Never accepts a host DSN.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TASK_DIR="$(mktemp -d /tmp/nchat-efficacy.XXXXXXXX)"
PG_NAME=""; SOCKET_VOLUME=""
cleanup() {
 if [[ -n "$PG_NAME" ]]; then docker rm -f -v "$PG_NAME" >/dev/null; fi
 if [[ -n "$SOCKET_VOLUME" ]]; then docker volume rm "$SOCKET_VOLUME" >/dev/null; fi
 rm -rf -- "$TASK_DIR"
}
trap cleanup EXIT
chmod 0755 "$TASK_DIR"
IMAGE='clamav/clamav:1.4@sha256:d97f5ef70fbe9f1f8e5490278082067f16f5824eca2eb4176f4df3748a45a4c2'
docker image inspect "$IMAGE" >/dev/null
cp "$ROOT/infra/k8s/base/services/clamav/clamd.conf" "$TASK_DIR/clamd.conf"
(cd "$ROOT" && CGO_ENABLED=0 go test -c ./services/file-service/internal/http -o "$TASK_DIR/http.test")
for version in 16 17; do
 if [[ "$version" == 16 ]]; then
  PG_IMAGE='postgres@sha256:1a6ab3f5345eb6dbe04a1349529caabdb0ab09293a09590fad07b2246bfa4b54'
 else
  PG_IMAGE='postgres@sha256:31ba9c94a03ada9988f2bb493a22a75eef3a185884533e5cdbcd498ea835b08f'
 fi
 PG_NAME="nchat-d-efficacy-$version-${TASK_DIR##*.}"
 SOCKET_VOLUME="$PG_NAME-socket"
 docker volume create "$SOCKET_VOLUME" >/dev/null
 docker run -d --pull=never --name "$PG_NAME" --network none --memory 512m --cpus 1 --pids-limit 128 \
  --mount "type=volume,source=$SOCKET_VOLUME,target=/var/run/postgresql" \
  --mount "type=bind,source=$ROOT/migrations,target=/repo/migrations,readonly" \
  --mount "type=bind,source=$ROOT/scripts/db/grant-runtime.sql,target=/repo/grant-runtime.sql,readonly" \
  --tmpfs /var/lib/postgresql/data:rw,nosuid,size=256m \
  -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=nchat_scan_test "$PG_IMAGE" >/dev/null
 for attempt in {1..100}; do
  if docker exec "$PG_NAME" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1; then break; fi
  sleep 0.1
 done
 docker exec "$PG_NAME" psql -q -U postgres -d nchat_scan_test -v ON_ERROR_STOP=1 -c \
  'CREATE ROLE nchat_migrator LOGIN; CREATE ROLE nchat_app LOGIN; ALTER DATABASE nchat_scan_test OWNER TO nchat_migrator;'
 docker exec "$PG_NAME" bash -c 'set -e; shopt -s nullglob; for schema in auth chat files notifications; do
  for f in /repo/migrations/$schema/*.up.sql; do psql -q -U nchat_migrator -d nchat_scan_test -v ON_ERROR_STOP=1 -f "$f"; done; done' >/dev/null
 docker exec "$PG_NAME" psql -q -U postgres -d nchat_scan_test -v ON_ERROR_STOP=1 -f /repo/grant-runtime.sql
 # Explicit synthetic-only activation. No operational policy/cluster is accessed.
 docker exec "$PG_NAME" psql -q -U nchat_migrator -d nchat_scan_test -v ON_ERROR_STOP=1 -c \
  "UPDATE files.signature_policy SET enabled=true,max_age_seconds=31536000,approval_reference='isolated-D-fixture-only'; UPDATE files.scan_queue_policy SET enabled=true,approval_reference='isolated-D-fixture-only';"
 echo "==> Efficacy real engine + B/C + runtime role + HTTP: PostgreSQL $version"
 docker run --rm --pull=never --network none --user 100:101 --memory 3g --cpus 2 --pids-limit 64 \
  --cap-drop ALL --security-opt no-new-privileges --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=2g,uid=100,gid=101,mode=700 \
  --mount "type=bind,source=$TASK_DIR,target=/test,readonly" \
  --mount "type=volume,source=$SOCKET_VOLUME,target=/pgsocket,readonly" \
  -e NCHAT_D_EFFICACY_TEST=owned-network-none -e NCHAT_D_BENCHMARK="${NCHAT_D_BENCHMARK:-false}" --entrypoint /test/http.test "$IMAGE" \
  -test.run '^TestIsolatedMalwareEfficacyHTTP$' -test.v -test.timeout 180s
 docker rm -f -v "$PG_NAME" >/dev/null; PG_NAME=""
 docker volume rm "$SOCKET_VOLUME" >/dev/null; SOCKET_VOLUME=""
done
