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
# The production mutation lock, shared with the rollback path
# (scripts/deploy/nchat-prod/production-mutation-lock.sh). Distinct from the id
# above and taken BEFORE it, never after: the order is
#
#   production mutation lock  ->  migration internal lock
#
# and stating it in one place is what keeps two scripts from inverting it.
#
# Only production takes it, which is what NCHAT_PROD_MUTATION_LOCK selects. Dev
# and staging migrate against their own databases with nothing to exclude, and
# making them fail-fast would break local workflows for no gain.
PRODUCTION_MUTATION_LOCK_ID=2026052202
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
# How long cleanup will wait for the session to close before it stops asking
# politely. Short, deterministic and testable: a psql stuck inside a blocking
# query never reads `\q`, and waiting on it forever would hold the production
# mutation lock open for everyone else.
MIGRATION_LOCK_CLOSE_ATTEMPTS="${MIGRATION_LOCK_CLOSE_ATTEMPTS:-50}"
# The server-side ceiling on any single lock acquisition, so a wait that is
# declared to be bounded actually is. `read -t` alone bounds only this script's
# patience, not the query, and the two disagreeing is what left a coprocess
# blocked in the server while its owner had already given up on it.
MIGRATION_LOCK_TIMEOUT_MS="${MIGRATION_LOCK_TIMEOUT_MS:-60000}"
# How long to wait for a lock verdict. Production keeps the values it had; the
# tests override it, and it is read here rather than written literally at each
# call site so that override is real -- setting it and still waiting ninety
# seconds is a comment that disagrees with the code.
MIGRATION_LOCK_REPLY_TIMEOUT="${MIGRATION_LOCK_REPLY_TIMEOUT:-60}"
# The blocking acquisition outside production waits on the server as well, so it
# is given more room than a try-lock verdict needs.
MIGRATION_LOCK_BLOCKING_TIMEOUT="${MIGRATION_LOCK_BLOCKING_TIMEOUT:-90}"

production_mutation_lock_enabled() {
  [[ "${NCHAT_PROD_MUTATION_LOCK:-0}" == "1" ]]
}

# Reads the session until one of the expected tokens or the deadline. psql emits
# an empty line for a void return, so anything unrecognised is skipped.
migration_lock_await() {
  local timeout="$1" line expected read_fd
  shift
  # Captured once, before the loop. bash unsets the coprocess array when it
  # reaps the dead coprocess, and that can happen between two iterations -- so
  # re-expanding it each time turns a session death into an unbound-variable
  # abort instead of the clean read failure the callers handle. A stale fd
  # number simply fails to read, which is exactly the answer wanted.
  read_fd="${MIGRATION_LOCK_PSQL[0]:-}"
  [[ -n "$read_fd" ]] || return 1
  while IFS= read -r -t "$timeout" line 0<&"$read_fd"; do
    for expected in "$@"; do
      [[ "$line" != "$expected" ]] || { printf '%s' "$line"; return 0; }
    done
  done
  return 1
}

# The production mutation lock, taken first and never waited on.
#
# `pg_try_advisory_lock`, not `pg_advisory_lock`: a migration that queued behind
# a rollback would wake when the rollback finished and apply itself onto the
# release that rollback had just restored. Refusing is the safe answer -- nothing
# has been applied, and whoever is deploying gets to look at what the other
# operation did before deciding again.
#
# Taken in the same session as the internal lock below, so there is one session
# to clean up and the order between the two cannot be got wrong.
acquire_production_mutation_lock_for_migration() {
  local verdict
  production_mutation_lock_enabled || return 0
  printf "%s\n" \
    "SELECT CASE WHEN pg_try_advisory_lock(:'prod_lock_id'::bigint) THEN 'prod-acquired' ELSE 'prod-busy' END;" \
    1>&"${MIGRATION_LOCK_PSQL[1]}"
  verdict="$(migration_lock_await "$MIGRATION_LOCK_REPLY_TIMEOUT" prod-acquired prod-busy)" || verdict=""
  case "$verdict" in
    prod-acquired) PRODUCTION_LOCK_ACQUIRED=true; return 0 ;;
    prod-busy)
      echo "[ERROR] BUSY: a production rollback or another production mutation holds the mutation lock." >&2
      echo "[ERROR] No migration has been applied. This is not queued and will not resume on its own;" >&2
      echo "[ERROR] look at what that operation did, then run the migration again if it is still correct." >&2
      return 1
      ;;
  esac
  echo "[ERROR] The production mutation lock could not be taken. No migration has been applied." >&2
  return 1
}

# The migration-specific lock, taken second and only ever second.
#
# In production it is a try-lock for the same reason the outer one is: this
# process is already holding the production mutation lock, and blocking here
# would hold that lock -- the one every rollback and deploy contends for -- for
# as long as the other migration runs. A refusal costs a release; queueing
# behind an unknown operation while holding the barrier costs everyone.
#
# Outside production the wait is kept, because local and CI flows rely on a
# second `migrate up` settling behind the first. It is a bounded wait now:
# `lock_timeout` makes the server itself give up, so the declared timeout is
# true of the query and not merely of this script's patience.
acquire_migration_specific_lock() {
  local verdict
  if production_mutation_lock_enabled; then
    printf "%s\n" \
      "SELECT CASE WHEN pg_try_advisory_lock(:'lock_id'::bigint) THEN 'locked' ELSE 'inner-busy' END;" \
      1>&"${MIGRATION_LOCK_PSQL[1]}"
    verdict="$(migration_lock_await "$MIGRATION_LOCK_REPLY_TIMEOUT" locked inner-busy)" || verdict=""
    case "$verdict" in
      locked) LOCK_ACQUIRED=true; return 0 ;;
      inner-busy)
        echo "[ERROR] BUSY: another migration is already running." >&2
        echo "[ERROR] No migration has been applied, and this will not resume on its own." >&2
        return 1
        ;;
    esac
    echo "[ERROR] The migration lock could not be taken. No migration has been applied." >&2
    return 1
  fi
  printf "%s\n" \
    "SET lock_timeout = '${MIGRATION_LOCK_TIMEOUT_MS}ms';" \
    "SELECT pg_advisory_lock(:'lock_id'::bigint);" \
    "SELECT 'locked';" 1>&"${MIGRATION_LOCK_PSQL[1]}"
  if verdict="$(migration_lock_await "$MIGRATION_LOCK_BLOCKING_TIMEOUT" locked)"; then
    LOCK_ACQUIRED=true
    return 0
  fi
  echo "[ERROR] Timed out waiting for PostgreSQL advisory lock." >&2
  return 1
}

# Everything protected runs in the session that holds the locks.
#
# This is the property the advisory locks were useless without. They are
# session-level: PostgreSQL releases them the instant their session dies. But
# every statement used to run through `db_exec`, which opens a NEW psql each
# time -- so a lock session that died left the locks free for a rollback to take
# while the migration carried on applying schema from connections that knew
# nothing about it. Checking `kill -0`, or asking pg_locks, or a heartbeat, all
# leave the same window: the answer is stale the moment it is read.
#
# There is no window if the SQL and the locks are the same connection. If it
# dies the locks go and the SQL goes with them, atomically, because they were
# never separable in the first place.
#
# Advisory locks are session-scoped rather than transaction-scoped, so the
# migrations keep their own transaction boundaries; only the connection is
# shared.
LOCK_SESSION_ACTIVE=false
SESSION_SEQ=0
# How long one statement may take to answer. A migration can be slow, so this is
# generous in production; the tests override it to keep the suite about
# lifecycle rather than about the clock.
SESSION_REPLY_TIMEOUT="${MIGRATION_SESSION_REPLY_TIMEOUT:-900}"

# Sends a script into the lock session and waits for its own sentinel.
#
# Both halves are failure paths that matter. A write to a dead coprocess fails,
# and a session that stopped answering never returns the sentinel -- either way
# nothing further is applied and the runner exits non-zero. No reconnection is
# attempted: a new attempt is a new, explicit run.
# Whether the session is still there to be spoken to.
#
# bash UNSETS the coprocess array when the coprocess dies, so the fd expansion
# is what notices first -- and under `set -u` that would end the run with an
# unbound-variable diagnostic instead of the refusal this is supposed to report.
# Asking first turns the same fact into a message an operator can act on.
lock_session_is_open() {
  [[ -n "${MIGRATION_LOCK_PSQL[1]:-}" && -n "${LOCK_HOLDER_PID:-}" ]]
}

session_send_and_confirm() {
  local script="$1" token write_fd
  [[ "$LOCK_SESSION_ACTIVE" == "true" ]] || {
    echo "[ERROR] Refusing to run protected SQL without the lock-owning session." >&2
    return 1
  }
  lock_session_is_open || {
    echo "[ERROR] The session holding the advisory locks is gone; nothing further is applied." >&2
    return 1
  }
  SESSION_SEQ=$((SESSION_SEQ + 1))
  token="nchat-session-ok-$SESSION_SEQ"
  write_fd="${MIGRATION_LOCK_PSQL[1]:-}"
  printf '%s\n' "$script" "SELECT '$token';" 1>&"$write_fd" 2>/dev/null || {
    echo "[ERROR] The session holding the advisory locks is gone; nothing further is applied." >&2
    return 1
  }
  migration_lock_await "$SESSION_REPLY_TIMEOUT" "$token" >/dev/null || {
    echo "[ERROR] The session holding the advisory locks stopped responding; nothing further is applied." >&2
    return 1
  }
}

# SQL in the lock session. Extra arguments are psql variables as NAME=VALUE, set
# with `\set` inside that same session, so the SQL keeps referring to :'name'
# and a value is never interpolated into the statement text.
protected_sql() {
  local sql="$1" script="" pair
  shift
  for pair in "$@"; do
    script+="\\set ${pair%%=*} '${pair#*=}'"$'\n'
  done
  script+="$sql"
  session_send_and_confirm "$script"
}

# A .sql file, executed by the lock session itself rather than by a psql of its
# own. If the session dies part-way through the file, the file stops there.
protected_file() {
  # The path goes through a psql variable, not straight into the meta-command.
  #
  # `\i $1` is split on whitespace by psql's own parser, so a checkout under a
  # directory with a space in its name -- "/work/NChat Project/migrations/..." --
  # arrives as several arguments and the migration is never found. `:'name'`
  # quotes it the way psql quotes anything else.
  session_send_and_confirm "$(printf "\\set migration_file '%s'\n\\i :'migration_file'" "$1")"
}

acquire_migration_lock() {
  coproc MIGRATION_LOCK_PSQL { psql --no-password -v ON_ERROR_STOP=1 -q -t -A --set=lock_id="$MIGRATION_LOCK_ID" --set=prod_lock_id="$PRODUCTION_MUTATION_LOCK_ID" "$DSN"; }
  LOCK_HOLDER_PID="$MIGRATION_LOCK_PSQL_PID"

  # Order, stated once: production mutation lock, then the migration lock, and
  # never the reverse. A failure at either point releases whatever was taken.
  if ! acquire_production_mutation_lock_for_migration; then
    release_migration_lock
    exit 1
  fi
  if ! acquire_migration_specific_lock; then
    release_migration_lock
    exit 1
  fi
  LOCK_SESSION_ACTIVE=true
}

# Ends the session in bounded time, whatever state it is in.
#
# `wait` alone was the defect: a psql blocked inside pg_advisory_lock never
# reads the `\q`, so the wait never returned -- and the production mutation lock
# stayed held while it did not. So the quit is asked for, the process is polled
# for a bounded number of attempts, and if it is still there it is signalled and
# killed. `wait` runs last, on a process already known to be finishing, so it
# reaps rather than blocks.
close_migration_lock_session() {
  local attempt=0
  [[ -z "${MIGRATION_LOCK_PSQL[1]:-}" ]] ||
    printf "\\q\n" 1>&"${MIGRATION_LOCK_PSQL[1]}" 2>/dev/null || true
  while kill -0 "$LOCK_HOLDER_PID" 2>/dev/null; do
    attempt=$((attempt + 1))
    if [[ "$attempt" -gt "$MIGRATION_LOCK_CLOSE_ATTEMPTS" ]]; then
      kill -TERM "$LOCK_HOLDER_PID" 2>/dev/null || true
      kill -KILL "$LOCK_HOLDER_PID" 2>/dev/null || true
      break
    fi
    sleep 0.1
  done
  wait "$LOCK_HOLDER_PID" >/dev/null 2>&1 || true
}

# Idempotent: a second call finds nothing to do and returns. That matters
# because the signal handlers exit, which runs the EXIT trap, which releases
# again -- and each unlock must be sent exactly once.
release_migration_lock() {
  LOCK_SESSION_ACTIVE=false
  [[ -n "${LOCK_HOLDER_PID:-}" ]] || return 0
  if [[ "${LOCK_ACQUIRED:-false}" == "true" ]]; then
    [[ -z "${MIGRATION_LOCK_PSQL[1]:-}" ]] ||
      printf "%s\n" "SELECT pg_advisory_unlock(:'lock_id'::bigint);" 1>&"${MIGRATION_LOCK_PSQL[1]}" 2>/dev/null || true
    LOCK_ACQUIRED=false
  fi
  # Released after the inner one, mirroring the order they were taken in.
  if [[ "${PRODUCTION_LOCK_ACQUIRED:-false}" == "true" ]]; then
    [[ -z "${MIGRATION_LOCK_PSQL[1]:-}" ]] ||
      printf "%s\n" "SELECT pg_advisory_unlock(:'prod_lock_id'::bigint);" 1>&"${MIGRATION_LOCK_PSQL[1]}" 2>/dev/null || true
    PRODUCTION_LOCK_ACQUIRED=false
  fi
  close_migration_lock_session
  LOCK_HOLDER_PID=""
}

# What a signal must do, and why this is not the EXIT handler.
#
# `trap release_migration_lock EXIT INT TERM` was wrong in a way that is easy to
# miss: on a signal the handler ran, released both locks, and then RETURNED to
# the interrupted flow, which carried on migrating with no lock held and exited
# 0. A signal handler for an operation like this has to end the process.
#
# The guard is not decoration: a second Ctrl-C during teardown would otherwise
# re-enter it half-way through and send each unlock twice.
migration_lock_signal_exit() {
  local code="$1"
  [[ "${MIGRATION_LOCK_SIGNALLED:-false}" != "true" ]] || return 0
  MIGRATION_LOCK_SIGNALLED=true
  echo "[ERROR] Signalled; releasing the migration locks and stopping." >&2
  release_migration_lock
  exit "$code"
}

migration_lock_on_exit() { release_migration_lock; }
migration_lock_on_int() { migration_lock_signal_exit 130; }
migration_lock_on_term() { migration_lock_signal_exit 143; }

with_migration_lock() {
  local status=0
  acquire_migration_lock
  trap migration_lock_on_exit EXIT
  trap migration_lock_on_int INT
  trap migration_lock_on_term TERM
  set +e
  "$@"
  status=$?
  set -e
  release_migration_lock
  trap - EXIT INT TERM
  return "$status"
}

# ---------------------------------------------------------------------------
# schema_migrations table: tracks clean/dirty state and file checksum.
# ---------------------------------------------------------------------------
ensure_migrations_table() {
  protected_sql "
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
  protected_sql "INSERT INTO public.schema_migrations(domain, filename, checksum_sha256, dirty, in_progress, applied_at, updated_at)
    VALUES(:'domain', :'filename', :'checksum', true, true, now(), now());" \
    "domain=$domain" "filename=$filename" "checksum=$checksum"
}

record_apply_clean() {
  local domain="$1" filename="$2" checksum="$3"
  validate_domain "$domain"
  validate_migration_base "$filename"
  validate_checksum "$checksum"
  protected_sql "UPDATE public.schema_migrations
       SET checksum_sha256 = :'checksum', dirty = false, in_progress = false, applied_at = now(), updated_at = now()
     WHERE domain = :'domain' AND filename = :'filename';" \
    "domain=$domain" "filename=$filename" "checksum=$checksum"
}

record_rollback_started() {
  local domain="$1" filename="$2"
  validate_domain "$domain"
  validate_migration_base "$filename"
  protected_sql "UPDATE public.schema_migrations
       SET dirty = true, in_progress = true, updated_at = now()
     WHERE domain = :'domain' AND filename = :'filename';" \
    "domain=$domain" "filename=$filename"
}

record_rollback_clean() {
  local domain="$1" filename="$2"
  validate_domain "$domain"
  validate_migration_base "$filename"
  protected_sql "DELETE FROM public.schema_migrations WHERE domain = :'domain' AND filename = :'filename';" \
    "domain=$domain" "filename=$filename"
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
    protected_file "$up_file"
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
  protected_file "$sql_file"
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
    protected_file "$down_file"
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
  # Status is a read, and reads do not create things.
  #
  # It used to call ensure_migrations_table, which was harmless while that was a
  # plain `db_exec` and became a regression the moment the ledger writers moved
  # into the lock-owning session: `protected_sql` refuses to run without one,
  # and a status run has no reason to open a session or take the production
  # mutation lock. So the table is probed rather than created; a database that
  # has never been migrated simply reports every migration as pending.
  MIGRATIONS_TABLE_EXISTS=false
  if migrations_table_exists; then
    MIGRATIONS_TABLE_EXISTS=true
    assert_no_dirty_migrations
  fi
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
