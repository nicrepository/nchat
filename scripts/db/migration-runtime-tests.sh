#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
MIGRATE="$ROOT_DIR/scripts/db/migrate.sh"
WRAPPER="$ROOT_DIR/scripts/db/run-migrations.sh"
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-migration-tests.XXXXXX")"
trap 'rm -rf "$TEMP_DIR"' EXIT

fail() {
  echo "migration runtime test failed: $*" >&2
  exit 1
}

source_migrate() {
  set -- help
  # shellcheck source=scripts/db/migrate.sh
  source "$MIGRATE" >/dev/null
}

test_dsn_resolution() {
  (
    source_migrate
    MIGRATIONS_DATABASE_URL='postgres://migrator:p@host/db?x=a;b'
    DATABASE_URL='postgres://runtime:ignored@host/db'
    need_db
    [[ "$DSN" == "$MIGRATIONS_DATABASE_URL" ]]
  ) || fail "MIGRATIONS_DATABASE_URL must take precedence"

  (
    source_migrate
    unset MIGRATIONS_DATABASE_URL
    DATABASE_URL='postgres://runtime:p@host/db?application_name=nchat runtime'
    need_db
    [[ "$DSN" == "$DATABASE_URL" ]]
  ) || fail "DATABASE_URL fallback failed"

  (
    source_migrate
    unset MIGRATIONS_DATABASE_URL DATABASE_URL
    load_env() {
      PGUSER=local_user
      PGHOST=localhost
      PGPORT=55432
      PGDATABASE=local_db
      PGPASSWORD='local;password'
    }
    need_db
    [[ "$DSN" == 'postgresql://local_user@localhost:55432/local_db' ]]
  ) || fail "local environment fallback failed"

  if (
    source_migrate
    unset MIGRATIONS_DATABASE_URL DATABASE_URL
    COMPOSE_DIR="$TEMP_DIR/missing-compose"
    need_db
  ) >/dev/null 2>&1; then
    fail "missing DSN and local config must fail"
  fi
}

test_database_wait_retry() {
  (
    source_migrate

    DSN='postgresql://redacted'
    attempts=0
    sleeps=0

    db_exec() {
      attempts=$((attempts + 1))
      [[ "$attempts" -ge 2 ]]
    }

    sleep() {
      sleeps=$((sleeps + 1))
    }

    MIGRATIONS_DATABASE_WAIT_ATTEMPTS=3
    MIGRATIONS_DATABASE_WAIT_SECONDS=0

    wait_for_database >/dev/null 2>&1

    [[ "$attempts" -eq 2 ]]
    [[ "$sleeps" -eq 1 ]]
  ) || fail "database wait did not recover from a transient failure"

  local output="$TEMP_DIR/database-wait-failure.log"

  set +e
  (
    source_migrate

    DSN='postgresql://redacted'

    db_exec() {
      return 1
    }

    sleep() {
      :
    }

    MIGRATIONS_DATABASE_WAIT_ATTEMPTS=2
    MIGRATIONS_DATABASE_WAIT_SECONDS=0

    wait_for_database
  ) >"$output" 2>&1
  local status=$?
  set -e

  [[ "$status" -ne 0 ]] ||
    fail "database wait accepted a permanent failure"

  grep -Fq     'PostgreSQL unavailable after 2 attempts.'     "$output" ||
    fail "database wait did not report exhaustion"
}

test_internal_identifier_validation() {
  (
    source_migrate
    MIGRATIONS_TABLE_EXISTS=true
    db_scalar() { printf '1'; }
    is_applied auth 000001_create_users
    [[ "$(stored_checksum auth 000001_create_users)" == 1 ]]
  ) || fail "valid migration identifiers were rejected"

  local invalid
  invalid=("" "'" '"' ";" "-- comment" $'bad\nvalue' $'bad\rvalue' "../auth" 'bad\value' '${HOME}' '$(touch /tmp/nchat-migrate-injection)' "auth-name" "UPPER")
  for invalid in "${invalid[@]}"; do
    if (
      source_migrate
      MIGRATIONS_TABLE_EXISTS=true
      db_scalar() { fail "SQL executed for invalid domain"; }
      is_applied "$invalid" 000001_create_users
    ) >/dev/null 2>&1; then
      fail "is_applied accepted invalid domain"
    fi
    if (
      source_migrate
      db_scalar() { fail "SQL executed for invalid filename"; }
      stored_checksum auth "$invalid"
    ) >/dev/null 2>&1; then
      fail "stored_checksum accepted invalid filename"
    fi
  done
}

test_sql_parameterization() {
  local record="$TEMP_DIR/psql-record" checksum
  checksum='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  (
    source_migrate
    db_exec() {
      printf 'arg=%s\n' "$@" >"$record"
      cat >>"$record"
    }
    record_apply_started auth 000001_create_users "$checksum"
  ) || fail "valid parameterized apply record failed"
  grep -Fxq 'arg=--set=domain=auth' "$record" || fail "domain was not passed as a psql variable"
  grep -Fxq 'arg=--set=filename=000001_create_users' "$record" || fail "filename was not passed as a psql variable"
  grep -Fxq "arg=--set=checksum=$checksum" "$record" || fail "checksum was not passed as a psql variable"
  grep -Fq "VALUES(:'domain', :'filename', :'checksum'" "$record" || fail "SQL does not use psql literals"
  ! grep -Fq "VALUES('auth'" "$record" || fail "SQL contains interpolated migration values"

  local function invalid
  for function in record_apply_started record_apply_clean; do
    for invalid in "'" '"' ";" "-- comment" $'bad\nvalue' $'bad\rvalue' '../auth' 'bad\value' '${HOME}' '$(touch /tmp/nchat-migrate-injection)' ''; do
      if (
        source_migrate
        db_exec() { fail "SQL executed for invalid $function input"; }
        "$function" "$invalid" 000001_create_users "$checksum"
      ) >/dev/null 2>&1; then
        fail "$function accepted an invalid domain"
      fi
    done
  done
  for function in record_rollback_started record_rollback_clean; do
    if (
      source_migrate
      db_exec() { fail "SQL executed for invalid $function input"; }
      "$function" auth '../000001_create_users'
    ) >/dev/null 2>&1; then
      fail "$function accepted an invalid filename"
    fi
  done
  if (
    source_migrate
    db_exec() { fail "SQL executed for invalid checksum"; }
    record_apply_started auth 000001_create_users 'not-a-checksum'
  ) >/dev/null 2>&1; then
    fail "record_apply_started accepted an invalid checksum"
  fi
  [[ ! -e /tmp/nchat-migrate-injection ]] || fail "malicious input executed command substitution"
}

prepare_fake_wrapper() {
  mkdir -p "$TEMP_DIR/wrapper"
  cp "$WRAPPER" "$TEMP_DIR/wrapper/run-migrations.sh"
  cp "$ROOT_DIR/scripts/db/grant-runtime.sql" "$TEMP_DIR/wrapper/grant-runtime.sql"
  cp "$ROOT_DIR/scripts/db/testdata/fake-migrate.sh" "$TEMP_DIR/wrapper/migrate.sh"
  chmod 0555 "$TEMP_DIR/wrapper/run-migrations.sh"
  chmod 0555 "$TEMP_DIR/wrapper/migrate.sh"
}

test_wrapper() {
  prepare_fake_wrapper

  local record="$TEMP_DIR/wrapper-record"
  MIGRATIONS_DATABASE_URL='postgres://migrator:special;value@host/db' \
    FAKE_RECORD="$record" "$TEMP_DIR/wrapper/run-migrations.sh" up --steps '2' >/dev/null
  grep -Fxq 'hook=set' "$record" || fail "up did not request runtime grants"
  grep -Fxq 'arg=--steps' "$record" || fail "wrapper changed additional arguments"
  grep -Fxq 'arg=2' "$record" || fail "wrapper changed argument values"

  DATABASE_URL='postgres://runtime:local@host/db' FAKE_RECORD="$record" \
    "$TEMP_DIR/wrapper/run-migrations.sh" up --dry-run >/dev/null
  grep -Fxq 'hook=unset' "$record" || fail "dry-run must not apply grants"

  local marker="$TEMP_DIR/injection-marker"
  FAKE_RECORD="$record" "$TEMP_DIR/wrapper/run-migrations.sh" status \
    "; touch $marker" >/dev/null
  [[ ! -e "$marker" ]] || fail "wrapper executed argument content"

  set +e
  FAKE_EXIT=37 FAKE_RECORD="$record" "$TEMP_DIR/wrapper/run-migrations.sh" up >/dev/null
  local status=$?
  set -e
  [[ "$status" -eq 37 ]] || fail "migration exit code was not propagated"
}

test_grant_failure() {
  set +e
  (
    source_migrate
    DRY_RUN=false
    MIGRATIONS_POST_UP_SQL_FILE="$ROOT_DIR/scripts/db/grant-runtime.sql"
    db_exec() { return 41; }
    run_post_up_sql
  )
  local status=$?
  set -e
  [[ "$status" -eq 41 ]] || fail "grant failure was not propagated"
}

test_dsn_resolution
test_database_wait_retry
test_internal_identifier_validation
test_sql_parameterization
test_wrapper
test_grant_failure

echo "Migration runtime tests passed."
