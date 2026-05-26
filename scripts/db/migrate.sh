#!/usr/bin/env bash
# Migration runner for NChat PostgreSQL (psql-based, no external tools required).
# Usage: scripts/db/migrate.sh <up|down|status|reset|smoke> [--dry-run] [--steps N]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
MIGRATIONS_DIR="$ROOT_DIR/migrations"
COMPOSE_DIR="$ROOT_DIR/infra/compose"

COMMAND="${1:-help}"
DRY_RUN=false
STEPS=1
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=true ;;
    --steps)   STEPS="${2:?--steps requires a value}"; shift ;;
    *) ;;
  esac
  shift
done

# ---------------------------------------------------------------------------
# Env loading — maps POSTGRES_* variables; PGPASSWORD never logged
# ---------------------------------------------------------------------------
PGPASSWORD=""
PGHOST="localhost"
PGPORT="5432"
PGDATABASE="nchat"
PGUSER="nchat"
DSN=""

load_env() {
  local env_file
  if [[ -f "$COMPOSE_DIR/.env.dev" ]]; then
    env_file="$COMPOSE_DIR/.env.dev"
  elif [[ -f "$COMPOSE_DIR/.env.dev.example" ]]; then
    env_file="$COMPOSE_DIR/.env.dev.example"
  else
    echo "[ERROR] No .env.dev or .env.dev.example found in $COMPOSE_DIR" >&2
    exit 1
  fi
  while IFS= read -r line; do
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" =~ ^POSTGRES_ ]] || continue
    local key="${line%%=*}"
    local val="${line#*=}"
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
  done < "$env_file"
}

need_db() {
  load_env
  export PGPASSWORD
  DSN="postgresql://$PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
}

# ---------------------------------------------------------------------------
# psql wrappers — password in PGPASSWORD env var, never on command line
# ---------------------------------------------------------------------------
db_exec() {
  psql --no-password -v ON_ERROR_STOP=1 "$DSN" "$@"
}

db_scalar() {
  db_exec -t -A -c "$1" | tr -d '[:space:]'
}

# ---------------------------------------------------------------------------
# schema_migrations table — tracks applied migrations
# ---------------------------------------------------------------------------
ensure_migrations_table() {
  db_exec -q -c "
    CREATE TABLE IF NOT EXISTS public.schema_migrations (
      id         SERIAL PRIMARY KEY,
      domain     TEXT        NOT NULL,
      filename   TEXT        NOT NULL,
      applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
      CONSTRAINT schema_migrations_uq UNIQUE (domain, filename)
    );"
}

is_applied() {
  local n
  n=$(db_scalar "SELECT COUNT(*) FROM public.schema_migrations WHERE domain='$1' AND filename='$2';")
  [[ "${n:-0}" -gt 0 ]]
}

record_applied() {
  db_exec -q -c "
    INSERT INTO public.schema_migrations(domain, filename)
    VALUES('$1','$2')
    ON CONFLICT DO NOTHING;"
}

record_rollback() {
  db_exec -q -c "
    DELETE FROM public.schema_migrations WHERE domain='$1' AND filename='$2';"
}

# ---------------------------------------------------------------------------
# Migration discovery
# ---------------------------------------------------------------------------
MDOM=""
MFILE=""

parse_up_file() {
  MDOM="$(basename "$(dirname "$1")")"
  MFILE="$(basename "$1" .up.sql)"
}

collect_up_files() {
  find "$MIGRATIONS_DIR" -name "*.up.sql" | sort
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_up() {
  need_db
  ensure_migrations_table
  local applied=0
  echo "=== migrate up${DRY_RUN:+ (dry-run)} ==="
  while IFS= read -r up_file; do
    parse_up_file "$up_file"
    local down_file="${up_file%.up.sql}.down.sql"
    if [[ ! -f "$down_file" ]]; then
      echo "[ERROR] missing down migration for $MFILE — refusing to apply" >&2
      exit 1
    fi
    if is_applied "$MDOM" "$MFILE"; then
      echo "  [SKIP]  $MDOM/$MFILE"
      continue
    fi
    if $DRY_RUN; then
      echo "  [DRY]   $MDOM/$MFILE"
      echo "--- SQL ---"
      cat "$up_file"
      echo "-----------"
      continue
    fi
    echo "  [APPLY] $MDOM/$MFILE"
    db_exec -f "$up_file"
    record_applied "$MDOM" "$MFILE"
    applied=$((applied + 1))
  done < <(collect_up_files)
  echo ""
  echo "Applied $applied migration(s)."
}

cmd_down() {
  local steps="${1:-$STEPS}"
  need_db
  ensure_migrations_table
  echo "=== migrate down (steps: $steps)${DRY_RUN:+ (dry-run)} ==="
  local rolled=0
  while IFS='|' read -r dom file; do
    dom="${dom// /}"
    file="${file// /}"
    [[ -z "${dom:-}" ]] && continue
    local down_file="$MIGRATIONS_DIR/$dom/$file.down.sql"
    if [[ ! -f "$down_file" ]]; then
      echo "[ERROR] missing down file: $down_file" >&2
      exit 1
    fi
    if $DRY_RUN; then
      echo "  [DRY]      $dom/$file"
      echo "--- SQL ---"
      cat "$down_file"
      echo "-----------"
      continue
    fi
    echo "  [ROLLBACK] $dom/$file"
    db_exec -f "$down_file"
    record_rollback "$dom" "$file"
    rolled=$((rolled + 1))
  done < <(db_exec -t -A -c "
    SELECT domain, filename FROM public.schema_migrations
    ORDER BY applied_at DESC, id DESC
    LIMIT $steps;")
  echo ""
  echo "Rolled back $rolled migration(s)."
}

cmd_status() {
  need_db
  ensure_migrations_table
  echo "=== migration status ==="
  printf "%-12s %-52s %s\n" "DOMAIN" "FILENAME" "STATUS"
  printf "%-12s %-52s %s\n" "------" "--------" "------"
  while IFS= read -r up_file; do
    parse_up_file "$up_file"
    if is_applied "$MDOM" "$MFILE"; then
      printf "%-12s %-52s %s\n" "$MDOM" "$MFILE" "applied"
    else
      printf "%-12s %-52s %s\n" "$MDOM" "$MFILE" "pending"
    fi
  done < <(collect_up_files)
}

cmd_reset() {
  need_db
  ensure_migrations_table
  local total
  total=$(db_scalar "SELECT COUNT(*) FROM public.schema_migrations;")
  total="${total:-0}"
  if [[ "$total" -eq 0 ]]; then
    echo "No applied migrations. Nothing to reset."
    return
  fi
  echo "[WARN] This will roll back all $total applied migration(s)."
  if [[ -t 0 ]]; then
    read -r -p "Continue? [y/N] " confirm
    [[ "$confirm" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 0; }
  fi
  cmd_down "$total"
  echo "Reset complete."
}

cmd_help() {
  cat <<'USAGE'
Usage: migrate.sh <command> [flags]

Commands:
  up        Apply all pending migrations
  down      Roll back last N migrations (--steps N, default 1)
  status    Show applied / pending migration status
  reset     Roll back all applied migrations (interactive)
  smoke     Run DB smoke test (scripts/db/migrations-smoke.sh)
  help      Show this help

Flags:
  --dry-run    Print SQL without executing (up / down)
  --steps N    Migrations to roll back (down only, default 1)

Source: infra/compose/.env.dev (falls back to .env.dev.example)
Tables:  public.schema_migrations (created automatically on first run)
USAGE
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------
case "$COMMAND" in
  up)     cmd_up ;;
  down)   cmd_down ;;
  status) cmd_status ;;
  reset)  cmd_reset ;;
  smoke)  "$SCRIPT_DIR/migrations-smoke.sh" ;;
  help|--help|-h) cmd_help ;;
  *)
    echo "[ERROR] Unknown command: $COMMAND" >&2
    cmd_help >&2
    exit 1
    ;;
esac
