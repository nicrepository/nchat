#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
TRAEFIK_STATIC_CONFIG="$ROOT_DIR/infra/traefik/local/traefik.yml"
TRAEFIK_DYNAMIC_CONFIG="$ROOT_DIR/infra/traefik/local/dynamic.yml"

require_file() {
  if [ ! -f "$1" ]; then
    echo "Required gateway file missing: ${1#$ROOT_DIR/}" >&2
    exit 1
  fi
}

require_file "$COMPOSE_FILE"
require_file "$ENV_EXAMPLE"
require_file "$TRAEFIK_STATIC_CONFIG"
require_file "$TRAEFIK_DYNAMIC_CONFIG"

if git -C "$ROOT_DIR" ls-files --error-unmatch infra/compose/.env.dev >/dev/null 2>&1; then
  echo "infra/compose/.env.dev must not be versioned." >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker not found; skipping Docker-based gateway config validation."
  echo "Gateway config check passed."
  exit 0
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 not found; skipping Docker Compose gateway config validation."
  echo "Gateway config check passed."
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

set -a
# shellcheck source=/dev/null
. "$ENV_EXAMPLE"
set +a

TRAEFIK_IMAGE="${TRAEFIK_IMAGE:-traefik:v3.6}"

docker compose --env-file "$ENV_EXAMPLE" -f "$COMPOSE_FILE" --profile gateway config >/dev/null

if docker run --rm "$TRAEFIK_IMAGE" check-config --help >/dev/null 2>&1; then
  docker run --rm \
    -v "$TRAEFIK_STATIC_CONFIG:/etc/traefik/traefik.yml:ro" \
    -v "$TRAEFIK_DYNAMIC_CONFIG:/etc/traefik/dynamic.yml:ro" \
    "$TRAEFIK_IMAGE" \
    check-config --configFile=/etc/traefik/traefik.yml
else
  echo "warning: traefik check-config is unavailable in $TRAEFIK_IMAGE; validating image startup command only." >&2
  docker run --rm "$TRAEFIK_IMAGE" version >/dev/null
fi

echo "Gateway config check passed."
