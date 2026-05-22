#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

# shellcheck source=_observability_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_observability_env.sh"

echo "==> Observability stack status:"
docker compose \
  --env-file "$EFFECTIVE_ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  ps prometheus grafana jaeger

echo
echo "Endpoints:"
echo "  Prometheus : http://localhost:${PROMETHEUS_HOST_PORT:-9090}"
echo "  Grafana    : http://localhost:${GRAFANA_HOST_PORT:-3000}"
echo "  Jaeger     : http://localhost:${JAEGER_UI_HOST_PORT:-16686}"
