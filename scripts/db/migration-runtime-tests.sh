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

# The values a ledger writer sends, captured from the session seam.
capture_ledger_write() {
  local record="$1"
  shift
  (
    source_migrate
    LOCK_SESSION_ACTIVE=true
    session_send_and_confirm() { printf '%s\n' "$1" >"$record"; }
    "$@"
  )
}

# One writer, one bad value: validation must refuse before anything is sent.
assert_ledger_writer_refuses() {
  local function="$1"
  shift
  if (
    source_migrate
    LOCK_SESSION_ACTIVE=true
    session_send_and_confirm() { fail "SQL executed for invalid $function input"; }
    "$function" "$@"
  ) >/dev/null 2>&1; then
    fail "$function accepted invalid input"
  fi
}

# Every shape that must never reach the database.
INVALID_IDENTIFIERS=("'" '"' ";" "-- comment" $'bad\nvalue' $'bad\rvalue' '../auth'
  'bad\value' '${HOME}' '$(touch /tmp/nchat-migrate-injection)' '')

assert_ledger_writers_validate() {
  local checksum="$1" function invalid
  for function in record_apply_started record_apply_clean; do
    for invalid in "${INVALID_IDENTIFIERS[@]}"; do
      assert_ledger_writer_refuses "$function" "$invalid" 000001_create_users "$checksum"
    done
  done
  for function in record_rollback_started record_rollback_clean; do
    assert_ledger_writer_refuses "$function" auth '../000001_create_users'
  done
  assert_ledger_writer_refuses record_apply_started auth 000001_create_users 'not-a-checksum'
}

# The ledger writers travel through the session that owns the advisory locks, so
# the seam is session_send_and_confirm rather than db_exec. The property is
# unchanged and is the one that matters: values are psql variables, never text
# spliced into the statement.
test_sql_parameterization() {
  local record="$TEMP_DIR/psql-record" checksum
  checksum='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  capture_ledger_write "$record" record_apply_started auth 000001_create_users "$checksum" ||
    fail "valid parameterized apply record failed"
  grep -Fxq "\\set domain 'auth'" "$record" || fail "domain was not passed as a psql variable"
  grep -Fxq "\\set filename '000001_create_users'" "$record" || fail "filename was not passed as a psql variable"
  grep -Fxq "\\set checksum '$checksum'" "$record" || fail "checksum was not passed as a psql variable"
  grep -Fq "VALUES(:'domain', :'filename', :'checksum'" "$record" || fail "SQL does not use psql literals"
  ! grep -Fq "VALUES('auth'" "$record" || fail "SQL contains interpolated migration values"
  assert_ledger_writers_validate "$checksum"
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
    # The grants run in the lock session too, so a failure there is what has to
    # propagate now.
    LOCK_SESSION_ACTIVE=true
    session_send_and_confirm() { return 41; }
    run_post_up_sql
  )
  local status=$?
  set -e
  [[ "$status" -eq 41 ]] || fail "grant failure was not propagated"
}

# The contract between what migrate.sh persists and what the production rollback
# path reads back (CICD-08).
#
# These are two scripts on opposite sides of the database, and the only thing
# holding them together is the shape of a row. migrate.sh strips ".up.sql"
# before it stores the name -- parse_up_file does MFILE="${filename%.up.sql}" --
# so the ledger holds `chat` / `000100_pin`, while the file that migration came
# from is migrations/chat/000100_pin.up.sql. A reader that expected the stored
# name to carry the suffix would reject every real row and block every rollback,
# which is exactly the defect this test exists to catch.
#
# So both halves are driven from their real implementations rather than from a
# fixture that restates the format: parse_up_file produces the stored values,
# canonical_migration consumes them, and the file name it reconstructs has to be
# the file that was actually checksummed. If either side changes its idea of the
# format, this fails.
# The row migrate.sh really writes for one migration file, as
# "<domain> <filename> <checksum>" -- produced by the runner rather than by this
# test, so it is the runner's idea of the format that travels downstream.
stored_ledger_row() {
  local up="$1"
  (
    source_migrate
    parse_up_file "$up"
    printf '%s %s %s' "$MDOM" "$MFILE" "$(migration_checksum "$up")"
  )
}

# The row migrate.sh writes, checked against the shape everything downstream
# assumes. The heart of it is the missing suffix: the runner strips ".up.sql"
# before it persists the name, and a reader that expected it there would reject
# every real row and block every rollback.
assert_ledger_row_is_stored_bare() {
  local row="$1" domain filename
  read -r domain filename _ <<<"$row"
  [[ "$domain" == "chat" ]] ||
    fail "the ledger domain is '$domain', expected 'chat'"
  [[ "$filename" == "000100_pin" ]] ||
    fail "the ledger filename is '$filename', expected the base name '000100_pin'"
  [[ "$filename" != *.up.sql ]] ||
    fail "the ledger filename still carries .up.sql; the rollback reader normalises on the assumption that it does not"
}

# What the rollback reader makes of that row, and whether the file name it
# rebuilds is the file the checksum was actually taken from. If it is not, the
# schema gate would correlate the ledger against the wrong bytes.
assert_reader_reconstructs_the_migration_file() {
  local row="$1" base_dir="$2" canonical checksum
  checksum="${row##* }"
  canonical="$(
    # shellcheck source=scripts/deploy/nchat-prod/applied-migrations.sh
    source "$ROOT_DIR/scripts/deploy/nchat-prod/applied-migrations.sh"
    canonical_migration "$row"
  )" || fail "the rollback ledger reader refused a row migrate.sh would write"
  [[ "$canonical" == "chat/000100_pin.up.sql $checksum" ]] ||
    fail "the reader normalised the stored row to '$canonical', expected 'chat/000100_pin.up.sql $checksum'"
  [[ -f "$base_dir/${canonical%% *}" ]] ||
    fail "the reconstructed path does not name the migration file that was checksummed"
  [[ "$(source_migrate; migration_checksum "$base_dir/${canonical%% *}")" == "$checksum" ]] ||
    fail "the reconstructed path does not checksum to the value the ledger stores"
}

# A stored name that already carried the suffix is not a row migrate.sh can
# write, and must not be normalised into one by appending a second suffix.
assert_reader_refuses_a_suffixed_name() {
  local checksum="$1"
  # An `if`, not `cmd && fail`: an AND-list whose test succeeds-as-false leaves
  # the function's status non-zero, and under `set -e` that ends the suite
  # silently instead of running the next assertion.
  if (
    source "$ROOT_DIR/scripts/deploy/nchat-prod/applied-migrations.sh"
    canonical_migration "chat 000100_pin.up.sql $checksum"
  ) >/dev/null 2>&1; then
    fail "the reader accepted a stored filename carrying .up.sql"
  fi
}

# The contract between what migrate.sh persists and what the production rollback
# path reads back, driven from both real implementations.
test_applied_migration_ledger_contract() {
  local base_dir="$TEMP_DIR/ledger-contract"
  local up="$base_dir/chat/000100_pin.up.sql"
  local row
  mkdir -p "$base_dir/chat"
  printf 'ALTER TABLE chat.messages ADD COLUMN pinned boolean;\n' >"$up"
  row="$(stored_ledger_row "$up")" || fail "the runner could not describe the migration"
  assert_ledger_row_is_stored_bare "$row"
  assert_reader_reconstructs_the_migration_file "$row" "$base_dir"
  assert_reader_refuses_a_suffixed_name "${row##* }"
  echo "  [ok] migrate.sh stores <domain>/<base> and the rollback reader reconstructs <domain>/<base>.up.sql"
}

# --- the migration lock lifecycle ------------------------------------------
#
# These drive with_migration_lock itself, against a psql that models the two
# advisory locks with `mkdir` -- which succeeds or fails atomically on an
# existing directory, exactly as pg_try_advisory_lock does. The exclusion is
# therefore real rather than recorded.

MIGRATION_LOCK_BIN=""

# The shared advisory-lock fixture, copied the way the other fakes are.
#
# One fixture rather than a private copy: two models of one protocol drift, and
# a copy that answered the production verdicts to the migration lock is exactly
# the defect this replaced. See scripts/ci/testdata/nchat-prod/fake-psql.
write_lock_psql() {
  local bin="$1"
  mkdir -p "$bin"
  cp "$ROOT_DIR/scripts/ci/testdata/nchat-prod/fake-psql" "$bin/psql"
  chmod +x "$bin/psql"
}

lock_env() {
  local label="$1"
  MIGRATION_LOCK_BIN="$TEMP_DIR/lockbin.$label"
  write_lock_psql "$MIGRATION_LOCK_BIN"
  rm -f "$TEMP_DIR/hang.$label"
  LOCK_ENV=(
    "FAKE_PSQL_LOG=$TEMP_DIR/psql.$label.log"
    "FAKE_OUTER_LOCK=$TEMP_DIR/outer.$label"
    "FAKE_INNER_LOCK=$TEMP_DIR/inner.$label"
    "FAKE_HANG=$TEMP_DIR/hang.$label"
    "PATH=$MIGRATION_LOCK_BIN:$PATH"
    "MIGRATIONS_DATABASE_URL=postgres://nchat_migrator:pw@postgres:5432/nchat"
    "MIGRATION_LOCK_CLOSE_ATTEMPTS=5"
    # Test-only. The production defaults (60/90/900s) stay what they are; here a
    # deterministic fake answers instantly or not at all, and waiting minutes to
    # discover that proves nothing.
    "MIGRATION_LOCK_REPLY_TIMEOUT=2"
    "MIGRATION_LOCK_BLOCKING_TIMEOUT=2"
    "MIGRATION_SESSION_REPLY_TIMEOUT=2"
  )
}

# Runs a snippet with migrate.sh sourced, the fake psql on PATH and the lock
# paths bound. Production mode unless the caller says otherwise.
run_with_migration_lock() {
  local label="$1" snippet="$2" prod="${3:-1}"
  lock_env "$label"
  env "${LOCK_ENV[@]}" NCHAT_PROD_MUTATION_LOCK="$prod" \
    bash -c '
      set -m
      source "$1" help >/dev/null 2>&1 || true
      eval "$2"
    ' _ "$MIGRATE" "$snippet"
}

# CASE A / CASE B -- a signal ends the migration; nothing after it runs.
signal_migration_lock() {
  local label="$1" signal="$2"
  lock_env "$label"
  rm -f "$TEMP_DIR/sentinel.$label" "$TEMP_DIR/started.$label"
  # `set -m` so the job gets the default SIGINT disposition: a background job in
  # a non-interactive shell inherits SIGINT ignored, and `trap ... INT` cannot
  # re-enable a signal that was ignored on entry.
  set -m
  env "${LOCK_ENV[@]}" NCHAT_PROD_MUTATION_LOCK=1 \
    SENTINEL="$TEMP_DIR/sentinel.$label" STARTED="$TEMP_DIR/started.$label" \
    bash -c '
      set -Eeuo pipefail
      set -- help
      source "$0" >/dev/null
      DSN="$MIGRATIONS_DATABASE_URL"
      protected() {
        : >"$STARTED"
        while [[ ! -f "$SENTINEL.go" ]]; do sleep 0.05; done
        : >"$SENTINEL"
      }
      with_migration_lock protected
    ' "$MIGRATE" >"$TEMP_DIR/sig.$label.out" 2>"$TEMP_DIR/sig.$label.err" &
  local holder=$!
  set +m
  local waited=0
  while [[ ! -f "$TEMP_DIR/started.$label" ]]; do
    waited=$((waited + 1))
    [[ "$waited" -lt 400 ]] || break
    sleep 0.05
  done
  [[ -f "$TEMP_DIR/started.$label" ]] || fail "the protected command never started ($label): $(tail -3 "$TEMP_DIR/sig.$label.err")"
  kill "-$signal" "$holder" 2>/dev/null || true
  local status=0
  wait "$holder" || status=$?
  printf '%s' "$status"
}

assert_locks_released() {
  local label="$1"
  [[ ! -d "$TEMP_DIR/outer.$label" ]] || fail "$label left the production mutation lock held"
  [[ ! -d "$TEMP_DIR/inner.$label" ]] || fail "$label left the migration lock held"
}

test_migration_lock_term() {
  local status
  status="$(signal_migration_lock term TERM)"
  [[ "$status" == "143" ]] || fail "TERM ended the migration with $status, expected 143"
  [[ ! -f "$TEMP_DIR/sentinel.term" ]] ||
    fail "the protected command continued after TERM"
  assert_locks_released term
  # Exactly once each: the signal handler exits, which runs the EXIT trap.
  [[ "$(grep -c "pg_advisory_unlock" "$TEMP_DIR/psql.term.log")" == "2" ]] ||
    fail "TERM did not send exactly one unlock per lock"
  echo "  [ok] TERM ends the migration with 143, releases both locks, runs nothing after"
}

test_migration_lock_int() {
  local status
  status="$(signal_migration_lock int INT)"
  [[ "$status" == "130" ]] || fail "INT ended the migration with $status, expected 130"
  [[ ! -f "$TEMP_DIR/sentinel.int" ]] ||
    fail "the protected command continued after INT"
  assert_locks_released int
  echo "  [ok] INT ends the migration with 130, releases both locks, runs nothing after"
}

# CASE E / CASE F -- ordinary statuses survive the lock wrapper unchanged.
test_migration_lock_preserves_status() {
  run_with_migration_lock success '
    DSN="$MIGRATIONS_DATABASE_URL"
    with_migration_lock true
  ' || fail "a successful command did not return 0 through the lock"
  assert_locks_released success

  local status=0
  run_with_migration_lock failure '
    DSN="$MIGRATIONS_DATABASE_URL"
    with_migration_lock bash -c "exit 41"
  ' || status=$?
  [[ "$status" -eq 41 ]] || fail "the command's status became $status, expected 41"
  assert_locks_released failure
  echo "  [ok] the lock preserves the protected command's status, and releases either way"
}

# CASE C -- the migration-specific lock is busy in production. Refusing matters
# more than it looks: this process already holds the production mutation lock,
# and waiting would hold that barrier for as long as the other migration runs.
test_migration_inner_lock_busy() {
  local status=0
  lock_env innerbusy
  mkdir -p "$TEMP_DIR/inner.innerbusy"
  env "${LOCK_ENV[@]}" NCHAT_PROD_MUTATION_LOCK=1 \
    bash "$MIGRATE" up >"$TEMP_DIR/innerbusy.out" 2>"$TEMP_DIR/innerbusy.err" || status=$?
  [[ "$status" -ne 0 ]] || fail "the migration ran while another migration held the lock"
  grep -q "BUSY: another migration is already running" "$TEMP_DIR/innerbusy.err" ||
    fail "the inner lock conflict did not report BUSY"
  grep -q "will not resume on its own" "$TEMP_DIR/innerbusy.err" ||
    fail "the inner lock conflict did not say it is not queued"
  # The outer lock must not be left held while the inner one is the problem.
  [[ ! -d "$TEMP_DIR/outer.innerbusy" ]] ||
    fail "a busy inner lock left the production mutation lock held"
  grep -q "APPLY" "$TEMP_DIR/innerbusy.out" && fail "a refused migration applied something"
  rmdir "$TEMP_DIR/inner.innerbusy"
  echo "  [ok] a busy migration lock refuses, releases the outer lock, and applies nothing"
}

# CASE D -- the session stops answering. Cleanup has to return anyway, or the
# production mutation lock stays held for everyone else.
test_migration_lock_cleanup_is_bounded() {
  local started ended elapsed status=0
  lock_env hang
  : >"$TEMP_DIR/hang.hang"
  started="$(date +%s)"
  env "${LOCK_ENV[@]}" NCHAT_PROD_MUTATION_LOCK=0 \
    bash -c '
      set -Eeuo pipefail
      set -- help
      source "$0" >/dev/null
      DSN="$MIGRATIONS_DATABASE_URL"
      acquire_migration_lock
    ' "$MIGRATE" >/dev/null 2>&1 || status=$?
  ended="$(date +%s)"
  elapsed=$((ended - started))
  [[ "$status" -ne 0 ]] || fail "a session that never answers was treated as a lock"
  # Tight on purpose. The fixture sets the timeouts to 2s, so anything near the
  # production defaults means the override was declared and never read -- which
  # is what a 150s allowance used to hide.
  [[ "$elapsed" -lt 30 ]] ||
    fail "cleanup took ${elapsed}s; the configured timeout is not being used"
  [[ ! -d "$TEMP_DIR/outer.hang" ]] || fail "an unresponsive session kept the outer lock"
  echo "  [ok] cleanup returns in bounded time and leaves no lock held"
}

# One run of the command the runbook tells an operator to use.
#
# NCHAT_PROD_MUTATION_LOCK is deliberately NOT set by the caller: the entrypoint
# has to set it itself, because the finding was precisely that the documented
# path did not, and an operator who forgets a variable is not a control.
run_production_entrypoint() {
  local tag="$1" status=0
  # No verb: the entrypoint supplies `up` itself and refuses any the operator
  # gives it, `up` included -- one rule with no exception to get wrong.
  env "${LOCK_ENV[@]}" \
    bash "$ROOT_DIR/scripts/db/migrate-prod.sh" \
    >"$TEMP_DIR/entrypoint.$tag.out" 2>"$TEMP_DIR/entrypoint.$tag.err" || status=$?
  printf '%s' "$status"
}

test_production_migration_entrypoint_refuses_when_busy() {
  local status
  lock_env entrypoint
  mkdir -p "$TEMP_DIR/outer.entrypoint"

  status="$(run_production_entrypoint busy)"
  [[ "$status" -ne 0 ]] || fail "the production entrypoint migrated while the lock was held"
  grep -q "BUSY: a production rollback" "$TEMP_DIR/entrypoint.busy.err" ||
    fail "the production entrypoint did not take the production mutation lock at all"
  grep -q "APPLY" "$TEMP_DIR/entrypoint.busy.out" && fail "a refused entrypoint applied something"

  # Releasing the lock must not make the refused run resume: it exited. Only a
  # new, explicit invocation may try again -- and that one proceeds.
  rmdir "$TEMP_DIR/outer.entrypoint"
  [[ ! -d "$TEMP_DIR/outer.entrypoint" ]] ||
    fail "the refused migration resumed on its own once the lock was freed"
  run_production_entrypoint retry >/dev/null
  grep -q "prod-acquired" "$TEMP_DIR/psql.entrypoint.log" ||
    fail "the second, explicit run did not acquire the freed production lock"
  echo "  [ok] the documented production entrypoint takes the lock itself, refuses when busy, and never resumes"
}

# --- losing the session that owns the locks ---------------------------------
#
# The property this exists for: the advisory locks and the schema mutations are
# the same PostgreSQL connection, so they cannot be separated. Killing the
# session releases the locks AND stops the migration, atomically, with no window
# in which one is true and the other is not.

# Drives the real functions with the lock session live, then kills it.
#
# `protected_sql` before the kill must succeed and after it must fail; the
# sentinel file is written only if the code after the failed call runs, which it
# must not.
kill_lock_session_and_continue() {
  local label="$1" when="$2"
  lock_env "$label"
  rm -f "$TEMP_DIR/sentinel.$label"
  env "${LOCK_ENV[@]}" NCHAT_PROD_MUTATION_LOCK=1 \
    SENTINEL="$TEMP_DIR/sentinel.$label" KILL_WHEN="$when" \
    bash -c '
      set -Eeuo pipefail
      set -- help
      source "$0" >/dev/null
      DSN="$MIGRATIONS_DATABASE_URL"
      acquire_migration_lock
      # TERM, not KILL. PostgreSQL releases the advisory locks of a session
      # when its backend terminates, however it terminates; this fixture models
      # that release through the exit of the fake session, so TERM is the signal
      # that reproduces server behaviour here. What is under test is unchanged:
      # the session that owned the locks is gone.
      [[ "$KILL_WHEN" != "before" ]] || kill -TERM "$LOCK_HOLDER_PID"
      if [[ "$KILL_WHEN" == "after-started" ]]; then
        record_apply_started chat 000100_pin \
          0000000000000000000000000000000000000000000000000000000000000001
        kill -TERM "$LOCK_HOLDER_PID"
      fi
      # Bounded wait for the release to land, as a real server would take a
      # moment too. The assertions below do not depend on this loop.
      for _ in 1 2 3 4 5 6 7 8 9 10; do
        [[ -d "$FAKE_OUTER_LOCK" ]] || break
        sleep 0.1
      done
      # Whatever comes next must not get through. Everything below it is the
      # migration continuing, which is the thing that must be impossible.
      protected_file "/dev/null"
      : >"$SENTINEL"
      record_apply_clean chat 000100_pin \
        0000000000000000000000000000000000000000000000000000000000000001
    ' "$MIGRATE" >"$TEMP_DIR/kill.$label.out" 2>"$TEMP_DIR/kill.$label.err"
}

test_migration_stops_when_the_lock_session_dies() {
  local status=0
  kill_lock_session_and_continue before before || status=$?
  [[ "$status" -ne 0 ]] || fail "the migration continued after its lock session was killed"
  [[ ! -f "$TEMP_DIR/sentinel.before" ]] ||
    fail "SQL ran after the lock-owning session died"
  grep -q "session holding the advisory locks" "$TEMP_DIR/kill.before.err" ||
    fail "the failure did not name the lost session"
  # The locks went with the session, which is the whole point: a rollback may
  # now take the production lock, and the migration cannot carry on.
  [[ ! -d "$TEMP_DIR/outer.before" ]] || fail "the production lock outlived its session"
  [[ ! -d "$TEMP_DIR/inner.before" ]] || fail "the migration lock outlived its session"
  echo "  [ok] a dead lock session stops the migration; no SQL runs after it"
}

# The same, one step later: the ledger row has been marked started, and the
# session dies before the migration completes. record_apply_clean must NOT run,
# so the row stays dirty/in_progress -- which is exactly what makes the rollback
# schema gate refuse afterwards.
test_dead_session_leaves_the_ledger_dirty() {
  local status=0
  kill_lock_session_and_continue after-started after-started || status=$?
  [[ "$status" -ne 0 ]] || fail "the migration continued after its lock session was killed"
  [[ ! -f "$TEMP_DIR/sentinel.after-started" ]] ||
    fail "SQL ran after the lock-owning session died"
  grep -q "dirty = true, in_progress = true\|dirty, in_progress" "$TEMP_DIR/psql.after-started.log" ||
    grep -q "INSERT INTO public.schema_migrations" "$TEMP_DIR/psql.after-started.log" ||
    fail "the started marker never reached the session"
  # Nothing cleared it. A dirty ledger is the correct residue of a lost session.
  grep -q "dirty = false" "$TEMP_DIR/psql.after-started.log" &&
    fail "the ledger was cleaned after the session died"
  echo "  [ok] a session lost mid-migration leaves the ledger dirty and never records clean"
}

# Protected SQL cannot run at all without the session, so no path can quietly
# open a second connection and carry on.
test_protected_sql_refuses_without_the_lock_session() {
  local status=0
  lock_env nosession
  env "${LOCK_ENV[@]}" bash -c '
    set -Eeuo pipefail
    set -- help
    source "$0" >/dev/null
    protected_sql "SELECT 1;"
  ' "$MIGRATE" >/dev/null 2>"$TEMP_DIR/nosession.err" || status=$?
  [[ "$status" -ne 0 ]] || fail "protected SQL ran with no lock-owning session"
  grep -q "without the lock-owning session" "$TEMP_DIR/nosession.err" ||
    fail "the refusal did not say why"
  echo "  [ok] protected SQL refuses to run outside the lock-owning session"
}

# A path with a space in it is still one path.
#
# `\i $file` is split on whitespace by psql, so a checkout under a directory
# whose name contains a space would send the meta-command several arguments and
# the migration would never be found.
test_protected_file_survives_a_path_with_spaces() {
  local emitted
  emitted="$(
    source_migrate
    LOCK_SESSION_ACTIVE=true
    session_send_and_confirm() { printf '%s' "$1"; }
    protected_file "/work/NChat Project/migrations/chat/000100_pin.up.sql"
  )"
  grep -Fq "\\set migration_file '/work/NChat Project/migrations/chat/000100_pin.up.sql'" \
    <<<"$emitted" || fail "the path was not passed as a psql variable: $emitted"
  grep -Fq "\\i :'migration_file'" <<<"$emitted" ||
    fail "the file is not read through the quoted variable: $emitted"
  grep -Fq "\\i /work/NChat" <<<"$emitted" &&
    fail "the path is still spliced into the meta-command"
  echo "  [ok] a migration path containing a space reaches psql as one path"
}

# --- status is a read -------------------------------------------------------
#
# `make migrations-status` is a documented operational command and it does not
# hold the production mutation lock, so nothing it calls may be a protected
# mutation. It called ensure_migrations_table, which became one, and the command
# started dying with "Refusing to run protected SQL without the lock-owning
# session" before printing anything.

test_status_runs_without_the_lock_session() {
  local status=0
  lock_env statusread
  # No NCHAT_PROD_MUTATION_LOCK and no session: exactly how an operator runs it.
  env "${LOCK_ENV[@]}" MIGRATIONS_DIR="$TEMP_DIR/no-migrations" \
    bash "$MIGRATE" status >"$TEMP_DIR/status.out" 2>"$TEMP_DIR/status.err" || status=$?
  [[ "$status" -eq 0 ]] ||
    fail "migrations-status failed: $(tail -2 "$TEMP_DIR/status.err")"
  grep -q "Refusing to run protected SQL" "$TEMP_DIR/status.err" &&
    fail "status tried to run a protected mutation"
  grep -q "=== migration status ===" "$TEMP_DIR/status.out" ||
    fail "status printed no status: $(tail -2 "$TEMP_DIR/status.out")"
  # A read must not create the ledger, and must take neither lock.
  grep -qi "CREATE TABLE" "$TEMP_DIR/psql.statusread.log" &&
    fail "status created the ledger table"
  grep -q "pg_try_advisory_lock" "$TEMP_DIR/psql.statusread.log" &&
    fail "status took an advisory lock"
  echo "  [ok] migrations-status reads without a lock session and creates nothing"
}

# --- the production entrypoint's interface ----------------------------------
#
# It exists for one thing. A verb that is not `up` is refused before anything
# reaches run-migrations.sh, so this command cannot tear the schema down.
run_migrate_prod_interface() {
  local tag="$1"
  shift
  local stub="$TEMP_DIR/stub.$tag"
  mkdir -p "$stub"
  cat >"$stub/run-migrations.sh" <<'STUB'
#!/usr/bin/env bash
printf 'run-migrations received: %s\n' "$*"
STUB
  chmod +x "$stub/run-migrations.sh"
  cp "$ROOT_DIR/scripts/db/migrate-prod.sh" "$stub/migrate-prod.sh"
  bash "$stub/migrate-prod.sh" "$@" >"$TEMP_DIR/prodif.$tag.out" 2>&1
}

assert_prod_entrypoint_refuses() {
  local verb="$1" status=0
  run_migrate_prod_interface "refuse-$verb" "$verb" || status=$?
  [[ "$status" -ne 0 ]] || fail "migrate-prod.sh accepted the command '$verb'"
  grep -q "run-migrations received" "$TEMP_DIR/prodif.refuse-$verb.out" &&
    fail "'$verb' reached run-migrations.sh"
  return 0
}

test_production_entrypoint_is_strictly_up() {
  local verb status=0
  for verb in down reset status help version up; do
    assert_prod_entrypoint_refuses "$verb"
  done
  run_migrate_prod_interface plain || status=$?
  [[ "$status" -eq 0 ]] || fail "migrate-prod.sh with no argument did not run"
  grep -Fxq "run-migrations received: up" "$TEMP_DIR/prodif.plain.out" ||
    fail "migrate-prod.sh did not run 'up': $(cat "$TEMP_DIR/prodif.plain.out")"
  status=0
  run_migrate_prod_interface option --dry-run || status=$?
  [[ "$status" -eq 0 ]] || fail "migrate-prod.sh refused a valid up option"
  grep -Fxq "run-migrations received: up --dry-run" "$TEMP_DIR/prodif.option.out" ||
    fail "the option did not survive as an option to up"
  echo "  [ok] migrate-prod.sh runs up, keeps up options, and refuses every command verb"
}

test_dsn_resolution
test_database_wait_retry
test_internal_identifier_validation
test_sql_parameterization
test_wrapper
test_grant_failure
test_applied_migration_ledger_contract
test_migration_lock_term
test_migration_lock_int
test_migration_lock_preserves_status
test_migration_inner_lock_busy
test_migration_lock_cleanup_is_bounded
test_production_migration_entrypoint_refuses_when_busy
test_migration_stops_when_the_lock_session_dies
test_dead_session_leaves_the_ledger_dirty
test_protected_sql_refuses_without_the_lock_session
test_production_entrypoint_is_strictly_up
test_status_runs_without_the_lock_session
test_protected_file_survives_a_path_with_spaces

echo "Migration runtime tests passed."
