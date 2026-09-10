#!/usr/bin/env bash
# Behaviour tests for the rollback critical section (CICD-08).
#
# These run scripts/deploy/nchat-prod/rollback-critical-section.sh itself, end
# to end, against the fake cluster -- not its helpers in isolation and not the
# workflow YAML. What it composes is the whole point: the lock, the fresh ledger
# read inside it, the schema gate over that reading, rollback.sh, and the
# liveness proofs either side of the switch. A suite that exercised each piece
# separately would say nothing about whether they are wired in that order.
#
# The lock is modelled with `mkdir`, which succeeds or fails atomically on an
# existing directory. That is `pg_try_advisory_lock`'s shape, so the exclusion
# below is real: two holders cannot coexist, and the second is refused rather
# than queued.
#
# No cluster, no database, no network.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SCRIPTS="$ROOT_DIR/scripts/deploy/nchat-prod"
CRITICAL="$SCRIPTS/rollback-critical-section.sh"
FAKE_BIN="$(mktemp -d "${TMPDIR:-/tmp}/nchat-cs-fakebin.XXXXXX")"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-cs-tests.XXXXXX")"
trap 'rm -rf "$FAKE_BIN" "$WORK"' EXIT

cp "$ROOT_DIR/scripts/ci/testdata/nchat-prod/fake-kubectl" "$FAKE_BIN/kubectl"
chmod +x "$FAKE_BIN/kubectl"
PATH="$FAKE_BIN:$PATH"
export PATH

SERVICES=(nchat-web nchat-admin-web auth-service chat-service file-service
  document-converter notification-service admin-service search-service media-service)
FAILURES=0
CASE=""
CASE_FAILURES=0

begin() { CASE="$1"; CASE_FAILURES="$FAILURES"; }
fail() { echo "  [FAIL] $CASE: $*" >&2; FAILURES=$((FAILURES + 1)); }
pass() { [[ "$FAILURES" -eq "$CASE_FAILURES" ]] || return 0; echo "  [OK]   $CASE"; }
assert_equals() {
  local what="$1" expected="$2" actual="$3"
  [[ "$expected" == "$actual" ]] || fail "$what: expected [$expected], got [$actual]"
}

RELEASE_A=a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0
RELEASE_ID_A=0000000000000000000000000000000000000000000000000000000000000001
EXPAND_SQL='ALTER TABLE chat.messages ADD COLUMN pinned boolean;'
CONTRACT_SQL='-- nchat:blue-green contract-phase the previous release no longer reads it
ALTER TABLE chat.messages DROP COLUMN legacy_body;'

component_for() {
  case "$1" in
    nchat-web) printf 'web' ;;
    nchat-admin-web) printf 'admin-web' ;;
    *) printf '%s' "${1%-service}" ;;
  esac
}

replicas_for() { [[ "$1" == "auth-service" ]] && printf '1' || printf '2'; }

# A namespace both slots are deployed and Ready in, whose postgres answers a
# held session, and whose ledger is empty unless a case says otherwise.
new_state() {
  local active="$1" state service slot count component
  state="$(mktemp -d "$WORK/state.XXXXXXXX")"
  mkdir -p "$state/services" "$state/ready" "$state/sha" "$state/image" \
    "$state/observed" "$state/component" "$state/secret-data/nchat-secrets"
  printf 'nchat-prod-deployer' >"$state/context"
  printf 'nchat-prod' >"$state/namespace"
  : >"$state/patch-log"
  printf 'CLEAN' >"$state/ledger-state"
  : >"$state/ledger-rows"
  printf 'postgres://nchat_app:pw@postgres:5432/nchat' \
    >"$state/secret-data/nchat-secrets/DATABASE_URL"
  for service in "${SERVICES[@]}"; do
    printf '%s' "$active" >"$state/services/$service"
    component="$(component_for "$service")"
    for slot in blue green; do
      count="$(replicas_for "$service")"
      printf '%s %s %s %s %s %s %s\n' 1 1 "$count" "$count" "$count" "$count" 0 \
        >"$state/ready/$service-$slot"
      printf '%s' "$RELEASE_A" >"$state/sha/$service-$slot"
      printf '%s' "$component" >"$state/component/$service-$slot"
      printf '%s:%s\n' "$RELEASE_A" "$RELEASE_ID_A" >"$state/observed/$component-$slot"
      printf 'ghcr.io/nicrepository/nchat/%s@sha256:%064d' "$service" 1 \
        >"$state/image/$service-$slot"
    done
  done
  printf '%s' "$state"
}

# A git repository whose HEAD is the release both slots carry, so the schema
# gate has a real tree to read. The critical section runs from inside it.
new_repo() {
  local repo="$WORK/repo.$1" contract="${2:-}"
  rm -rf "$repo"
  mkdir -p "$repo/migrations/chat"
  git -C "$repo" init --quiet
  git -C "$repo" config user.email nchat-tests@example.invalid
  git -C "$repo" config user.name "nchat tests"
  git -C "$repo" config commit.gpgsign false
  printf '%s\n' "$EXPAND_SQL" >"$repo/migrations/chat/000099_base.up.sql"
  git -C "$repo" add -A
  git -C "$repo" commit --quiet -m base
  if [[ -n "$contract" ]]; then
    printf '%s\n' "$CONTRACT_SQL" >"$repo/migrations/chat/000100_drop.up.sql"
    git -C "$repo" add -A
    git -C "$repo" commit --quiet -m contract
  fi
  printf '%s' "$repo"
}

# The release both slots report has to BE the repository's first commit, or the
# gate would refuse for reasons that have nothing to do with the case under
# test. Read back from the repo rather than carried out of the subshell that
# built it.
base_sha_of() { git -C "$1" rev-list --max-parents=0 HEAD; }

# Points every workload at the commit the fixture repository actually holds.
set_release_to_head() {
  local state="$1" sha="$2" service slot component
  for service in "${SERVICES[@]}"; do
    component="$(component_for "$service")"
    for slot in blue green; do
      printf '%s' "$sha" >"$state/sha/$service-$slot"
      printf '%s:%s\n' "$sha" "$RELEASE_ID_A" >"$state/observed/$component-$slot"
    done
  done
}

# "<domain> <filename> <checksum>", the shape PostgreSQL really stores.
ledger_row_for() {
  local repo="$1" domain="$2" base="$3"
  printf '%s %s %s\n' "$domain" "$base" \
    "$(sha256sum "$repo/migrations/$domain/$base.up.sql" | cut -d ' ' -f 1)"
}

run_critical_section() {
  local state="$1" repo="$2" target="$3"
  shift 3
  (
    cd "$repo"
    # `env` so extra "NAME=value" arguments are applied as environment rather
    # than looked up as a command: a word expanded from "$@" is not reparsed as
    # an assignment prefix.
    FAKE_STATE_DIR="$state" NCHAT_PROD_ASSUME_YES=1 \
      NCHAT_PROD_APPLIED_LEDGER="$WORK/ledger.txt" \
      env "$@" bash "$CRITICAL" --target "$target" "an incident"
  ) >"$WORK/out.txt" 2>"$WORK/err.txt"
}

expect_exit() {
  local expected="$1" actual="$2"
  [[ "$expected" == "$actual" ]] ||
    fail "exit $actual, expected $expected: $(tail -3 "$WORK/err.txt")"
}

slot_of() { cat "$1/services/$2" 2>/dev/null || printf 'unset'; }

assert_all_on() {
  local state="$1" expected="$2" service actual
  for service in "${SERVICES[@]}"; do
    actual="$(slot_of "$state" "$service")"
    [[ "$actual" == "$expected" ]] ||
      { fail "service/$service is '$actual', expected '$expected'"; return; }
  done
}

assert_lock_released() {
  [[ ! -d "$1/advisory-lock" ]] || fail "the lock was not released"
}

assert_no_switch() {
  [[ ! -s "$1/patch-log" ]] || fail "a Service selector was patched"
}

echo "=== rollback critical section ==="

# CASE 1 -- the ordinary rollback: lock free, ledger clean, schema compatible,
# target Ready and consistent, traffic moves, everything released.
begin "a compatible rollback takes the lock, proves the schema and switches"
repo="$(new_repo success)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
status=0; run_critical_section "$state" "$repo" blue || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
grep -q "COMPATIBLE" "$WORK/out.txt" || fail "the schema gate did not report a verdict"
grep -q "held continuously across the proof and the switch" "$WORK/out.txt" ||
  fail "did not report continuous exclusion"
assert_lock_released "$state"
pass

# CASE 2 -- the target is already active. rollback.sh reports a validated no-op
# and patches nothing.
begin "a rollback to the slot already serving traffic is a validated no-op"
repo="$(new_repo noop)"
state="$(new_state blue)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
status=0; run_critical_section "$state" "$repo" blue || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
assert_no_switch "$state"
grep -q "nothing to move" "$WORK/out.txt" || fail "did not report a no-op"
assert_lock_released "$state"
pass

# CASE 3 -- the gate refuses. The lock was taken and the ledger read, and the
# switch must not happen at all.
begin "an incompatible schema blocks before rollback.sh is reached"
repo="$(new_repo incompatible contract)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
{ ledger_row_for "$repo" chat 000099_base; ledger_row_for "$repo" chat 000100_drop; } \
  >"$state/ledger-rows"
status=0; run_critical_section "$state" "$repo" blue || status=$?
expect_exit 1 "$status"
grep -q "rollback blocked; incident/DB recovery required" "$WORK/err.txt" ||
  fail "did not name the incident path"
assert_all_on "$state" green
assert_no_switch "$state"
assert_lock_released "$state"
pass

# CASE 4 -- the switch stops part-way. The wrapper fails, the state is left as
# it is for an operator to see, and nothing reaches for the other slot.
begin "a partial switch fails and never falls back to the opposite slot"
repo="$(new_repo partial)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
printf 'chat-service\n' >"$state/patch-fails"
status=0; run_critical_section "$state" "$repo" blue || status=$?
expect_exit 1 "$status"
grep -q "Re-run 'rollback.sh --target blue" "$WORK/err.txt" ||
  fail "did not tell the operator to converge on the same target"
# The refused patch leaves that Service on the slot it had, so the namespace is
# genuinely mixed -- which is the state an operator has to see rather than one
# the run tidies away.
assert_equals "the Service whose patch was refused" "green" \
  "$(slot_of "$state" chat-service)"
[[ "$(slot_of "$state" nchat-web)" == "blue" ]] ||
  fail "no Service moved at all; this is not a partial switch"
grep -q "target green" "$WORK/out.txt" && fail "reached for the opposite slot"
assert_lock_released "$state"
pass

# CASE 7 -- another production mutation holds the lock. The refusal is
# immediate, the ledger is never read as authorisation, and rollback.sh is never
# reached.
begin "a busy mutation lock refuses at once and reads nothing as authorisation"
repo="$(new_repo busy)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
mkdir -p "$state/advisory-lock"
rm -f "$WORK/ledger.txt"
started="$(date +%s)"
status=0; run_critical_section "$state" "$repo" blue || status=$?
expect_exit 1 "$status"
grep -q "BUSY" "$WORK/err.txt" || fail "did not report BUSY"
grep -q "not queued and will not resume on its own" "$WORK/err.txt" ||
  fail "did not say the refusal is not a queue"
[[ "$(($(date +%s) - started))" -lt 15 ]] || fail "the refusal waited for the lock"
[[ ! -f "$WORK/ledger.txt" ]] || fail "the ledger was read before the lock was held"
assert_all_on "$state" green
assert_no_switch "$state"
[[ -d "$state/advisory-lock" ]] || fail "the refused caller released someone else's lock"
rmdir "$state/advisory-lock"
pass

# CASES 5 and 6 -- signals end the operation. The traffic must not move after
# the signal, and the exit code has to be the conventional 128+signal so the
# workflow reports a cancellation rather than a failure of its own.
signal_critical_section() {
  local state="$1" repo="$2" signal="$3"
  printf '1' >"$state/hold-switch"
  # `set -m` matters and is not decoration. A background job started by a
  # non-interactive shell without job control inherits SIGINT set to SIG_IGN,
  # and a `trap ... INT` cannot re-enable a signal that was ignored on entry --
  # so the INT case would be testing bash's job handling rather than the
  # handler. With job control the job gets its own process group and the
  # default disposition, which is what an operator's Ctrl-C really delivers.
  #
  # `exec` so that $! is the script itself rather than a wrapper shell.
  set -m
  FAKE_STATE_DIR="$state" NCHAT_PROD_ASSUME_YES=1 \
    NCHAT_PROD_APPLIED_LEDGER="$WORK/ledger.txt" \
    bash -c 'cd "$1"; exec bash "$2" --target blue "an incident"' \
    _ "$repo" "$CRITICAL" >"$WORK/out.txt" 2>"$WORK/err.txt" &
  local holder=$!
  set +m
  # Deterministic: the fake creates this the moment the lock is taken.
  for _ in $(seq 1 400); do [[ -d "$state/advisory-lock" ]] && break; sleep 0.05; done
  [[ -d "$state/advisory-lock" ]] || fail "the critical section never took the lock"
  rm -f "$state/hold-switch"
  kill "-$signal" "$holder" 2>/dev/null || true
  local status=0
  wait "$holder" || status=$?
  printf '%s' "$status"
}

begin "TERM during the critical section stops it with 143 and moves no traffic"
repo="$(new_repo term)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
status="$(signal_critical_section "$state" "$repo" TERM)"
assert_equals "the exit status" "143" "$status"
assert_all_on "$state" green
assert_no_switch "$state"
assert_lock_released "$state"
pass

begin "INT during the critical section stops it with 130 and moves no traffic"
repo="$(new_repo int)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
status="$(signal_critical_section "$state" "$repo" INT)"
assert_equals "the exit status" "130" "$status"
assert_all_on "$state" green
assert_no_switch "$state"
assert_lock_released "$state"
pass

echo "--- production mutation lock: the real migration path ---"
#
# Not two callers of the same helper. PROCESS B here is scripts/db/migrate.sh
# itself, running the way the production migration Job runs it
# (NCHAT_PROD_MUTATION_LOCK=1, which the k3s-prod overlay sets), against a psql
# that models pg_try_advisory_lock with the same `mkdir` the kubectl fake uses.
# Both paths therefore contend for one directory, exactly as they contend for
# one advisory lock in production.
#
# What is proved is the semantics that matters: a conflict is a REFUSAL. A
# migration that queued would wake when the rollback finished and apply a
# contract-phase migration onto the release the rollback had just restored.

MIGRATE="$ROOT_DIR/scripts/db/migrate.sh"
SHARED_LOCK="$WORK/shared-advisory-lock"

# The same advisory-lock fixture the migration suite uses.
#
# It matters that it is the same one. The copy that lived here answered the
# PRODUCTION verdicts to the MIGRATION lock query, so a second migration run
# waited for a token that never came, timed out, and the case still printed a
# pass because nothing asserted its status. Two locks, two directories, two
# vocabularies -- from one file.
write_fake_psql() {
  local bin="$1"
  mkdir -p "$bin"
  cp "$ROOT_DIR/scripts/ci/testdata/nchat-prod/fake-psql" "$bin/psql"
  chmod +x "$bin/psql"
}

# One production migration attempt, end to end through migrate.sh.
run_production_migration() {
  local label="$1"
  # Two statements, not `local label=... bin=...$label`: `local` expands every
  # word before it assigns any of them, so the second would read $label while it
  # is still unset and `set -u` would end the suite.
  local bin="$WORK/psqlbin.$label"
  write_fake_psql "$bin"
  rm -rf "$WORK/inner.$label"
  # FAKE_OUTER_LOCK is the shared production mutation lock the rollback also
  # contends for; FAKE_INNER_LOCK is this run's own migration-specific lock.
  # Two names because they are two locks.
  FAKE_PSQL_LOG="$WORK/psql.$label.log" \
    FAKE_OUTER_LOCK="$SHARED_LOCK" FAKE_INNER_LOCK="$WORK/inner.$label" \
    FAKE_HANG="$WORK/hang.$label" \
    PATH="$bin:$PATH" \
    NCHAT_PROD_MUTATION_LOCK=1 \
    MIGRATION_LOCK_REPLY_TIMEOUT=2 MIGRATION_SESSION_REPLY_TIMEOUT=2 \
    MIGRATIONS_DATABASE_URL='postgres://nchat_migrator:pw@postgres:5432/nchat' \
    MIGRATIONS_DIR="$WORK/no-migrations" \
    bash "$MIGRATE" up >"$WORK/mig.$label.out" 2>"$WORK/mig.$label.err"
}

mkdir -p "$WORK/no-migrations"

# CASE -- migration attempted while the rollback holds the lock.
begin "a production migration is refused while a rollback holds the mutation lock"
repo="$(new_repo mig-during-rollback)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
# The rollback and the migration contend for one directory.
rm -rf "$SHARED_LOCK"
printf '1' >"$state/hold-switch"
NCHAT_TEST_LOCK_DIR="$SHARED_LOCK" FAKE_STATE_DIR="$state" \
  NCHAT_PROD_ASSUME_YES=1 NCHAT_PROD_APPLIED_LEDGER="$WORK/ledger.txt" \
  bash -c 'cd "$1"; exec bash "$2" --target blue "an incident"' \
  _ "$repo" "$CRITICAL" >"$WORK/rb.out" 2>"$WORK/rb.err" &
rollback_pid=$!
for _ in $(seq 1 400); do [[ -d "$SHARED_LOCK" ]] && break; sleep 0.05; done
if [[ ! -d "$SHARED_LOCK" ]]; then
  fail "the rollback never took the shared lock: $(tail -3 "$WORK/rb.err")"
else
  started="$(date +%s)"
  status=0; run_production_migration busy || status=$?
  [[ "$status" -ne 0 ]] || fail "the production migration ran while a rollback held the lock"
  grep -q "BUSY" "$WORK/mig.busy.err" || fail "the migration did not report BUSY"
  grep -q "not queued and will not resume on its own" "$WORK/mig.busy.err" ||
    fail "the migration did not say it is not queued"
  grep -q "No migration has been applied" "$WORK/mig.busy.err" ||
    fail "the migration did not say nothing was applied"
  # It refused rather than waiting for the rollback that is still paused.
  [[ "$(($(date +%s) - started))" -lt 15 ]] || fail "the migration waited for the lock"
  grep -q "ONESHOT" "$WORK/psql.busy.log" &&
    grep -q '\-f ' "$WORK/psql.busy.log" && fail "the migration executed SQL from a file"
fi
rm -f "$state/hold-switch"
wait "$rollback_pid" 2>/dev/null || true
pass

# The refusal is final: process B exited, so nothing resumes when A finishes.
# Only an explicit second run proceeds, and it does because the lock is free.
begin "the refused migration does not resume; a new explicit run succeeds"
[[ ! -d "$SHARED_LOCK" ]] || fail "the rollback did not release the shared lock"
status=0; run_production_migration retry || status=$?
# The status IS the assertion. Without it this case passed while the run was
# timing out against a lock protocol the fixture answered wrongly.
expect_exit 0 "$status"
grep -q "BUSY" "$WORK/mig.retry.err" && fail "the freed lock still reported BUSY"
# Both locks were asked for, each by its own id. The log records the statements
# sent, not the answers, so acquisition is proved by what follows rather than by
# the verdict words -- a busy answer on either lock aborts the run before any
# migration is applied.
grep -q "pg_try_advisory_lock(:'prod_lock_id'" "$WORK/psql.retry.log" ||
  fail "the second run never asked for the production mutation lock"
grep -q "pg_try_advisory_lock(:'lock_id'" "$WORK/psql.retry.log" ||
  fail "the second run never asked for the migration-specific lock"
# The migration path itself, reached and completed.
grep -q "Applied .* migration" "$WORK/mig.retry.out" ||
  fail "the second run never reached the migration path: $(tail -2 "$WORK/mig.retry.out")"
# And it left nothing behind.
[[ ! -d "$SHARED_LOCK" ]] || fail "the second run kept the production mutation lock"
[[ ! -d "$WORK/inner.retry" ]] || fail "the second run kept the migration lock"
pass

# CASE -- the inverse: the migration holds the lock, the rollback is refused.
begin "a rollback is refused while a production migration holds the mutation lock"
repo="$(new_repo rollback-during-mig)"
state="$(new_state green)"
set_release_to_head "$state" "$(base_sha_of "$repo")"
ledger_row_for "$repo" chat 000099_base >"$state/ledger-rows"
rm -rf "$SHARED_LOCK"
mkdir -p "$SHARED_LOCK"   # the migration's session holds it
rm -f "$WORK/ledger.txt"
started="$(date +%s)"
status=0
run_critical_section "$state" "$repo" blue NCHAT_TEST_LOCK_DIR="$SHARED_LOCK" || status=$?
expect_exit 1 "$status"
grep -q "BUSY" "$WORK/err.txt" || fail "the rollback did not report BUSY"
[[ "$(($(date +%s) - started))" -lt 15 ]] || fail "the rollback waited for the migration"
[[ ! -f "$WORK/ledger.txt" ]] || fail "the rollback read the ledger without the lock"
assert_all_on "$state" green
assert_no_switch "$state"
rmdir "$SHARED_LOCK"
pass


echo
if [[ "$FAILURES" -gt 0 ]]; then
  echo "rollback critical section tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "rollback critical section tests passed."
