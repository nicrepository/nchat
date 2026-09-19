#!/usr/bin/env bash
# Behaviour tests for the nchat-dev deployment smoke (issue #933).
#
# The smoke is the step that decides whether `CD / Develop` succeeded, so the
# property that matters is that it fails. Each case breaks exactly one thing a
# deploy can get wrong -- a rollout that did not finish, a container in a
# restart back-off, a migration Job that is not Complete, a Service that does
# not answer, a public host that 404s, a protected API that lets an anonymous
# request through -- and requires a non-zero exit.
#
# The last case is the one that is not a refusal: a healthy environment must
# pass, or every case above is satisfied by a smoke that always fails.
#
# A fake kubectl, a fake curl, and a `sleep` that returns immediately, so the
# probe back-off does not make the suite slow. No cluster and no network.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SMOKE="$ROOT_DIR/scripts/deploy/nchat-dev/smoke.sh"
FAKE_BIN="$(mktemp -d "${TMPDIR:-/tmp}/nchat-dev-smoke-bin.XXXXXX")"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-dev-smoke.XXXXXX")"
trap 'rm -rf "$FAKE_BIN" "$WORK"' EXIT

cp "$ROOT_DIR/scripts/ci/testdata/nchat-dev/fake-kubectl" "$FAKE_BIN/kubectl"
cp "$ROOT_DIR/scripts/ci/testdata/nchat-dev/fake-curl" "$FAKE_BIN/curl"
# The probe back-off is real behaviour and is not being tested here; waiting
# out its fifteen seconds for every failing service would make the suite slow
# enough that nobody runs it.
printf '#!/usr/bin/env bash\nexit 0\n' >"$FAKE_BIN/sleep"
chmod +x "$FAKE_BIN/kubectl" "$FAKE_BIN/curl" "$FAKE_BIN/sleep"
PATH="$FAKE_BIN:$PATH"
export PATH

FAILURES=0
CASE=""
CASE_FAILURES=0

fail() { echo "  [FAIL] $CASE: $*" >&2; FAILURES=$((FAILURES + 1)); }
begin() { CASE="$1"; CASE_FAILURES="$FAILURES"; }
pass() { [[ "$FAILURES" -eq "$CASE_FAILURES" ]] && echo "  [OK]   $CASE"; return 0; }

# A healthy nchat-dev: every rollout complete, nothing waiting, the migration
# Job Complete, every Service answering, and the public surface behaving.
new_state() {
  local state
  state="$WORK/state.$RANDOM$RANDOM"
  mkdir -p "$state/status"
  printf 'nchat-dev-deployer' >"$state/context"
  printf 'True' >"$state/migration-condition"
  printf '200' >"$state/status/_"
  printf '401' >"$state/status/_api_chat_sidebar"
  printf '401' >"$state/status/_api_chat_ws"
  printf '%s' "$state"
}

run_smoke() {
  status=0
  output="$(FAKE_STATE_DIR="$1" NCHAT_DEV_HOST="${2-dev.example.invalid}" bash "$SMOKE" 2>&1)" ||
    status=$?
}

assert_status() {
  local what="$1" expected="$2"
  [[ "$status" -eq "$expected" ]] || fail "$what: expected exit $expected, got $status
$output"
}

assert_contains() {
  [[ "$output" == *"$2"* ]] || fail "$1: output does not mention '$2'
$output"
}

echo "--- a healthy environment ---"

begin "a healthy nchat-dev passes"
STATE="$(new_state)"
run_smoke "$STATE"
assert_status "healthy" 0
assert_contains "healthy" "nchat-dev smoke: PASS"
pass

echo
echo "--- workload state ---"

begin "an unfinished rollout fails the smoke"
STATE="$(new_state)"
printf 'chat-service\n' >"$STATE/rollout-fails"
run_smoke "$STATE"
assert_status "rollout" 1
assert_contains "rollout" "deployment/chat-service rollout not complete"
assert_contains "rollout" "nchat-dev smoke: FAIL"
pass

begin "a container in CrashLoopBackOff fails the smoke"
# The failure a readiness-only gate walks past: between two probes the pod
# looks healthy, and it is being restarted on a back-off.
STATE="$(new_state)"
printf 'CrashLoopBackOff\n' >"$STATE/waiting-reasons"
run_smoke "$STATE"
assert_status "crash loop" 1
assert_contains "crash loop" "CrashLoopBackOff"
pass

begin "an image that cannot be pulled fails the smoke"
STATE="$(new_state)"
printf 'ImagePullBackOff\n' >"$STATE/waiting-reasons"
run_smoke "$STATE"
assert_status "image pull" 1
assert_contains "image pull" "ImagePullBackOff"
pass

begin "a container waiting to be created fails the smoke"
STATE="$(new_state)"
printf 'CreateContainerConfigError\n' >"$STATE/waiting-reasons"
run_smoke "$STATE"
assert_status "config error" 1
pass

begin "an ordinary ContainerCreating does not fail the smoke"
# The distinction the check exists to draw: a container being created is not a
# container that will never come up.
STATE="$(new_state)"
printf 'ContainerCreating\n' >"$STATE/waiting-reasons"
run_smoke "$STATE"
assert_status "container creating" 0
pass

echo
echo "--- migrations ---"

begin "a migration Job that is not Complete fails the smoke"
STATE="$(new_state)"
printf 'False' >"$STATE/migration-condition"
run_smoke "$STATE"
assert_status "migration incomplete" 1
assert_contains "migration incomplete" "does not report Complete"
pass

begin "an absent migration Job fails the smoke"
# "The Job is gone" is not "the migration ran".
STATE="$(new_state)"
rm -f "$STATE/migration-condition"
run_smoke "$STATE"
assert_status "migration absent" 1
assert_contains "migration absent" "absent"
pass

echo
echo "--- service reachability ---"

begin "a service that does not answer /healthz fails the smoke"
STATE="$(new_state)"
printf 'auth-service/healthz\n' >"$STATE/probe-fails"
run_smoke "$STATE"
assert_status "healthz" 1
assert_contains "healthz" "auth-service /healthz"
pass

begin "a service that does not answer /readyz fails the smoke"
STATE="$(new_state)"
printf 'search-service/readyz\n' >"$STATE/probe-fails"
run_smoke "$STATE"
assert_status "readyz" 1
assert_contains "readyz" "search-service /readyz"
pass

echo
echo "--- the public surface ---"

begin "a web root that does not serve the application fails the smoke"
STATE="$(new_state)"
printf '502' >"$STATE/status/_"
run_smoke "$STATE"
assert_status "web 502" 1
assert_contains "web 502" "returned '502'"
pass

begin "a host that does not answer at all fails the smoke"
STATE="$(new_state)"
printf 'unreachable' >"$STATE/status/_"
run_smoke "$STATE"
assert_status "unreachable host" 1
assert_contains "unreachable host" "no response"
pass

begin "a protected API answering an anonymous request fails the smoke"
# The failure this exists to catch: an unwired auth middleware ships as a 200.
STATE="$(new_state)"
printf '200' >"$STATE/status/_api_chat_sidebar"
run_smoke "$STATE"
assert_status "open API" 1
assert_contains "open API" "/api/chat/sidebar returned '200'"
pass

begin "a protected API that 404s fails the smoke"
STATE="$(new_state)"
printf '404' >"$STATE/status/_api_chat_sidebar"
run_smoke "$STATE"
assert_status "missing route" 1
pass

begin "a WebSocket route that is not there fails the smoke"
STATE="$(new_state)"
printf '404' >"$STATE/status/_api_chat_ws"
run_smoke "$STATE"
assert_status "ws missing" 1
assert_contains "ws missing" "/api/chat/ws answered '404'"
pass

begin "a gateway that cannot carry an upgrade fails the smoke"
STATE="$(new_state)"
printf '502' >"$STATE/status/_api_chat_ws"
run_smoke "$STATE"
assert_status "ws 502" 1
pass

echo
echo "--- refusing to run at all ---"

begin "no public host is a refusal, not a pass"
STATE="$(new_state)"
run_smoke "$STATE" ""
assert_status "no host" 1
assert_contains "no host" "NCHAT_DEV_HOST"
pass

begin "the wrong kube context is a refusal"
STATE="$(new_state)"
printf 'nchat-prod-deployer' >"$STATE/context"
run_smoke "$STATE"
assert_status "wrong context" 1
assert_contains "wrong context" "nchat-dev-deployer"
pass

begin "several failures are all reported and counted"
STATE="$(new_state)"
printf 'chat-service\n' >"$STATE/rollout-fails"
printf '502' >"$STATE/status/_"
run_smoke "$STATE"
assert_status "several" 1
assert_contains "several" "2 check(s)"
pass

if [[ "$FAILURES" -ne 0 ]]; then
  echo "$FAILURES nchat-dev smoke test(s) failed." >&2
  exit 1
fi
echo
echo "nchat-dev smoke tests passed."
