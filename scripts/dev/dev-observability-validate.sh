#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"
if [ ! -f "$ENV_FILE" ]; then
  ENV_FILE="$ROOT_DIR/infra/compose/.env.dev.example"
fi

PROMETHEUS_PORT="${PROMETHEUS_HOST_PORT:-9090}"
GRAFANA_PORT="${GRAFANA_HOST_PORT:-3000}"
JAEGER_PORT="${JAEGER_UI_HOST_PORT:-16686}"

ERRORS=0

check_http() {
  local name="$1"
  local url="$2"
  if curl -sf --max-time 5 "$url" > /dev/null 2>&1; then
    echo "  [OK]   $name — $url"
  else
    echo "  [FAIL] $name — $url"
    ERRORS=$((ERRORS + 1))
  fi
}

skip_http() {
  local name="$1"
  local url="$2"
  if curl -sf --max-time 3 "$url" > /dev/null 2>&1; then
    echo "  [OK]   $name — $url"
  else
    echo "  [SKIP] $name — $url (not running, skipping)"
  fi
}

echo "==> Validating observability stack..."
echo

echo "--- Core stack ---"
check_http "Prometheus /-/ready"  "http://localhost:${PROMETHEUS_PORT}/-/ready"
check_http "Grafana /api/health"  "http://localhost:${GRAFANA_PORT}/api/health"
check_http "Jaeger /"             "http://localhost:${JAEGER_PORT}/"

echo
echo "--- Go services (skipped if not running) ---"
skip_http "auth-service     /metrics" "http://localhost:8081/metrics"
skip_http "chat-service     /metrics" "http://localhost:8082/metrics"
skip_http "file-service     /metrics" "http://localhost:8083/metrics"
skip_http "notification-svc /metrics" "http://localhost:8084/metrics"
skip_http "admin-service    /metrics" "http://localhost:8085/metrics"
skip_http "search-service   /metrics" "http://localhost:8086/metrics"
skip_http "media-service    /metrics" "http://localhost:8087/metrics"

echo

if [ "$ERRORS" -gt 0 ]; then
  echo "Observability validation FAILED with $ERRORS error(s)." >&2
  exit 1
fi

echo "Observability validation passed."
