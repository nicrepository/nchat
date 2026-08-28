#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"

if [ ! -f "$ENV_FILE" ]; then
  cp "$ENV_EXAMPLE" "$ENV_FILE"
  echo "Created $ENV_FILE from .env.dev.example. Review it before using this outside local dev."
fi

docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" up -d
docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" ps

echo "Run 'make dev-env-validate' to validate PostgreSQL, Valkey and SeaweedFS."
