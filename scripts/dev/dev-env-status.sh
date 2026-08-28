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

load_env

docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" ps

cat <<EOF

Local endpoints:
- PostgreSQL: localhost:${POSTGRES_HOST_PORT}
- Valkey: localhost:${VALKEY_HOST_PORT}
- SeaweedFS master: http://localhost:${SEAWEEDFS_MASTER_HOST_PORT}
- SeaweedFS filer: http://localhost:${SEAWEEDFS_FILER_HOST_PORT}
- SeaweedFS S3: http://localhost:${SEAWEEDFS_S3_HOST_PORT}
EOF
