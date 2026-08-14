#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
LAUNCHER="$ROOT_DIR/scripts/dev/nchat-local.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -x "$LAUNCHER" ]] || fail "launcher must exist and be executable"
bash -n "$LAUNCHER"

help_output="$($LAUNCHER --help)"
for command_name in up down restart status logs check; do
  grep -q "^[[:space:]]*$command_name" <<<"$help_output" || \
    fail "help must document '$command_name'"
done

check_output="$(NCHAT_LOCAL_TEST_MODE=1 "$LAUNCHER" check)"
grep -q "Local prerequisites passed" <<<"$check_output" || \
  fail "check must confirm successful prerequisite validation"

grep -q 'nohup setsid' "$LAUNCHER" || \
  fail "background commands must run in isolated process groups"
grep -q 'kill -- "-$pid"' "$LAUNCHER" || \
  fail "shutdown must terminate each complete process group"
grep -q 'dev-tls-generate.sh' "$LAUNCHER" || \
  fail "launcher must generate the TLS certificate required by the gateway"
grep -q 'rm -f upload-guard' "$LAUNCHER" || \
  fail "shutdown must remove the gateway upload guard"

echo "nchat-local launcher contract passed"
