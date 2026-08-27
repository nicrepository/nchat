#!/usr/bin/env bash
# Behaviour tests for the production Blue/Green operations (issue #626).
#
# These assert observable behaviour and exit codes against a fake kubectl, never
# internals: what the scripts do to the Service selectors, what they refuse, and
# — the property the review found missing — what a second run does. A rollback
# that reverses itself on retry is indistinguishable from a correct one until it
# is executed twice, so every mutation is executed twice here.
#
# No cluster, no network.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SCRIPTS="$ROOT_DIR/scripts/deploy/nchat-prod"
FAKE_BIN="$(mktemp -d "${TMPDIR:-/tmp}/nchat-prod-fakebin.XXXXXX")"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-prod-tests.XXXXXX")"
trap 'rm -rf "$FAKE_BIN" "$WORK"' EXIT

cp "$ROOT_DIR/scripts/ci/testdata/nchat-prod/fake-kubectl" "$FAKE_BIN/kubectl"
chmod +x "$FAKE_BIN/kubectl"
PATH="$FAKE_BIN:$PATH"
export PATH

SERVICES=(nchat-web nchat-admin-web auth-service chat-service file-service
  notification-service admin-service search-service media-service)
FAILURES=0
CASE=""
CASE_FAILURES=0

fail() { echo "  [FAIL] $CASE: $*" >&2; FAILURES=$((FAILURES + 1)); }

# Opens a case and closes the previous one. Tracking the failure count per case
# is what stops a case that already reported [FAIL] from also printing [OK].
begin() {
  CASE="$1"
  CASE_FAILURES="$FAILURES"
  # Reset the per-case release override. "RELEASE_SHA=x status=0" is a plain
  # assignment, not a command prefix, so without this a case that deliberately
  # supplies a bad SHA leaks it into every case after it.
  RELEASE_SHA=""
}

pass() {
  [[ "$FAILURES" -eq "$CASE_FAILURES" ]] || return 0
  echo "  [OK]   $CASE"
}

# The release every fixture slot carries unless a case changes it.
RELEASE_A=a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0
RELEASE_B=b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1
# The Secrets bootstrap requires before it will touch the namespace.
REQUIRED_SECRETS=(nchat-secrets nchat-postgres-runtime nchat-postgres-migrator
  nchat-file-encryption ghcr-pull)
# Services the shared stateful layer must already publish.
REQUIRED_STATEFUL=(postgres valkey seaweedfs-filer)

# The replica count each workload really runs in production.
#
# auth-service is one, not two: its avatar volume is ReadWriteOnce and a single
# replica owns writes. The fixture says two everywhere only if nobody checks the
# manifests, and a fake that disagrees with production tests the wrong system.
replicas_for() {
  [[ "$1" == "auth-service" ]] && printf '1' || printf '2'
}

# Builds a cluster: every Service on $1, every Deployment of the slots in $2 Ready.
new_state() {
  local active="$1" ready_slots="$2" state slot service secret count
  state="$WORK/state.$RANDOM"
  mkdir -p "$state/services" "$state/ready" "$state/sha" "$state/image" "$state/secrets"
  printf 'nchat-prod-deployer' >"$state/context"
  printf 'nchat-prod' >"$state/namespace"
  : >"$state/patch-log"
  for secret in "${REQUIRED_SECRETS[@]}"; do
    : >"$state/secrets/$secret"
  done
  # A cluster with room to spare: the deploy cases are about migrations and
  # rollouts, so capacity must not be what decides them. The preflight itself
  # has its own fixtures in test_prod_capacity_preflight.sh.
  mkdir -p "$state/quota"
  printf '16' >"$state/quota/hard-cpu"; printf '1' >"$state/quota/used-cpu"
  printf '32Gi' >"$state/quota/hard-memory"; printf '2Gi' >"$state/quota/used-memory"
  printf '80' >"$state/quota/hard-pods"; printf '10' >"$state/quota/used-pods"
  printf '500Gi' >"$state/quota/hard-storage"; printf '10Gi' >"$state/quota/used-storage"
  printf '32 128Gi 500Gi 220\n' >"$state/node-allocatable"
  printf 'Running|node-a|500m 2Gi 1Gi\n' >"$state/cluster-pods"
  printf 'Running|node-a\n' >"$state/cluster-pod-slots"
  mkdir -p "$state/jobs"
  printf 'nchat-migrations-%s\n' "${RELEASE_A:0:12}" >"$state/pending-job-name"
  for service in "${REQUIRED_STATEFUL[@]}"; do
    printf 'shared' >"$state/services/$service"
  done
  for service in "${SERVICES[@]}"; do
    [[ "$active" == "none" ]] || printf '%s' "$active" >"$state/services/$service"
  done
  for slot in $ready_slots; do
    for service in "${SERVICES[@]}"; do
      count="$(replicas_for "$service")"
      set_rollout "$state" "$service-$slot" 1 1 "$count" "$count" "$count" "$count" 0
      set_release "$state" "$service" "$slot" "$RELEASE_A"
      printf 'ghcr.io/nicrepository/nchat/%s@sha256:%064d' "$service" 1 >"$state/image/$service-$slot"
    done
  done
  printf '%s' "$state"
}

# The component label a Deployment selects on, mirroring the real manifests.
component_for() {
  case "$1" in
    nchat-web) printf 'web' ;;
    nchat-admin-web) printf 'admin-web' ;;
    auth-service) printf 'auth' ;;
    chat-service) printf 'chat' ;;
    file-service) printf 'file' ;;
    notification-service) printf 'notification' ;;
    admin-service) printf 'admin' ;;
    search-service) printf 'search' ;;
    media-service) printf 'media' ;;
  esac
}

# generation observedGeneration replicas updated ready available unavailable
set_rollout() {
  local state="$1" deployment="$2"
  shift 2
  printf '%s %s %s %s %s %s %s\n' "$@" >"$state/ready/$deployment"
}

# Sets both the release the Deployment declares and the one its Ready pods run.
# A third argument makes them differ, which is what a stuck rollout looks like.
set_release() {
  local state="$1" service="$2" slot="$3" desired="$4" observed="${5:-$4}" component
  component="$(component_for "$service")"
  mkdir -p "$state/observed" "$state/component"
  printf '%s' "$desired" >"$state/sha/$service-$slot"
  printf '%s' "$component" >"$state/component/$service-$slot"
  printf '%s\n' "$observed" >"$state/observed/$component-$slot"
}

# Moves one workload of a slot onto a different release, the shape a deploy that
# failed part-way leaves behind.
set_workload_release() {
  local state="$1" deployment="$2" release="$3" service slot component
  slot="${deployment##*-}"
  service="${deployment%-"$slot"}"
  component="$(component_for "$service")"
  printf '%s' "$release" >"$state/sha/$deployment"
  printf '%s\n' "$release" >"$state/observed/$component-$slot"
}

slot_of() { cat "$1/services/$2" 2>/dev/null || printf 'unset'; }

# Asserts every Service selects $2.
assert_all_on() {
  local state="$1" expected="$2" service actual
  for service in "${SERVICES[@]}"; do
    actual="$(slot_of "$state" "$service")"
    [[ "$actual" == "$expected" ]] || { fail "service/$service is '$actual', expected '$expected'"; return; }
  done
}

run() {
  local state="$1"; shift
  FAKE_STATE_DIR="$state" NCHAT_PROD_ASSUME_YES=1 NCHAT_PROD_SMOKE_CONFIRMED="${SMOKE:-}" \
    "$@" >"$WORK/out.txt" 2>"$WORK/err.txt"
}

expect_exit() {
  local expected="$1" actual="$2"
  [[ "$actual" == "$expected" ]] || fail "exit $actual, expected $expected: $(tail -2 "$WORK/err.txt")"
}

echo "=== production blue/green script behaviour ==="
echo
echo "--- status ---"

begin "status reports blue active"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 0 "$status"
grep -q "ACTIVE   : blue" "$WORK/out.txt" || fail "did not report blue as active"
pass

begin "status reports green active"
state="$(new_state green "blue green")"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 0 "$status"
grep -q "ACTIVE   : green" "$WORK/out.txt" || fail "did not report green as active"
pass

begin "status detects a mixed state and exits non-zero"
state="$(new_state blue "blue green")"
printf 'green' >"$state/services/chat-service"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "MIXED STATE" "$WORK/err.txt" || fail "did not name the mixed state"
pass

begin "status reports a slot whose workloads carry different releases"
state="$(new_state blue "blue green")"
# A genuinely half-applied slot: one workload's Ready pods are on another
# release. Writing only the Deployment's annotation would express a stale
# rollout instead, which is the separate case covered below.
set_workload_release "$state" file-service-blue "$RELEASE_B"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "release   MIXED" "$WORK/out.txt" || fail "hid a half-applied release behind one service"
grep -q "file-service" "$WORK/out.txt" || fail "did not name the diverging workload"
pass

begin "status reports a missing Service without abandoning the rest of the map"
state="$(new_state blue "blue green")"
rm -f "$state/services/file-service"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "file-service *-> MISSING" "$WORK/out.txt" || fail "did not report the Service as MISSING"
grep -q "chat-service *-> blue" "$WORK/out.txt" || fail "stopped before reporting the other Services"
grep -q "media-service *-> blue" "$WORK/out.txt" || fail "did not reach the end of the Service list"
grep -q "MISSING" "$WORK/out.txt" && ! grep -q "file-service *-> mixed" "$WORK/out.txt" ||
  fail "collapsed MISSING into mixed"
pass

begin "status reports a Service whose selector was never patched"
state="$(new_state blue "blue green")"
: >"$state/services/search-service"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "search-service *-> UNSET" "$WORK/out.txt" || fail "did not distinguish an absent selector"
pass

begin "status exits non-zero when the candidate carries a mixed release"
state="$(new_state blue "blue green")"
set_workload_release "$state" chat-service-green "$RELEASE_B"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "state     INVALID" "$WORK/out.txt" || fail "did not mark the candidate invalid"
grep -q "chat-service" "$WORK/out.txt" || fail "did not name the diverging workload"
grep -q "ACTIVE   : blue" "$WORK/out.txt" || fail "did not print the full picture first"
pass

begin "status reports an undeployed candidate as such, not as an error"
state="$(new_state blue "blue")"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 0 "$status"
grep -q "state     NOT DEPLOYED" "$WORK/out.txt" || fail "did not distinguish an undeployed slot"
pass

begin "status fails on an unexpected kube context"
state="$(new_state blue "blue green")"
printf 'some-other-cluster' >"$state/context"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "expected 'nchat-prod-deployer'" "$WORK/err.txt" || fail "did not refuse the context"
pass

begin "status fails when the namespace is absent"
state="$(new_state blue "blue green")"
printf 'nchat-dev' >"$state/namespace"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
pass

echo
echo "--- rollout observation ---"

# Readiness used to be readyReplicas == spec.replicas, which the pods of the
# release being REPLACED satisfy. Every case below keeps that comparison true
# and must still be refused.
begin "a fully rolled out workload is ready"
state="$(new_state blue "blue green")"
set_rollout "$state" chat-service-green 2 2 2 2 2 2 0
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 0 "$status"
pass

begin "a generation the controller has not observed is not ready"
state="$(new_state blue "blue green")"
set_rollout "$state" chat-service-green 4 3 2 2 2 2 0
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "ROLLING OUT" "$WORK/out.txt" || fail "a stale observedGeneration was reported as settled"
pass

begin "old pods still Ready do not make a stuck rollout ready"
state="$(new_state blue "blue green")"
# Green declares B; its new pods never scheduled, so the two Ready pods are the
# A pods it is replacing.
set_rollout "$state" chat-service-green 2 2 2 0 2 2 0
set_release "$state" chat-service green "$RELEASE_B" "$RELEASE_A"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "ROLLING OUT" "$WORK/out.txt" || fail "reported a stuck rollout as a finished release"
grep -q "observed=$RELEASE_A" "$WORK/out.txt" || fail "did not report the release actually running"
pass

begin "a partial rollout is not ready"
state="$(new_state blue "blue green")"
set_rollout "$state" chat-service-green 2 2 2 1 2 2 0
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
pass

begin "updated but not available is not ready"
state="$(new_state blue "blue green")"
set_rollout "$state" chat-service-green 2 2 2 2 2 1 0
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
pass

begin "any unavailable replica means not ready"
state="$(new_state blue "blue green")"
set_rollout "$state" chat-service-green 2 2 2 2 2 2 1
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
pass

begin "a stuck rollout produces no smoke evidence"
state="$(new_state blue "blue green")"
set_rollout "$state" chat-service-green 2 2 2 0 2 2 0
set_release "$state" chat-service green "$RELEASE_B" "$RELEASE_A"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target green || status=$?
expect_exit 1 "$status"
grep -q "has not finished rolling out" "$WORK/err.txt" || fail "smoked a slot that never rolled out"
grep -q "NCHAT_PROD_SMOKE_CONFIRMED=green:$RELEASE_B" "$WORK/out.txt" &&
  fail "emitted evidence for a release that is not running"
pass

begin "a stuck rollout cannot be promoted even with evidence in hand"
state="$(new_state blue "blue green")"
set_rollout "$state" chat-service-green 2 2 2 0 2 2 0
set_release "$state" chat-service green "$RELEASE_B" "$RELEASE_A"
SMOKE="green:$RELEASE_B" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
[[ ! -s "$state/patch-log" ]] || fail "promoted a slot whose Ready pods are the previous release"
pass

echo
echo "--- cutover ---"

begin "cutover blue -> green converges every service"
state="$(new_state blue "blue green")"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 0 "$status"
assert_all_on "$state" green
pass

begin "cutover green -> blue converges every service"
state="$(new_state green "blue green")"
SMOKE="blue:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target blue || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
pass

begin "repeating a cutover is a no-op, not a reversal"
state="$(new_state blue "blue green")"
SMOKE="green:$RELEASE_A" run "$state" "$SCRIPTS/cutover.sh" --target green || fail "first cutover failed"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 0 "$status"
assert_all_on "$state" green
grep -q "nothing to move" "$WORK/out.txt" || fail "did not report a no-op"
pass

begin "cutover converges a mixed state onto the named target"
state="$(new_state blue "blue green")"
printf 'green' >"$state/services/chat-service"
printf 'green' >"$state/services/file-service"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 0 "$status"
assert_all_on "$state" green
pass

begin "cutover to a mixed state's other side also converges"
state="$(new_state green "blue green")"
printf 'blue' >"$state/services/chat-service"
SMOKE="blue:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target blue || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
pass

begin "cutover refuses an unhealthy target before touching anything"
state="$(new_state blue "blue")"
printf '2 0\n' >"$state/ready/chat-service-green"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
[[ ! -s "$state/patch-log" ]] || fail "patched a service despite an unhealthy target"
pass

begin "cutover refuses an invalid target before any mutation"
state="$(new_state blue "blue green")"
SMOKE=purple status=0; run "$state" "$SCRIPTS/cutover.sh" --target purple || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
grep -q "target slot is required" "$WORK/err.txt" || fail "did not reject the target"
pass

begin "cutover with no target at all is refused"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/cutover.sh" || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
pass

begin "cutover without recorded smoke evidence is blocked"
state="$(new_state blue "blue green")"
SMOKE="" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
grep -q "authenticated release smoke" "$WORK/err.txt" || fail "did not require the manual smoke"
pass

begin "a patch failure part-way leaves a mixed state the operator can see"
state="$(new_state blue "blue green")"
printf 'file-service\n' >"$state/patch-fails"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
grep -q "mixed state" "$WORK/err.txt" || fail "did not report the partial failure"
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 1 "$status"
grep -q "MIXED STATE" "$WORK/err.txt" || fail "status hid the partial cutover"
pass

begin "re-running after a partial failure converges to the same target"
rm -f "$state/patch-fails"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 0 "$status"
assert_all_on "$state" green
pass

begin "cutover is blocked when the candidate carries more than one release"
state="$(new_state blue "blue green")"
set_workload_release "$state" chat-service-green "$RELEASE_B"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
[[ ! -s "$state/patch-log" ]] || fail "promoted a slot carrying two releases"
grep -q "does not carry one release" "$WORK/err.txt" || fail "did not name the inconsistency"
pass

begin "cutover is blocked when the candidate changed after the smoke"
state="$(new_state blue "blue green")"
for svc in "${SERVICES[@]}"; do set_workload_release "$state" "$svc-green" "$RELEASE_B"; done
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
grep -q "does not match" "$WORK/err.txt" || fail "accepted evidence for a superseded release"
pass

begin "cutover proceeds when the smoke evidence matches the current release"
state="$(new_state blue "blue green")"
for svc in "${SERVICES[@]}"; do set_workload_release "$state" "$svc-green" "$RELEASE_B"; done
SMOKE="green:$RELEASE_B" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 0 "$status"
assert_all_on "$state" green
pass

begin "smoke evidence naming only the slot is not accepted"
state="$(new_state blue "blue green")"
SMOKE=green status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
pass

begin "cutover is blocked when the candidate is not deployed at all"
state="$(new_state blue "blue")"
SMOKE="green:$RELEASE_A" status=0; run "$state" "$SCRIPTS/cutover.sh" --target green || status=$?
expect_exit 1 "$status"
assert_all_on "$state" blue
pass

echo
echo "--- rollback ---"

begin "rollback green -> blue converges every service"
state="$(new_state green "blue green")"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "5xx after cutover" || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
pass

begin "repeating a rollback does not send production back to the bad release"
state="$(new_state green "blue green")"
run "$state" "$SCRIPTS/rollback.sh" --target blue "5xx after cutover" || fail "first rollback failed"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "5xx after cutover" || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
grep -q "nothing to move" "$WORK/out.txt" || fail "did not report a no-op on retry"
pass

begin "rollback blue -> green is possible when explicitly asked for"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target green "blue baseline is faulty" || status=$?
expect_exit 0 "$status"
assert_all_on "$state" green
pass

begin "rollback requires a reason"
state="$(new_state green "blue green")"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue || status=$?
expect_exit 1 "$status"
assert_all_on "$state" green
pass

begin "rollback refuses an unhealthy target rather than choosing another slot"
state="$(new_state green "green")"
printf '2 0\n' >"$state/ready/auth-service-blue"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 1 "$status"
assert_all_on "$state" green
[[ ! -s "$state/patch-log" ]] || fail "moved traffic onto a slot that cannot serve it"
pass

begin "rollback converges a mixed state onto the named target"
state="$(new_state green "blue green")"
printf 'blue' >"$state/services/chat-service"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "mixed after failed cutover" || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
pass

begin "a rollback that fails part-way reports it and can be re-run"
state="$(new_state green "blue green")"
printf 'search-service\n' >"$state/patch-fails"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 1 "$status"
rm -f "$state/patch-fails"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
pass

begin "rollback is blocked when the target is Ready but carries a mixed release"
state="$(new_state green "blue green")"
set_workload_release "$state" chat-service-blue "$RELEASE_B"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 1 "$status"
assert_all_on "$state" green
[[ ! -s "$state/patch-log" ]] || fail "moved traffic onto a slot carrying two releases"
grep -q "does not carry one release" "$WORK/err.txt" || fail "did not name the inconsistency"
pass

begin "rollback to an already-active slot with a mixed release is a failure, not a no-op"
state="$(new_state blue "blue green")"
set_workload_release "$state" file-service-blue "$RELEASE_B"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 1 "$status"
grep -q "does not carry one release" "$WORK/err.txt" || fail "reported a mixed active slot as success"
grep -q "No change made" "$WORK/out.txt" && fail "claimed a clean no-op over a mixed release"
pass

begin "rollback to an already-active consistent slot is a validated no-op"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "checking" || status=$?
expect_exit 0 "$status"
grep -q "carrying release $RELEASE_A" "$WORK/out.txt" || fail "did not report the validated release"
[[ ! -s "$state/patch-log" ]] || fail "patched a Service during a no-op"
pass

begin "rollback is blocked when the target is not deployed at all"
state="$(new_state green "green")"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 1 "$status"
assert_all_on "$state" green
pass

begin "rollback converges mixed Services onto a consistent target"
# Mixed Services is a different fault from a mixed release inside one slot, and
# only the first is something rollback should fix.
state="$(new_state green "blue green")"
printf 'blue' >"$state/services/chat-service"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "converge after failed cutover" || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
pass

begin "rollback fails when the target release breaks during convergence"
state="$(new_state green "blue green")"
printf 'search-service' >"$state/patch-fails"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 1 "$status"
# The partial patch is visible, and re-running must still be possible.
rm -f "$state/patch-fails"
set_workload_release "$state" media-service-blue "$RELEASE_B"
status=0; run "$state" "$SCRIPTS/rollback.sh" --target blue "incident" || status=$?
expect_exit 1 "$status"
grep -q "does not carry one release" "$WORK/err.txt" || fail "resumed onto an inconsistent slot"
pass

echo
echo "--- drain-old ---"

begin "drain-old refuses to scale down the slot serving traffic"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/drain-old.sh" --target blue || status=$?
expect_exit 1 "$status"
[[ ! -f "$state/scale-log" ]] || fail "scaled down the active slot"
pass

begin "drain-old scales the retired slot to zero"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/drain-old.sh" --target green || status=$?
expect_exit 0 "$status"
[[ "$(wc -l <"$state/scale-log")" -eq "${#SERVICES[@]}" ]] || fail "did not scale every workload"
pass

echo
echo "--- smoke ---"

begin "smoke never reports a bare pass and blocks cutover"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target green || status=$?
expect_exit 0 "$status"
grep -q "Automated smoke            : PASS" "$WORK/out.txt" || fail "no automated verdict"
grep -q "NOT CONFIRMED BY THIS COMMAND" "$WORK/out.txt" || fail "did not flag the manual smoke"
grep -q "Cutover eligibility        : BLOCKED" "$WORK/out.txt" || fail "did not block cutover"
grep -qx "smoke passed" "$WORK/out.txt" && fail "claimed an unqualified pass"
pass

begin "smoke fails when the candidate carries more than one release"
state="$(new_state blue "blue green")"
set_workload_release "$state" file-service-green "$RELEASE_B"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target green || status=$?
expect_exit 1 "$status"
grep -q "more than one release" "$WORK/err.txt" || fail "smoked an incoherent candidate"
grep -q "Release validated          : NONE" "$WORK/out.txt" || fail "claimed to have validated a release"
pass

begin "smoke prints evidence bound to the slot and the release"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target green || status=$?
expect_exit 0 "$status"
grep -q "NCHAT_PROD_SMOKE_CONFIRMED=green:$RELEASE_A" "$WORK/out.txt" ||
  fail "evidence is not bound to the release"
pass

begin "smoke refuses a slot that is already serving production traffic"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target blue || status=$?
expect_exit 1 "$status"
grep -q "already selected by" "$WORK/err.txt" || fail "did not detect the lack of isolation"
pass

echo
echo "--- bootstrap ---"

# Bootstrap and deploy render real Kustomize overlays and rewrite images from
# digest artefacts, so the cases below supply both. RELEASE_SHA has to look like
# a commit, because validating it is one of the gates under test.
ARTIFACTS="$WORK/artifacts"
mkdir -p "$ARTIFACTS"
for image in web admin-web auth-service chat-service file-service \
  notification-service admin-service search-service media-service migrations; do
  printf 'sha256:%064d' 1 >"$ARTIFACTS/digest-$image.txt"
done
TOPOLOGY="$WORK/topology.env"
cat >"$TOPOLOGY" <<TOPO
NCHAT_PROD_HOST=nchat.example.com
NCHAT_PROD_PUBLIC_URL=https://nchat.example.com
NCHAT_PROD_PREVIEW_ALLOW_CIDR=198.51.100.0/24
NCHAT_PROD_LIVEKIT_URL=wss://livekit.example.com
NCHAT_PROD_LIVEKIT_CONNECT_SRC=wss://livekit.example.com https://livekit.example.com
TOPO

release_run() {
  local state="$1"; shift
  FAKE_STATE_DIR="$state" NCHAT_PROD_ASSUME_YES=1 \
    NCHAT_PROD_RELEASE_SHA="${RELEASE_SHA:-$RELEASE_A}" \
    NCHAT_PROD_TOPOLOGY_FILE="$TOPOLOGY" ARTIFACTS_DIR="$ARTIFACTS" \
    "$@" >"$WORK/out.txt" 2>"$WORK/err.txt"
}

# Kustomize is required, never skipped.
#
# These cases exercise bootstrap and deploy — the two flows that render and apply
# production — so skipping them when the binary is missing produced a green CI
# that had tested none of it. The repository already pins and checksum-verifies
# a Kustomize; that is what gets used, exactly as scripts/security/trivy-config.sh
# does. Only a genuine failure to obtain it stops the suite, and then it stops
# rather than passing.
require_kustomize() {
  local directory
  command -v kustomize >/dev/null 2>&1 && return 0
  echo "  kustomize not on PATH; installing the pinned, checksum-verified build"
  directory="$("$ROOT_DIR/scripts/deploy/nchat-dev/install-kustomize.sh")" || return 1
  PATH="$directory:$PATH"
  export PATH
  command -v kustomize >/dev/null 2>&1
}

if ! require_kustomize; then
  echo "Kustomize is required for production Blue/Green operational tests." >&2
  echo "Install it, or make scripts/deploy/nchat-dev/install-kustomize.sh usable here." >&2
  exit 1
fi

begin "bootstrap refuses to start when the migrator Secret is absent"
state="$(new_state none "")"
rm -f "$state/secrets/nchat-postgres-migrator"
status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 1 "$status"
grep -q "missing Secret: nchat-postgres-migrator" "$WORK/err.txt" ||
  fail "did not name the missing Secret"
[[ ! -f "$state/apply-log" ]] || fail "applied resources despite a missing prerequisite"
[[ ! -f "$state/wait-log" ]] || fail "started the migration despite a missing prerequisite"
pass

begin "bootstrap refuses to start when the stateful layer is absent"
state="$(new_state none "")"
rm -f "$state/services/postgres"
status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 1 "$status"
grep -q "missing Service: postgres" "$WORK/err.txt" || fail "did not name the missing Service"
[[ ! -f "$state/apply-log" ]] || fail "applied resources despite a missing prerequisite"
pass

begin "bootstrap refuses a release that is not a commit SHA"
state="$(new_state none "")"
RELEASE_SHA=not-a-sha status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 1 "$status"
[[ ! -f "$state/apply-log" ]] || fail "applied resources for an unidentified release"
pass

begin "bootstrap stops when the migration Job fails and never establishes Blue"
state="$(new_state none "blue")"
printf '1' >"$state/migration-fails"
status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 1 "$status"
grep -qE "migration .* did not complete" "$WORK/err.txt" || fail "did not report the migration failure"
[[ ! -f "$state/rollout-log" ]] || fail "rolled out Blue after a failed migration"
pass

# Capacity is checked before the migration runs and before any workload exists:
# discovering that Blue does not fit *after* the schema has moved leaves a
# half-established production to unpick by hand.
begin "bootstrap aborts before migrations when the cluster cannot hold Blue"
state="$(new_state blue "blue")"
printf '1' >"$state/quota/hard-cpu"
printf '900m' >"$state/quota/used-cpu"
status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 1 "$status"
grep -qE "cannot hold slot .* at production capacity" "$WORK/err.txt" || fail "did not report the shortfall"
[[ ! -f "$state/wait-log" ]] || fail "ran the migration despite insufficient capacity"
[[ ! -f "$state/rollout-log" ]] || fail "deployed Blue despite insufficient capacity"
grep -q "baseline.yaml" "$state/apply-log" 2>/dev/null && fail "applied the Blue workloads anyway"
pass

begin "bootstrap aborts before migrations when capacity cannot be determined"
state="$(new_state blue "blue")"
rm -rf "$state/quota" "$state/node-allocatable" "$state/cluster-pods" "$state/cluster-pod-slots"
status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 1 "$status"
grep -q "inconclusive" "$WORK/err.txt" || fail "did not report the preflight as inconclusive"
[[ ! -f "$state/wait-log" ]] || fail "ran the migration on an unverified capacity picture"
[[ ! -f "$state/rollout-log" ]] || fail "deployed Blue on an unverified capacity picture"
pass

begin "bootstrap establishes Blue and leaves it selected"
state="$(new_state blue "blue")"
status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 0 "$status"
[[ -f "$state/wait-log" ]] || fail "never ran the migration Job"
[[ "$(wc -l <"$state/rollout-log")" -eq "${#SERVICES[@]}" ]] || fail "did not wait for every workload"
grep -q "Users must NOT be given the address yet" "$WORK/out.txt" ||
  fail "did not withhold availability until the smoke"
pass

echo
echo "--- bootstrap -> baseline smoke (first production, end to end) ---"

# The gap the third review found: bootstrap and smoke each passed alone, and were
# incompatible with each other. Bootstrap leaves Blue selected by the stable
# Services, and the candidate smoke refuses a slot that holds traffic -- so the
# documented first launch could never be completed. This exercises the two in
# sequence, which is the only way that class of contradiction shows up.
begin "bootstrap then baseline smoke completes the first production sequence"
state="$(new_state blue "blue")"
release_run "$state" "$SCRIPTS/bootstrap.sh" || fail "bootstrap failed"
# Bootstrap leaves Blue selected: that is the baseline, not a mistake.
assert_all_on "$state" blue
status=0; run "$state" "$SCRIPTS/status.sh" || status=$?
expect_exit 0 "$status"
grep -q "ACTIVE   : blue" "$WORK/out.txt" || fail "status does not report Blue as active"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target blue --baseline || status=$?
expect_exit 0 "$status"
grep -q "mode: baseline" "$WORK/out.txt" || fail "did not run in baseline mode"
grep -q "baseline: blue is the only deployed slot" "$WORK/out.txt" || fail "baseline preconditions not reported"
grep -q "NCHAT_PROD_SMOKE_CONFIRMED=blue:$RELEASE_A" "$WORK/out.txt" ||
  fail "baseline smoke did not bind evidence to the release"
pass

begin "the candidate smoke still refuses a live slot without --baseline"
state="$(new_state blue "blue")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target blue || status=$?
expect_exit 1 "$status"
grep -q "already selected by" "$WORK/err.txt" || fail "isolation rule was weakened"
pass

begin "--baseline is refused for green"
state="$(new_state green "green")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target green --baseline || status=$?
expect_exit 1 "$status"
grep -q "applies only to the baseline slot" "$WORK/err.txt" || fail "accepted a non-baseline slot"
pass

begin "--baseline is refused when the slot is not what the Services select"
state="$(new_state green "blue green")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target blue --baseline || status=$?
expect_exit 1 "$status"
grep -q "not what the stable Services select" "$WORK/err.txt" || fail "accepted an unselected baseline"
pass

begin "--baseline is refused in a mixed state"
state="$(new_state blue "blue")"
printf 'green' >"$state/services/chat-service"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target blue --baseline || status=$?
expect_exit 1 "$status"
grep -q "do not agree on a slot" "$WORK/err.txt" || fail "accepted a mixed state as a baseline"
pass

begin "--baseline is refused once the other slot has been deployed"
state="$(new_state blue "blue green")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target blue --baseline || status=$?
expect_exit 1 "$status"
grep -q "past its baseline" "$WORK/err.txt" || fail "baseline mode survived past the first release"
pass

begin "an unknown smoke argument is refused"
state="$(new_state blue "blue")"
status=0; run "$state" "$SCRIPTS/smoke.sh" --target blue --force || status=$?
expect_exit 1 "$status"
pass

echo
echo "--- migration coordination ---"

# Deploys used to delete the migration Job before applying the next one, which
# kills a migration another operator is running: the pod dies, its advisory lock
# is released, and the schema_migrations row it marked in progress is left
# behind for the next run to refuse. Nothing below may delete an active Job.
JOB_A="nchat-migrations-${RELEASE_A:0:12}"
JOB_B="nchat-migrations-${RELEASE_B:0:12}"

begin "the first deploy of a release creates its own migration Job"
state="$(new_state blue "blue green")"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 0 "$status"
grep -q "$JOB_A" "$state/wait-log" || fail "did not wait on the release's own Job"
[[ ! -f "$state/delete-log" ]] || fail "deleted something during a clean migration"
pass

begin "a second deploy of the same release reuses the completed migration"
state="$(new_state blue "blue green")"
printf '1 0\n' >"$state/jobs/$JOB_A"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 0 "$status"
grep -q "already completed" "$WORK/out.txt" || fail "did not reuse the completed migration"
[[ ! -f "$state/delete-log" ]] || fail "deleted a completed migration Job"
grep -q "migrations.yaml" "$state/apply-log" && fail "re-applied a migration that had already run"
pass

begin "a deploy never deletes a migration Job that is still running"
state="$(new_state blue "blue green")"
printf '0 0\n' >"$state/jobs/$JOB_A"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 0 "$status"
grep -q "already running" "$WORK/out.txt" || fail "did not recognise the running migration"
[[ ! -f "$state/delete-log" ]] || fail "deleted an active migration Job"
pass

begin "a migration running for another release blocks the deploy"
state="$(new_state blue "blue green")"
printf '0 0\n' >"$state/jobs/$JOB_B"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
grep -q "in progress for another release" "$WORK/err.txt" || fail "allowed two releases to migrate at once"
[[ ! -f "$state/delete-log" ]] || fail "deleted another release's active Job"
grep -q "migrations.yaml" "$state/apply-log" 2>/dev/null && fail "applied a second concurrent migration"
pass

begin "a failed migration blocks the deploy and is preserved for diagnosis"
state="$(new_state blue "blue green")"
printf '0 1\n' >"$state/jobs/$JOB_A"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
grep -q "Failed state" "$WORK/err.txt" || fail "did not report the failed migration"
grep -q "kubectl logs job/$JOB_A" "$WORK/err.txt" || fail "did not offer an inspection command"
[[ ! -f "$state/delete-log" ]] || fail "destroyed the evidence of a failed migration"
[[ -f "$state/jobs/$JOB_A" ]] || fail "the failed Job was removed"
pass

begin "bootstrap follows the same rules and never deletes an active Job"
state="$(new_state blue "blue")"
printf '0 0\n' >"$state/jobs/$JOB_A"
status=0; release_run "$state" "$SCRIPTS/bootstrap.sh" || status=$?
expect_exit 0 "$status"
[[ ! -f "$state/delete-log" ]] || fail "bootstrap deleted an active migration Job"
pass

echo
echo "--- deploy ---"

begin "deploy refuses a release that is not a commit SHA"
state="$(new_state blue "blue green")"
RELEASE_SHA=abc status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
[[ ! -f "$state/apply-log" ]] || fail "applied a candidate for an unidentified release"
pass

begin "deploy refuses an unexpected kube context"
state="$(new_state blue "blue green")"
printf 'some-other-cluster' >"$state/context"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
[[ ! -f "$state/apply-log" ]] || fail "applied a candidate against the wrong cluster"
pass

begin "deploy stops when the migration fails and never applies the candidate"
state="$(new_state blue "blue green")"
printf '1' >"$state/migration-fails"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
grep -qE "migration .* did not complete" "$WORK/err.txt" || fail "did not report the migration failure"
[[ ! -f "$state/rollout-log" ]] || fail "waited on a candidate that was never applied"
pass

begin "deploy fails when the candidate never becomes Ready"
state="$(new_state blue "blue green")"
printf '1' >"$state/rollout-fails"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
grep -q "did not become Ready" "$WORK/err.txt" || fail "did not report the rollout failure"
pass

begin "deploy stops when the namespace cannot hold a second slot"
state="$(new_state blue "blue green")"
printf '1' >"$state/quota/hard-cpu"
printf '900m' >"$state/quota/used-cpu"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
grep -qE "cannot hold slot .* at production capacity" "$WORK/err.txt" || fail "did not report the shortfall"
[[ ! -f "$state/rollout-log" ]] || fail "waited on a candidate that could not be admitted"
pass

begin "deploy stops when capacity cannot be determined at all"
state="$(new_state blue "blue green")"
rm -rf "$state/quota" "$state/node-allocatable" "$state/cluster-pods"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 1 "$status"
grep -q "inconclusive" "$WORK/err.txt" || fail "did not report the preflight as inconclusive"
pass

begin "deploy never moves traffic to the candidate"
state="$(new_state blue "blue green")"
status=0; release_run "$state" "$SCRIPTS/deploy.sh" || status=$?
expect_exit 0 "$status"
assert_all_on "$state" blue
[[ ! -s "$state/patch-log" ]] || fail "deploy patched a Service; promotion is cutover's job"
pass



echo
if [ "$FAILURES" -gt 0 ]; then
  echo "production blue/green script tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "production blue/green script tests passed."
