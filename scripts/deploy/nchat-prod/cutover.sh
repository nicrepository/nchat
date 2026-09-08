#!/usr/bin/env bash
# Promote a named release slot to production traffic (issue #626).
#
#   cutover.sh --target green
#
# The target is named, never derived. Deriving it as "the slot that is not
# active" makes the command depend on the state it is about to change, so a
# second run reverses the first instead of confirming it. Naming it makes the
# operation idempotent: run it twice and production is still on the slot the
# operator asked for.
#
# The old slot is left running. Retiring it is drain-old.sh, deliberately a
# separate decision taken after an observation window, because a slot that has
# been scaled to zero is not a rollback that takes seconds.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# For verified_release_manifest_id: the promotion re-derives the release
# identity from the sealed manifest instead of accepting one it was handed.
# shellcheck source=scripts/deploy/nchat-prod/release-manifest.sh
source "$SCRIPT_DIR/release-manifest.sh"

verify_final_state() {
  local expected="$1" mapping actual
  mapping="$(collect_service_slots)"
  if ! actual="$(resolve_active_slot "$mapping" 2>/dev/null)"; then
    echo "MIXED STATE after cutover:" >&2
    printf '%s\n' "$mapping" >&2
    echo "Re-run: cutover.sh --target $expected" >&2
    return 1
  fi
  [[ "$actual" == "$expected" ]] || prod_fail "cutover ended on slot $actual, expected $expected"
}

# Already there: validate and stop. Reporting success without touching anything
# is what makes a retry after a partial failure safe to run.
report_no_op() {
  local target="$1"
  echo "Every stable Service already selects slot $target; nothing to move."
  slot_ready "$target" || prod_fail "slot $target is active but not fully Ready"
  echo "Slot $target is active and Ready. No change made."
}

# The evidence must name the slot AND the release now on it.
#
# Recomputed here rather than trusted from the deploy: between the smoke run and
# this moment the candidate may have been redeployed, and a confirmation that
# only named the slot would still match. Comparing against the release the
# cluster is holding right now is what makes the evidence expire when the
# candidate changes.
require_smoke_evidence() {
  local target="$1" expected="$2" recorded="${NCHAT_PROD_SMOKE_CONFIRMED:-}"
  [[ "$recorded" == "$expected" ]] && return 0
  if [[ -z "$recorded" ]]; then
    echo "The authenticated release smoke for slot $target has not been recorded." >&2
  else
    echo "The recorded smoke evidence '$recorded' does not match slot $target's" >&2
    echo "current release. Expected '$expected' -- the candidate has changed since" >&2
    echo "it was validated, so the earlier smoke no longer covers what would be promoted." >&2
  fi
  echo "Run smoke.sh --target $target, complete its manual checklist, then set" >&2
  echo "NCHAT_PROD_SMOKE_CONFIRMED=$expected for this command." >&2
  return 1
}

# Re-derives the release identity from the sealed manifest and requires the
# cluster to be carrying that exact release.
#
# The pipeline hands this command an identity in its evidence, but an output can
# be edited and a stale one can be replayed, so nothing is taken on trust: the
# seal is verified here, the id is recomputed from it, and the result is
# compared against what the slot is running right now. A rebuild of the same
# commit seals a different manifest, so this is what a source SHA cannot see.
require_release_identity() {
  local observed_id="$1" manifest_dir="${NCHAT_PROD_RELEASE_MANIFEST_DIR:-}" recomputed
  if [[ -z "$manifest_dir" ]]; then
    echo "NCHAT_PROD_RELEASE_MANIFEST_DIR must name the directory holding the" >&2
    echo "sealed release-manifest.json and release-manifest.sha256 being promoted." >&2
    echo "A commit SHA does not identify a build: two builds of one commit carry" >&2
    echo "different image digests, so the manifest is what says which bytes these are." >&2
    return 1
  fi
  if ! recomputed="$(verified_release_manifest_id "$manifest_dir")"; then
    echo "the release manifest in '$manifest_dir' is missing, unsealed or does not" >&2
    echo "satisfy the release contract; it cannot identify what is being promoted." >&2
    return 1
  fi
  if [[ "$recomputed" != "$observed_id" ]]; then
    echo "the slot is running release $observed_id, but the sealed manifest being" >&2
    echo "promoted is $recomputed. The candidate was rebuilt or redeployed after it" >&2
    echo "was validated, so the approval does not cover what is on the cluster." >&2
    return 1
  fi
  echo "release id verified against the sealed manifest: $recomputed"
}

main() {
  local target mapping release
  target="$(require_target_slot "$@")"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  # The reading that decides the mutation is the reading that gets classified,
  # and it is classified here rather than only by whoever called this.
  #
  # A caller that validated its own earlier reading has proved something about a
  # different moment: between that check and this one a Service can be deleted,
  # have its selector cleared, or be patched to a value that is neither slot,
  # and every one of those states used to reach switch_services_to_slot -- the
  # `all_services_on_slot` test below simply returns false for them, which is
  # the same answer it gives for an ordinary pending promotion. So the gate has
  # to sit on this mapping, before anything is patched.
  #
  # A blue/green split still passes: that is the shape a cutover to this same
  # target that stopped part-way leaves behind, and converging it is what a
  # retry with the same --target exists for.
  require_promotable_selectors "$mapping" "$target"
  print_context_banner "$mapping"
  echo "target slot: $target"
  # Two gates, and readiness alone is not the interesting one. A slot can be
  # entirely Ready while carrying two different releases, because a deploy that
  # failed part-way leaves the workloads it did not reach running and healthy on
  # the previous version.
  slot_ready "$target" || prod_fail "slot $target is not fully Ready; cutover blocked"
  release="$(require_consistent_release "$target")" || return 1
  echo "target release: $release"
  # "<sha>:<id>" -- the commit and the sealed build, checked as one identity.
  require_release_identity "${release#*:}" || return 1
  if all_services_on_slot "$mapping" "$target"; then
    report_no_op "$target"
    return 0
  fi
  require_smoke_evidence "$target" "$target:$release"
  confirm "Move production traffic to slot $target"
  if ! switch_services_to_slot "$target"; then
    echo "Cutover stopped part-way. Production is in a mixed state." >&2
    echo "Re-run 'cutover.sh --target $target' to finish converging, or" >&2
    echo "'rollback.sh --target <previous> <reason>' to go back. Do not leave it mixed." >&2
    return 1
  fi
  verify_final_state "$target"
  slot_ready "$target" || prod_fail "slot $target degraded immediately after cutover"
  echo
  echo "Production now serves slot $target. The previous slot is still running and"
  echo "is the rollback target. Observe before running drain-old.sh."
}

main "$@"
