#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
if [ ! -f "$ENV_FILE" ]; then
  ENV_FILE="$ROOT_DIR/infra/compose/.env.dev.example"
fi

echo "==> Stopping observability stack (Prometheus, Grafana, Jaeger)..."
docker compose \
  --env-file "$ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  stop prometheus grafana jaeger

docker compose \
  --env-file "$ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  rm -f prometheus grafana jaeger

echo "Observability stack stopped. Volumes preserved."
echo "To remove volumes: docker volume rm nchat-dev_nchat_prometheus_data nchat-dev_nchat_grafana_data"
