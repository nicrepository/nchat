#!/usr/bin/env bash
# Everything that must hold before a cutover may patch its first Service
# (issue #933).
#
#   cutover-preflight.sh <selectors-snapshot-file>
#
# Prints, on stdout, the facts the cutover run needs and nothing else:
#
#   candidate=<slot>
#   release_sha=<40 hex>
#   release_id=<64 hex>
#   prepare_run_id=<n>
#   rollback_target=<slot>
#
# in the `key=value` form GitHub Actions appends straight to $GITHUB_OUTPUT.
# Everything else this prints goes to stderr, because a stray line on stdout
# would become a job output.
#
# WHY THIS FILE EXISTS. Before #933 the cutover was a second job in the deploy
# run, and it knew the candidate because the job before it had just built it.
# Now the two are separate workflows separated by an operator, possibly by
# hours, and the operator's action is a decision -- not the transcription of a
# slot, a commit, a release id and a run id out of a previous run's log. So the
# four are derived. That makes this the place where "derived" must not become
# "assumed".
#
# The lifecycle record says which candidate was prepared. It is a claim and it
# is treated as one: the slot it names is then read from the cluster and
# required to be carrying exactly the release it claims, before anything else
# happens. A candidate that was redeployed, rebuilt, degraded or replaced since
# preparation fails here, with production untouched. The record can only ever
# narrow what may be promoted -- there is no field in it that can make an
# unprovable promotion proceed.
#
# What it does NOT do is verify the sealed manifest: that needs the artifact,
# which the run downloads with the `prepare_run_id` printed here, and cutover.sh
# re-derives the release id from the seal and compares it against the cluster
# itself. Doing it twice, in two places, from two readings, is the point.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-prod/release-state.sh
source "$SCRIPT_DIR/release-state.sh"
# shellcheck source=scripts/deploy/nchat-prod/lifecycle.sh
source "$SCRIPT_DIR/lifecycle.sh"

log() { printf '%s\n' "$*" >&2; }

# The selectors, classified against the target rather than resolved into an
# active slot.
#
# Those are different questions and only the second one is answerable here. A
# namespace split between blue and green is the normal shape of a cutover to
# this same target that stopped part-way, and converging it is exactly what a
# retry is for; refusing every mixed reading would close the one path that
# finishes it. What must fail is a reading this cannot describe -- a Service
# selecting something that is neither slot, one carrying no release-slot key,
# one that is not there -- and require_promotable_selectors fails on each.
snapshot_and_classify() {
  local snapshot="$1" target="$2" mapping
  mapping="$(collect_service_slots)"
  printf '%s\n' "$mapping" >"$snapshot"
  print_context_banner "$mapping" >&2
  require_promotable_selectors "$mapping" "$target"
}

main() {
  local snapshot="${1:-}" record prepared candidate release release_sha release_id run_id
  local record_status=0
  [[ -n "$snapshot" ]] ||
    prod_fail "usage: cutover-preflight.sh <selectors-snapshot-file>"
  require_context
  require_namespace
  # Read once. Two reads could return two records, and the promotion would then
  # be judged against a state nobody ever observed as a whole.
  #
  # Absent and unreadable are handled apart. Absent falls through to
  # require_prepared_candidate, which says "no prepared candidate is recorded"
  # -- the right message for a namespace that has never prepared one. A read
  # that failed says nothing at all about the candidate, and must not be
  # allowed to wear that message.
  record="$(release_state_read)" || record_status=$?
  ((record_status == 0 || record_status == NCHAT_PROD_RELEASE_STATE_ABSENT)) ||
    prod_fail "the release lifecycle record could not be read; the prepared candidate cannot be identified and nothing will be promoted"
  # "<slot> <sha>:<id> <run id>", or a refusal naming what the record lacks.
  prepared="$(require_prepared_candidate "$record")" || return 1
  read -r candidate release run_id <<<"$prepared"
  release_sha="${release%%:*}"
  release_id="${release#*:}"
  log "recorded candidate: slot $candidate, release $release, prepared by run $run_id"
  require_candidate_not_stale "$(release_state_field "$record" candidate_ready_at)" >&2 ||
    prod_fail "the recorded candidate is too old to promote; prepare the release again"
  snapshot_and_classify "$snapshot" "$candidate"
  # The claim, proved against the cluster. Readiness first: a slot that is not
  # Ready cannot serve, whatever it is carrying.
  slot_ready "$candidate" ||
    prod_fail "slot $candidate is not fully Ready; cutover blocked"
  require_slot_release_identity "$candidate" "$release" >/dev/null
  log "slot $candidate is Ready and still carries exactly $release"
  # opposite_slot(candidate), never read back from the selectors: once the
  # namespace has converged they name the candidate itself, which is the one
  # slot a rollback can never go to.
  printf 'candidate=%s\nrelease_sha=%s\nrelease_id=%s\nprepare_run_id=%s\nrollback_target=%s\n' \
    "$candidate" "$release_sha" "$release_id" "$run_id" "$(opposite_slot "$candidate")"
}

main "$@"
