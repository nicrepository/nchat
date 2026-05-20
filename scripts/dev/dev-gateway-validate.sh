#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"

GATEWAY_ENV_KEYS=(
  TRAEFIK_IMAGE
  TRAEFIK_HTTP_HOST_PORT
  TRAEFIK_DASHBOARD_HOST_PORT
  NCHAT_LOCAL_HOST
  WEB_HOST_PORT
  AUTH_SERVICE_HOST_PORT
  CHAT_SERVICE_HOST_PORT
  FILE_SERVICE_HOST_PORT
  NOTIFICATION_SERVICE_HOST_PORT
  ADMIN_SERVICE_HOST_PORT
  SEARCH_SERVICE_HOST_PORT
  MEDIA_SERVICE_HOST_PORT
)

ensure_env_file() {
  if [ ! -f "$ENV_FILE" ]; then
    cp "$ENV_EXAMPLE" "$ENV_FILE"
    echo "Created $ENV_FILE from .env.dev.example."
  fi
}

load_env_file() {
  local file="$1"
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
  done < "$file"
}

load_gateway_env() {
  load_env_file "$ENV_EXAMPLE"
  load_env_file "$ENV_FILE"

  local missing=()
  for key in "${GATEWAY_ENV_KEYS[@]}"; do
    if ! grep -q "^${key}=" "$ENV_FILE"; then
      missing+=("$key")
    fi
  done

  if [ "${#missing[@]}" -gt 0 ]; then
    echo "warning: $ENV_FILE is missing gateway keys; using .env.dev.example defaults for this command:" >&2
    printf '  %s\n' "${missing[@]}" >&2
  fi
}

compose() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" --profile gateway "$@"
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

TRAEFIK_STATIC_CONFIG="$ROOT_DIR/infra/traefik/local/traefik.yml"
TRAEFIK_DYNAMIC_CONFIG="$ROOT_DIR/infra/traefik/local/dynamic.yml"

ensure_env_file
load_gateway_env

require_command docker
require_command curl
docker compose version >/dev/null

for file in "$TRAEFIK_STATIC_CONFIG" "$TRAEFIK_DYNAMIC_CONFIG"; do
  if [ ! -f "$file" ]; then
    echo "Required gateway config missing: ${file#$ROOT_DIR/}" >&2
    exit 1
  fi
done

compose config >/dev/null

container_running=0
if compose ps --status running --services | grep -qx traefik; then
  container_running=1
fi

if [ "$container_running" -eq 0 ]; then
  echo "Traefik is not running; skipped runtime gateway probes."
  echo "NChat local gateway validation passed."
  exit 0
fi

curl --fail --silent --show-error "http://localhost:${TRAEFIK_HTTP_HOST_PORT}/ping" >/dev/null
curl --fail --silent --show-error "http://localhost:${TRAEFIK_DASHBOARD_HOST_PORT}/dashboard/" >/dev/null

probe_route() {
  local name="$1"
  local port="$2"
  local path="$3"

  if curl --fail --silent --show-error --max-time 2 "http://localhost:${port}/healthz" >/dev/null 2>&1; then
    echo "==> Validating gateway route for ${name}"
    curl --fail --silent --show-error --max-time 5 \
      --resolve "${NCHAT_LOCAL_HOST}:${TRAEFIK_HTTP_HOST_PORT}:127.0.0.1" \
      "http://${NCHAT_LOCAL_HOST}:${TRAEFIK_HTTP_HOST_PORT}${path}" >/dev/null
  else
    echo "skip: ${name} is not listening on localhost:${port}; skipped ${path} route probe."
  fi
}

probe_route auth-service "$AUTH_SERVICE_HOST_PORT" /api/auth/healthz
probe_route chat-service "$CHAT_SERVICE_HOST_PORT" /api/chat/healthz
probe_route file-service "$FILE_SERVICE_HOST_PORT" /api/files/healthz
probe_route notification-service "$NOTIFICATION_SERVICE_HOST_PORT" /api/notifications/healthz
probe_route admin-service "$ADMIN_SERVICE_HOST_PORT" /api/admin/healthz
probe_route search-service "$SEARCH_SERVICE_HOST_PORT" /api/search/healthz
probe_route media-service "$MEDIA_SERVICE_HOST_PORT" /api/media/healthz

echo "NChat local gateway validation passed."
