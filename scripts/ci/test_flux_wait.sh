#!/usr/bin/env bash
# Tests for scripts/deploy/flux-wait.sh: success only when every named status
# is success, a failed one fails at once, silence fails at the timeout, and the
# token never reaches curl's argv.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
WAIT="$ROOT_DIR/scripts/deploy/flux-wait.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/flux-wait-test.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
FAILURES=0
REPO=https://git.example.test/group/infra.git
COMMIT=3333333333333333333333333333333333333333

fail() {
  echo "  FAIL: $*" >&2
  FAILURES=$((FAILURES + 1))
}

# A curl that records its arguments and answers with $STATUSES.
mkdir -p "$WORK/bin"
cat >"$WORK/bin/curl" <<'CURL'
#!/usr/bin/env bash
printf '%s\n' "$@" >>"$CURL_ARGS"
printf '%s' "$STATUSES"
CURL
chmod +x "$WORK/bin/curl"

run() {
  PATH="$WORK/bin:$PATH" CURL_ARGS="$WORK/args" FLUX_STATUS_TOKEN="${TOKEN-secret-token}" \
    FLUX_WAIT_TIMEOUT_SECONDS=1 FLUX_WAIT_POLL_SECONDS=0 "$WAIT" "$@" >"$WORK/out" 2>&1
}

both='[{"name":"ns/producao-migracoes","status":"success"},{"name":"ns/producao","status":"success"}]'
STATUSES="$both" run "$REPO" "$COMMIT" ns/producao-migracoes ns/producao ||
  fail "every status success was not accepted: $(cat "$WORK/out")"
grep -q 'https://git.example.test/api/v4/projects/group%2Finfra/repository/commits/3333333333333333333333333333333333333333/statuses' "$WORK/args" ||
  fail "the statuses URL is wrong: $(cat "$WORK/args")"
! grep -q secret-token "$WORK/args" || fail "the token reached curl's argv"

rm -f "$WORK/args"
STATUSES="$both" FLUX_GIT_SERVER_ADDRESS=203.0.113.7 run "$REPO" "$COMMIT" ns/producao ||
  fail "a direct address was not accepted: $(cat "$WORK/out")"
grep -qx 'git.example.test:443:203.0.113.7' "$WORK/args" || fail "the direct address did not reach curl as --resolve"
STATUSES="$both" FLUX_GIT_SERVER_ADDRESS='1.2.3.4 --insecure' run "$REPO" "$COMMIT" ns/producao &&
  fail "a malformed direct address was accepted"
rm -f "$WORK/args"
STATUSES="$both" run "$REPO" "$COMMIT" ns/producao
grep -q -- '--resolve' "$WORK/args" && fail "--resolve was used without an address"

STATUSES='[{"name":"ns/producao","status":"success"}]' run "$REPO" "$COMMIT" ns/producao-migracoes ns/producao &&
  fail "a missing status was accepted"
STATUSES='[{"name":"ns/producao","status":"failed","description":"health check failed"}]' run "$REPO" "$COMMIT" ns/producao &&
  fail "a failed status was accepted"
grep -q 'health check failed' "$WORK/out" || fail "the failure does not carry Flux's description"
STATUSES='[{"name":"other/producao","status":"success"}]' run "$REPO" "$COMMIT" ns/producao &&
  fail "another namespace's status was accepted"
STATUSES="$both" run "$REPO" abc ns/producao && fail "a short commit was accepted"
STATUSES="$both" run http://git.example.test/group/infra.git "$COMMIT" ns/producao && fail "a plain-http repository was accepted"
STATUSES="$both" TOKEN="" run "$REPO" "$COMMIT" ns/producao && fail "a missing token was accepted"
STATUSES="$both" run "$REPO" "$COMMIT" && fail "no status name was accepted"

if [[ "$FAILURES" -ne 0 ]]; then
  echo "flux-wait tests: $FAILURES failure(s)" >&2
  exit 1
fi
echo "flux-wait tests passed."
