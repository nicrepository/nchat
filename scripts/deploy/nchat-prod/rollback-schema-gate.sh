#!/usr/bin/env bash
# Can the schema that exists NOW still serve the release on the rollback target?
# (CICD-08)
#
#   rollback-schema-gate.sh migrations <target-sha> <applied-ledger-file>
#
# Rollback is a selector change. It does not touch the database, and that is
# only safe because of a contract the repository already enforces at review
# time: an up migration may expand the schema freely, and an operation that
# takes something away from code that is still running -- a DROP, a RENAME, a
# SET NOT NULL, a type change -- is a contract-phase operation that must be
# declared in the migration itself. scripts/ci/blue-green-migration-gate.sh is
# what refuses an undeclared one, and the declaration is what makes a
# contract-phase migration visible here.
#
# The question is therefore about FILES, and it is asked against the ledger of
# migrations that were actually applied -- never against what the cluster's
# workloads appear to be running. deploy.sh runs the migration Job before it
# applies the candidate and before it waits for it, so a release can advance the
# schema and then fail to roll out, leaving no Pod, Deployment or slot
# annotation anywhere carrying it. A gate that read the schema back off the
# workloads would report that release as never having happened and clear a
# rollback straight across its migration. applied-migrations.sh produces the
# ledger; this compares it to the target's tree.
#
# Three ways a rollback is refused, and they are different failures:
#
#   * a migration applied since the target's release declared itself
#     contract-phase, or is one of the historical exceptions known not to be
#     expand-only -- something the target's code depends on has been taken away;
#   * an applied migration cannot be correlated with this checkout, by name or
#     by checksum -- the schema contains something whose shape cannot be read;
#   * a migration the target's release expects has not been applied -- the
#     target's code is newer than the schema.
#
# Everything undecidable is a refusal, never a pass: a ledger with no header, an
# unresolvable commit, a malformed identity, a dirty ledger. Proving a rollback
# safe is the only outcome that permits one.
#
# It reads git, the working tree and one file. It runs no migration, opens no
# database connection and touches no cluster.
set -Eeuo pipefail

SCHEMA_GATE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCHEMA_GATE_ROOT="$(cd "$SCHEMA_GATE_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/ci/blue-green-migration-gate.sh
source "$SCHEMA_GATE_ROOT/scripts/ci/blue-green-migration-gate.sh"

# Written by applied-migrations.sh, and the proof that a read actually happened.
# Without it an empty file, a truncated file and a file nobody wrote all look
# like "no migrations applied" -- reading the most dangerous state as the safest.
APPLIED_HEADER='# nchat-applied-migrations v1'

MIGRATIONS_DIR=""
EXTRACT_DIR=""
# The release the rollback targets, named in every refusal so the message says
# which release the schema no longer fits.
TARGET_RELEASE_SHA=""

schema_gate_fail() {
  echo "rollback schema gate: $*" >&2
  echo "rollback blocked; incident/DB recovery required" >&2
  return 1
}

# The up-migration paths the target's tree holds, or failure.
#
# The listing is captured and its status checked before anything is filtered.
# Piping git straight into a process substitution is what made this fail open:
# a `git ls-tree` that exits non-zero produces no lines, which is byte for byte
# what a release carrying no migrations produces, and the gate then had nothing
# to compare and reported COMPATIBLE. A tree that cannot be read is not a tree
# with nothing in it.
#
# `|| true` stays on the grep alone, where it means "no line matched" and
# nothing else -- git's status has already been decided by then.
target_migration_paths() {
  local target="$1" listing
  listing="$(git ls-tree -r --name-only "$target" -- "$MIGRATIONS_DIR")" ||
    schema_gate_fail "cannot list the migrations of release $target; its tree could not be read" ||
    return 1
  printf '%s\n' "$listing" | grep -E '\.up\.sql$' || true
}

# "<domain>/<filename> <checksum>" for every migration the target's tree holds.
#
# From the commit, not the working tree: the question is what the release on the
# target slot expects, and the checkout may be many releases ahead of it.
#
# Each blob is written out and checksummed as a file rather than round-tripped
# through a command substitution: `$(git show ...)` strips trailing newlines, so
# a migration whose file does not end in exactly one would hash differently here
# than it did when the runner recorded it, and every rollback across it would be
# refused for a mismatch that is not real. The redirection also carries git's
# own status, so a blob that cannot be read stops the gate instead of being
# hashed as nothing.
target_migrations() {
  local target="$1" paths path checksum
  paths="$(target_migration_paths "$target")" || return 1
  while read -r path; do
    [[ -n "$path" ]] || continue
    git show "$target:$path" >"$EXTRACT_DIR/blob" ||
      schema_gate_fail "cannot read $path at release $target" || return 1
    checksum="$(sha256sum "$EXTRACT_DIR/blob" | cut -d ' ' -f 1)"
    printf '%s %s\n' "${path#"$MIGRATIONS_DIR"/}" "$checksum"
  done <<<"$paths"
}

# One migration as this checkout holds it, written under its "<domain>/<file>"
# path so the existing marker and exceptions helpers read the key they expect.
extract_migration() {
  local key="$1" source="$MIGRATIONS_DIR/$1" dest="$EXTRACT_DIR/$1"
  [[ -f "$source" && ! -L "$source" ]] || return 1
  mkdir -p "$(dirname "$dest")"
  cp "$source" "$dest"
  printf '%s' "$dest"
}

# A migration that took something away from the release still running. The
# declaration and the historical exception are the same answer: this file is not
# expand-only, so a slot built before it cannot be proved to fit the schema.
takes_something_away() {
  blue_green_has_contract_marker "$1" || blue_green_is_pre_policy "$1"
}

# One migration that is in the ledger and not in the target's tree: it was
# applied after the target's release was built. Correlate it with this checkout
# by name AND by content, then ask whether it is expand-only.
check_applied_after_target() {
  local key="$1" checksum="$2" file actual
  file="$(extract_migration "$key")" ||
    schema_gate_fail "applied migration $key is not in this checkout; it cannot be correlated with any release" ||
    return 1
  actual="$(sha256sum "$file" | cut -d ' ' -f 1)"
  [[ "$actual" == "$checksum" ]] ||
    schema_gate_fail "applied migration $key does not match the file of that name in this checkout" ||
    return 1
  ! takes_something_away "$file" ||
    schema_gate_fail "$key was applied after release $TARGET_RELEASE_SHA and is not expand-only; the schema no longer provides what that release depends on"
}

# One migration that is in both. The bytes must be the same bytes: a file whose
# name the target knows but whose content was applied differently is not a
# migration this can reason about.
check_applied_before_target() {
  local key="$1" checksum="$2" expected="$3"
  # An if, not `[[ ]] && return 0`: an AND-list whose test fails leaves the
  # function's last status non-zero, which under `set -e` ends the caller
  # somewhere other than the refusal it is supposed to report.
  if [[ "$checksum" != "$expected" ]]; then
    schema_gate_fail "applied migration $key differs from the one release $TARGET_RELEASE_SHA carries"
    return 1
  fi
}

# Every applied migration, against the target's tree.
check_applied() {
  local applied="$1" expected="$2" key checksum known
  while read -r key checksum; do
    [[ -n "$key" ]] || continue
    known="$(awk -v k="$key" '$1 == k { print $2 }' <<<"$expected")"
    if [[ -n "$known" ]]; then
      check_applied_before_target "$key" "$checksum" "$known" || return 1
    else
      check_applied_after_target "$key" "$checksum" || return 1
    fi
  done <<<"$applied"
}

# The other direction, and it is not symmetric with the one above. A migration
# the target's release carries and the ledger does not hold was never applied,
# so the target's code expects a schema that is not there. That is a rollback
# onto a release the database cannot serve.
check_target_is_satisfied() {
  local applied="$1" expected="$2" key missing
  missing=""
  while read -r key _; do
    [[ -n "$key" ]] || continue
    awk -v k="$key" '$1 == k { found = 1 } END { exit !found }' <<<"$applied" ||
      missing+="$key"$'\n'
  done <<<"$expected"
  [[ -n "$missing" ]] || return 0
  echo "migrations release $TARGET_RELEASE_SHA expects and the ledger does not hold:" >&2
  printf '%s' "$missing" | sed 's/^/  /' >&2
  schema_gate_fail "the schema is behind release $TARGET_RELEASE_SHA"
}

# The ledger, refused unless it proves it was read from the authoritative source.
read_applied_ledger() {
  local file="$1"
  [[ -f "$file" && ! -L "$file" ]] ||
    schema_gate_fail "no applied-migration ledger at $file" || return 1
  [[ "$(head -n 1 "$file")" == "$APPLIED_HEADER" ]] ||
    schema_gate_fail "$file does not carry the applied-migration header; an unproven ledger is not evidence that nothing was applied" ||
    return 1
  tail -n +2 "$file"
}

require_arguments() {
  local migrations_dir="$1" target="$2" ledger="$3"
  [[ -n "$migrations_dir" && -n "$target" && -n "$ledger" ]] ||
    schema_gate_fail "usage: rollback-schema-gate.sh <migrations-dir> <target-sha> <applied-ledger-file>" ||
    return 1
  [[ -d "$migrations_dir" ]] ||
    schema_gate_fail "$migrations_dir is not a directory" || return 1
}

# The target as an identity, separately from the arguments carrying it: the
# shape is refused before the value ever reaches git, and a well-formed SHA this
# checkout does not hold is refused before anything is compared against it.
require_target_commit() {
  local target="$1"
  [[ "$target" =~ ^[a-f0-9]{40}$ ]] ||
    schema_gate_fail "'$target' is not a 40-character lowercase commit SHA" || return 1
  git rev-parse --verify --quiet "$target^{commit}" >/dev/null 2>&1 ||
    schema_gate_fail "$target is not a commit in this checkout" || return 1
}

report_compatible() {
  echo "rollback schema gate: COMPATIBLE"
  echo "Every migration the ledger records as applied is either one release $1 carries"
  echo "or an expand-only one applied since, and every migration that release expects has"
  echo "been applied. No migration is run by a rollback."
}

main() {
  local target ledger applied expected
  MIGRATIONS_DIR="${1:-}"
  target="${2:-}"
  ledger="${3:-}"
  require_arguments "$MIGRATIONS_DIR" "$target" "$ledger" || return 1
  require_target_commit "$target" || return 1
  TARGET_RELEASE_SHA="$target"
  applied="$(read_applied_ledger "$ledger")" || return 1
  EXTRACT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-rollback-schema.XXXXXX")"
  trap 'rm -rf "$EXTRACT_DIR"' EXIT
  # `|| return 1` on the substitution, not `set -e`: a failed command
  # substitution in a bare assignment does not reliably end the caller, and the
  # value it leaves behind is the empty set this gate must never accept.
  expected="$(target_migrations "$target")" || return 1
  check_applied "$applied" "$expected" || return 1
  check_target_is_satisfied "$applied" "$expected" || return 1
  report_compatible "$target"
}

main "$@"
