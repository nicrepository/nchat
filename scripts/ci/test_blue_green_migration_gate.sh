#!/usr/bin/env bash
# Fixture tests for the Blue/Green migration gate (issue #626).
#
# The review found the gate had no fixtures of its own and let DROP CONSTRAINT
# and ALTER TYPE ... RENAME VALUE through. Each pattern it is supposed to catch
# now has a file, and so does each shape it must NOT flag — a gate that fails on
# an ordinary ADD COLUMN would be turned off within a week.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
FIXTURES="$ROOT_DIR/scripts/ci/testdata/blue-green-migrations"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-bg-gate-tests.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# An empty exceptions list: the fixtures must stand on their own content, never
# on being allowlisted.
: >"$WORK/no-exceptions.txt"
export BLUE_GREEN_EXCEPTIONS_FILE="$WORK/no-exceptions.txt"
# shellcheck source=scripts/ci/blue-green-migration-gate.sh
source "$ROOT_DIR/scripts/ci/blue-green-migration-gate.sh"

FAILURES=0

expect_safe() {
  local file="$1"
  if blue_green_check_file "$file" 2>"$WORK/err.txt"; then
    echo "  [OK]   accepted: $(basename "$file")"
    return
  fi
  echo "  [FAIL] rejected a compatible migration: $(basename "$file")" >&2
  sed 's/^/         /' "$WORK/err.txt" >&2
  FAILURES=$((FAILURES + 1))
}

expect_unsafe() {
  local file="$1"
  if blue_green_check_file "$file" 2>"$WORK/err.txt"; then
    echo "  [FAIL] accepted an incompatible migration: $(basename "$file")" >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "  [OK]   rejected: $(basename "$file") ($(cut -d' ' -f2- <"$WORK/err.txt" | head -1))"
}

echo "=== blue/green migration gate ==="
echo
echo "--- compatible migrations are accepted ---"
for file in "$FIXTURES"/safe/*.up.sql; do
  expect_safe "$file"
done

echo
echo "--- incompatible migrations are rejected ---"
for file in "$FIXTURES"/unsafe/*.up.sql; do
  expect_unsafe "$file"
done

echo
echo "--- a declared contract phase is allowed ---"
expect_safe "$FIXTURES/marked/000001_declared_contract.up.sql"

echo
echo "--- a marker with no reason is not a declaration ---"
expect_unsafe "$FIXTURES/marked/000002_marker_without_reason.up.sql"

echo
echo "--- the exceptions list covers historical migrations without editing them ---"
printf 'unsafe/000001_drop_column.up.sql\n' >"$WORK/no-exceptions.txt"
expect_safe "$FIXTURES/unsafe/000001_drop_column.up.sql"
# A comment line must not be read as an entry.
printf '# unsafe/000001_drop_table.up.sql\n' >"$WORK/no-exceptions.txt"
expect_unsafe "$FIXTURES/unsafe/000001_drop_table.up.sql"

echo
echo "--- the repository's own migrations pass ---"
: >"$WORK/no-exceptions.txt"
if BLUE_GREEN_EXCEPTIONS_FILE="$ROOT_DIR/scripts/ci/blue-green-migration-exceptions.txt" \
  bash "$ROOT_DIR/scripts/ci/blue-green-migration-gate.sh" "$ROOT_DIR/migrations" 2>"$WORK/err.txt"; then
  echo "  [OK]   migrations/ passes the gate as committed"
else
  echo "  [FAIL] migrations/ does not pass the gate" >&2
  sed 's/^/         /' "$WORK/err.txt" >&2
  FAILURES=$((FAILURES + 1))
fi

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "blue/green migration gate tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "blue/green migration gate tests passed."
