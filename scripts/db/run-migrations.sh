#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
COMMAND="${1:-up}"
APPLY_GRANTS=true

for argument in "$@"; do
  if [[ "$argument" == "--dry-run" ]]; then
    APPLY_GRANTS=false
  fi
done

if [[ "$COMMAND" == "up" ]] && $APPLY_GRANTS; then
  export MIGRATIONS_POST_UP_SQL_FILE="$SCRIPT_DIR/grant-runtime.sql"
fi

exec "$SCRIPT_DIR/migrate.sh" "$@"
