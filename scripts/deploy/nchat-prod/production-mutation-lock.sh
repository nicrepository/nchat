#!/usr/bin/env bash
# The exclusion that makes a schema proof still true when the traffic moves
# (CICD-08). Sourcing this file only defines functions.
#
# The problem it exists for is a race, not a bug in any one script. A rollback
# reads the migration ledger, proves the target's release still fits the schema,
# and then switches ten Service selectors. Between the proof and the switch a
# migration can complete -- from the deploy workflow's migration Job, or from an
# operator running `make migrations-up` -- and the traffic then lands on a
# release the schema no longer supports. Checking again afterwards is not a fix:
# by then production is already serving it.
#
# GitHub's `concurrency:` cannot close this. It serialises workflow runs and
# nothing else, and the runbook documents shell commands that migrate outside it
# entirely. The barrier is therefore the one thing both paths already share: the
# production database.
#
# CONFLICT IS A REFUSAL, NEVER A QUEUE.
#
# The acquisition is `pg_try_advisory_lock`, which answers immediately, and not
# `pg_advisory_lock`, which waits. That difference is the whole contract. A
# migration that queued behind a rollback would wake up when the rollback
# finished and apply a contract-phase migration onto the release that rollback
# had just restored -- the exact outcome the lock exists to prevent, arriving a
# few seconds later. So a busy lock ends the operation:
#
#   free   -> ACQUIRED, the caller proceeds
#   held   -> BUSY, the caller stops, having changed nothing
#   error  -> ERROR, the caller stops, having changed nothing
#
# BUSY is not an internal error, but it is a refusal: the operator sees what the
# other mutation did and decides again. Nothing is retried on their behalf.
#
# LOCK ORDER, in the one place it can be stated.
#
#   production mutation lock   (this file, id below)
#       └── migration internal lock   (scripts/db/migrate.sh, its own id)
#
# and never the reverse. The rollback path takes only the outer lock and never
# calls migrate.sh. The production migration path takes the outer lock first and
# then migrate.sh's own, in one session, so there is a single order and no
# inversion has a spelling.
#
# The lock is session-level: it lives in a psql session held open by a
# coprocess, and the server releases it if that session dies. Its liveness IS
# the lock's validity, which is why the critical section proves the session is
# still there both immediately before the switch and immediately after it.
#
# It runs no migration and writes nothing: it takes a lock, and asks pg_locks
# whether it still holds it.

# Distinct from migrate.sh's MIGRATION_LOCK_ID on purpose. That one serialises
# migrations against each other; this one serialises every production mutation
# against every other, and the two are nested rather than shared so the order
# above can be stated at all.
NCHAT_PROD_MUTATION_LOCK_ID="${NCHAT_PROD_MUTATION_LOCK_ID:-2026052202}"
# Bounded technical limits only. There is no "wait for the lock" timeout any
# more, because there is no waiting: these cover connecting, one statement's
# answer, and closing down.
NCHAT_PROD_LOCK_CONNECT_TIMEOUT="${NCHAT_PROD_LOCK_CONNECT_TIMEOUT:-10}"
NCHAT_PROD_LOCK_REPLY_TIMEOUT="${NCHAT_PROD_LOCK_REPLY_TIMEOUT:-30}"
# Attempts of 0.1s each before the session is killed rather than waited on.
NCHAT_PROD_LOCK_CLOSE_ATTEMPTS="${NCHAT_PROD_LOCK_CLOSE_ATTEMPTS:-50}"
NCHAT_PROD_PSQL_WORKLOAD="${NCHAT_PROD_PSQL_WORKLOAD:-statefulset/postgres}"

# bash names the coprocess's pid PROD_LOCK_PID for `coproc PROD_LOCK`, so the
# holder is kept under a name of our own rather than shadowing that one.
PROD_LOCK_HOLDER_PID=""
PROD_LOCK_HELD=false
# Set by the first signal handler to run, so a second signal cannot re-enter the
# teardown while it is half-way through.
PROD_LOCK_SIGNALLED=false

prod_lock_fail() {
  echo "production mutation lock: $*" >&2
  return 1
}

# Whether this backend still holds the lock, asked of the server rather than
# assumed. The id fits in 32 bits, so PostgreSQL records it as classid 0.
prod_lock_held_query() {
  printf "SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND classid = 0 AND objid = %s) THEN 'held' ELSE 'lost' END;" \
    "$NCHAT_PROD_MUTATION_LOCK_ID"
}

# Immediate, by construction. pg_try_advisory_lock returns rather than waits, so
# this statement cannot block on another mutation however long that one runs.
prod_lock_try_query() {
  printf "SELECT CASE WHEN pg_try_advisory_lock(%s) THEN 'acquired' ELSE 'busy' END;" \
    "$NCHAT_PROD_MUTATION_LOCK_ID"
}

prod_lock_send() {
  printf '%s\n' "$1" >&"${PROD_LOCK[1]}" 2>/dev/null ||
    prod_lock_fail "the lock session is gone; it cannot be spoken to"
}

# Reads until one of the expected tokens or the deadline, printing what it saw.
# psql emits an empty line for a void return, so anything unrecognised is
# skipped rather than treated as an answer.
prod_lock_read_verdict() {
  local timeout="$1" line
  while IFS= read -r -t "$timeout" line 0<&"${PROD_LOCK[0]}"; do
    case "$line" in
      acquired | busy | held | lost) printf '%s' "$line"; return 0 ;;
    esac
  done
  return 1
}

# Opens the session. The connection string arrives on standard input and never
# becomes an argument, for the same reason it does not in the ledger reader:
# this output is a public run log.
prod_lock_open_session() {
  coproc PROD_LOCK {
    kubectl exec -i "$NCHAT_PROD_PSQL_WORKLOAD" -n "$NCHAT_PROD_NAMESPACE" -- \
      sh -c 'read -r dsn; read -r connect_timeout; PGCONNECT_TIMEOUT="$connect_timeout" exec psql "$dsn" -X -q -A -t -v ON_ERROR_STOP=1' 2>/dev/null
  }
  PROD_LOCK_HOLDER_PID="$PROD_LOCK_PID"
  PROD_LOCK_SIGNALLED=false
  prod_lock_send "$1" &&
    prod_lock_send "$NCHAT_PROD_LOCK_CONNECT_TIMEOUT"
}

# ACQUIRED, BUSY or ERROR, and it takes no longer than one statement to say so.
acquire_production_mutation_lock() {
  local verdict
  prod_lock_open_session "$1" || return 1
  prod_lock_send "$(prod_lock_try_query)" || return 1
  verdict="$(prod_lock_read_verdict "$NCHAT_PROD_LOCK_REPLY_TIMEOUT")" || verdict=""
  case "$verdict" in
    acquired)
      PROD_LOCK_HELD=true
      return 0
      ;;
    busy)
      release_production_mutation_lock
      prod_lock_fail "BUSY: another production mutation holds the lock. Nothing has been moved. Look at what that operation did, then run this again if it is still the right thing to do -- it is not queued and will not resume on its own."
      return 1
      ;;
  esac
  release_production_mutation_lock
  prod_lock_fail "ERROR: the lock could not be taken and nothing has been moved"
}

# Proves the lock is still ours, from the server. Called immediately before the
# switch and immediately after it: together those two answers are what turns
# "we took a lock once" into "nothing else could have migrated in between".
assert_production_mutation_lock_held() {
  local when="$1" verdict
  [[ "$PROD_LOCK_HELD" == true ]] ||
    prod_lock_fail "the lock was never acquired ($when)" || return 1
  prod_lock_send "$(prod_lock_held_query)" || return 1
  verdict="$(prod_lock_read_verdict "$NCHAT_PROD_LOCK_REPLY_TIMEOUT")" || verdict=""
  [[ "$verdict" == held ]] ||
    prod_lock_fail "the lock is no longer held $when; exclusion with migrations cannot be proved"
}

# Ends the session in bounded time, whatever state it is in.
#
# `wait` alone is not enough and was the defect: a psql blocked in the server
# never reads the `\q`, and the wait then never returns. So the quit is asked
# for, the process is then polled for a bounded number of attempts, and if it is
# still there it is signalled and killed. `wait` runs last, on a process already
# known to be finishing, so it reaps rather than blocks.
#
# Closing the coprocess's write end would be a tidier way to deliver EOF, and it
# is deliberately not done: `exec {fd}>&-` that fails terminates a
# non-interactive shell outright, which would end the run somewhere no error
# message could explain. The bounded kill below needs no such favour.
prod_lock_close_session() {
  local attempt=0
  printf '\\q\n' >&"${PROD_LOCK[1]}" 2>/dev/null || true
  while kill -0 "$PROD_LOCK_HOLDER_PID" 2>/dev/null; do
    attempt=$((attempt + 1))
    if [[ "$attempt" -gt "$NCHAT_PROD_LOCK_CLOSE_ATTEMPTS" ]]; then
      kill -TERM "$PROD_LOCK_HOLDER_PID" 2>/dev/null || true
      kill -KILL "$PROD_LOCK_HOLDER_PID" 2>/dev/null || true
      break
    fi
    sleep 0.1
  done
  wait "$PROD_LOCK_HOLDER_PID" >/dev/null 2>&1 || true
}

# Idempotent: the second call finds nothing to do and says so by returning. That
# matters because the signal handlers exit, which runs the EXIT trap, which
# releases again -- and the unlock must happen exactly once.
release_production_mutation_lock() {
  [[ -n "$PROD_LOCK_HOLDER_PID" ]] || return 0
  if [[ "$PROD_LOCK_HELD" == true ]]; then
    printf '%s\n' "SELECT pg_advisory_unlock($NCHAT_PROD_MUTATION_LOCK_ID);" \
      >&"${PROD_LOCK[1]}" 2>/dev/null || true
    PROD_LOCK_HELD=false
  fi
  prod_lock_close_session
  PROD_LOCK_HOLDER_PID=""
}

# What a signal must do, and the reason this is not the EXIT handler.
#
# `trap cleanup INT TERM` was wrong in a way that is easy to miss: the handler
# ran, released the lock, and then RETURNED to the interrupted flow, which
# carried on switching production traffic with no lock held and reported
# success. A signal handler for an operation like this has to end the process.
#
# The reentrance guard is not decoration: a second Ctrl-C during teardown would
# otherwise re-enter it half-way through and unlock twice.
prod_lock_signal_exit() {
  local code="$1"
  [[ "$PROD_LOCK_SIGNALLED" != true ]] || return 0
  PROD_LOCK_SIGNALLED=true
  echo "production mutation lock: signalled; releasing and stopping" >&2
  release_production_mutation_lock
  exit "$code"
}

# Runs a command inside the lock, releasing on success, failure and signal.
#
# The EXIT trap is the idempotent cleanup and preserves the caller's status; the
# INT and TERM traps end the process with the conventional 128+signal codes, so
# nothing after the signal runs.
with_production_mutation_lock() {
  local dsn="$1" status=0
  shift
  acquire_production_mutation_lock "$dsn" || return 1
  trap 'release_production_mutation_lock' EXIT
  trap 'prod_lock_signal_exit 130' INT
  trap 'prod_lock_signal_exit 143' TERM
  set +e
  "$@"
  status=$?
  set -e
  release_production_mutation_lock
  trap - EXIT INT TERM
  return "$status"
}
