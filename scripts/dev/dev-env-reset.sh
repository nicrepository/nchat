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

read -r -p "This will delete local dev volumes. Type RESET to continue: " confirmation

if [ "$confirmation" != "RESET" ]; then
  echo "Reset cancelled."
  exit 1
fi

docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" down -v --remove-orphans
