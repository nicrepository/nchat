#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

DASHBOARD_JSON="$ROOT_DIR/infra/observability/grafana/dashboards/nchat-overview.json"
DASHBOARDS_YML="$ROOT_DIR/infra/observability/grafana/provisioning/dashboards/dashboards.yml"
DATASOURCES_YML="$ROOT_DIR/infra/observability/grafana/provisioning/datasources/datasources.yml"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

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

echo "==> Grafana dashboard check"
echo

echo "--- Required files ---"
require_file "$DASHBOARD_JSON"
require_file "$DASHBOARDS_YML"
require_file "$DATASOURCES_YML"

echo
echo "--- JSON validation ---"
if command -v python3 > /dev/null 2>&1; then
  if python3 -m json.tool "$DASHBOARD_JSON" > /dev/null 2>&1; then
    echo "  [OK]   nchat-overview.json is valid JSON"
  else
    echo "  [FAIL] nchat-overview.json is invalid JSON" >&2
    ERRORS=$((ERRORS + 1))
  fi
else
  echo "  [SKIP] python3 not available for JSON validation"
fi

echo
echo "--- YAML syntax ---"
if command -v python3 > /dev/null 2>&1; then
  if python3 -c "import yaml; yaml.safe_load(open('$DASHBOARDS_YML'))" > /dev/null 2>&1; then
    echo "  [OK]   dashboards.yml is valid YAML"
  else
    echo "  [FAIL] dashboards.yml is invalid YAML" >&2
    ERRORS=$((ERRORS + 1))
  fi
  if python3 -c "import yaml; yaml.safe_load(open('$DATASOURCES_YML'))" > /dev/null 2>&1; then
    echo "  [OK]   datasources.yml is valid YAML"
  else
    echo "  [FAIL] datasources.yml is invalid YAML" >&2
    ERRORS=$((ERRORS + 1))
  fi
else
  echo "  [SKIP] python3 not available for YAML validation"
fi

echo
echo "--- Dashboard content validation ---"

check_contains() {
  local label="$1"
  local pattern="$2"
  local file="$3"
  if grep -q "$pattern" "$file" 2>/dev/null; then
    echo "  [OK]   $label"
  else
    echo "  [FAIL] $label not found in $file" >&2
    ERRORS=$((ERRORS + 1))
  fi
}

check_contains 'uid nchat-overview'                '"uid": "nchat-overview"'                   "$DASHBOARD_JSON"
check_contains 'title NChat Overview'              '"title": "NChat Overview"'                  "$DASHBOARD_JSON"
check_contains 'datasource uid prometheus'         '"uid": "prometheus"'                        "$DASHBOARD_JSON"
check_contains 'query nchat_http_requests_total'   'nchat_http_requests_total'                  "$DASHBOARD_JSON"
check_contains 'query duration_seconds_bucket'     'nchat_http_request_duration_seconds_bucket' "$DASHBOARD_JSON"
check_contains 'query nchat_service_info'          'nchat_service_info'                         "$DASHBOARD_JSON"
check_contains 'query in_flight_requests'          'nchat_http_in_flight_requests'              "$DASHBOARD_JSON"
check_contains 'query up{job=nchat-go-services}'   'nchat-go-services'                          "$DASHBOARD_JSON"

echo
echo "--- Provisioning config validation ---"

if grep -q '/var/lib/grafana/dashboards' "$DASHBOARDS_YML" 2>/dev/null; then
  echo "  [OK]   dashboards.yml points to /var/lib/grafana/dashboards"
else
  echo "  [FAIL] dashboards.yml does not point to /var/lib/grafana/dashboards" >&2
  ERRORS=$((ERRORS + 1))
fi

echo
echo "--- Compose volume validation ---"

if grep -q 'grafana/dashboards:/var/lib/grafana/dashboards' "$COMPOSE_FILE" 2>/dev/null; then
  echo "  [OK]   compose.dev.yml mounts grafana dashboards"
else
  echo "  [FAIL] compose.dev.yml is missing the grafana dashboards volume mount" >&2
  ERRORS=$((ERRORS + 1))
fi

echo

if [ "$ERRORS" -gt 0 ]; then
  echo "Grafana dashboard check FAILED with $ERRORS error(s)." >&2
  exit 1
fi

echo "Grafana dashboard check passed."
