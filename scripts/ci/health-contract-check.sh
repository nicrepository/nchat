#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

services=(
  auth-service
  chat-service
  file-service
  notification-service
  admin-service
  search-service
  media-service
)

for service in "${services[@]}"; do
  route_dir="$ROOT/services/$service/internal/http"
  server_dir="$ROOT/services/$service/internal/server"

  if [ -d "$route_dir" ]; then
    search_dir="$route_dir"
  elif [ -d "$server_dir" ]; then
    search_dir="$server_dir"
  else
    echo "No HTTP route directory found for $service." >&2
    exit 1
  fi

  if ! grep -R "healthz" "$search_dir" >/dev/null; then
    echo "$service does not reference /healthz in HTTP routes or tests." >&2
    exit 1
  fi

  if ! grep -R "readyz" "$search_dir" >/dev/null; then
    echo "$service does not reference /readyz in HTTP routes or tests." >&2
    exit 1
  fi

  deployment="$ROOT/infra/k8s/base/services/$service/deployment.yaml"
  if [ ! -f "$deployment" ]; then
    echo "Missing Kubernetes deployment for $service." >&2
    exit 1
  fi

  healthz_count="$(grep -c "path: /healthz" "$deployment" || true)"
  if [ "$healthz_count" -lt 2 ]; then
    echo "$service deployment must use /healthz for livenessProbe and startupProbe." >&2
    exit 1
  fi

  if ! grep -q "path: /readyz" "$deployment"; then
    echo "$service deployment must use /readyz for readinessProbe." >&2
    exit 1
  fi
done

bash "$ROOT/scripts/ci/go-test.sh"

echo "Health contract check passed."
