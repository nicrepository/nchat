#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

# shellcheck source=_observability_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_observability_env.sh"

echo "==> Stopping observability stack (Prometheus, Grafana, Jaeger)..."
docker compose \
  --env-file "$EFFECTIVE_ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  stop prometheus grafana jaeger

docker compose \
  --env-file "$EFFECTIVE_ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  rm -f prometheus grafana jaeger

echo "Observability stack stopped. Volumes preserved."
echo "To remove volumes: docker volume rm nchat-dev_nchat_prometheus_data nchat-dev_nchat_grafana_data"
