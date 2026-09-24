#!/usr/bin/env bash
# Behaviour tests for the release lifecycle of issue #933.
#
# What #933 adds to Blue/Green is a rule about time: after a cutover the
# demoted slot is the rollback, and it stays running until the next release is
# prepared. Everything here tests the consequences of that rule, against a fake
# kubectl, with no cluster and no network:
#
#   - the idle slot is no longer free by definition;
#   - a reservation whose retention window has not elapsed blocks the release;
#   - one that has elapsed is retired through drain-old.sh, and only under the
#     full set of preconditions;
#   - a cutover derives its candidate from the record and proves it against the
#     cluster, and refuses when the two disagree;
#   - a rollback recalculates the state rather than assuming the last promotion
#     still holds.
#
# The record itself is tested the same way: it is a claim, so every case that
# matters is one where the claim and the cluster disagree and the operation has
# to refuse.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SCRIPTS="$ROOT_DIR/scripts/deploy/nchat-prod"
FAKE_BIN="$(mktemp -d "${TMPDIR:-/tmp}/nchat-lifecycle-fakebin.XXXXXX")"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-lifecycle-tests.XXXXXX")"
trap 'rm -rf "$FAKE_BIN" "$WORK"' EXIT

cp "$ROOT_DIR/scripts/ci/testdata/nchat-prod/fake-kubectl" "$FAKE_BIN/kubectl"
# The probe back-off is real behaviour and is not what these cases test;
# waiting out its fifteen seconds for every failing Service would make the
# suite slow enough that nobody runs it.
printf '#!/usr/bin/env bash\nexit 0\n' >"$FAKE_BIN/sleep"
chmod +x "$FAKE_BIN/kubectl" "$FAKE_BIN/sleep"
PATH="$FAKE_BIN:$PATH"
export PATH

SERVICES=(nchat-web nchat-admin-web auth-service chat-service file-service
  document-converter notification-service admin-service search-service media-service)
RELEASE_A=a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0
RELEASE_B=b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1
ID_A=$(printf 'a%.0s' {1..64})
ID_B=$(printf 'b%.0s' {1..64})

FAILURES=0
CASE=""
CASE_FAILURES=0

fail() { echo "  [FAIL] $CASE: $*" >&2; FAILURES=$((FAILURES + 1)); }
begin() { CASE="$1"; CASE_FAILURES="$FAILURES"; }
pass() { [[ "$FAILURES" -eq "$CASE_FAILURES" ]] && echo "  [OK]   $CASE"; return 0; }

component_for() {
  case "$1" in
    nchat-web) printf 'web' ;;
    nchat-admin-web) printf 'admin-web' ;;
    auth-service) printf 'auth' ;;
    chat-service) printf 'chat' ;;
    file-service) printf 'file' ;;
    document-converter) printf 'document-converter' ;;
    notification-service) printf 'notification' ;;
    admin-service) printf 'admin' ;;
    search-service) printf 'search' ;;
    media-service) printf 'media' ;;
  esac
}

replicas_for() { [[ "$1" == "auth-service" ]] && printf '1' || printf '2'; }

set_slot_release() {
  local state="$1" slot="$2" sha="$3" id="$4" service component count
  for service in "${SERVICES[@]}"; do
    component="$(component_for "$service")"
    count="$(replicas_for "$service")"
    printf '1 1 %s %s %s %s 0\n' "$count" "$count" "$count" "$count" >"$state/ready/$service-$slot"
    printf '%s' "$sha" >"$state/sha/$service-$slot"
    printf '%s' "$component" >"$state/component/$service-$slot"
    printf '%s:%s\n' "$sha" "$id" >"$state/observed/$component-$slot"
    printf 'ghcr.io/nicrepository/nchat/%s@sha256:%064d' "$service" 1 >"$state/image/$service-$slot"
  done
}

# A namespace in the ordinary post-cutover shape: `active` serving everything,
# both slots deployed and Ready.
new_state() {
  local active="$1" state service
  state="$WORK/state.$RANDOM$RANDOM"
  mkdir -p "$state/services" "$state/ready" "$state/sha" "$state/image" \
    "$state/observed" "$state/component"
  printf 'nchat-prod-deployer' >"$state/context"
  printf 'nchat-prod' >"$state/namespace"
  : >"$state/patch-log"
  : >"$state/scale-log"
  : >"$state/apply-log"
  for service in "${SERVICES[@]}"; do
    printf '%s' "$active" >"$state/services/$service"
  done
  set_slot_release "$state" blue "$RELEASE_A" "$ID_A"
  set_slot_release "$state" green "$RELEASE_B" "$ID_B"
  printf '%s' "$state"
}

# Writes the lifecycle record directly, which is how a fixture describes "the
# last cutover happened at this instant" without waiting for one.
record() {
  local state="$1" key value
  mkdir -p "$state/release-state"
  printf 'nchat-prod-release-state/v1' >"$state/release-state/schema"
  shift
  for key in candidate_slot candidate_release candidate_ready_at prepare_run_id \
    active_slot cutover_at rollback_reserved_slot post_cutover_smoke; do
    : >"$state/release-state/$key"
  done
  for key in "$@"; do
    value="${key#*=}"
    printf '%s' "$value" >"$state/release-state/${key%%=*}"
  done
}

record_field() { cat "$1/release-state/$2" 2>/dev/null || printf ''; }

# A record holding exactly the keys named, and nothing else. `record` writes
# the whole contract; this one writes a malformed one on purpose, which is the
# only way to tell "key absent" from "key present and empty".
record_only() {
  local state="$1" pair
  shift
  mkdir -p "$state/release-state"
  rm -f "$state"/release-state/*
  for pair in "$@"; do
    printf '%s' "${pair#*=}" >"$state/release-state/${pair%%=*}"
  done
}

# The ConfigMap exists and its `data` is empty -- which renders exactly like a
# ConfigMap that is not there.
record_empty() {
  mkdir -p "$1/release-state"
  rm -f "$1"/release-state/*
}

# Nothing wrote the lifecycle record during this case.
assert_no_state_write() {
  local state="$1" what="$2"
  [[ ! -s "$state/release-state-write-log" ]] ||
    fail "$what: the lifecycle record was written"
  grep -q 'apply -f -' "$state/apply-log" 2>/dev/null &&
    fail "$what: the lifecycle record was written"
  return 0
}

assert_no_drain() {
  local state="$1" what="$2"
  [[ ! -s "$state/scale-log" ]] || fail "$what: a slot was drained"
}

ago() { date -u -d "@$(($(date -u +%s) - $1))" +%Y-%m-%dT%H:%M:%SZ; }

# Runs a script under a fixture and reports its exit status through `status`.
run_script() {
  local state="$1"
  shift
  status=0
  output="$(FAKE_STATE_DIR="$state" "$@" 2>&1)" || status=$?
}

assert_status() {
  local what="$1" expected="$2"
  [[ "$status" -eq "$expected" ]] ||
    fail "$what: expected exit $expected, got $status
$output"
}

assert_contains() {
  local what="$1" needle="$2"
  [[ "$output" == *"$needle"* ]] || fail "$what: output does not mention '$needle'
$output"
}

assert_absent() {
  local what="$1" needle="$2"
  [[ "$output" != *"$needle"* ]] || fail "$what: output unexpectedly mentions '$needle'"
}

echo "--- the lifecycle record is a claim, never an authorisation ---"

# One release-state.sh function, run in a fresh shell as the current context of
# the fixture would run it -- the deploy identity unless a case switches it.
state_run() {
  local state="$1"
  shift
  # shellcheck disable=SC2016 # expanded by the inner shell.
  run_script "$state" bash -Eeuo pipefail -c '
    source "$1/lib.sh"; source "$1/release-state.sh"; shift; "$@"' _ "$SCRIPTS" "$@"
}

# An administrative identity: its own context, allowed to create ConfigMaps.
# The deploy identity is never given that; the fake refuses a fixture that tries.
ADMIN_CONTEXT=nchat-prod-admin

as_context() { printf '%s' "$2" >"$1/context"; }

grant_admin() { printf '%s\n' "$ADMIN_CONTEXT" >"$1/admin-contexts"; }

# The administrative bootstrap, as an administrator, then back to the deployer
# for whatever the case does next.
bootstrap_state() {
  local state="$1"
  grant_admin "$state"
  as_context "$state" "$ADMIN_CONTEXT"
  run_script "$state" env NCHAT_PROD_CONTEXT="$ADMIN_CONTEXT" NCHAT_PROD_ASSUME_YES=1 \
    bash "$SCRIPTS/bootstrap-release-state.sh"
  as_context "$state" nchat-prod-deployer
}

begin "privileged bootstrap creates the complete empty contract"
STATE="$(new_state green)"
bootstrap_state "$STATE"
assert_status "fresh bootstrap" 0
EXPECTED="$(new_state green)"
record "$EXPECTED"
diff -r "$EXPECTED/release-state" "$STATE/release-state" || fail "initial contract differs"
pass

begin "bootstrap preserves every byte of an existing valid record"
STATE="$(new_state green)"
record "$STATE" candidate_slot=blue "candidate_release=$RELEASE_A:$ID_A" \
  candidate_ready_at=2026-09-24T10:00:00Z prepare_run_id=123 rollback_reserved_slot=green
cp -r "$STATE/release-state" "$WORK/preserved-record"
bootstrap_state "$STATE"
assert_status "repeat bootstrap" 0
diff -r "$WORK/preserved-record" "$STATE/release-state" || fail "bootstrap reset live state"
assert_no_state_write "$STATE" "repeat bootstrap"
pass

for defect in missing schema unexpected partial empty; do
  begin "bootstrap refuses and preserves invalid record: $defect"
  STATE="$(new_state green)"
  record "$STATE"
  case "$defect" in
    missing) rm "$STATE/release-state/prepare_run_id" ;;
    schema) printf 'unknown/v2' >"$STATE/release-state/schema" ;;
    unexpected) printf 'value' >"$STATE/release-state/unexpected" ;;
    partial) printf 'blue' >"$STATE/release-state/candidate_slot" ;;
    empty) record_empty "$STATE" ;;
  esac
  cp -r "$STATE/release-state" "$STATE/before"
  bootstrap_state "$STATE"
  assert_status "invalid bootstrap" 1
  diff -r "$STATE/before" "$STATE/release-state" || fail "invalid state was modified"
  assert_no_state_write "$STATE" "invalid bootstrap"
  pass
done

for failure in release-state-read-fails release-state-exists-fails; do
  begin "bootstrap fails closed on $failure"
  STATE="$(new_state green)"
  printf '1' >"$STATE/$failure"
  bootstrap_state "$STATE"
  assert_status "unreadable bootstrap" 1
  [[ ! -d "$STATE/release-state" ]] || fail "read error became absence"
  assert_no_state_write "$STATE" "unreadable bootstrap"
  pass
done

begin "first candidate transition after bootstrap needs no CREATE"
STATE="$(new_state green)"
bootstrap_state "$STATE"
assert_status "initial bootstrap" 0
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/bootstrap-before.txt"
assert_status "first candidate preparation" 0
assert_contains "first candidate preparation" "candidate=blue"
state_run "$STATE" record_candidate_ready blue "$RELEASE_A:$ID_A" 123
assert_status "candidate write without CREATE" 0
[[ "$(record_field "$STATE" candidate_slot)" == blue ]] || fail "candidate slot missing"
[[ "$(record_field "$STATE" candidate_release)" == "$RELEASE_A:$ID_A" ]] || fail "candidate release missing"
[[ -n "$(record_field "$STATE" candidate_ready_at)" ]] || fail "candidate timestamp missing"
[[ "$(record_field "$STATE" prepare_run_id)" == 123 ]] || fail "candidate run missing"
pass

begin "Forbidden read is not absence, even on a populated record"
STATE="$(new_state green)"
record "$STATE" active_slot=green
cp -r "$STATE/release-state" "$STATE/before"
printf 'forbidden' >"$STATE/release-state-read-fails"
bootstrap_state "$STATE"
assert_status "Forbidden bootstrap" 1
diff -r "$STATE/before" "$STATE/release-state" || fail "Forbidden reset data"
assert_no_state_write "$STATE" "Forbidden bootstrap"
pass

begin "full-record patch replaces all data in a single write"
STATE="$(new_state green)"
record "$STATE"
printf 'stale' >"$STATE/release-state/obsolete"
state_run "$STATE" record_candidate_ready blue "$RELEASE_A:$ID_A" 123
assert_status "atomic full replacement" 0
[[ ! -e "$STATE/release-state/obsolete" ]] || fail "writer merged instead of replacing data"
[[ "$(wc -l <"$STATE/release-state-write-log")" == 1 ]] || fail "more than one write"
pass

begin "Forbidden patch leaves the entire record unchanged"
STATE="$(new_state green)"
record "$STATE" active_slot=green
cp -r "$STATE/release-state" "$STATE/before"
printf '1' >"$STATE/release-state-write-fails"
state_run "$STATE" record_candidate_ready blue "$RELEASE_A:$ID_A" 123
assert_status "Forbidden write" 1
diff -r "$STATE/before" "$STATE/release-state" || fail "partial transition persisted"
assert_no_state_write "$STATE" "Forbidden write"
pass

begin "the administrative bootstrap refuses the deploy context before anything"
STATE="$(new_state green)"
grant_admin "$STATE"
run_script "$STATE" env NCHAT_PROD_CONTEXT=nchat-prod-deployer NCHAT_PROD_ASSUME_YES=1 \
  bash "$SCRIPTS/bootstrap-release-state.sh"
assert_status "deploy-context bootstrap" 1
assert_contains "deploy-context bootstrap" "cannot run as nchat-prod-deployer"
[[ ! -e "$STATE/configmap-create-log" ]] || fail "attempted a create as the deploy identity"
[[ ! -d "$STATE/release-state" ]] || fail "the deploy context created the record"
pass

# A context that is not the deployer by name, but cannot create ConfigMaps
# either. The name proves nothing; the API server's answer does. No
# NCHAT_PROD_ASSUME_YES and no stdin: reaching the confirmation would itself
# fail, with a different message, so the refusal below can only come first.
begin "the administrative bootstrap refuses an identity that cannot create, before confirming"
STATE="$(new_state green)"
grant_admin "$STATE"
as_context "$STATE" ops-readonly
run_script "$STATE" env NCHAT_PROD_CONTEXT=ops-readonly \
  bash "$SCRIPTS/bootstrap-release-state.sh" </dev/null
assert_status "read-only bootstrap" 1
assert_contains "read-only bootstrap" "requires an administrative identity"
assert_absent "read-only bootstrap" "aborted by operator"
[[ ! -e "$STATE/configmap-create-log" ]] || fail "attempted a create without the permission"
[[ ! -d "$STATE/release-state" ]] || fail "an unprivileged identity created the record"
pass

begin "the fake answers can-i by identity, never for the deploy context"
STATE="$(new_state green)"
grant_admin "$STATE"
for context in nchat-prod-deployer "$ADMIN_CONTEXT" ops-readonly; do
  as_context "$STATE" "$context"
  run_script "$STATE" kubectl auth can-i create configmaps -n nchat-prod
  case "$context" in
    "$ADMIN_CONTEXT") [[ "$status" -eq 0 && "$output" == yes ]] || fail "$context: expected yes, got '$output'" ;;
    *) [[ "$status" -ne 0 && "$output" == no ]] || fail "$context: expected no, got '$output'" ;;
  esac
done
printf 'nchat-prod-deployer\n' >>"$STATE/admin-contexts"
as_context "$STATE" nchat-prod-deployer
run_script "$STATE" kubectl auth can-i create configmaps -n nchat-prod
[[ "$status" -eq 65 ]] || fail "a fixture granting create to the deployer was not refused"
pass

begin "candidate write cannot create even with a privileged credential"
STATE="$(new_state green)"
grant_admin "$STATE"
as_context "$STATE" "$ADMIN_CONTEXT"
state_run "$STATE" record_candidate_ready blue "$RELEASE_A:$ID_A" 123
assert_status "missing bootstrap" 1
assert_contains "missing bootstrap" "bootstrap-release-state.sh"
[[ ! -d "$STATE/release-state" ]] || fail "writer implicitly created the ConfigMap"
[[ ! -e "$STATE/configmap-create-log" ]] || fail "the writer attempted a create"
assert_no_state_write "$STATE" "missing bootstrap"
pass

# The writer replaces the whole record, so a key left out would be deleted from
# it and a repeated one is ambiguous. Both are refused before the API is asked.
begin "a write that does not carry every key exactly once is refused before any request"
for pairs in "candidate_slot=blue" \
  "candidate_slot=blue candidate_slot=green candidate_release= candidate_ready_at= prepare_run_id= active_slot= cutover_at= rollback_reserved_slot=" \
  "candidate_slot=blue candidate_release= candidate_ready_at= prepare_run_id= active_slot= cutover_at= rollback_reserved_slot= post_cutover_smoke= promote_now=yes"; do
  STATE="$(new_state green)"
  record "$STATE" active_slot=green
  cp -r "$STATE/release-state" "$STATE/before"
  # shellcheck disable=SC2086 # the pairs are a deliberate word list.
  state_run "$STATE" release_state_write $pairs
  assert_status "incomplete write" 1
  diff -r "$STATE/before" "$STATE/release-state" >/dev/null || fail "an incomplete write changed the record"
  assert_no_state_write "$STATE" "incomplete write"
done
pass

begin "the first cutover after bootstrap is recorded without CREATE"
STATE="$(new_state green)"
bootstrap_state "$STATE"
state_run "$STATE" record_candidate_ready blue "$RELEASE_A:$ID_A" 123
assert_status "first candidate write" 0
state_run "$STATE" record_cutover blue green
assert_status "first cutover write" 0
[[ "$(record_field "$STATE" active_slot)" == blue ]] || fail "active slot not recorded"
[[ "$(record_field "$STATE" rollback_reserved_slot)" == green ]] || fail "reservation not recorded"
[[ -z "$(record_field "$STATE" candidate_slot)" ]] || fail "candidate not consumed"
[[ "$(grep -c '^patch ' "$STATE/release-state-write-log")" == 2 ]] || fail "expected two patches"
[[ "$(grep -c '^create ' "$STATE/release-state-write-log")" == 1 ]] || fail "expected only the bootstrap create"
pass

begin "an absent record blocks a cutover instead of allowing one"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "cutover with no record" 1
assert_contains "cutover with no record" "no prepared candidate is recorded"
pass

begin "a record naming a candidate the cluster is not carrying blocks the cutover"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_B:$ID_B" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7" "active_slot=green"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "candidate release mismatch" 1
assert_contains "candidate release mismatch" "carries"
pass

begin "a record with a half-written candidate blocks the cutover"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=" "candidate_ready_at=$(ago 60)" \
  "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "incomplete record" 1
pass

begin "a record with no preparation run id blocks the cutover"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id="
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "no run id" 1
assert_contains "no run id" "run id"
pass

begin "a stale candidate blocks the cutover"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 200000)" "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "stale candidate" 1
assert_contains "stale candidate" "too old to promote"
pass

begin "a candidate dated in the future blocks the cutover"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(date -u -d "@$(($(date -u +%s) + 4000))" +%Y-%m-%dT%H:%M:%SZ)" \
  "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "future candidate" 1
pass

begin "a candidate not Ready blocks the cutover before any patch"
STATE="$(new_state green)"
printf '1 1 2 2 0 0 2\n' >"$STATE/ready/chat-service-blue"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "candidate not Ready" 1
assert_contains "candidate not Ready" "not fully Ready"
[[ ! -s "$STATE/patch-log" ]] || fail "candidate not Ready: a Service was patched"
pass

begin "a candidate carrying two releases blocks the cutover"
STATE="$(new_state green)"
printf '%s:%s\n' "$RELEASE_B" "$ID_B" >"$STATE/observed/chat-blue"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "mixed candidate release" 1
pass

begin "a valid record yields the candidate, the release and the rollback target"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=99"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "valid candidate" 0
assert_contains "valid candidate" "candidate=blue"
assert_contains "valid candidate" "release_sha=$RELEASE_A"
assert_contains "valid candidate" "release_id=$ID_A"
assert_contains "valid candidate" "prepare_run_id=99"
assert_contains "valid candidate" "rollback_target=green"
[[ ! -s "$STATE/patch-log" ]] || fail "valid candidate: the preflight patched a Service"
pass

echo
echo "--- the rollback reservation protects the idle slot ---"

begin "a reservation inside the retention window blocks the next release"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 300)" "rollback_reserved_slot=blue"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "reservation still standing" 1
assert_contains "reservation still standing" "reserved for rollback"
[[ ! -s "$STATE/scale-log" ]] || fail "reservation still standing: the slot was drained anyway"
pass

begin "a reservation whose window has elapsed is retired and the slot reused"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_B:$ID_B"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "expired reservation" 0
assert_contains "expired reservation" "candidate=blue"
assert_contains "expired reservation" "scaled to zero"
[[ "$(grep -c . "$STATE/scale-log")" -eq 10 ]] ||
  fail "expired reservation: expected ten workloads scaled, got $(grep -c . "$STATE/scale-log")"
grep -q 'deployment/chat-service-green' "$STATE/scale-log" &&
  fail "expired reservation: the active slot was scaled"
pass

begin "an undated reservation is never retired"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=" "rollback_reserved_slot=blue"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "undated reservation" 1
assert_contains "undated reservation" "cannot be dated"
[[ ! -s "$STATE/scale-log" ]] || fail "undated reservation: the slot was drained anyway"
pass

begin "an unreadable retention window refuses rather than defaulting"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_B:$ID_B"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=soon \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "malformed retention" 1
assert_contains "malformed retention" "invalid NCHAT_PROD_ROLLBACK_RETENTION_SECONDS"
pass

begin "an unhealthy active slot blocks the retirement"
STATE="$(new_state green)"
printf '1 1 2 2 0 0 2\n' >"$STATE/ready/chat-service-green"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_B:$ID_B"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "unhealthy active" 1
assert_contains "unhealthy active" "not fully Ready"
[[ ! -s "$STATE/scale-log" ]] || fail "unhealthy active: the rollback slot was drained anyway"
pass

begin "an active slot carrying two releases blocks the retirement"
STATE="$(new_state green)"
printf '%s:%s\n' "$RELEASE_A" "$ID_A" >"$STATE/observed/chat-green"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_B:$ID_B"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "mixed active release" 1
[[ ! -s "$STATE/scale-log" ]] || fail "mixed active release: the rollback slot was drained anyway"
pass

begin "a stable Service still on the reserved slot blocks the retirement"
STATE="$(new_state green)"
printf 'blue' >"$STATE/services/media-service"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_B:$ID_B"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "mixed selectors" 1
[[ ! -s "$STATE/scale-log" ]] || fail "mixed selectors: the slot was drained during a mixed state"
pass

begin "an unreserved idle slot is reused without any drain"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot="
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "free slot" 0
assert_contains "free slot" "candidate=blue"
assert_contains "free slot" "carries no rollback reservation"
[[ ! -s "$STATE/scale-log" ]] || fail "free slot: an unreserved slot was drained"
pass

begin "a record whose active slot the selectors contradict blocks before anything else"
# The record claims green is active; the selectors say blue. The cluster wins,
# and it wins by refusing rather than by being quietly preferred: a record this
# wrong was written by something that is not this pipeline, or by something
# that died half-way, and neither is a state to reason forward from.
STATE="$(new_state blue)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 60)" "rollback_reserved_slot=green"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "stale active claim" 1
assert_contains "stale active claim" "claims slot 'green' is active"
[[ ! -s "$STATE/scale-log" ]] || fail "stale active claim: something was drained"
pass

begin "a mixed namespace blocks preparation outright"
STATE="$(new_state green)"
printf 'blue' >"$STATE/services/chat-service"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "mixed namespace" 1
assert_contains "mixed namespace" "mixed state"
pass

begin "the selector snapshot is written for the invariant that follows"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "rollback_reserved_slot="
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/snapshot.txt"
assert_status "snapshot" 0
[[ "$(grep -c . "$WORK/snapshot.txt")" -eq 10 ]] ||
  fail "snapshot: expected ten stable Services, got $(grep -c . "$WORK/snapshot.txt")"
pass

echo
echo "--- the record's transitions ---"

begin "a cutover reserves the slot it demoted and consumes the candidate"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7" "active_slot=green"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_cutover blue green' _ "$SCRIPTS"
assert_status "record_cutover" 0
[[ "$(record_field "$STATE" active_slot)" == blue ]] ||
  fail "record_cutover: active_slot is $(record_field "$STATE" active_slot)"
[[ "$(record_field "$STATE" rollback_reserved_slot)" == green ]] ||
  fail "record_cutover: the demoted slot was not reserved"
[[ -z "$(record_field "$STATE" candidate_slot)" ]] ||
  fail "record_cutover: the candidate was not consumed"
[[ -n "$(record_field "$STATE" cutover_at)" ]] ||
  fail "record_cutover: the cutover was not dated"
pass

begin "a rollback creates no reservation on the slot it left"
STATE="$(new_state blue)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 60)" "rollback_reserved_slot=blue"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_rollback blue' _ "$SCRIPTS"
assert_status "record_rollback" 0
[[ "$(record_field "$STATE" active_slot)" == blue ]] ||
  fail "record_rollback: active_slot is $(record_field "$STATE" active_slot)"
[[ -z "$(record_field "$STATE" rollback_reserved_slot)" ]] ||
  fail "record_rollback: the slot under investigation was offered as a rollback target"
pass

begin "preparation clears a reservation on the slot it has just overwritten"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_candidate_ready blue "$2:$3" 42' _ "$SCRIPTS" "$RELEASE_A" "$ID_A"
assert_status "record_candidate_ready" 0
[[ -z "$(record_field "$STATE" rollback_reserved_slot)" ]] ||
  fail "record_candidate_ready: a reservation survived the slot being redeployed"
[[ "$(record_field "$STATE" active_slot)" == green ]] ||
  fail "record_candidate_ready: preparation changed the active slot"
[[ "$(record_field "$STATE" candidate_release)" == "$RELEASE_A:$ID_A" ]] ||
  fail "record_candidate_ready: the release was not recorded"
pass

begin "preparation carries forward a reservation on the other slot"
STATE="$(new_state blue)"
record "$STATE" "active_slot=blue" "cutover_at=$(ago 600)" "rollback_reserved_slot=green"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_candidate_ready blue "$2:$3" 42' _ "$SCRIPTS" "$RELEASE_A" "$ID_A"
assert_status "carry forward" 0
[[ "$(record_field "$STATE" rollback_reserved_slot)" == green ]] ||
  fail "carry forward: an unrelated reservation was dropped"
pass

begin "a key outside the record's schema cannot be written"
STATE="$(new_state green)"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  release_state_write "promote_now=yes"' _ "$SCRIPTS"
assert_status "unknown key" 1
assert_contains "unknown key" "unknown release state key"
pass

echo
echo "--- the rollback preflight ---"

begin "a rollback target that is Ready and consistent reports both releases"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/rollback-preflight.sh" --target blue "$WORK/before.txt"
assert_status "rollback preflight" 0
assert_contains "rollback preflight" "target=blue"
assert_contains "rollback preflight" "target_release_sha=$RELEASE_A"
assert_contains "rollback preflight" "from_slot=green"
assert_contains "rollback preflight" "from_release_sha=$RELEASE_B"
pass

begin "a rollback target that is not Ready is refused before any patch"
STATE="$(new_state green)"
printf '1 1 2 2 0 0 2\n' >"$STATE/ready/auth-service-blue"
run_script "$STATE" bash "$SCRIPTS/rollback-preflight.sh" --target blue "$WORK/before.txt"
assert_status "rollback target not Ready" 1
assert_contains "rollback target not Ready" "not Ready"
[[ ! -s "$STATE/patch-log" ]] || fail "rollback target not Ready: a Service was patched"
pass

begin "a rollback target carrying two releases is refused"
STATE="$(new_state green)"
printf '%s:%s\n' "$RELEASE_B" "$ID_B" >"$STATE/observed/file-blue"
run_script "$STATE" bash "$SCRIPTS/rollback-preflight.sh" --target blue "$WORK/before.txt"
assert_status "rollback target mixed" 1
pass

begin "a rollback whose current release cannot be identified is refused"
STATE="$(new_state green)"
printf '%s:%s\n' "$RELEASE_A" "$ID_A" >"$STATE/observed/search-green"
run_script "$STATE" bash "$SCRIPTS/rollback-preflight.sh" --target blue "$WORK/before.txt"
assert_status "unidentifiable current release" 1
assert_contains "unidentifiable current release" "incident procedure"
pass

begin "a target that is neither blue nor green never reaches a script"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/rollback-preflight.sh" --target purple "$WORK/before.txt"
assert_status "invalid target" 1
assert_contains "invalid target" "target slot is required"
pass

begin "a partially cut-over namespace can still be converged on an explicit target"
STATE="$(new_state green)"
printf 'blue' >"$STATE/services/nchat-web"
printf 'blue' >"$STATE/services/auth-service"
run_script "$STATE" bash "$SCRIPTS/rollback-preflight.sh" --target blue "$WORK/before.txt"
assert_status "mixed but promotable" 0
assert_contains "mixed but promotable" "target=blue"
pass

begin "a Service selecting an unknown slot blocks the rollback"
STATE="$(new_state green)"
printf 'purple' >"$STATE/services/nchat-web"
run_script "$STATE" bash "$SCRIPTS/rollback-preflight.sh" --target blue "$WORK/before.txt"
assert_status "unknown selector" 1
assert_contains "unknown selector" "which is neither"
pass

echo
echo "--- the scale-to-zero proof ---"

begin "a slot the drain did not reach is reported, not assumed retired"
STATE="$(new_state green)"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"; source "$1/lifecycle.sh"
  require_slot_scaled_to_zero blue' _ "$SCRIPTS"
assert_status "not scaled" 1
assert_contains "not scaled" "still wants"
pass

begin "a slot every workload of which is at zero is reusable"
STATE="$(new_state green)"
for service in "${SERVICES[@]}"; do printf '1 1 0 0 0 0 0\n' >"$STATE/ready/$service-blue"; done
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"; source "$1/lifecycle.sh"
  require_slot_scaled_to_zero blue' _ "$SCRIPTS"
assert_status "scaled to zero" 0
assert_contains "scaled to zero" "Deployments remain"
pass

echo
echo "--- the post-traffic smoke profile ---"

begin "a converged, Ready, consistent slot passes the post-cutover smoke"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target green --after cutover
assert_status "post-cutover pass" 0
assert_contains "post-cutover pass" "Post-cutover smoke        : PASS"
assert_contains "post-cutover pass" "every stable Service selects green"
pass

begin "the same slot passes the post-rollback smoke, reported as one"
STATE="$(new_state blue)"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target blue --after rollback
assert_status "post-rollback pass" 0
assert_contains "post-rollback pass" "Post-rollback smoke        : PASS"
pass

begin "the post-traffic smoke requires the target to be serving, unlike the candidate smoke"
# The inversion that makes this a separate command: smoke.sh refuses a slot
# that carries traffic, and this one refuses a slot that does not.
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target blue --after cutover
assert_status "not serving" 1
assert_contains "not serving" "not all on blue"
pass

begin "one Service left behind fails the post-cutover smoke"
STATE="$(new_state green)"
printf 'blue' >"$STATE/services/media-service"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target green --after cutover
assert_status "partial convergence" 1
assert_contains "partial convergence" "media-service=blue"
pass

begin "a target that is not Ready fails the post-cutover smoke"
STATE="$(new_state green)"
printf '1 1 2 2 0 0 2\n' >"$STATE/ready/chat-service-green"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target green --after cutover
assert_status "not Ready" 1
assert_contains "not Ready" "not all replicas Ready"
pass

begin "a target carrying two releases fails the post-cutover smoke"
STATE="$(new_state green)"
printf '%s:%s\n' "$RELEASE_A" "$ID_A" >"$STATE/observed/file-green"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target green --after cutover
assert_status "mixed release" 1
assert_contains "mixed release" "serving traffic while its release state is"
pass

begin "a stable Service that does not answer fails the post-cutover smoke"
STATE="$(new_state green)"
printf '1' >"$STATE/probes-fail"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target green --after cutover
assert_status "probes" 1
assert_contains "probes" "/healthz"
pass

begin "a failing post-traffic smoke says the other slot is still the way back"
STATE="$(new_state green)"
printf '1' >"$STATE/probes-fail"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target green --after cutover
assert_status "guidance" 1
assert_contains "guidance" "Nothing here rolls back"
pass

begin "an unrecognised operation is refused rather than defaulted"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target green --after drain
assert_status "bad operation" 1
assert_contains "bad operation" "must be cutover or rollback"
pass

begin "a target that is not a slot never reaches the cluster"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/stable-smoke.sh" --target purple --after cutover
assert_status "bad target" 1
assert_contains "bad target" "target slot is required"
pass


echo
echo "--- the post-traffic smoke gates the retirement ---"

begin "a release whose post-cutover smoke was never recorded keeps its rollback"
# The HIGH finding of the code quality review: record_cutover writes the
# reservation before the smoke, so a failed smoke leaves the record with no
# evidence. Retention alone must not be enough to retire the way back from a
# release nobody proved.
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "no smoke recorded" 1
assert_contains "no smoke recorded" "no post-traffic smoke is recorded"
[[ ! -s "$STATE/scale-log" ]] || fail "no smoke recorded: the rollback slot was drained anyway"
pass

begin "a smoke recorded for a different release keeps the rollback"
# The active slot was redeployed after it was smoked: the evidence names a
# release, so it stops matching.
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_A:$ID_A"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "smoke for another release" 1
assert_contains "smoke for another release" "is serving"
[[ ! -s "$STATE/scale-log" ]] || fail "smoke for another release: the slot was drained anyway"
pass

begin "the refusal names the command that unblocks it"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "guidance" 1
assert_contains "guidance" "record-traffic-smoke.sh --target green --after cutover"
pass

begin "recording the smoke unblocks the retirement"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_traffic_smoke_passed "$2:$3"' _ "$SCRIPTS" "$RELEASE_B" "$ID_B"
assert_status "record the smoke" 0
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "retirement after recording" 0
assert_contains "retirement after recording" "candidate=blue"
assert_contains "retirement after recording" "scaled to zero"
pass

begin "recording the smoke does not disturb the reservation it is evidence for"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 600)" "rollback_reserved_slot=blue"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_traffic_smoke_passed "$2:$3"' _ "$SCRIPTS" "$RELEASE_B" "$ID_B"
assert_status "smoke record preserves state" 0
[[ "$(record_field "$STATE" rollback_reserved_slot)" == blue ]] ||
  fail "smoke record preserves state: the reservation was dropped"
[[ "$(record_field "$STATE" active_slot)" == green ]] ||
  fail "smoke record preserves state: the active slot changed"
pass

begin "a cutover clears the previous release's smoke evidence"
STATE="$(new_state green)"
record "$STATE" "active_slot=blue" "cutover_at=$(ago 7200)" "rollback_reserved_slot=" \
  "post_cutover_smoke=$RELEASE_A:$ID_A"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_cutover green blue' _ "$SCRIPTS"
assert_status "cutover clears smoke" 0
[[ -z "$(record_field "$STATE" post_cutover_smoke)" ]] ||
  fail "cutover clears smoke: the new release inherited the old one's evidence"
pass

echo
echo "--- the record must not contradict the cluster or itself ---"

begin "a record claiming the wrong active slot blocks the release"
STATE="$(new_state blue)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 7200)" "rollback_reserved_slot="
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "active slot contradiction" 1
assert_contains "active slot contradiction" "claims slot 'green' is active"
[[ ! -s "$STATE/scale-log" ]] || fail "active slot contradiction: something was drained"
pass

begin "a reservation on the serving slot blocks the release"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 7200)" "rollback_reserved_slot=green"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "reservation on active" 1
assert_contains "reservation on active" "can only be on the idle slot"
pass

begin "a half-written candidate blocks the release"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 7200)" "candidate_slot=blue" \
  "candidate_release=$RELEASE_A:$ID_A"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "half-written candidate" 1
assert_contains "half-written candidate" "half-written candidate"
pass

begin "a record of an unknown schema blocks the release"
STATE="$(new_state green)"
record "$STATE" "active_slot=green"
printf 'nchat-prod-release-state/v99' >"$STATE/release-state/schema"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "unknown schema" 1
assert_contains "unknown schema" "schema"
pass

begin "a complete, agreeing record is accepted"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 7200)" "rollback_reserved_slot=" \
  "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "agreeing record" 0
assert_contains "agreeing record" "candidate=blue"
pass

begin "no record at all is a bootstrap, not a contradiction"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "absent record" 0
assert_contains "absent record" "candidate=blue"
pass

echo
echo "--- candidate staleness, at its boundaries ---"

# The window is compared with <=, so exactly max_age passes and max_age + 1
# does not. Both are asserted: an off-by-one here is a promotion gate that is
# one second more or less permissive than the contract says.
# Against the pure comparison, not through the script: an age of exactly
# `window` cannot be asserted against a live clock, because the second it takes
# to build the fixture and start a script moves the age. The rule is inclusive,
# and that is the off-by-one worth pinning.
window_case() {
  local label="$1" age="$2" window="$3" expected="$4"
  begin "$label"
  STATE="$(new_state green)"
  run_script "$STATE" bash -c '
    set -Eeuo pipefail
    source "$1/lib.sh"; source "$1/release-state.sh"; source "$1/lifecycle.sh"
    candidate_age_within_window "$2" "$3"' _ "$SCRIPTS" "$age" "$window"
  assert_status "$label" "$expected"
  pass
}

window_case "an age one second inside the window is within it" 1799 1800 0
window_case "an age exactly at the window is within it" 1800 1800 0
window_case "an age one second past the window is not" 1801 1800 1
window_case "a window of zero admits only this instant" 0 0 0
window_case "a window of zero refuses one second later" 1 0 1
window_case "a minute of clock skew into the future is tolerated" -60 1800 0
window_case "a second more than that is refused" -61 1800 1

begin "a candidate well inside the window is promotable end to end"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7"
NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "fresh candidate" 0
pass

begin "a candidate well past the window is refused end to end"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 3600)" "prepare_run_id=7"
NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "expired candidate" 1
assert_contains "expired candidate" "too old to promote"
pass

begin "a malformed staleness window refuses rather than defaulting"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7"
NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS=soon \
  run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "malformed window" 1
assert_contains "malformed window" "invalid NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS"
pass

begin "a staleness window that would overflow bash arithmetic is refused"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7"
NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS=9999999999999999999 \
  run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "overflowing window" 1
assert_contains "overflowing window" "at most 18 digits"
pass

begin "an unparseable candidate timestamp is refused"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=yesterday" "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "unparseable timestamp" 1
assert_contains "unparseable timestamp" "candidate_ready_at"
pass

begin "a timestamp shaped like an instant but not one is refused"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=2026-02-30T00:00:00Z" "prepare_run_id=7"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "impossible date" 1
pass


echo
echo "--- absent, malformed and unreadable are three different records ---"

begin "a ConfigMap that does not exist is a bootstrap"
STATE="$(new_state green)"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "absent configmap" 0
assert_contains "absent configmap" "first release"
assert_contains "absent configmap" "candidate=blue"
assert_no_drain "$STATE" "absent configmap"
pass

begin "a ConfigMap that exists with empty data is not a bootstrap"
# It renders identically to an absent one, which is the whole trap: only the
# existence check separates them.
STATE="$(new_state green)"
record_empty "$STATE"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "empty data" 1
assert_contains "empty data" "carries no data at all"
assert_no_drain "$STATE" "empty data"
assert_no_state_write "$STATE" "empty data"
pass

begin "a read that fails is never mistaken for an absent record"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_B:$ID_B"
printf '1' >"$STATE/release-state-read-fails"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "read error" 1
assert_contains "read error" "could not be read"
assert_no_drain "$STATE" "read error"
assert_no_state_write "$STATE" "read error"
pass

begin "a read error does not read as 'no reservation, slot free'"
# The exact shape of the finding: the reservation is real, the read fails, and
# the old reader would have answered FREE and overwritten the rollback.
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke=$RELEASE_B:$ID_B"
printf '1' >"$STATE/release-state-read-fails"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "read error is not FREE" 1
assert_absent "read error is not FREE" "candidate=blue"
pass

begin "a failed existence probe is not a bootstrap either"
# The render and the existence probe are two reads. The first succeeding with
# no output is ambiguous, and the second is what resolves it -- so a failure
# in the second is exactly as uninformative as a failure in the first, and
# must not be counted as a vote for absence.
STATE="$(new_state green)"
printf '1' >"$STATE/release-state-exists-fails"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "existence probe error" 1
assert_contains "existence probe error" "could not be"
assert_absent "existence probe error" "first release"
assert_no_drain "$STATE" "existence probe error"
assert_no_state_write "$STATE" "existence probe error"
pass

begin "a record missing rollback_reserved_slot blocks the release"
# The HIGH finding, exactly: selectors on green, a record that has an
# active_slot but no reservation key at all, retention long past. A missing
# key is not an empty one.
STATE="$(new_state green)"
record_only "$STATE" "schema=nchat-prod-release-state/v1" "active_slot=green" \
  "cutover_at=$(ago 3600)" "candidate_slot=" "candidate_release=" \
  "candidate_ready_at=" "prepare_run_id=" "post_cutover_smoke="
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "missing reservation key" 1
assert_contains "missing reservation key" "missing required key(s): rollback_reserved_slot"
assert_no_drain "$STATE" "missing reservation key"
assert_no_state_write "$STATE" "missing reservation key"
pass

begin "a record missing several keys names all of them"
STATE="$(new_state green)"
record_only "$STATE" "schema=nchat-prod-release-state/v1" "active_slot=green"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "missing several keys" 1
assert_contains "missing several keys" "rollback_reserved_slot"
assert_contains "missing several keys" "candidate_slot"
assert_no_drain "$STATE" "missing several keys"
pass

begin "a record with every key present but empty is well formed"
# The other half of the distinction: present-and-empty is a namespace with no
# candidate and no reservation, and it must keep working.
STATE="$(new_state green)"
record_only "$STATE" "schema=nchat-prod-release-state/v1" "active_slot=green" \
  "cutover_at=$(ago 3600)" "rollback_reserved_slot=" "candidate_slot=" \
  "candidate_release=" "candidate_ready_at=" "prepare_run_id=" "post_cutover_smoke="
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "all keys empty" 0
assert_contains "all keys empty" "candidate=blue"
pass

begin "a record with no schema key blocks the release"
STATE="$(new_state green)"
record_only "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" \
  "rollback_reserved_slot=" "candidate_slot=" "candidate_release=" \
  "candidate_ready_at=" "prepare_run_id=" "post_cutover_smoke="
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "no schema" 1
assert_contains "no schema" "declares no schema"
pass

begin "a record carrying a key the contract does not define blocks the release"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 3600)" "rollback_reserved_slot="
printf 'yes' >"$STATE/release-state/promote_now"
run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "unknown key" 1
assert_contains "unknown key" "does not define: promote_now"
pass

begin "a cutover cannot identify a candidate through a failed read"
STATE="$(new_state green)"
record "$STATE" "candidate_slot=blue" "candidate_release=$RELEASE_A:$ID_A" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7"
printf '1' >"$STATE/release-state-read-fails"
run_script "$STATE" bash "$SCRIPTS/cutover-preflight.sh" "$WORK/before.txt"
assert_status "cutover read error" 1
assert_contains "cutover read error" "could not be read"
assert_absent "cutover read error" "no prepared candidate is recorded"
[[ ! -s "$STATE/patch-log" ]] || fail "cutover read error: a Service was patched"
pass

begin "a transition refuses to carry forward a record it could not read"
# Without this the writer would overwrite active_slot, cutover_at and the
# reservation with empty strings -- losing exactly the state it was reading in
# order to preserve.
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 600)" "rollback_reserved_slot=blue"
printf '1' >"$STATE/release-state-read-fails"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_candidate_ready blue "$2:$3" 42' _ "$SCRIPTS" "$RELEASE_A" "$ID_A"
assert_status "transition read error" 1
rm -f "$STATE/release-state-read-fails"
[[ "$(record_field "$STATE" rollback_reserved_slot)" == blue ]] ||
  fail "transition read error: the reservation was overwritten"
[[ "$(record_field "$STATE" active_slot)" == green ]] ||
  fail "transition read error: the active slot was overwritten"
pass

echo
echo "--- the post-traffic smoke wrapper ---"

begin "a failed smoke records nothing"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 60)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
printf '1' >"$STATE/probes-fail"
run_script "$STATE" bash "$SCRIPTS/record-traffic-smoke.sh" --target green --after cutover
assert_status "wrapper smoke fail" 1
[[ -z "$(record_field "$STATE" post_cutover_smoke)" ]] ||
  fail "wrapper smoke fail: evidence was recorded for a smoke that failed"
assert_no_state_write "$STATE" "wrapper smoke fail"
assert_absent "wrapper smoke fail" "Recorded:"
pass

begin "a failed smoke leaves the rest of the record untouched"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 60)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
printf '1' >"$STATE/probes-fail"
run_script "$STATE" bash "$SCRIPTS/record-traffic-smoke.sh" --target green --after cutover
assert_status "wrapper preserves record" 1
[[ "$(record_field "$STATE" rollback_reserved_slot)" == blue ]] ||
  fail "wrapper preserves record: the reservation changed"
pass

begin "a passing smoke records the release the slot is actually serving"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 60)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
run_script "$STATE" bash "$SCRIPTS/record-traffic-smoke.sh" --target green --after cutover
assert_status "wrapper smoke pass" 0
[[ "$(record_field "$STATE" post_cutover_smoke)" == "$RELEASE_B:$ID_B" ]] ||
  fail "wrapper smoke pass: recorded '$(record_field "$STATE" post_cutover_smoke)', expected $RELEASE_B:$ID_B"
assert_contains "wrapper smoke pass" "Recorded:"
pass

begin "a passing smoke whose record cannot be written fails the wrapper"
# Otherwise the run reports a validated release while the lifecycle has no
# evidence of it, and the next release blocks for a reason nobody saw.
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 60)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
printf '1' >"$STATE/release-state-write-fails"
run_script "$STATE" bash "$SCRIPTS/record-traffic-smoke.sh" --target green --after cutover
assert_status "wrapper write fail" 1
rm -f "$STATE/release-state-write-fails"
[[ -z "$(record_field "$STATE" post_cutover_smoke)" ]] ||
  fail "wrapper write fail: evidence appeared despite the refused write"
pass

begin "a wrapper run on a slot carrying no traffic fails and records nothing"
STATE="$(new_state green)"
record "$STATE" "active_slot=green" "cutover_at=$(ago 60)" "rollback_reserved_slot=blue" \
  "post_cutover_smoke="
run_script "$STATE" bash "$SCRIPTS/record-traffic-smoke.sh" --target blue --after cutover
assert_status "wrapper wrong slot" 1
[[ -z "$(record_field "$STATE" post_cutover_smoke)" ]] ||
  fail "wrapper wrong slot: evidence was recorded for an idle slot"
pass

begin "the whole contract: cutover, failed smoke, retention expired, no drain"
# The regression test for the HIGH finding of the previous review, driven
# through the real transitions rather than a hand-written record.
STATE="$(new_state green)"
record "$STATE" "candidate_slot=green" "candidate_release=$RELEASE_B:$ID_B" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7" "active_slot=blue"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_cutover green blue' _ "$SCRIPTS"
assert_status "contract: record the cutover" 0
printf '1' >"$STATE/probes-fail"
run_script "$STATE" bash "$SCRIPTS/record-traffic-smoke.sh" --target green --after cutover
assert_status "contract: the smoke fails" 1
rm -f "$STATE/probes-fail"
# Backdate the cutover so the retention window has long passed.
printf '%s' "$(ago 7200)" >"$STATE/release-state/cutover_at"
: >"$STATE/scale-log"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "contract: the next release is blocked" 1
assert_contains "contract: the next release is blocked" "no post-traffic smoke is recorded"
assert_no_drain "$STATE" "contract"
pass

begin "the same contract, with the smoke recorded, retires the slot"
# The positive half, so the case above cannot pass because the flow is broken.
STATE="$(new_state green)"
record "$STATE" "candidate_slot=green" "candidate_release=$RELEASE_B:$ID_B" \
  "candidate_ready_at=$(ago 60)" "prepare_run_id=7" "active_slot=blue"
run_script "$STATE" bash -c '
  set -Eeuo pipefail
  source "$1/lib.sh"; source "$1/release-state.sh"
  record_cutover green blue' _ "$SCRIPTS"
assert_status "contract+: record the cutover" 0
run_script "$STATE" bash "$SCRIPTS/record-traffic-smoke.sh" --target green --after cutover
assert_status "contract+: the smoke passes" 0
printf '%s' "$(ago 7200)" >"$STATE/release-state/cutover_at"
: >"$STATE/scale-log"
NCHAT_PROD_ROLLBACK_RETENTION_SECONDS=1800 \
  run_script "$STATE" bash "$SCRIPTS/prepare-slot.sh" "$WORK/before.txt"
assert_status "contract+: the next release proceeds" 0
assert_contains "contract+: the next release proceeds" "candidate=blue"
[[ "$(grep -c . "$STATE/scale-log")" -eq 10 ]] ||
  fail "contract+: expected ten workloads drained, got $(grep -c . "$STATE/scale-log")"
pass

if [[ "$FAILURES" -ne 0 ]]; then
  echo "$FAILURES release lifecycle test(s) failed." >&2
  exit 1
fi
echo
echo "Release lifecycle tests passed."
