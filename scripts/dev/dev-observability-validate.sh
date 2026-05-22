#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

# shellcheck source=_observability_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_observability_env.sh"

PROMETHEUS_PORT="${PROMETHEUS_HOST_PORT:-9090}"
GRAFANA_PORT="${GRAFANA_HOST_PORT:-3000}"
JAEGER_PORT="${JAEGER_UI_HOST_PORT:-16686}"

ERRORS=0
_HEALTH_TMP="$(mktemp /tmp/nchat_obs_health_XXXXXX)"
trap 'rm -f "$_HEALTH_TMP"' EXIT

# wait_for_http <name> <url> [timeout_seconds]
# Polls <url> until HTTP 200, or fails after timeout.
# Prints the last status code + first 200 chars of body on failure.
wait_for_http() {
  local name="$1"
  local url="$2"
  local timeout="${3:-60}"
  local interval=3
  local elapsed=0
  local http_status=""

  while [ "$elapsed" -lt "$timeout" ]; do
    http_status=$(curl -s -o "$_HEALTH_TMP" -w "%{http_code}" --max-time 5 "$url" 2>/dev/null || echo "000")
    if [ "$http_status" = "200" ]; then
      echo "  [OK]   $name — $url  (ready in ${elapsed}s)"
      return 0
    fi
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done

  echo "  [FAIL] $name — $url  (timeout after ${timeout}s)" >&2
  echo "         last HTTP status : $http_status" >&2
  local body
  body=$(head -c 200 "$_HEALTH_TMP" 2>/dev/null | tr -d '\n' || true)
  [ -n "$body" ] && echo "         last body        : $body" >&2
  return 1
}

# check_http_once <name> <url>
# Single attempt, no retry. Used for Go services that may not be running.
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

echo "--- Core stack (retrying up to 60s each) ---"
wait_for_http "Prometheus /-/ready" "http://localhost:${PROMETHEUS_PORT}/-/ready" 60 || ERRORS=$((ERRORS + 1))
wait_for_http "Grafana /api/health"  "http://localhost:${GRAFANA_PORT}/api/health"  60 || ERRORS=$((ERRORS + 1))
wait_for_http "Jaeger /"             "http://localhost:${JAEGER_PORT}/"             60 || ERRORS=$((ERRORS + 1))

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
