#!/usr/bin/env bash
# Behaviour tests for the rollback schema compatibility gate (issues #801, #933).
#
# The property under test is narrow and worth restating: can the release on the
# slot a rollback targets still serve against the schema as it now stands? That
# is decided by the migrations added between the two releases, and by nothing
# else this gate can see.
#
# The cases are built as real git history in a throwaway repository, because
# the gate answers by walking the object graph between two commits. A fixture
# that handed it a list of files would test a function nobody calls.
#
# The asymmetry with the forward gate is deliberate and is tested here: a
# migration that declares itself contract-phase is *accepted* going forwards
# and *refused* going back. "No slot depends on the old shape any more" is a
# statement about the future, and the slot being rolled back to is precisely
# one that depends on it.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GATE="$ROOT_DIR/scripts/deploy/nchat-prod/rollback-schema-gate.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-rollback-schema.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

FAILURES=0
CASE=""
CASE_FAILURES=0

fail() { echo "  [FAIL] $CASE: $*" >&2; FAILURES=$((FAILURES + 1)); }
begin() { CASE="$1"; CASE_FAILURES="$FAILURES"; }
pass() { [[ "$FAILURES" -eq "$CASE_FAILURES" ]] && echo "  [OK]   $CASE"; return 0; }

# A checkout the gate can run inside: the real scripts, and a migrations tree
# whose history each case writes.
REPO="$WORK/repo"

git_quiet() {
  git -C "$REPO" -c user.email=ci@example.invalid -c user.name=CI "$@" >/dev/null 2>&1
}

new_repository() {
  rm -rf "$REPO"
  mkdir -p "$REPO/scripts/deploy/nchat-prod" "$REPO/scripts/ci" "$REPO/migrations/chat"
  cp "$ROOT_DIR/scripts/deploy/nchat-prod/rollback-schema-gate.sh" \
    "$ROOT_DIR/scripts/deploy/nchat-prod/lib.sh" "$REPO/scripts/deploy/nchat-prod/"
  cp "$ROOT_DIR/scripts/ci/blue-green-migration-gate.sh" \
    "$ROOT_DIR/scripts/ci/blue-green-migration-exceptions.txt" "$REPO/scripts/ci/"
  git -C "$REPO" init --quiet
  printf -- '-- baseline\nCREATE TABLE messages (id uuid PRIMARY KEY);\n' \
    >"$REPO/migrations/chat/000001_baseline.up.sql"
  git_quiet add -A
  git_quiet commit -m baseline
  git -C "$REPO" rev-parse HEAD
}

# Adds one migration and returns the commit that introduced it.
add_migration() {
  local name="$1" body="$2"
  printf '%s\n' "$body" >"$REPO/migrations/chat/$name.up.sql"
  git_quiet add -A
  git_quiet commit -m "$name"
  git -C "$REPO" rev-parse HEAD
}

run_gate() {
  status=0
  output="$(cd "$REPO" && bash "$REPO/scripts/deploy/nchat-prod/rollback-schema-gate.sh" "$1" "$2" 2>&1)" ||
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

echo "--- expand-only migrations keep a rollback possible ---"

begin "adding a nullable column does not block a rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_add_column \
  'ALTER TABLE messages ADD COLUMN edited_at timestamptz;')"
run_gate "$TARGET" "$CURRENT"
assert_status "nullable column" 0
assert_contains "nullable column" "Schema compatibility: PASS"
pass

begin "adding a table does not block a rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_add_table \
  'CREATE TABLE reactions (id uuid PRIMARY KEY, message_id uuid NOT NULL);')"
run_gate "$TARGET" "$CURRENT"
assert_status "new table" 0
pass

begin "adding an index does not block a rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_add_index \
  'CREATE INDEX messages_created_idx ON messages (id);')"
run_gate "$TARGET" "$CURRENT"
assert_status "new index" 0
pass

begin "several expand-only migrations are all reported and all pass"
TARGET="$(new_repository)"
add_migration 000002_a 'ALTER TABLE messages ADD COLUMN a text;' >/dev/null
add_migration 000003_b 'ALTER TABLE messages ADD COLUMN b text;' >/dev/null
CURRENT="$(add_migration 000004_c 'ALTER TABLE messages ADD COLUMN c text;')"
run_gate "$TARGET" "$CURRENT"
assert_status "three expands" 0
assert_contains "three expands" "migrations added between"
assert_contains "three expands" "000004_c.up.sql is expand-only"
pass

begin "no migrations at all between the two releases passes"
TARGET="$(new_repository)"
printf 'a change that is not a migration\n' >"$REPO/scripts/ci/note.txt"
git_quiet add -A
git_quiet commit -m note
CURRENT="$(git -C "$REPO" rev-parse HEAD)"
run_gate "$TARGET" "$CURRENT"
assert_status "no migrations" 0
assert_contains "no migrations" "migrations added between"
pass

begin "rolling back to the release already serving is compatible by definition"
TARGET="$(new_repository)"
run_gate "$TARGET" "$TARGET"
assert_status "same release" 0
assert_contains "same release" "by definition compatible"
pass

echo
echo "--- contract-phase migrations block a rollback ---"

begin "a dropped column blocks the rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_drop_column \
  'ALTER TABLE messages DROP COLUMN body;')"
run_gate "$TARGET" "$CURRENT"
assert_status "drop column" 1
assert_contains "drop column" "DROP COLUMN"
assert_contains "drop column" "ROLLBACK BLOCKED"
pass

begin "a dropped table blocks the rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_drop_table 'DROP TABLE reactions;')"
run_gate "$TARGET" "$CURRENT"
assert_status "drop table" 1
assert_contains "drop table" "DROP TABLE"
pass

begin "a rename blocks the rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_rename \
  'ALTER TABLE messages RENAME COLUMN body TO content;')"
run_gate "$TARGET" "$CURRENT"
assert_status "rename" 1
assert_contains "rename" "RENAME COLUMN"
pass

begin "SET NOT NULL blocks the rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_not_null \
  'ALTER TABLE messages ALTER COLUMN edited_at SET NOT NULL;')"
run_gate "$TARGET" "$CURRENT"
assert_status "set not null" 1
assert_contains "set not null" "SET NOT NULL"
pass

begin "an incompatible type change blocks the rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_retype \
  'ALTER TABLE messages ALTER COLUMN id TYPE text;')"
run_gate "$TARGET" "$CURRENT"
assert_status "type change" 1
assert_contains "type change" "column type change"
pass

begin "removing an enum value blocks the rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_enum \
  "ALTER TYPE message_kind RENAME VALUE 'draft' TO 'pending';")"
run_gate "$TARGET" "$CURRENT"
assert_status "enum value" 1
assert_contains "enum value" "RENAME VALUE"
pass

begin "a dropped constraint blocks the rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_drop_constraint \
  'ALTER TABLE messages DROP CONSTRAINT messages_body_check;')"
run_gate "$TARGET" "$CURRENT"
assert_status "drop constraint" 1
assert_contains "drop constraint" "DROP CONSTRAINT"
pass

begin "a declared contract-phase migration still blocks the rollback"
# Accepted by the forward gate, refused here: the declaration says no slot
# depends on the old shape any more, and the rollback target is a slot that
# does.
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_declared \
  '-- nchat:blue-green contract-phase the previous release no longer reads this column
ALTER TABLE messages DROP COLUMN legacy_body;')"
run_gate "$TARGET" "$CURRENT"
assert_status "declared contract phase" 1
assert_contains "declared contract phase" "DROP COLUMN"
pass

begin "one incompatible migration among several expands blocks the rollback"
TARGET="$(new_repository)"
add_migration 000002_a 'ALTER TABLE messages ADD COLUMN a text;' >/dev/null
add_migration 000003_drop 'ALTER TABLE messages DROP COLUMN a;' >/dev/null
CURRENT="$(add_migration 000004_c 'ALTER TABLE messages ADD COLUMN c text;')"
run_gate "$TARGET" "$CURRENT"
assert_status "one bad among many" 1
assert_contains "one bad among many" "000003_drop.up.sql"
pass

begin "the refusal names the incident procedure and never a down migration"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_drop 'DROP TABLE messages;')"
run_gate "$TARGET" "$CURRENT"
assert_status "guidance" 1
assert_contains "guidance" "production-blue-green-deployment.md"
assert_contains "guidance" "Do not run a down migration"
pass

echo
echo "--- unusable input is never a pass ---"

begin "a SHA that is not a commit is refused"
TARGET="$(new_repository)"
run_gate "$TARGET" "0000000000000000000000000000000000000000"
assert_status "unknown commit" 1
assert_contains "unknown commit" "not a commit in this checkout"
pass

begin "an abbreviated SHA is refused"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_a 'ALTER TABLE messages ADD COLUMN a text;')"
run_gate "${TARGET:0:12}" "$CURRENT"
assert_status "abbreviated" 1
assert_contains "abbreviated" "full 40-character commit SHA"
pass

begin "a missing argument is refused"
new_repository >/dev/null
status=0
output="$(cd "$REPO" && bash "$REPO/scripts/deploy/nchat-prod/rollback-schema-gate.sh" 2>&1)" || status=$?
assert_status "no arguments" 1
assert_contains "no arguments" "usage"
pass

if [[ "$FAILURES" -ne 0 ]]; then
  echo "$FAILURES rollback schema gate test(s) failed." >&2
  exit 1
fi
echo
echo "Rollback schema gate tests passed."
