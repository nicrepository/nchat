#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"

GATEWAY_ENV_KEYS=(
  TRAEFIK_IMAGE
  TRAEFIK_HTTP_HOST_PORT
  TRAEFIK_HTTPS_HOST_PORT
  TRAEFIK_DASHBOARD_HOST_PORT
  TRAEFIK_TLS_CERT_FILE
  TRAEFIK_TLS_KEY_FILE
  NCHAT_LOCAL_HOST
  NCHAT_LOCAL_HTTPS_URL
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

ensure_env_file
load_gateway_env

require_command docker
docker compose version >/dev/null

compose ps traefik

cat <<EOF

Local gateway endpoints:
- HTTP gateway: http://${NCHAT_LOCAL_HOST}:${TRAEFIK_HTTP_HOST_PORT}
- HTTPS gateway: ${NCHAT_LOCAL_HTTPS_URL}
- Traefik dashboard: http://localhost:${TRAEFIK_DASHBOARD_HOST_PORT}/dashboard/
EOF
