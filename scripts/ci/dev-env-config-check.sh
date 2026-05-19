#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
VALKEY_CONFIG="$ROOT_DIR/infra/compose/valkey/valkey.conf"
POSTGRES_INIT="$ROOT_DIR/infra/compose/postgres/init/001_init.sql"

require_file() {
  if [ ! -f "$1" ]; then
    echo "Required file is missing: $1" >&2
    exit 1
  fi
}

require_file "$COMPOSE_FILE"
require_file "$ENV_EXAMPLE"
require_file "$VALKEY_CONFIG"
require_file "$POSTGRES_INIT"

if git -C "$ROOT_DIR" ls-files --error-unmatch infra/compose/.env.dev >/dev/null 2>&1; then
  echo "infra/compose/.env.dev must not be versioned." >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker not found; skipping Docker Compose config validation."
  exit 0
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 not found; skipping Docker Compose config validation."
  exit 0
fi

created_env=0
if [ ! -f "$ENV_FILE" ]; then
  cp "$ENV_EXAMPLE" "$ENV_FILE"
  created_env=1
fi

cleanup() {
  if [ "$created_env" -eq 1 ]; then
    rm -f "$ENV_FILE"
  fi
}
trap cleanup EXIT

docker compose --env-file "$ENV_EXAMPLE" -f "$COMPOSE_FILE" config >/dev/null
echo "NChat dev environment config check passed."
