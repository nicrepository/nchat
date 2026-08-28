#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

# shellcheck source=_observability_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_observability_env.sh"

echo "==> Starting observability stack (Prometheus, Grafana, Jaeger)..."
docker compose \
  --env-file "$EFFECTIVE_ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  up -d prometheus grafana jaeger

echo
echo "Observability stack started."
echo "  Prometheus : http://localhost:${PROMETHEUS_HOST_PORT:-9090}"
echo "  Grafana    : http://localhost:${GRAFANA_HOST_PORT:-3000}  (admin / check .env.dev)"
echo "  Jaeger     : http://localhost:${JAEGER_UI_HOST_PORT:-16686}"
echo
echo "Run 'make dev-observability-validate' to verify the stack is healthy."
