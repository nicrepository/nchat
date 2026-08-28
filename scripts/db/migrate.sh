#!/usr/bin/env bash
# Migration runner for NChat PostgreSQL (psql-based, no external tools required).
# Usage: scripts/db/migrate.sh <up|down|status|reset|smoke> [--dry-run] [--steps N] [--force]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
MIGRATIONS_DIR="$ROOT_DIR/migrations"
COMPOSE_DIR="$ROOT_DIR/infra/compose"

COMMAND="${1:-help}"
DRY_RUN=false
FORCE_RESET=false
STEPS=1
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=true ;;
    --force)   FORCE_RESET=true ;;
    --steps)   STEPS="${2:?--steps requires a value}"; shift ;;
    *)
      echo "[ERROR] Unknown flag: $1" >&2
      exit 1
      ;;
  esac
  shift
done

# ---------------------------------------------------------------------------
# Env loading: maps POSTGRES_* variables; PGPASSWORD never logged.
# ---------------------------------------------------------------------------
PGPASSWORD=""
PGHOST="localhost"
PGPORT="5432"
PGDATABASE="nchat"
PGUSER="nchat"
DSN=""
MIGRATIONS_TABLE_EXISTS=false
MIGRATION_LOCK_ID=2026052201
LOCK_HOLDER_PID=""
LOCK_ACQUIRED=false

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
  while IFS= read -r line || [[ -n "$line" ]]; do
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
  if [[ -n "${MIGRATIONS_DATABASE_URL:-}" ]]; then
    DSN="$MIGRATIONS_DATABASE_URL"
  elif [[ -n "${DATABASE_URL:-}" ]]; then
    DSN="$DATABASE_URL"
  else
    load_env
    export PGPASSWORD
    DSN="postgresql://$PGUSER@$PGHOST:$PGPORT/$PGDATABASE"
  fi
}

# ---------------------------------------------------------------------------
# psql wrappers: password in PGPASSWORD env var, never on command line.
# ---------------------------------------------------------------------------
db_exec() {
  psql --no-password -v ON_ERROR_STOP=1 "$DSN" "$@"
}

db_scalar() {
  local query="$1"
  shift
  printf '%s\n' "$query" | db_exec "$@" -t -A | tr -d '[:space:]'
}

wait_for_database() {
  local max_attempts="${MIGRATIONS_DATABASE_WAIT_ATTEMPTS:-30}"
  local delay_seconds="${MIGRATIONS_DATABASE_WAIT_SECONDS:-2}"
  local attempt

  if [[ ! "$max_attempts" =~ ^[1-9][0-9]*$ ]]; then
    echo "[ERROR] MIGRATIONS_DATABASE_WAIT_ATTEMPTS must be a positive integer." >&2
    return 1
  fi

  if [[ ! "$delay_seconds" =~ ^[0-9]+$ ]]; then
    echo "[ERROR] MIGRATIONS_DATABASE_WAIT_SECONDS must be a non-negative integer." >&2
    return 1
  fi

  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    if db_exec -t -A -c 'SELECT 1' >/dev/null 2>&1; then
      if ((attempt > 1)); then
        echo "[INFO] PostgreSQL became available on attempt $attempt/$max_attempts." >&2
      fi
      return 0
    fi

    if ((attempt == max_attempts)); then
      echo "[ERROR] PostgreSQL unavailable after $max_attempts attempts." >&2
      return 1
    fi

    echo "[INFO] PostgreSQL unavailable (attempt $attempt/$max_attempts); retrying in ${delay_seconds}s." >&2
    sleep "$delay_seconds"
  done
}

# ---------------------------------------------------------------------------
# Validation helpers: path-derived identifiers must be safe before SQL/path use.
# ---------------------------------------------------------------------------
validate_domain() {
  local domain="$1"
  if [[ ! "$domain" =~ ^[a-z][a-z0-9_]*$ ]]; then
    echo "[ERROR] Invalid migration domain: $domain" >&2
    exit 1
  fi
}

validate_migration_base() {
  local filename="$1"
  if [[ ! "$filename" =~ ^[0-9]{6}_[a-z0-9_]+$ ]]; then
    echo "[ERROR] Invalid migration filename: $filename" >&2
    exit 1
  fi
}

validate_up_filename() {
  local filename="$1"
  if [[ ! "$filename" =~ ^[0-9]{6}_[a-z0-9_]+\.up\.sql$ ]]; then
    echo "[ERROR] Invalid up migration filename: $filename" >&2
    exit 1
  fi
}

validate_down_filename() {
  local filename="$1"
  if [[ ! "$filename" =~ ^[0-9]{6}_[a-z0-9_]+\.down\.sql$ ]]; then
    echo "[ERROR] Invalid down migration filename: $filename" >&2
    exit 1
  fi
}

validate_checksum() {
  local checksum="$1"
  if [[ ! "$checksum" =~ ^[a-f0-9]{64}$ ]]; then
    echo "[ERROR] Invalid migration checksum: $checksum" >&2
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# Advisory lock: held by an open psql coprocess for the full mutating command.
# ---------------------------------------------------------------------------
acquire_migration_lock() {
  coproc MIGRATION_LOCK_PSQL { psql --no-password -v ON_ERROR_STOP=1 -q -t -A --set=lock_id="$MIGRATION_LOCK_ID" "$DSN"; }
  LOCK_HOLDER_PID="$MIGRATION_LOCK_PSQL_PID"

  printf "%s\n" "SELECT pg_advisory_lock(:'lock_id'::bigint);" "SELECT 'locked';" 1>&"${MIGRATION_LOCK_PSQL[1]}"

  local line
  while true; do
    if IFS= read -r -t 60 line 0<&"${MIGRATION_LOCK_PSQL[0]}"; then
      if [[ "$line" == "locked" ]]; then
        LOCK_ACQUIRED=true
        return
      fi
      continue
    fi
    echo "[ERROR] Timed out waiting for PostgreSQL advisory lock." >&2
    release_migration_lock
    exit 1
  done
}

release_migration_lock() {
  if [[ -n "${LOCK_HOLDER_PID:-}" ]]; then
    if [[ "${LOCK_ACQUIRED:-false}" == "true" ]]; then
      printf "%s\n" "SELECT pg_advisory_unlock(:'lock_id'::bigint);" 1>&"${MIGRATION_LOCK_PSQL[1]}" 2>/dev/null || true
      LOCK_ACQUIRED=false
    fi
    printf "\\q\n" 1>&"${MIGRATION_LOCK_PSQL[1]}" 2>/dev/null || true
    wait "$LOCK_HOLDER_PID" >/dev/null 2>&1 || true
    LOCK_HOLDER_PID=""
  fi
}

with_migration_lock() {
  acquire_migration_lock
  trap release_migration_lock EXIT INT TERM
  set +e
  "$@"
  local status=$?
  set -e
  release_migration_lock
  trap - EXIT INT TERM
  return "$status"
}

# ---------------------------------------------------------------------------
# schema_migrations table: tracks clean/dirty state and file checksum.
# ---------------------------------------------------------------------------
ensure_migrations_table() {
  db_exec -q -c "
    CREATE TABLE IF NOT EXISTS public.schema_migrations (
      id              SERIAL PRIMARY KEY,
      domain          TEXT        NOT NULL,
      filename        TEXT        NOT NULL,
      checksum_sha256 TEXT        NOT NULL DEFAULT '',
      dirty           BOOLEAN     NOT NULL DEFAULT false,
      in_progress     BOOLEAN     NOT NULL DEFAULT false,
      applied_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
      updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
      CONSTRAINT schema_migrations_uq UNIQUE (domain, filename)
    );
    ALTER TABLE public.schema_migrations ADD COLUMN IF NOT EXISTS checksum_sha256 TEXT;
    ALTER TABLE public.schema_migrations ADD COLUMN IF NOT EXISTS dirty BOOLEAN;
    ALTER TABLE public.schema_migrations ADD COLUMN IF NOT EXISTS in_progress BOOLEAN;
    ALTER TABLE public.schema_migrations ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
    UPDATE public.schema_migrations SET checksum_sha256 = '' WHERE checksum_sha256 IS NULL;
    UPDATE public.schema_migrations SET dirty = false WHERE dirty IS NULL;
    UPDATE public.schema_migrations SET in_progress = false WHERE in_progress IS NULL;
    UPDATE public.schema_migrations SET updated_at = COALESCE(updated_at, applied_at, now());
    ALTER TABLE public.schema_migrations ALTER COLUMN checksum_sha256 SET NOT NULL;
    ALTER TABLE public.schema_migrations ALTER COLUMN dirty SET NOT NULL;
    ALTER TABLE public.schema_migrations ALTER COLUMN dirty SET DEFAULT false;
    ALTER TABLE public.schema_migrations ALTER COLUMN in_progress SET NOT NULL;
    ALTER TABLE public.schema_migrations ALTER COLUMN in_progress SET DEFAULT false;
    ALTER TABLE public.schema_migrations ALTER COLUMN updated_at SET NOT NULL;
    ALTER TABLE public.schema_migrations ALTER COLUMN updated_at SET DEFAULT now();"
  MIGRATIONS_TABLE_EXISTS=true
}

migrations_table_exists() {
  local n
  n=$(db_scalar "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations';")
  [[ "${n:-0}" -gt 0 ]]
}

assert_no_dirty_migrations() {
  local n
  n=$(db_scalar "SELECT COUNT(*) FROM public.schema_migrations WHERE dirty OR in_progress;")
  if [[ "${n:-0}" -gt 0 ]]; then
    echo "[ERROR] Dirty or in-progress migration detected. Manual repair required before continuing." >&2
    db_exec -t -A -F '/' -c "SELECT domain, filename FROM public.schema_migrations WHERE dirty OR in_progress ORDER BY id;" >&2 || true
    exit 1
  fi
}

is_applied() {
  validate_domain "$1"
  validate_migration_base "$2"
  [[ "$MIGRATIONS_TABLE_EXISTS" == "true" ]] || return 1
  local n
  n=$(db_scalar "SELECT COUNT(*) FROM public.schema_migrations WHERE domain=:'domain' AND filename=:'filename';" \
    --set=domain="$1" --set=filename="$2")
  [[ "${n:-0}" -gt 0 ]]
}

migration_checksum() {
  local file="$1"
  sha256sum "$file" | cut -d ' ' -f 1
}

stored_checksum() {
  validate_domain "$1"
  validate_migration_base "$2"
  db_scalar "SELECT COALESCE((SELECT checksum_sha256 FROM public.schema_migrations WHERE domain=:'domain' AND filename=:'filename'), '');" \
    --set=domain="$1" --set=filename="$2"
}

verify_applied_checksum() {
  local domain="$1" filename="$2" checksum="$3" stored
  validate_domain "$domain"
  validate_migration_base "$filename"
  validate_checksum "$checksum"

  stored=$(stored_checksum "$domain" "$filename")
  if [[ -z "$stored" ]]; then
    echo "[ERROR] Applied migration $domain/$filename has no checksum. Manual repair required." >&2
    exit 1
  fi
  if [[ "$stored" != "$checksum" ]]; then
    echo "[ERROR] Applied migration $domain/$filename checksum changed. Refusing to continue." >&2
    echo "        stored:  $stored" >&2
    echo "        current: $checksum" >&2
    exit 1
  fi
}

record_apply_started() {
  local domain="$1" filename="$2" checksum="$3"
  validate_domain "$domain"
  validate_migration_base "$filename"
  validate_checksum "$checksum"
  db_exec -q --set=domain="$domain" --set=filename="$filename" --set=checksum="$checksum" <<'SQL'
    INSERT INTO public.schema_migrations(domain, filename, checksum_sha256, dirty, in_progress, applied_at, updated_at)
    VALUES(:'domain', :'filename', :'checksum', true, true, now(), now());
SQL
}

record_apply_clean() {
  local domain="$1" filename="$2" checksum="$3"
  validate_domain "$domain"
  validate_migration_base "$filename"
  validate_checksum "$checksum"
  db_exec -q --set=domain="$domain" --set=filename="$filename" --set=checksum="$checksum" <<'SQL'
    UPDATE public.schema_migrations
       SET checksum_sha256 = :'checksum', dirty = false, in_progress = false, applied_at = now(), updated_at = now()
     WHERE domain = :'domain' AND filename = :'filename';
SQL
}

record_rollback_started() {
  local domain="$1" filename="$2"
  validate_domain "$domain"
  validate_migration_base "$filename"
  db_exec -q --set=domain="$domain" --set=filename="$filename" <<'SQL'
    UPDATE public.schema_migrations
       SET dirty = true, in_progress = true, updated_at = now()
     WHERE domain = :'domain' AND filename = :'filename';
SQL
}

record_rollback_clean() {
  local domain="$1" filename="$2"
  validate_domain "$domain"
  validate_migration_base "$filename"
  db_exec -q --set=domain="$domain" --set=filename="$filename" <<'SQL'
    DELETE FROM public.schema_migrations WHERE domain = :'domain' AND filename = :'filename';
SQL
}

# ---------------------------------------------------------------------------
# Migration discovery
# ---------------------------------------------------------------------------
MDOM=""
MFILE=""

parse_up_file() {
  local domain filename
  domain="$(basename "$(dirname "$1")")"
  filename="$(basename "$1")"
  validate_domain "$domain"
  validate_up_filename "$filename"
  MDOM="$domain"
  MFILE="${filename%.up.sql}"
  validate_migration_base "$MFILE"
}

collect_up_files() {
  [[ -d "$MIGRATIONS_DIR" ]] || return 0
  find "$MIGRATIONS_DIR" -name "*.up.sql" | sort
}

validate_steps() {
  local steps="$1"
  if [[ ! "$steps" =~ ^[1-9][0-9]*$ ]]; then
    echo "[ERROR] --steps must be a positive integer." >&2
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_up_locked() {
  if $DRY_RUN; then
    if migrations_table_exists; then
      MIGRATIONS_TABLE_EXISTS=true
      assert_no_dirty_migrations
    fi
  else
    ensure_migrations_table
    assert_no_dirty_migrations
  fi

  local applied=0 dry_run_suffix=""
  $DRY_RUN && dry_run_suffix=" (dry-run)"
  echo "=== migrate up$dry_run_suffix ==="
  while IFS= read -r up_file; do
    parse_up_file "$up_file"
    local down_file="${up_file%.up.sql}.down.sql"
    local down_name checksum
    down_name="$(basename "$down_file")"
    validate_down_filename "$down_name"
    if [[ ! -f "$down_file" ]]; then
      echo "[ERROR] missing down migration for $MFILE - refusing to apply" >&2
      exit 1
    fi
    checksum="$(migration_checksum "$up_file")"
    validate_checksum "$checksum"
    if is_applied "$MDOM" "$MFILE"; then
      verify_applied_checksum "$MDOM" "$MFILE" "$checksum"
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
    record_apply_started "$MDOM" "$MFILE" "$checksum"
    db_exec -f "$up_file"
    record_apply_clean "$MDOM" "$MFILE" "$checksum"
    applied=$((applied + 1))
  done < <(collect_up_files)
  run_post_up_sql
  echo ""
  echo "Applied $applied migration(s)."
}

run_post_up_sql() {
  $DRY_RUN && return 0
  local sql_file="${MIGRATIONS_POST_UP_SQL_FILE:-}"
  [[ -n "$sql_file" ]] || return 0
  if [[ ! -f "$sql_file" || -L "$sql_file" ]]; then
    echo "[ERROR] Post-migration SQL file is missing or unsafe." >&2
    return 1
  fi
  db_exec -f "$sql_file"
}

cmd_up() {
  need_db
  wait_for_database
  with_migration_lock cmd_up_locked
}

run_down_steps() {
  local steps="$1"
  validate_steps "$steps"
  local dry_run_suffix=""
  $DRY_RUN && dry_run_suffix=" (dry-run)"
  echo "=== migrate down (steps: $steps)$dry_run_suffix ==="
  local rolled=0
  while IFS='|' read -r dom file; do
    dom="${dom// /}"
    file="${file// /}"
    [[ -z "${dom:-}" ]] && continue
    validate_domain "$dom"
    validate_migration_base "$file"
    local down_name="$file.down.sql"
    validate_down_filename "$down_name"
    local down_file="$MIGRATIONS_DIR/$dom/$down_name"
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
    record_rollback_started "$dom" "$file"
    db_exec -f "$down_file"
    record_rollback_clean "$dom" "$file"
    rolled=$((rolled + 1))
  done < <(db_exec -t -A -F '|' --set=steps="$steps" <<'SQL'
    SELECT domain, filename FROM public.schema_migrations
    ORDER BY applied_at DESC, id DESC
    LIMIT :'steps'::integer;
SQL
  )
  echo ""
  echo "Rolled back $rolled migration(s)."
}

cmd_down_locked() {
  local steps="$1"
  if $DRY_RUN; then
    if migrations_table_exists; then
      MIGRATIONS_TABLE_EXISTS=true
      assert_no_dirty_migrations
    else
      echo "No applied migrations. Nothing to roll back."
      return
    fi
  else
    ensure_migrations_table
    assert_no_dirty_migrations
  fi
  run_down_steps "$steps"
}

cmd_down() {
  local steps="${1:-$STEPS}"
  validate_steps "$steps"
  need_db
  wait_for_database
  with_migration_lock cmd_down_locked "$steps"
}

cmd_status() {
  need_db
  wait_for_database
  ensure_migrations_table
  assert_no_dirty_migrations
  echo "=== migration status ==="
  printf "%-12s %-52s %s\n" "DOMAIN" "FILENAME" "STATUS"
  printf "%-12s %-52s %s\n" "------" "--------" "------"
  while IFS= read -r up_file; do
    parse_up_file "$up_file"
    local checksum
    checksum="$(migration_checksum "$up_file")"
    validate_checksum "$checksum"
    if is_applied "$MDOM" "$MFILE"; then
      verify_applied_checksum "$MDOM" "$MFILE" "$checksum"
      printf "%-12s %-52s %s\n" "$MDOM" "$MFILE" "applied"
    else
      printf "%-12s %-52s %s\n" "$MDOM" "$MFILE" "pending"
    fi
  done < <(collect_up_files)
}

cmd_reset_locked() {
  ensure_migrations_table
  assert_no_dirty_migrations
  local total
  total=$(db_scalar "SELECT COUNT(*) FROM public.schema_migrations;")
  total="${total:-0}"
  if [[ "$total" -eq 0 ]]; then
    echo "No applied migrations. Nothing to reset."
    return
  fi
  echo "[WARN] Destructive reset: this will roll back all $total applied migration(s)."
  if [[ -t 0 ]]; then
    read -r -p "Continue? [y/N] " confirm
    [[ "$confirm" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 0; }
  elif ! $FORCE_RESET; then
    echo "[ERROR] Refusing non-interactive reset without --force." >&2
    exit 1
  fi
  run_down_steps "$total"
  echo "Reset complete."
}

cmd_reset() {
  need_db
  wait_for_database
  with_migration_lock cmd_reset_locked
}

cmd_help() {
  cat <<'USAGE'
Usage: migrate.sh <command> [flags]

Commands:
  up        Apply all pending migrations
  down      Roll back last N migrations (--steps N, default 1)
  status    Show applied / pending migration status
  reset     Roll back all applied migrations (interactive; destructive)
  smoke     Run DB smoke test (scripts/db/migrations-smoke.sh)
  help      Show this help

Flags:
  --dry-run    Print SQL without executing (up / down)
  --steps N    Migrations to roll back (down only, default 1)
  --force      Allow non-interactive reset

Connection: MIGRATIONS_DATABASE_URL, then DATABASE_URL, then infra/compose/.env.dev
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
