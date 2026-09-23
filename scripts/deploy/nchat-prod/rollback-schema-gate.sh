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

main() {
  local target="${1:-}" current="${2:-}" file findings listing blocked=0
  local -a added=()
  [[ -n "$target" && -n "$current" ]] ||
    prod_fail "usage: rollback-schema-gate.sh <target-release-sha> <current-release-sha>"
  require_release_sha "the rollback target release" "$target"
  require_release_sha "the currently serving release" "$current"
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
    if findings="$(report_blocking_operations "$file")"; then
      printf '%s\n' "$findings" >&2
      blocked=$((blocked + 1))
    else
      printf '  [OK]   %s is expand-only\n' "$file"
    fi
  done
  if ((blocked > 0)); then
    report_incident_procedure
    return 1
  fi
  echo "Schema compatibility: PASS. Every migration added since the target release is expand-only,"
  echo "so the release on the target slot can serve against the schema as it stands."
}

main "$@"
