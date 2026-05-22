#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
if [ ! -f "$ENV_FILE" ]; then
  ENV_FILE="$ROOT_DIR/infra/compose/.env.dev.example"
fi

exec docker compose \
  --env-file "$ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  logs -f prometheus grafana jaeger
