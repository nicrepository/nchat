#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
TRAEFIK_STATIC_CONFIG="$ROOT_DIR/infra/traefik/local/traefik.yml"
TRAEFIK_DYNAMIC_CONFIG="$ROOT_DIR/infra/traefik/local/dynamic.yml"
LOCAL_CERT_DIR="$ROOT_DIR/infra/traefik/local/certs"
TEMP_CERT_DIR=""

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

if ! grep -q 'rewrite-auth-api-prefix:' "$TRAEFIK_DYNAMIC_CONFIG"; then
  echo "Local gateway must rewrite /api/auth/* to auth-service /auth/* paths." >&2
  exit 1
fi

if ! grep -q 'nchat-auth-health:' "$TRAEFIK_DYNAMIC_CONFIG"; then
  echo "Local gateway must keep /api/auth/healthz probe routing explicit." >&2
  exit 1
fi

if ! grep -q 'nchat-chat-health:' "$TRAEFIK_DYNAMIC_CONFIG"; then
  echo "Local gateway must keep /api/chat/healthz probe routing explicit." >&2
  exit 1
fi

if sed -n '/nchat-chat:/,/nchat-chat-https:/p' "$TRAEFIK_DYNAMIC_CONFIG" | grep -q 'strip-chat-prefix'; then
  echo "Local gateway must not strip /api/chat for normal chat API routes." >&2
  exit 1
fi

if sed -n '/nchat-chat-https:/,/nchat-files:/p' "$TRAEFIK_DYNAMIC_CONFIG" | grep -q 'strip-chat-prefix'; then
  echo "Local gateway must not strip /api/chat for normal HTTPS chat API routes." >&2
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
  if [ -n "$TEMP_CERT_DIR" ]; then
    rm -rf "$TEMP_CERT_DIR"
  fi
}
trap cleanup EXIT

set -a
# shellcheck source=/dev/null
. "$ENV_EXAMPLE"
set +a

TRAEFIK_IMAGE="${TRAEFIK_IMAGE:-traefik:v3.6}"

docker compose --env-file "$ENV_EXAMPLE" -f "$COMPOSE_FILE" --profile gateway config >/dev/null

CERT_MOUNT_DIR="$LOCAL_CERT_DIR"
if [ ! -f "$LOCAL_CERT_DIR/nchat.local.pem" ] || [ ! -f "$LOCAL_CERT_DIR/nchat.local-key.pem" ]; then
  if command -v openssl >/dev/null 2>&1; then
    TEMP_CERT_DIR="$(mktemp -d)"
    OPENSSL_CONFIG="$TEMP_CERT_DIR/openssl.cnf"
    cat >"$OPENSSL_CONFIG" <<'EOF'
[req]
default_bits = 2048
prompt = no
default_md = sha256
distinguished_name = dn
x509_extensions = v3_req

[dn]
CN = nchat.local

[v3_req]
subjectAltName = @alt_names

[alt_names]
DNS.1 = nchat.local
DNS.2 = localhost
IP.1 = 127.0.0.1
EOF
    openssl req -x509 -nodes -days 1 -newkey rsa:2048 \
      -keyout "$TEMP_CERT_DIR/nchat.local-key.pem" \
      -out "$TEMP_CERT_DIR/nchat.local.pem" \
      -config "$OPENSSL_CONFIG" >/dev/null 2>&1
    CERT_MOUNT_DIR="$TEMP_CERT_DIR"
  else
    echo "warning: openssl not found and local TLS certs are absent; skipping Traefik check-config." >&2
    docker run --rm "$TRAEFIK_IMAGE" version >/dev/null
    echo "Gateway config check passed."
    exit 0
  fi
fi

if docker run --rm "$TRAEFIK_IMAGE" check-config --help >/dev/null 2>&1; then
  docker run --rm \
    -v "$TRAEFIK_STATIC_CONFIG:/etc/traefik/traefik.yml:ro" \
    -v "$TRAEFIK_DYNAMIC_CONFIG:/etc/traefik/dynamic.yml:ro" \
    -v "$CERT_MOUNT_DIR:/certs:ro" \
    "$TRAEFIK_IMAGE" \
    check-config --configFile=/etc/traefik/traefik.yml
else
  echo "warning: traefik check-config is unavailable in $TRAEFIK_IMAGE; validating image startup command only." >&2
  docker run --rm "$TRAEFIK_IMAGE" version >/dev/null
fi

echo "Gateway config check passed."
