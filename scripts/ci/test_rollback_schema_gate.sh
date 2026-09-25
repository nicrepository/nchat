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
    "$ROOT_DIR/scripts/ci/blue-green-migration-exceptions.txt" \
    "$ROOT_DIR/scripts/ci/rollback-schema-attestations.txt" "$REPO/scripts/ci/"
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

assert_not_contains() {
  [[ "$output" != *"$2"* ]] || fail "$1: output mentions '$2'
$output"
}

# --- rollback attestations (issue #1008) ---------------------------------------
#
# The policy the gate reads is the one in the checkout it runs in: these write
# the throwaway repository's copy, never the real one.
POLICY_PATH=scripts/ci/rollback-schema-attestations.txt

# Replaces the policy with exactly the given lines.
write_policy() {
  printf '%s\n' "$@" >"$REPO/$POLICY_PATH"
}

# The checksum of a fixture migration, as the migration runner computes it.
fixture_checksum() {
  sha256sum "$REPO/migrations/chat/$1.up.sql" | cut -d ' ' -f 1
}

# Appends an exact attestation of a fixture migration as it is now.
attest() {
  printf '%s chat/%s.up.sql\n' "$(fixture_checksum "$1")" "$1" >>"$REPO/$POLICY_PATH"
}

DROP_CONSTRAINT_SQL='ALTER TABLE messages DROP CONSTRAINT messages_body_check;'

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
echo "--- rollback attestations: one exact file, one exact checksum ---"

begin "an exact attestation allows an otherwise incompatible migration"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
attest 000002_widen_check
run_gate "$TARGET" "$CURRENT"
assert_status "exact attestation" 0
assert_contains "exact attestation" "[ATTESTED] migrations/chat/000002_widen_check.up.sql is explicitly rollback-compatible despite DROP CONSTRAINT (sha256 $(fixture_checksum 000002_widen_check))"
assert_contains "exact attestation" "Schema compatibility: PASS"
assert_not_contains "exact attestation" "ROLLBACK BLOCKED"
pass

begin "the right key with the wrong checksum blocks, as a checksum mismatch"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
write_policy "$(printf '%064d' 0) chat/000002_widen_check.up.sql"
run_gate "$TARGET" "$CURRENT"
assert_status "wrong checksum" 1
assert_contains "wrong checksum" "chat/000002_widen_check.up.sql: DROP CONSTRAINT"
assert_contains "wrong checksum" "checksum mismatch"
assert_contains "wrong checksum" "ROLLBACK BLOCKED"
pass

begin "changing the migration after it was attested invalidates the attestation"
TARGET="$(new_repository)"
printf '%s\n' "$DROP_CONSTRAINT_SQL" >"$REPO/migrations/chat/000002_widen_check.up.sql"
attest 000002_widen_check
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL
-- one line added after the review")"
run_gate "$TARGET" "$CURRENT"
assert_status "changed after review" 1
assert_contains "changed after review" "checksum mismatch"
assert_contains "changed after review" "hashes to $(fixture_checksum 000002_widen_check)"
pass

begin "the right checksum under another key blocks"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
write_policy "$(fixture_checksum 000002_widen_check) chat/000003_widen_check.up.sql"
run_gate "$TARGET" "$CURRENT"
assert_status "other key" 1
assert_contains "other key" "not attested as rollback-compatible"
pass

begin "the same checksum under another domain blocks"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
write_policy "$(fixture_checksum 000002_widen_check) auth/000002_widen_check.up.sql"
run_gate "$TARGET" "$CURRENT"
assert_status "other domain" 1
assert_contains "other domain" "not attested as rollback-compatible"
pass

begin "a contract-phase marker alone still blocks, however exact the policy is otherwise"
TARGET="$(new_repository)"
add_migration 000002_attested "$DROP_CONSTRAINT_SQL" >/dev/null
attest 000002_attested
CURRENT="$(add_migration 000003_declared \
  '-- nchat:blue-green contract-phase the previous release no longer reads this column
ALTER TABLE messages DROP COLUMN legacy_body;')"
run_gate "$TARGET" "$CURRENT"
assert_status "marker only" 1
assert_contains "marker only" "chat/000003_declared.up.sql: DROP COLUMN"
assert_contains "marker only" "chat/000003_declared.up.sql: not attested"
pass

begin "a forward pre-policy exception does not authorise a rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_pre_policy "$DROP_CONSTRAINT_SQL")"
printf 'chat/000002_pre_policy.up.sql\n' >>"$REPO/scripts/ci/blue-green-migration-exceptions.txt"
run_gate "$TARGET" "$CURRENT"
assert_status "forward exception" 1
assert_contains "forward exception" "chat/000002_pre_policy.up.sql: not attested"
pass

begin "an attestation for one migration does not cover another"
TARGET="$(new_repository)"
add_migration 000002_attested "$DROP_CONSTRAINT_SQL" >/dev/null
attest 000002_attested
CURRENT="$(add_migration 000003_unattested 'ALTER TABLE messages DROP CONSTRAINT other_check;')"
run_gate "$TARGET" "$CURRENT"
assert_status "unrelated attestation" 1
assert_contains "unrelated attestation" "[ATTESTED] migrations/chat/000002_attested.up.sql"
assert_contains "unrelated attestation" "chat/000003_unattested.up.sql: not attested"
pass

begin "expand, attested and expand together pass"
TARGET="$(new_repository)"
add_migration 000002_a 'ALTER TABLE messages ADD COLUMN a text;' >/dev/null
add_migration 000003_attested "$DROP_CONSTRAINT_SQL" >/dev/null
attest 000003_attested
CURRENT="$(add_migration 000004_c 'ALTER TABLE messages ADD COLUMN c text;')"
run_gate "$TARGET" "$CURRENT"
assert_status "expand attested expand" 0
assert_contains "expand attested expand" "[OK]       migrations/chat/000002_a.up.sql is expand-only"
assert_contains "expand attested expand" "[ATTESTED] migrations/chat/000003_attested.up.sql"
assert_contains "expand attested expand" "[OK]       migrations/chat/000004_c.up.sql is expand-only"
pass

begin "an attested and an unattested incompatible migration together block"
TARGET="$(new_repository)"
add_migration 000002_attested "$DROP_CONSTRAINT_SQL" >/dev/null
attest 000002_attested
CURRENT="$(add_migration 000003_unattested 'ALTER TABLE messages DROP COLUMN body;')"
run_gate "$TARGET" "$CURRENT"
assert_status "attested plus unattested" 1
assert_contains "attested plus unattested" "chat/000003_unattested.up.sql: DROP COLUMN"
assert_contains "attested plus unattested" "ROLLBACK BLOCKED"
pass

begin "an expand-only migration needs no attestation"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_add_column 'ALTER TABLE messages ADD COLUMN edited_at timestamptz;')"
write_policy '# no attestations at all'
run_gate "$TARGET" "$CURRENT"
assert_status "expand without policy entry" 0
assert_contains "expand without policy entry" "000002_add_column.up.sql is expand-only"
pass

echo
echo "--- the policy is either valid or the gate stops ---"

MALFORMED_POLICIES=(
  "short checksum|$(printf '%063d' 0) chat/000002_widen_check.up.sql"
  "uppercase checksum|$(printf 'A%063d' 0) chat/000002_widen_check.up.sql"
  "key without a domain|$(printf '%064d' 0) 000002_widen_check.up.sql"
  "path traversal|$(printf '%064d' 0) ../../foo.sql"
  "traversal inside a key|$(printf '%064d' 0) chat/../000002_widen_check.up.sql"
  "absolute path|$(printf '%064d' 0) /migrations/chat/000002_widen_check.up.sql"
  "wildcard|$(printf '%064d' 0) chat/*.up.sql"
  "glob in the name|$(printf '%064d' 0) chat/00000?_widen_check.up.sql"
  "down migration|$(printf '%064d' 0) chat/000002_widen_check.down.sql"
  "extra column|$(printf '%064d' 0) chat/000002_widen_check.up.sql reviewed"
  "inline comment|$(printf '%064d' 0) chat/000002_widen_check.up.sql # reviewed"
  "checksum only|$(printf '%064d' 0)"
  "key only|chat/000002_widen_check.up.sql"
)
for malformed in "${MALFORMED_POLICIES[@]}"; do
  begin "a malformed policy fails the gate closed: ${malformed%%|*}"
  TARGET="$(new_repository)"
  CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
  write_policy '# a comment is fine' '' "${malformed#*|}"
  run_gate "$TARGET" "$CURRENT"
  assert_status "${malformed%%|*}" 1
  assert_contains "${malformed%%|*}" "line 3 is malformed"
  assert_contains "${malformed%%|*}" "policy $REPO/$POLICY_PATH is malformed"
  pass
done

begin "the same key twice fails closed, even with the same checksum"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
attest 000002_widen_check
attest 000002_widen_check
run_gate "$TARGET" "$CURRENT"
assert_status "duplicate same checksum" 1
assert_contains "duplicate same checksum" "names chat/000002_widen_check.up.sql again"
pass

begin "the same key with two checksums fails closed"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
attest 000002_widen_check
printf '%s chat/000002_widen_check.up.sql\n' "$(printf '%064d' 0)" >>"$REPO/$POLICY_PATH"
run_gate "$TARGET" "$CURRENT"
assert_status "duplicate other checksum" 1
assert_contains "duplicate other checksum" "names chat/000002_widen_check.up.sql again"
pass

# A policy that exists but is broken is a configuration error on every run, not
# only on the day a migration needs it -- otherwise it would sit unnoticed.
begin "a malformed policy fails closed even when every migration is expand-only"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_add_column 'ALTER TABLE messages ADD COLUMN edited_at timestamptz;')"
write_policy "not a policy line"
run_gate "$TARGET" "$CURRENT"
assert_status "malformed with expand only" 1
assert_contains "malformed with expand only" "is malformed"
assert_not_contains "malformed with expand only" "Schema compatibility: PASS"
pass

begin "the same release passes with the real policy"
TARGET="$(new_repository)"
run_gate "$TARGET" "$TARGET"
assert_status "same release, valid policy" 0
assert_contains "same release, valid policy" "by definition compatible"
pass

begin "the same release passes with no policy at all"
TARGET="$(new_repository)"
rm "$REPO/$POLICY_PATH"
run_gate "$TARGET" "$TARGET"
assert_status "same release, no policy" 0
assert_contains "same release, no policy" "by definition compatible"
pass

# The same-release shortcut is still a run of the gate: a broken policy must not
# have a path to success that hides it.
begin "a malformed policy fails closed even for the same release"
TARGET="$(new_repository)"
write_policy "not a policy line"
run_gate "$TARGET" "$TARGET"
assert_status "same release, malformed policy" 1
assert_contains "same release, malformed policy" "rollback attestation policy $REPO/$POLICY_PATH is malformed"
assert_not_contains "same release, malformed policy" "by definition compatible"
pass

begin "a missing policy cannot attest an incompatible migration"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
rm "$REPO/$POLICY_PATH"
run_gate "$TARGET" "$CURRENT"
assert_status "missing policy" 1
assert_contains "missing policy" "absent or unreadable"
assert_contains "missing policy" "ROLLBACK BLOCKED"
pass

# Unreadable without depending on file modes, which root ignores: a directory
# where the policy should be makes the read itself fail.
begin "an unreadable policy cannot attest an incompatible migration"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_widen_check "$DROP_CONSTRAINT_SQL")"
rm "$REPO/$POLICY_PATH"
mkdir "$REPO/$POLICY_PATH"
run_gate "$TARGET" "$CURRENT"
assert_status "unreadable policy" 1
assert_contains "unreadable policy" "absent or unreadable"
pass

begin "a missing policy does not stop an expand-only rollback"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_add_column 'ALTER TABLE messages ADD COLUMN edited_at timestamptz;')"
rm "$REPO/$POLICY_PATH"
run_gate "$TARGET" "$CURRENT"
assert_status "expand without policy" 0
pass

# The scan of a file that cannot be opened finds nothing, and nothing used to
# read as expand-only. A migration the checkout does not hold is never a pass.
begin "a migration missing from the checkout blocks, even an expand-only one"
TARGET="$(new_repository)"
CURRENT="$(add_migration 000002_add_column 'ALTER TABLE messages ADD COLUMN edited_at timestamptz;')"
rm "$REPO/migrations/chat/000002_add_column.up.sql"
run_gate "$TARGET" "$CURRENT"
assert_status "missing migration" 1
assert_contains "missing migration" "chat/000002_add_column.up.sql: the migration cannot be read"
assert_not_contains "missing migration" "is expand-only"
pass

echo
echo "--- the real policy and chat/000052 (the production refusal of issue #1008) ---"

REAL_000052=migrations/chat/000052_link_targets_convergence_and_previews.up.sql

# Runs one gate function against the real checkout, without running the gate.
gate_function() {
  status=0
  output="$(bash -c 'source "$1"; shift; "$@"' _ "$GATE" "$@" 2>&1)" || status=$?
}

begin "the gate computes checksums exactly as the migration runner pins them"
# migrate.sh runs on load, so only its migration_checksum definition is taken.
runner_checksum="$(bash -c 'set -Eeuo pipefail
  source <(sed -n "/^migration_checksum() {/,/^}/p" "$1")
  migration_checksum "$2"' _ "$ROOT_DIR/scripts/db/migrate.sh" "$ROOT_DIR/$REAL_000052")"
[[ "$runner_checksum" =~ ^[a-f0-9]{64}$ ]] || fail "could not run migrate.sh's migration_checksum: '$runner_checksum'"
gate_function rollback_migration_checksum "$ROOT_DIR/$REAL_000052"
assert_status "gate checksum" 0
[[ "$output" == "$runner_checksum" ]] || fail "the gate hashes 000052 to '$output', the runner to '$runner_checksum'"
gate_function rollback_migration_checksum "$ROOT_DIR/does/not/exist.up.sql"
[[ "$status" -ne 0 ]] || fail "a missing file produced a checksum: '$output'"
pass

begin "the real policy is valid, and every entry names an existing migration by its current checksum"
gate_function rollback_attestations_load
assert_status "real policy" 0
[[ -n "$output" ]] || fail "the real policy attests nothing"
while read -r checksum key; do
  [[ -f "$ROOT_DIR/migrations/$key" ]] || { fail "attested migration $key does not exist"; continue; }
  actual="$(sha256sum "$ROOT_DIR/migrations/$key" | cut -d ' ' -f 1)"
  [[ "$actual" == "$checksum" ]] || fail "$key is attested as $checksum but hashes to $actual"
done <<<"$output"
pass

begin "000052 still trips the scanner: the attestation does not teach it to ignore DROP CONSTRAINT"
gate_function blue_green_scan_file "$ROOT_DIR/$REAL_000052"
assert_contains "000052 scan" "chat/000052_link_targets_convergence_and_previews.up.sql: DROP CONSTRAINT"
pass

begin "the real attestation recognises 000052 by its exact key and checksum"
REAL_ATTESTATIONS="$(bash -c 'source "$1"; rollback_attestations_load' _ "$GATE")"
gate_function rollback_attestation_verdict "$REAL_000052" "$REAL_ATTESTATIONS" 0
assert_status "000052 verdict" 0
[[ "$output" == "$(sha256sum "$ROOT_DIR/$REAL_000052" | cut -d ' ' -f 1)" ]] ||
  fail "000052 was not attested by its checksum: $output"
pass

# The production refusal, rebuilt as real history in the throwaway repository
# with the real file and the real policy, so it needs neither the two release
# commits nor a full clone.
begin "a rollback across the real 000052 passes with the real policy"
TARGET="$(new_repository)"
cp "$ROOT_DIR/$REAL_000052" "$REPO/$REAL_000052"
git_quiet add -A
git_quiet commit -m 000052
CURRENT="$(git -C "$REPO" rev-parse HEAD)"
run_gate "$TARGET" "$CURRENT"
assert_status "real 000052" 0
assert_contains "real 000052" "[ATTESTED] $REAL_000052 is explicitly rollback-compatible despite DROP CONSTRAINT"
pass

begin "a copy of 000052 changed by one byte is not attested"
TARGET="$(new_repository)"
cp "$ROOT_DIR/$REAL_000052" "$REPO/$REAL_000052"
printf ' ' >>"$REPO/$REAL_000052"
git_quiet add -A
git_quiet commit -m 000052-edited
CURRENT="$(git -C "$REPO" rev-parse HEAD)"
run_gate "$TARGET" "$CURRENT"
assert_status "edited 000052" 1
assert_contains "edited 000052" "checksum mismatch"
assert_contains "edited 000052" "ROLLBACK BLOCKED"
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
