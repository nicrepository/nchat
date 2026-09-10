#!/usr/bin/env bash
# Behaviour tests for the rollback schema-compatibility gate (CICD-08).
#
# The gate decides whether returning production traffic to an older release is a
# selector change or a database incident, so what is proved here is refusal
# first: a ledger nobody proved was read, an applied migration that cannot be
# correlated with this checkout, a migration the target expects and the schema
# does not hold, a contract-phase migration applied since the target's release,
# an unresolvable commit and a malformed identity must all block, and none of
# them may be reported as "nothing found, therefore compatible".
#
# The case this file exists for is `a migration that completed for a release
# nothing is running`. deploy.sh runs the migration Job BEFORE it applies the
# candidate workloads and before it waits for them, so a release can advance the
# schema and then fail to roll out: the schema moves and no Pod, Deployment or
# slot annotation anywhere carries that release. The gate's first design read
# the releases observed on the workloads, which reports that release as never
# having happened -- and cleared a rollback straight across its migration. The
# ledger is what closes that, and the scenario is reproduced end to end below
# rather than asserted as a string.
#
# The positive half is proved too, because a gate that refused everything would
# pass every negative test and make rollback impossible.
#
# Each case is a throwaway git repository with real commits plus a ledger file
# in the shape applied-migrations.sh emits. No cluster, no database, no network.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
GATE="$ROOT_DIR/scripts/deploy/nchat-prod/rollback-schema-gate.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-rollback-schema-test.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

FAILURES=0
CASE=""
CASE_FAILURES=0

begin() { CASE="$1"; CASE_FAILURES="$FAILURES"; }
fail() { echo "  [FAIL] $CASE: $*" >&2; FAILURES=$((FAILURES + 1)); }
pass() { [[ "$FAILURES" -eq "$CASE_FAILURES" ]] || return 0; echo "  [OK]   $CASE"; }

# The header applied-migrations.sh writes, and the reason an empty file is not
# the same thing as an empty ledger.
HEADER='# nchat-applied-migrations v1'
# An expand-only migration: it adds, and takes nothing away.
EXPAND_SQL='ALTER TABLE chat.messages ADD COLUMN pinned boolean;'
# The same shape, declared contract-phase the way the migration policy requires.
CONTRACT_SQL='-- nchat:blue-green contract-phase the previous release no longer reads it
ALTER TABLE chat.messages DROP COLUMN legacy_body;'

new_repo() {
  local repo="$WORK/repo.$1"
  rm -rf "$repo"
  mkdir -p "$repo/migrations/chat" "$repo/migrations/auth"
  git -C "$repo" init --quiet
  git -C "$repo" config user.email nchat-tests@example.invalid
  git -C "$repo" config user.name "nchat tests"
  git -C "$repo" config commit.gpgsign false
  commit_file "$repo" README.md 'base' 'base'
  printf '%s' "$repo"
}

commit_file() {
  local repo="$1" path="$2" content="$3" message="$4"
  mkdir -p "$repo/$(dirname "$path")"
  printf '%s\n' "$content" >"$repo/$path"
  git -C "$repo" add -A
  git -C "$repo" commit --quiet -m "$message"
}

head_of() { git -C "$1" rev-parse HEAD; }

# The checksum the migration runner stores: sha256 of the whole up file.
checksum_of() { sha256sum "$1" | cut -d ' ' -f 1; }

# A ledger in the shape applied-migrations.sh emits -- built by that script's
# own normaliser, from rows in the shape the database really stores.
#
# Arguments are "<domain>/<base>" as PostgreSQL holds them: no ".up.sql", because
# migrate.sh strips it before persisting (parse_up_file: MFILE=...%). The
# canonical path is never written by hand here. If a fixture spelled the
# canonical form itself, this suite would keep passing while the reader and the
# database disagreed -- which is the defect this shape exists to prevent.
write_ledger() {
  local repo="$1" name="$2" file key domain base
  shift 2
  file="$WORK/ledger.$name"
  printf '%s\n' "$HEADER" >"$file"
  for key in "$@"; do
    domain="${key%%/*}"
    base="${key#*/}"
    (
      # shellcheck source=scripts/deploy/nchat-prod/applied-migrations.sh
      source "$ROOT_DIR/scripts/deploy/nchat-prod/applied-migrations.sh"
      canonical_migration "$domain $base $(checksum_of "$repo/migrations/$domain/$base.up.sql")"
      printf '\n'
    ) >>"$file"
  done
  printf '%s' "$file"
}

run_gate() {
  local repo="$1" target="$2" ledger="$3"
  (cd "$repo" && bash "$GATE" migrations "$target" "$ledger") \
    >"$WORK/out.txt" 2>"$WORK/err.txt"
}

expect_exit() {
  local expected="$1" actual="$2"
  [[ "$actual" == "$expected" ]] || fail "exit $actual, expected $expected: $(tail -3 "$WORK/err.txt")"
}

expect_blocked() {
  local status="$1" needle="$2"
  expect_exit 1 "$status"
  grep -q "rollback blocked; incident/DB recovery required" "$WORK/err.txt" ||
    fail "did not name the incident path"
  grep -q "$needle" "$WORK/err.txt" || fail "did not report: $needle"
  if grep -q "COMPATIBLE" "$WORK/out.txt"; then
    fail "claimed compatibility while blocking"
  fi
}

expect_compatible() {
  local status="$1"
  expect_exit 0 "$status"
  grep -q "rollback schema gate: COMPATIBLE" "$WORK/out.txt" ||
    fail "did not report a compatible verdict"
}

echo "=== rollback schema gate ==="

# ---------------------------------------------------------------------------
# The HIGH finding, reproduced.
# ---------------------------------------------------------------------------
#
# A is the release on the rollback target and is Ready. D is a later release
# whose migration Job COMPLETED and whose rollout then failed, so nothing
# anywhere is running D. D's migration is contract-phase: it took away something
# A depends on. The ledger holds D's migration because the database applied it,
# and that is the only place the fact exists.
begin "a completed migration for a release nothing is running still blocks the rollback"
repo="$(new_repo never-applied)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000101_drop.up.sql "$CONTRACT_SQL" 'release D'
ledger="$(write_ledger "$repo" never-applied chat/000101_drop)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "chat/000101_drop.up.sql was applied after release $target"
grep -q "not expand-only" "$WORK/err.txt" || fail "did not say why the schema no longer fits"
pass

# The same shape with an expand-only migration is the ordinary case and must
# still be allowed: the fix must not turn every failed deploy into a blocked
# rollback, only the ones that took something away.
begin "a completed expand-only migration for a release nothing is running is compatible"
repo="$(new_repo never-applied-expand)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000100_pin.up.sql "$EXPAND_SQL" 'release D'
ledger="$(write_ledger "$repo" never-applied-expand chat/000100_pin)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

# A migration whose Job FAILED never reaches the ledger -- the runner records a
# row only after the statement succeeded, and a half-applied one is recorded
# dirty, which applied-migrations.sh refuses outright. So the gate's view of a
# failed migration is a ledger without it, and that must not block.
begin "a migration that failed is not in the ledger and is not treated as applied"
repo="$(new_repo failed-migration)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000101_drop.up.sql "$CONTRACT_SQL" 'release D, migration failed'
ledger="$(write_ledger "$repo" failed-migration)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

# ---------------------------------------------------------------------------
# The ordinary contract.
# ---------------------------------------------------------------------------

begin "an expand-only migration applied since the target's release is compatible"
repo="$(new_repo expand)"
commit_file "$repo" migrations/chat/000099_base.up.sql "$EXPAND_SQL" 'in the target'
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000100_pin.up.sql "$EXPAND_SQL" 'after the target'
ledger="$(write_ledger "$repo" expand chat/000099_base chat/000100_pin)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

begin "a contract-phase migration applied since the target's release blocks"
repo="$(new_repo contract)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000101_drop.up.sql "$CONTRACT_SQL" 'contract'
ledger="$(write_ledger "$repo" contract chat/000101_drop)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "chat/000101_drop.up.sql"
pass

# The historical exceptions list records migrations known not to be expand-only.
# A rollback across one is exactly as unproven as one across a declaration.
begin "a pre-policy migration applied since the target's release blocks"
repo="$(new_repo prepolicy)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/auth/000005_smtp_worker_login_audit.up.sql \
  "$EXPAND_SQL" 'historical'
ledger="$(write_ledger "$repo" prepolicy auth/000005_smtp_worker_login_audit)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "not expand-only"
pass

begin "a contract-phase migration is found behind expand-only ones"
repo="$(new_repo mixed)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000100_pin.up.sql "$EXPAND_SQL" 'expand'
commit_file "$repo" migrations/chat/000101_drop.up.sql "$CONTRACT_SQL" 'contract'
commit_file "$repo" migrations/chat/000102_more.up.sql "$EXPAND_SQL" 'expand again'
ledger="$(write_ledger "$repo" mixed chat/000100_pin \
  chat/000101_drop chat/000102_more)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "chat/000101_drop.up.sql"
pass

# The target's own migrations are in the ledger and are not judged against the
# expand-only rule: a release is always compatible with the schema it shipped.
begin "the target's own contract-phase migration does not block a rollback to it"
repo="$(new_repo own-contract)"
commit_file "$repo" migrations/chat/000101_drop.up.sql "$CONTRACT_SQL" 'part of the target'
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" own-contract chat/000101_drop)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

begin "a down migration is not what the gate reads"
repo="$(new_repo down)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000101_drop.down.sql "$CONTRACT_SQL" 'down only'
ledger="$(write_ledger "$repo" down)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

# ---------------------------------------------------------------------------
# Everything undecidable is a refusal.
# ---------------------------------------------------------------------------

# The one that must never round down to "compatible": no ledger was produced,
# so nothing is known about what the schema contains.
begin "an unavailable authoritative source blocks"
repo="$(new_repo no-ledger)"
status=0; run_gate "$repo" "$(head_of "$repo")" "$WORK/ledger.does-not-exist" || status=$?
expect_blocked "$status" "no applied-migration ledger"
pass

# An empty file is what a failed reader, a truncated write and a file nobody
# wrote all look like. Only a ledger carrying the header proves a read happened.
begin "an empty file is not proof that nothing was applied"
repo="$(new_repo empty-file)"
: >"$WORK/ledger.empty-file"
status=0; run_gate "$repo" "$(head_of "$repo")" "$WORK/ledger.empty-file" || status=$?
expect_blocked "$status" "does not carry the applied-migration header"
pass

begin "a ledger whose header was stripped blocks"
repo="$(new_repo no-header)"
printf 'chat/000100_pin.up.sql %s\n' "$(printf 'x' | sha256sum | cut -d ' ' -f 1)" \
  >"$WORK/ledger.no-header"
status=0; run_gate "$repo" "$(head_of "$repo")" "$WORK/ledger.no-header" || status=$?
expect_blocked "$status" "does not carry the applied-migration header"
pass

# A header and no rows is the one empty answer that is allowed, because the
# authoritative source said so.
begin "an authoritative ledger with no rows is proof that nothing was applied"
repo="$(new_repo empty-ledger)"
ledger="$(write_ledger "$repo" empty-ledger)"
status=0; run_gate "$repo" "$(head_of "$repo")" "$ledger" || status=$?
expect_compatible "$status"
pass

# History this checkout cannot explain: the schema holds a migration whose file
# is not here, so its shape cannot be read and the rollback cannot be proved.
begin "an applied migration that is not in this checkout blocks"
repo="$(new_repo uncorrelatable)"
target="$(head_of "$repo")"
printf '%s\n' "$HEADER" >"$WORK/ledger.uncorrelatable"
printf 'chat/000999_unknown.up.sql %s\n' \
  "$(printf 'unknown' | sha256sum | cut -d ' ' -f 1)" >>"$WORK/ledger.uncorrelatable"
status=0; run_gate "$repo" "$target" "$WORK/ledger.uncorrelatable" || status=$?
expect_blocked "$status" "cannot be correlated with any release"
pass

# The same name, different bytes: the file here is not the file that ran, so
# whether it was expand-only is unknown.
begin "an applied migration whose checksum does not match this checkout blocks"
repo="$(new_repo checksum)"
target="$(head_of "$repo")"
commit_file "$repo" migrations/chat/000100_pin.up.sql "$EXPAND_SQL" 'after the target'
printf '%s\n' "$HEADER" >"$WORK/ledger.checksum"
printf 'chat/000100_pin.up.sql %s\n' \
  "$(printf 'different bytes' | sha256sum | cut -d ' ' -f 1)" >>"$WORK/ledger.checksum"
status=0; run_gate "$repo" "$target" "$WORK/ledger.checksum" || status=$?
expect_blocked "$status" "does not match the file of that name"
pass

begin "an applied migration the target carries with different bytes blocks"
repo="$(new_repo target-checksum)"
commit_file "$repo" migrations/chat/000099_base.up.sql "$EXPAND_SQL" 'in the target'
target="$(head_of "$repo")"
printf '%s\n' "$HEADER" >"$WORK/ledger.target-checksum"
printf 'chat/000099_base.up.sql %s\n' \
  "$(printf 'other' | sha256sum | cut -d ' ' -f 1)" >>"$WORK/ledger.target-checksum"
status=0; run_gate "$repo" "$target" "$WORK/ledger.target-checksum" || status=$?
expect_blocked "$status" "differs from the one release"
pass

# The other direction: the target's code is newer than the schema.
begin "a migration the target expects and the schema does not hold blocks"
repo="$(new_repo behind)"
commit_file "$repo" migrations/chat/000099_base.up.sql "$EXPAND_SQL" 'in the target'
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" behind)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "the schema is behind release $target"
grep -q "chat/000099_base.up.sql" "$WORK/err.txt" || fail "did not name the missing migration"
pass

begin "a ledger row that cannot be parsed blocks"
repo="$(new_repo malformed-row)"
printf '%s\nnot a ledger row\n' "$HEADER" >"$WORK/ledger.malformed-row"
status=0; run_gate "$repo" "$(head_of "$repo")" "$WORK/ledger.malformed-row" || status=$?
expect_blocked "$status" "cannot be correlated with any release"
pass

# ---------------------------------------------------------------------------
# A git that fails is never a target with no migrations.
# ---------------------------------------------------------------------------
#
# The two are the same empty output, and reading the first as the second is how
# a gate that could not read anything reported COMPATIBLE. These drive the real
# function through a git that fails, rather than grepping the source for a
# pattern: a stub named `git` earlier on PATH answers the subcommand under test
# with a non-zero exit and lets every other subcommand through to the real one.

# A PATH shim whose `git` fails for one subcommand and delegates the rest.
# `$1` is the subcommand to break; the exit code is deliberately not 1, so a
# test cannot pass by accident on some other failure that happens to exit 1.
with_failing_git() {
  local broken="$1" repo="$2" target="$3" ledger="$4" shim
  shim="$WORK/shim.$broken"
  rm -rf "$shim"
  mkdir -p "$shim"
  cat >"$shim/git" <<EOF
#!/usr/bin/env bash
if [[ "\${1:-}" == "$broken" ]]; then
  echo "fatal: simulated $broken failure" >&2
  exit 42
fi
exec "$(command -v git)" "\$@"
EOF
  chmod +x "$shim/git"
  (cd "$repo" && PATH="$shim:$PATH" bash "$GATE" migrations "$target" "$ledger") \
    >"$WORK/out.txt" 2>"$WORK/err.txt"
}

begin "a tree listing that fails blocks instead of reporting no migrations"
repo="$(new_repo ls-tree-fails)"
commit_file "$repo" migrations/chat/000099_base.up.sql "$EXPAND_SQL" 'in the target'
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" ls-tree-fails chat/000099_base)"
status=0; with_failing_git ls-tree "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "cannot list the migrations of release $target"
pass

# The same, with a ledger that holds nothing: an unreadable tree and an empty
# ledger together are the exact shape that used to pass.
begin "a tree listing that fails blocks even when the ledger is empty"
repo="$(new_repo ls-tree-fails-empty)"
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" ls-tree-fails-empty)"
status=0; with_failing_git ls-tree "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "cannot list the migrations of release $target"
pass

begin "a blob that cannot be read blocks instead of hashing as nothing"
repo="$(new_repo show-fails)"
commit_file "$repo" migrations/chat/000099_base.up.sql "$EXPAND_SQL" 'in the target'
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" show-fails chat/000099_base)"
status=0; with_failing_git show "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "cannot read migrations/chat/000099_base.up.sql at release $target"
pass

begin "a commit lookup that fails blocks"
repo="$(new_repo rev-parse-fails)"
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" rev-parse-fails)"
status=0; with_failing_git rev-parse "$repo" "$target" "$ledger" || status=$?
expect_blocked "$status" "is not a commit in this checkout"
pass

# The legitimate empty case, so the refusals above are not simply "git is hard".
# A release that really carries no migrations, against a ledger that really
# holds none, is compatible -- and it reaches that verdict through the same
# function the failures go through.
begin "a target that legitimately carries no migrations is not an error"
repo="$(new_repo no-migrations-at-all)"
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" no-migrations-at-all)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

# And a target that carries migrations still works: the fix must not turn every
# tree read into a refusal.
begin "a target that carries migrations is read through the same path"
repo="$(new_repo migrations-present)"
commit_file "$repo" migrations/chat/000099_base.up.sql "$EXPAND_SQL" 'in the target'
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" migrations-present chat/000099_base)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

# A migration file with no trailing newline: the checksum the runner stores is
# of the file, so the gate has to hash the blob as a file too. Round-tripping it
# through a command substitution would strip the difference and refuse a
# rollback for a mismatch that is not real.
begin "a migration file with no trailing newline still correlates"
repo="$(new_repo no-trailing-newline)"
mkdir -p "$repo/migrations/chat"
printf 'ALTER TABLE chat.messages ADD COLUMN pinned boolean;' \
  >"$repo/migrations/chat/000099_base.up.sql"
git -C "$repo" add -A
git -C "$repo" commit --quiet -m 'no trailing newline'
target="$(head_of "$repo")"
ledger="$(write_ledger "$repo" no-trailing-newline chat/000099_base)"
status=0; run_gate "$repo" "$target" "$ledger" || status=$?
expect_compatible "$status"
pass

begin "a target that is not a commit in this checkout blocks"
repo="$(new_repo missing-commit)"
ledger="$(write_ledger "$repo" missing-commit)"
status=0; run_gate "$repo" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$ledger" || status=$?
expect_blocked "$status" "is not a commit in this checkout"
pass

begin "a target that is not a commit SHA is refused before anything is read"
repo="$(new_repo bad-target)"
ledger="$(write_ledger "$repo" bad-target)"
status=0; run_gate "$repo" "HEAD~1; rm -rf /" "$ledger" || status=$?
expect_blocked "$status" "is not a 40-character lowercase commit SHA"
pass

begin "a missing migrations directory is refused"
repo="$(new_repo nomigrations)"
ledger="$(write_ledger "$repo" nomigrations)"
rm -rf "$repo/migrations"
status=0; run_gate "$repo" "$(head_of "$repo")" "$ledger" || status=$?
expect_blocked "$status" "is not a directory"
pass

begin "no arguments at all is refused"
repo="$(new_repo noargs)"
status=0; (cd "$repo" && bash "$GATE") >"$WORK/out.txt" 2>"$WORK/err.txt" || status=$?
expect_blocked "$status" "usage:"
pass

begin "a ledger without a target is refused"
repo="$(new_repo noledgerarg)"
status=0
(cd "$repo" && bash "$GATE" migrations "$(head_of "$repo")") \
  >"$WORK/out.txt" 2>"$WORK/err.txt" || status=$?
expect_blocked "$status" "usage:"
pass

echo
if [[ "$FAILURES" -gt 0 ]]; then
  echo "rollback schema gate tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "rollback schema gate tests passed."
