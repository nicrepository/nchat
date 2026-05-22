#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
if [ ! -f "$ENV_FILE" ]; then
  ENV_FILE="$ROOT_DIR/infra/compose/.env.dev.example"
fi

echo "==> Observability stack status:"
docker compose \
  --env-file "$ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  ps prometheus grafana jaeger

echo
echo "Endpoints:"
echo "  Prometheus : http://localhost:9090"
echo "  Grafana    : http://localhost:3000"
echo "  Jaeger     : http://localhost:16686"
