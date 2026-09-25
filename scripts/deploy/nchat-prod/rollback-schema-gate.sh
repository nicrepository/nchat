#!/usr/bin/env bash
# May the schema still serve the release on the rollback target? (issues #801, #933)
#
#   rollback-schema-gate.sh <target-release-sha> <current-release-sha>
#
# Rolling back an application is a selector change. Rolling back a database is
# not, and this gate exists so the first is never mistaken for the second.
#
# The property it decides is narrow and stated exactly: every migration applied
# between the two releases must be one the older code can keep running against.
# That is the expand/contract contract the release process already enforces
# forwards, in scripts/ci/blue-green-migration-gate.sh, at the moment a
# migration is written. This asks the same question backwards, about a specific
# pair of releases, at the moment someone wants to go back.
#
# It reuses that gate's operation detector rather than growing a second one.
# There is one definition in this repository of "an operation that breaks the
# slot running the previous release", it lives in blue_green_incompatible_
# operation, and two definitions of that would be one nobody maintains.
#
# One deliberate difference. The forward gate accepts a contract-phase
# operation when the migration declares itself:
#
#     -- nchat:blue-green contract-phase <why this is safe now>
#
# That marker means "no slot depends on the old shape any more", which is a
# statement about going forwards. It says nothing about going back, and the
# slot this rollback targets is precisely a slot that does depend on the old
# shape. So the marker is not honoured here, and neither is the pre-policy
# exceptions list: a declared contract-phase migration between the two releases
# blocks the rollback, loudly, with the incident procedure named.
#
# What IS honoured is a rollback attestation (issue #1008): a line in
# scripts/ci/rollback-schema-attestations.txt naming one migration by its exact
# key and the SHA-256 of its exact bytes, recorded in review after a person
# established that the release before it keeps working against it. The scanner
# is conservative by design -- a CHECK constraint dropped and re-added wider
# looks exactly like one dropped for good -- and the attestation is how a human
# answers the question the scanner cannot, for one file, without teaching the
# scanner to ignore anything. Change a byte of the migration and the
# attestation no longer matches.
#
# LIMITS, because a gate that overstates itself is worse than none. This reads
# the repository, not the database. It proves what the release *contained*; it
# cannot see a migration applied out of band, a schema edited by hand, or a
# data backfill run from a console. It is a necessary condition for a safe
# application rollback and never a sufficient one -- the runbook's incident
# procedure remains the authority when anything else has touched the schema.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/ci/blue-green-migration-gate.sh
source "$ROOT_DIR/scripts/ci/blue-green-migration-gate.sh"

MIGRATIONS_PREFIX=migrations/
# Read from the checkout the gate runs in, never from a path a caller chooses:
# the policy is reviewed code, not configuration.
ROLLBACK_ATTESTATIONS_FILE="$ROOT_DIR/scripts/ci/rollback-schema-attestations.txt"
# The policy could not be consulted, as opposed to being malformed: it only
# matters if a migration needs it, and then it blocks.
ROLLBACK_ATTESTATIONS_UNAVAILABLE=2

# The up migrations present at `current` and absent at `target`, as repository
# paths.
#
# `git diff --diff-filter=A` between the two commits: added files only, so a
# migration both releases carry is not reported, and one the target had that
# the current release dropped is not either -- rolling back to a release that
# has a migration the newer one lost is a different fault and not this gate's.
#
# awk rather than grep for the filter: "no up migrations between these two
# commits" is an ordinary answer, and grep reports it as exit 1, which under
# pipefail is indistinguishable from git having failed.
migrations_added_between() {
  local target="$1" current="$2"
  git -C "$ROOT_DIR" diff --name-only --diff-filter=A "$target" "$current" -- \
    "$MIGRATIONS_PREFIX" | awk '/\.up\.sql$/'
}

require_release_sha() {
  local label="$1" value="$2"
  [[ "$value" =~ ^[a-f0-9]{40}$ ]] ||
    prod_fail "$label must be a full 40-character commit SHA, got '$value'"
  git -C "$ROOT_DIR" cat-file -e "$value^{commit}" 2>/dev/null ||
    prod_fail "$label '$value' is not a commit in this checkout; fetch the full history before running the gate"
}

# Every finding of one migration, or nothing. The marker and the exceptions
# list are not consulted: see the header.
report_blocking_operations() {
  local file="$1"
  blue_green_scan_file "$ROOT_DIR/$file"
}

# --- rollback attestations (issue #1008) -----------------------------------

# The SHA-256 of a migration, defined exactly as scripts/db/migrate.sh
# migration_checksum defines it -- the value the runner pins in
# schema_migrations -- so an attestation names the same bytes the database
# recorded. Not sourced from migrate.sh: that script parses arguments and runs
# on load. test_rollback_schema_gate.sh holds the two definitions equal.
rollback_migration_checksum() {
  local file="$1" line
  line="$(sha256sum "$file")" || return 1
  line="${line%% *}"
  [[ "$line" =~ ^[a-f0-9]{64}$ ]] || return 1
  printf '%s' "$line"
}

# A migration key as the policy may name it: "<domain>/<ordinal>_<name>.up.sql",
# with the same domain and filename rules the migration runner enforces. Nothing
# else -- no absolute path, no "..", no wildcard, no second directory.
rollback_attestation_key_valid() {
  [[ "$1" =~ ^[a-z][a-z0-9_]*/[0-9]{6}_[a-z0-9_]+\.up\.sql$ ]]
}

# One policy line: "<sha256> <key>" on stdout, nothing for a blank line or a
# comment, and a failure for anything else. Comments are whole lines only.
rollback_attestation_entry() {
  local line="$1" checksum key extra
  [[ ! "$line" =~ ^[[:space:]]*(#.*)?$ ]] || return 0
  read -r checksum key extra <<<"$line"
  [[ -z "$extra" ]] || return 1
  [[ "$checksum" =~ ^[a-f0-9]{64}$ ]] || return 1
  rollback_attestation_key_valid "$key" || return 1
  printf '%s %s' "$checksum" "$key"
}

# The whole policy as "<sha256> <key>" lines, or a refusal naming the first
# problem. Deterministic and all-or-nothing: one malformed line or one key
# named twice -- with the same checksum or another -- and nothing is loaded.
rollback_attestations_parse() {
  local policy="$1" line entry key number=0
  local -A seen=()
  while IFS= read -r line || [[ -n "$line" ]]; do
    number=$((number + 1))
    entry="$(rollback_attestation_entry "$line")" ||
      { echo "rollback attestation policy line $number is malformed: expected '<64 lowercase hex> <domain>/<ordinal>_<name>.up.sql'" >&2; return 1; }
    [[ -n "$entry" ]] || continue
    key="${entry#* }"
    [[ -z "${seen["$key"]:-}" ]] ||
      { echo "rollback attestation policy line $number names $key again; one key, one checksum" >&2; return 1; }
    seen["$key"]=1
    printf '%s\n' "$entry"
  done <<<"$policy"
}

# The validated policy on stdout.
#
#   0                                  loaded (possibly with no entries)
#   1                                  present and malformed: a configuration
#                                      error, and the gate stops on it
#   ROLLBACK_ATTESTATIONS_UNAVAILABLE  absent or unreadable: nothing can be
#                                      attested, which blocks only a migration
#                                      that would have needed it
rollback_attestations_load() {
  local policy
  [[ -e "$ROLLBACK_ATTESTATIONS_FILE" ]] || return "$ROLLBACK_ATTESTATIONS_UNAVAILABLE"
  policy="$(cat -- "$ROLLBACK_ATTESTATIONS_FILE" 2>/dev/null)" ||
    return "$ROLLBACK_ATTESTATIONS_UNAVAILABLE"
  rollback_attestations_parse "$policy"
}

# Why a migration with findings is not attested, or nothing when it is: the
# exact key with the exact checksum of the bytes being judged. The key is
# derived from the file the git diff selected; the policy only answers whether
# that pair was reviewed, and never names a file to read.
rollback_attestation_problem() {
  local attestations="$1" key="$2" checksum="$3"
  if grep -Fxq -- "$checksum $key" <<<"$attestations"; then
    return 0
  fi
  if awk -v key="$key" '$2 == key { found = 1 } END { exit !found }' <<<"$attestations"; then
    printf 'checksum mismatch: its attestation names other bytes, and this file hashes to %s; it changed after it was reviewed' "$checksum"
    return 0
  fi
  printf 'not attested as rollback-compatible'
}

# For a migration the scanner flagged: its checksum on stdout when an exact
# attestation covers it, or the reason it does not, with exit 1. Four reasons,
# kept apart because each calls for a different action: the policy could not
# be read, the file could not be hashed, the bytes changed since review, or
# nobody reviewed it.
rollback_attestation_verdict() {
  local file="$1" attestations="$2" policy_status="$3" checksum problem
  if ((policy_status != 0)); then
    printf 'no attestation can apply: the policy %s is absent or unreadable' "$ROLLBACK_ATTESTATIONS_FILE"
    return 1
  fi
  checksum="$(rollback_migration_checksum "$ROOT_DIR/$file")" ||
    { printf 'its SHA-256 could not be computed'; return 1; }
  problem="$(rollback_attestation_problem "$attestations" "$(blue_green_migration_key "$file")" "$checksum")"
  [[ -z "$problem" ]] || { printf '%s' "$problem"; return 1; }
  printf '%s' "$checksum"
}

# "DROP CONSTRAINT, SET NOT NULL" from the scanner's "<key>: <operation>" lines.
findings_operations() {
  local findings="$1" line operations=""
  while IFS= read -r line; do
    operations+="${operations:+, }${line#*: }"
  done <<<"$findings"
  printf '%s' "$operations"
}

# One migration added since the target release: 0 when the target's code can
# keep running against it, 1 with the reason on stderr otherwise. A file that
# cannot be read is never expand-only: the scan of an unreadable file finds
# nothing, and nothing is not a pass.
judge_added_migration() {
  local file="$1" attestations="$2" policy_status="$3" findings verdict
  if [[ ! -f "$ROOT_DIR/$file" || ! -r "$ROOT_DIR/$file" ]]; then
    echo "$(blue_green_migration_key "$file"): the migration cannot be read from this checkout" >&2
    return 1
  fi
  if ! findings="$(report_blocking_operations "$file")"; then
    printf '  [OK]       %s is expand-only\n' "$file"
    return 0
  fi
  if verdict="$(rollback_attestation_verdict "$file" "$attestations" "$policy_status")"; then
    printf '  [ATTESTED] %s is explicitly rollback-compatible despite %s (sha256 %s)\n' \
      "$file" "$(findings_operations "$findings")" "$verdict"
    return 0
  fi
  printf '%s\n' "$findings" >&2
  echo "  $(blue_green_migration_key "$file"): $verdict" >&2
  return 1
}

report_incident_procedure() {
  cat >&2 <<'EOF'

ROLLBACK BLOCKED: the schema has moved past the release on the target slot.

The migrations listed above take something away that the target release's code
still expects, so pointing traffic at it would serve requests against a schema
that release cannot use. A blind rollback here turns one incident into two.

Nothing has been changed. The slot currently serving traffic is untouched and
still serving, and the target slot is still running.

Do not run a down migration to make this pass. Follow the schema incompatibility
procedure in docs/runbooks/production-blue-green-deployment.md: roll forward with
a fix, or perform a deliberate database recovery with the DBA present.
EOF
}

# Both releases named, and both full commits of this checkout.
require_release_pair() {
  local target="$1" current="$2"
  [[ -n "$target" && -n "$current" ]] ||
    prod_fail "usage: rollback-schema-gate.sh <target-release-sha> <current-release-sha>"
  require_release_sha "the rollback target release" "$target"
  require_release_sha "the currently serving release" "$current"
}

main() {
  local target="${1:-}" current="${2:-}" file listing attestations policy_status=0 blocked=0
  local -a added=()
  require_release_pair "$target" "$current"
  # Validated before anything is judged -- the same-release answer included -- so
  # a broken policy is a visible configuration error on every run rather than
  # one that surfaces only on the day a migration needs it.
  attestations="$(rollback_attestations_load)" || policy_status=$?
  ((policy_status != 1)) ||
    prod_fail "the rollback attestation policy $ROLLBACK_ATTESTATIONS_FILE is malformed; fix it in review, it is never skipped"
  if [[ "$target" == "$current" ]]; then
    echo "The target slot carries the release already serving traffic; the schema is by definition compatible."
    return 0
  fi
  # Through a variable, not `mapfile < <(...)`: a process substitution discards
  # the exit status, so a git that could not walk the history would read as
  # "no migrations were added" and pass this gate.
  listing="$(migrations_added_between "$target" "$current")" ||
    prod_fail "could not list the migrations added between $target and $current"
  [[ -z "$listing" ]] || mapfile -t added <<<"$listing"
  echo "migrations added between $target and $current: ${#added[@]}"
  for file in "${added[@]}"; do
    judge_added_migration "$file" "$attestations" "$policy_status" || blocked=$((blocked + 1))
  done
  if ((blocked > 0)); then
    report_incident_procedure
    return 1
  fi
  echo "Schema compatibility: PASS. Every migration added since the target release is expand-only"
  echo "or explicitly attested as rollback-compatible, so the release on the target slot can serve"
  echo "against the schema as it stands."
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
