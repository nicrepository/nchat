#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"

# shellcheck source=_observability_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/_observability_env.sh"

# logs.sh uses exec, so the EXIT trap from the helper won't run.
# Clean up the temp file manually before handing over to docker compose.
[ -n "$TEMP_ENV_FILE" ] && trap '' EXIT

exec docker compose \
  --env-file "$EFFECTIVE_ENV_FILE" \
  -f "$COMPOSE_FILE" \
  --profile observability \
  logs -f prometheus grafana jaeger
