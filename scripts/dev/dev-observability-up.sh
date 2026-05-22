#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
if [ ! -f "$ENV_FILE" ]; then
  ENV_FILE="$ROOT_DIR/infra/compose/.env.dev.example"
fi

echo "==> Starting observability stack (Prometheus, Grafana, Jaeger)..."
docker compose \
  --env-file "$ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  up -d prometheus grafana jaeger

echo
echo "Observability stack started."
echo "  Prometheus : http://localhost:9090"
echo "  Grafana    : http://localhost:3000  (admin / check .env.dev)"
echo "  Jaeger     : http://localhost:16686"
echo
echo "Run 'make dev-observability-validate' to verify the stack is healthy."
