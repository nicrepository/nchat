#!/usr/bin/env bash
# Negative tests for the one-migration-per-ordinal rule (issue #928).
#
# The rule exists because integrating two branches produced two migrations at
# chat/000055 and every gate in the repository passed: to git they are different
# filenames, and the runner keys applied rows on the whole basename, so nothing
# asserted that a number identifies one migration. The number is the order, and
# two migrations claiming one ordinal have no order between them.
#
# The exceptions list is the part worth testing hardest. It grandfathers three
# collisions that are already applied in running environments and therefore
# cannot be renumbered — and a list that merely said "chat/000050 is allowed to
# collide" would pre-authorise every future collision at that number. So an
# entry names the exact set, and these cases are what hold it to that.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-ordinal-gate-test.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

FAILURES=0

# migration writes a minimal valid pair, so the run reaches the ordinal check
# rather than failing an earlier one.
migration() {
  local domain base dir column
  domain="$1"
  base="$2"
  dir="$WORK/migrations/$domain"
  column="${base//[^a-zA-Z0-9]/_}"
  mkdir -p "$dir"
  printf 'BEGIN;\nALTER TABLE %s.t ADD COLUMN c_%s TEXT;\nCOMMIT;\n' \
    "$domain" "$column" >"$dir/$base.up.sql"
  printf 'BEGIN;\nALTER TABLE %s.t DROP COLUMN c_%s;\nCOMMIT;\n' \
    "$domain" "$column" >"$dir/$base.down.sql"
}

reset_tree() {
  rm -rf "$WORK/migrations"
  : >"$WORK/exceptions.txt"
}

# run_gate runs only the ordinal section's verdict: the check as a whole makes
# other assertions about these synthetic files, so the outcome under test is
# whether it reported an ordinal problem.
run_gate() {
  MIGRATIONS_DIR_OVERRIDE="$WORK/migrations" \
    ORDINAL_EXCEPTIONS_FILE="$WORK/exceptions.txt" \
    bash "$ROOT_DIR/scripts/ci/migrations-check.sh" >"$WORK/out.txt" 2>&1 || true
  grep -qE 'duplicate migration number|ordinal exception' "$WORK/out.txt"
}

expect_ordinal_ok() {
  local what="$1"
  if run_gate; then
    echo "  [FAIL] $what was reported as an ordinal problem" >&2
    sed -n '/one migration per ordinal/,/^$/p' "$WORK/out.txt" | sed 's/^/         /' >&2
    FAILURES=$((FAILURES + 1))
  else
    echo "  [OK]   accepted: $what"
  fi
}

expect_ordinal_rejected() {
  local what="$1"
  if run_gate; then
    echo "  [OK]   rejected: $what"
  else
    echo "  [FAIL] $what was accepted" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

echo "=== migration ordinal gate tests ==="
echo

echo "--- ordinals with one migration each ---"
reset_tree
migration chat 000001_first
migration chat 000002_second
migration auth 000001_unrelated_domain
expect_ordinal_ok "distinct ordinals, and the same ordinal in another domain"

echo
echo "--- a new collision, with no exception ---"
reset_tree
migration chat 000001_first
migration chat 000001_second
expect_ordinal_rejected "two migrations at chat/000001"

echo
echo "--- a grandfathered collision, exactly as recorded ---"
reset_tree
migration chat 000050_alpha
migration chat 000050_beta
printf 'chat/000050|000050_alpha|000050_beta\n' >"$WORK/exceptions.txt"
expect_ordinal_ok "the recorded pair at chat/000050"

echo
echo "--- the same exception, order reversed ---"
reset_tree
migration chat 000050_alpha
migration chat 000050_beta
printf 'chat/000050|000050_beta|000050_alpha\n' >"$WORK/exceptions.txt"
expect_ordinal_ok "the recorded pair listed in the other order"

# The case the second Code Quality review named: an exception keyed only on the
# ordinal would let a third file in forever.
echo
echo "--- a third migration at a grandfathered ordinal ---"
reset_tree
migration chat 000050_alpha
migration chat 000050_beta
migration chat 000050_gamma
printf 'chat/000050|000050_alpha|000050_beta\n' >"$WORK/exceptions.txt"
expect_ordinal_rejected "a third migration at chat/000050"

echo
echo "--- a recorded migration removed ---"
reset_tree
migration chat 000050_alpha
printf 'chat/000050|000050_alpha|000050_beta\n' >"$WORK/exceptions.txt"
expect_ordinal_rejected "chat/000050 no longer describes a duplicate"

echo
echo "--- a recorded basename replaced by another ---"
reset_tree
migration chat 000050_alpha
migration chat 000050_renamed
printf 'chat/000050|000050_alpha|000050_beta\n' >"$WORK/exceptions.txt"
expect_ordinal_rejected "an unexpected basename at chat/000050"

echo
echo "--- an exception for a different ordinal does not cover this one ---"
reset_tree
migration chat 000051_alpha
migration chat 000051_beta
printf 'chat/000050|000051_alpha|000051_beta\n' >"$WORK/exceptions.txt"
expect_ordinal_rejected "a collision at chat/000051 with only chat/000050 recorded"

echo
echo "--- the exception must not be a prefix or substring match ---"
reset_tree
migration chat 000050_alpha
migration chat 000050_alpha_extended
printf 'chat/000050|000050_alpha|000050_alpha_ext\n' >"$WORK/exceptions.txt"
expect_ordinal_rejected "a basename the recorded one is a prefix of"

echo
echo "--- a malformed exception line is refused rather than ignored ---"
reset_tree
migration chat 000050_alpha
migration chat 000050_beta
printf 'chat/000050\n' >"$WORK/exceptions.txt"
expect_ordinal_rejected "an exception with no basename list"

echo
echo "--- the repository's own migrations pass ---"
if bash "$ROOT_DIR/scripts/ci/migrations-check.sh" >"$WORK/real.txt" 2>&1; then
  echo "  [OK]   migrations/ passes the gate as committed"
else
  echo "  [FAIL] migrations/ does not pass the gate" >&2
  sed 's/^/         /' "$WORK/real.txt" >&2
  FAILURES=$((FAILURES + 1))
fi

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "migration ordinal gate tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "migration ordinal gate tests passed."
