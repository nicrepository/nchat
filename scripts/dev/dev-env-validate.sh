#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"

if [ ! -f "$ENV_FILE" ]; then
  cp "$ENV_EXAMPLE" "$ENV_FILE"
  echo "Created $ENV_FILE from .env.dev.example."
fi

load_env() {
  while IFS='=' read -r key value; do
    case "$key" in
      '' | \#*) continue ;;
    esac
    if [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      value="${value%$'\r'}"
      value="${value%\"}"
      value="${value#\"}"
      export "$key=$value"
    fi
  done < "$ENV_FILE"
}

compose() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

load_env

require_command docker
require_command curl
docker compose version >/dev/null

echo "==> Validating PostgreSQL"
compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"
compose exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "SELECT key, value FROM public.dev_environment_info;"

echo "==> Validating Valkey"
compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli ping'
compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli set nchat:dev:smoke "ok"' >/dev/null
valkey_value="$(compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli get nchat:dev:smoke' | tr -d '\r')"
if [ "$valkey_value" != "ok" ]; then
  echo "Valkey SET/GET validation failed." >&2
  exit 1
fi

compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli del nchat:dev:lock' >/dev/null
lock_result="$(compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli setnx nchat:dev:lock "locked"' | tr -d '\r')"
if [ "$lock_result" != "1" ]; then
  echo "Valkey SETNX validation failed." >&2
  exit 1
fi

compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli expire nchat:dev:lock 30' >/dev/null
ttl_result="$(compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli ttl nchat:dev:lock' | tr -d '\r')"
if [ "$ttl_result" -le 0 ]; then
  echo "Valkey TTL validation failed." >&2
  exit 1
fi

compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli xadd nchat:dev:stream "*" event smoke' >/dev/null
compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli xrange nchat:dev:stream - +' | grep -q smoke

compose exec -T valkey sh -ec '
  out=/tmp/nchat-pubsub.out
  rm -f "$out"
  export REDISCLI_AUTH="$VALKEY_PASSWORD"
  timeout 5 valkey-cli subscribe nchat:dev:pubsub > "$out" 2>/dev/null &
  pid="$!"
  sleep 1
  valkey-cli publish nchat:dev:pubsub smoke >/dev/null
  sleep 1
  kill "$pid" 2>/dev/null || true
  grep -q smoke "$out"
'

compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli del nchat:dev:window' >/dev/null
window_count="$(compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli incr nchat:dev:window' | tr -d '\r')"
compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli expire nchat:dev:window 60' >/dev/null
window_ttl="$(compose exec -T valkey sh -ec 'REDISCLI_AUTH="$VALKEY_PASSWORD" valkey-cli ttl nchat:dev:window' | tr -d '\r')"
if [ "$window_count" != "1" ] || [ "$window_ttl" -le 0 ]; then
  echo "Valkey sliding window validation failed." >&2
  exit 1
fi

echo "==> Validating SeaweedFS"
curl --fail --silent --show-error "http://localhost:${SEAWEEDFS_MASTER_HOST_PORT}/cluster/status" >/dev/null
curl --fail --silent --show-error "http://localhost:${SEAWEEDFS_FILER_HOST_PORT}/" >/dev/null

tmp_file="$(mktemp)"
download_file="$(mktemp)"
cleanup() {
  rm -f "$tmp_file" "$download_file"
}
trap cleanup EXIT

printf 'nchat seaweedfs smoke test\n' > "$tmp_file"
curl --fail --silent --show-error -F "file=@${tmp_file}" \
  "http://localhost:${SEAWEEDFS_FILER_HOST_PORT}/dev-smoke-test.txt" >/dev/null
curl --fail --silent --show-error \
  "http://localhost:${SEAWEEDFS_FILER_HOST_PORT}/dev-smoke-test.txt" > "$download_file"
cmp "$tmp_file" "$download_file"
curl --fail --silent --show-error -X DELETE \
  "http://localhost:${SEAWEEDFS_FILER_HOST_PORT}/dev-smoke-test.txt" >/dev/null

echo "NChat dev environment validation passed."
