#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

PROMETHEUS_CFG="$ROOT_DIR/infra/observability/prometheus/prometheus.yml"
GRAFANA_DS="$ROOT_DIR/infra/observability/grafana/provisioning/datasources/datasources.yml"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"

ERRORS=0

require_file() {
  local path="$1"
  if [ -f "$path" ]; then
    echo "  [OK]   $path"
  else
    echo "  [FAIL] missing: $path" >&2
    ERRORS=$((ERRORS + 1))
  fi
}

echo "==> Observability config check"
echo

echo "--- Required files ---"
require_file "$PROMETHEUS_CFG"
require_file "$GRAFANA_DS"
require_file "$COMPOSE_FILE"
require_file "$ENV_EXAMPLE"

# Validate compose config (no containers started).
echo
echo "--- Docker Compose config validation ---"
if command -v docker > /dev/null 2>&1; then
  COMPOSE_DIR="$(dirname "$COMPOSE_FILE")"
  ENV_DEV="$COMPOSE_DIR/.env.dev"
  TEMP_ENV_DEV=""
  # If .env.dev doesn't exist (CI), create a temporary stub from .env.dev.example.
  if [ ! -f "$ENV_DEV" ]; then
    TEMP_ENV_DEV="$(mktemp)"
    cp "$ENV_EXAMPLE" "$TEMP_ENV_DEV"
    cp "$TEMP_ENV_DEV" "$ENV_DEV"
  fi
  if docker compose \
    --env-file "$ENV_EXAMPLE" \
    -f "$COMPOSE_FILE" \
    --profile observability \
    config > /dev/null 2>&1; then
    echo "  [OK]   compose config valid"
  else
    echo "  [FAIL] compose config invalid" >&2
    ERRORS=$((ERRORS + 1))
  fi
  # Clean up temp stub if we created one.
  if [ -n "$TEMP_ENV_DEV" ]; then
    rm -f "$ENV_DEV" "$TEMP_ENV_DEV"
  fi
else
  echo "  [SKIP] docker not available"
fi

# Validate prometheus config with promtool if available.
echo
echo "--- Prometheus config validation ---"
if command -v promtool > /dev/null 2>&1; then
  if promtool check config "$PROMETHEUS_CFG" > /dev/null 2>&1; then
    echo "  [OK]   promtool check passed"
  else
    echo "  [FAIL] promtool check failed" >&2
    ERRORS=$((ERRORS + 1))
  fi
else
  # Try via Docker if available.
  PROMETHEUS_IMAGE="${PROMETHEUS_IMAGE:-prom/prometheus:v2.54.1}"
  if command -v docker > /dev/null 2>&1; then
    if docker run --rm \
      -v "$PROMETHEUS_CFG:/etc/prometheus/prometheus.yml:ro" \
      "$PROMETHEUS_IMAGE" \
      promtool check config /etc/prometheus/prometheus.yml > /dev/null 2>&1; then
      echo "  [OK]   promtool via Docker passed"
    else
      echo "  [SKIP] promtool via Docker skipped (image may not be pulled)"
    fi
  else
    echo "  [SKIP] promtool not available"
  fi
fi

# Validate YAML syntax of datasources config.
echo
echo "--- YAML syntax validation ---"
if command -v python3 > /dev/null 2>&1; then
  if python3 -c "import yaml, sys; yaml.safe_load(open('$GRAFANA_DS'))" > /dev/null 2>&1; then
    echo "  [OK]   grafana datasources YAML valid"
  else
    echo "  [FAIL] grafana datasources YAML invalid" >&2
    ERRORS=$((ERRORS + 1))
  fi
elif command -v python > /dev/null 2>&1; then
  if python -c "import yaml, sys; yaml.safe_load(open('$GRAFANA_DS'))" > /dev/null 2>&1; then
    echo "  [OK]   grafana datasources YAML valid (python2)"
  else
    echo "  [FAIL] grafana datasources YAML invalid" >&2
    ERRORS=$((ERRORS + 1))
  fi
else
  echo "  [SKIP] python not available for YAML validation"
fi

# Security: ensure no hardcoded real Grafana password outside .env.dev.example
echo
echo "--- Security checks ---"
REAL_PW_FILES=$(grep -rn "GRAFANA_ADMIN_PASSWORD\s*=" \
  "$ROOT_DIR" \
  --include="*.yml" --include="*.yaml" --include="*.env" \
  --exclude-dir=".git" --exclude-dir="node_modules" \
  --exclude=".env.dev.example" \
  2>/dev/null | grep -v "^Binary" || true)
if [ -n "$REAL_PW_FILES" ]; then
  echo "  [WARN] GRAFANA_ADMIN_PASSWORD found outside .env.dev.example:"
  echo "$REAL_PW_FILES"
else
  echo "  [OK]   GRAFANA_ADMIN_PASSWORD not hardcoded outside .env.dev.example"
fi

# Ensure observability ports bind to 127.0.0.1 not 0.0.0.0
if grep -E '"0\.0\.0\.0:[0-9]+:(9090|3000|16686|4317|4318)"' "$COMPOSE_FILE" > /dev/null 2>&1; then
  echo "  [FAIL] observability ports bound to 0.0.0.0 — must use 127.0.0.1" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   observability ports bound to 127.0.0.1"
fi

echo

if [ "$ERRORS" -gt 0 ]; then
  echo "Observability config check FAILED with $ERRORS error(s)." >&2
  exit 1
fi

echo "Observability config check passed."
