#!/usr/bin/env bash
# Smoke test: apply migrations, verify tables, rollback, verify clean.
# Requires local PostgreSQL via Docker Compose.
# Usage: scripts/db/migrations-smoke.sh
# Note:  Not intended for standard CI; run with 'make dev-env-up' first.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
COMPOSE_DIR="$ROOT_DIR/infra/compose"
MIGRATE="$SCRIPT_DIR/migrate.sh"

ERRORS=0
fail() { echo "  [FAIL] $*" >&2; ERRORS=$((ERRORS + 1)); }
ok()   { echo "  [OK]   $*"; }

echo "=== migrations smoke test ==="
echo ""

# ---------------------------------------------------------------------------
# Load env
# ---------------------------------------------------------------------------
if [[ -f "$COMPOSE_DIR/.env.dev" ]]; then
  ENV_FILE="$COMPOSE_DIR/.env.dev"
elif [[ -f "$COMPOSE_DIR/.env.dev.example" ]]; then
  ENV_FILE="$COMPOSE_DIR/.env.dev.example"
else
  echo "[ERROR] No .env.dev or .env.dev.example found in $COMPOSE_DIR" >&2
  exit 1
fi

PGPASSWORD=""
PGHOST="localhost"
PGPORT="5432"
PGDATABASE="nchat"
PGUSER="nchat"

while IFS= read -r line; do
  [[ "$line" =~ ^[[:space:]]*# ]] && continue
  [[ "$line" =~ ^POSTGRES_ ]] || continue
  key="${line%%=*}"
  val="${line#*=}"
  val="${val%\"}"
  val="${val#\"}"
  val="${val%\'}"
  val="${val#\'}"
  case "$key" in
    POSTGRES_DB)        PGDATABASE="$val" ;;
    POSTGRES_USER)      PGUSER="$val" ;;
    POSTGRES_PASSWORD)  PGPASSWORD="$val" ;;
    POSTGRES_HOST_PORT) PGPORT="$val" ;;
  esac
done < "$ENV_FILE"

export PGPASSWORD
DSN="postgresql://$PGUSER@$PGHOST:$PGPORT/$PGDATABASE"

pg_scalar() { psql --no-password -t -A "$DSN" -c "$1" | tr -d '[:space:]'; }

table_exists() {
  local schema="${1%%.*}" tbl="${1##*.}"
  local n
  n=$(pg_scalar "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='$schema' AND table_name='$tbl';")
  [[ "${n:-0}" -gt 0 ]]
}

# ---------------------------------------------------------------------------
# 1. Connectivity check
# ---------------------------------------------------------------------------
echo "--- connectivity ---"
if ! psql --no-password "$DSN" -c "SELECT 1;" > /dev/null 2>&1; then
  echo "[ERROR] Cannot connect to PostgreSQL at $PGHOST:$PGPORT/$PGDATABASE" >&2
  echo "        Run 'make dev-env-up' to start local database." >&2
  exit 1
fi
ok "connected to $PGHOST:$PGPORT/$PGDATABASE"
echo ""

# ---------------------------------------------------------------------------
# 2. Count already-applied migrations before running up
# ---------------------------------------------------------------------------
before=$(pg_scalar "SELECT COALESCE((SELECT COUNT(*) FROM public.schema_migrations),0);" 2>/dev/null || echo "0")

# ---------------------------------------------------------------------------
# 3. Apply all pending migrations
# ---------------------------------------------------------------------------
echo "--- migrate up ---"
"$MIGRATE" up
echo ""

after=$(pg_scalar "SELECT COUNT(*) FROM public.schema_migrations;")
applied_this_run=$((after - before))

# ---------------------------------------------------------------------------
# 4. Verify expected auth tables exist
# ---------------------------------------------------------------------------
echo "--- verify auth tables ---"
for tbl in \
  "auth.users" \
  "auth.user_password_credentials" \
  "auth.user_oidc_identities" \
  "auth.user_invitations" \
  "auth.user_sessions" \
  "auth.user_devices" \
  "auth.login_attempt_logs" \
  "auth.password_reset_tokens" \
  "auth.auth_policy_settings"; do
  if table_exists "$tbl"; then
    ok "table exists: $tbl"
  else
    fail "table missing: $tbl"
  fi
done
echo ""

# ---------------------------------------------------------------------------
# 5. Verify public.users is not created by auth migrations
# ---------------------------------------------------------------------------
echo "--- public.users untouched ---"
if ! table_exists "public.users"; then
  ok "public.users does not exist (expected)"
else
  fail "public.users exists — auth migrations must not create public.users"
fi
echo ""

# ---------------------------------------------------------------------------
# 6. Roll back what was applied in this run
# ---------------------------------------------------------------------------
if [[ "$applied_this_run" -gt 0 ]]; then
  echo "--- migrate down ($applied_this_run step(s)) ---"
  "$MIGRATE" down --steps "$applied_this_run"
  echo ""

  # 7. Verify auth tables are gone
  echo "--- verify auth tables removed ---"
  for tbl in \
    "auth.users" \
    "auth.user_password_credentials" \
    "auth.user_oidc_identities" \
    "auth.user_invitations" \
    "auth.user_sessions" \
    "auth.user_devices" \
    "auth.login_attempt_logs" \
    "auth.password_reset_tokens" \
    "auth.auth_policy_settings"; do
    if ! table_exists "$tbl"; then
      ok "removed: $tbl"
    else
      fail "still exists after rollback: $tbl"
    fi
  done
  echo ""
else
  echo "--- no new migrations applied — skipping rollback verification ---"
  echo ""
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
if [[ "$ERRORS" -gt 0 ]]; then
  echo "smoke test FAILED with $ERRORS error(s)." >&2
  exit 1
fi
echo "smoke test PASSED."
