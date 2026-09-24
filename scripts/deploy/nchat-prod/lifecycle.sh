#!/usr/bin/env bash
# Whether a slot may be reused, and whether a candidate may be promoted
# (issue #933).
#
# This file decides; it mutates nothing. Retirement is drain-old.sh, promotion
# is cutover.sh, and both stay exactly where they were -- what was missing was
# the question asked before either of them runs.
#
# The question exists because the old slot is now kept alive on purpose. After
#
#     blue -> green
#
# the correct state is not "green active, blue drained". It is
#
#     green = ACTIVE
#     blue  = ROLLBACK RESERVED
#
# and blue stays that way until the next release is prepared. That makes
# rollback a selector change for the whole window between two releases, which
# is the entire point of running two slots. It also means the next release
# cannot simply deploy into "the idle slot": the idle slot is the rollback, and
# overwriting it is how a Blue/Green namespace ends up with no way back.
#
# So preparation asks this file first, and gets one of three answers: the slot
# is free, the slot is reserved and the reservation has expired (retire it
# first, under the preconditions below), or the slot is reserved and the
# reservation stands (refuse).
#
# Every answer is computed from the cluster and the lifecycle record together,
# and the cluster wins. A record claiming a reservation the selectors
# contradict blocks; it never permits.
#
# Sourcing this file only defines functions.

# The reservation has run its course.
#
# An unreadable or absent cutover timestamp is not "long ago": it is a record
# that cannot date the reservation it is asserting, and a window that cannot be
# measured has not been shown to have elapsed.
retention_elapsed() {
  local cutover_at="$1" window cutover now age
  window="$(rollback_retention_seconds)" || return 1
  cutover="$(instant_to_epoch "$cutover_at")" ||
    { echo "the lifecycle record has no usable cutover_at ('$cutover_at'); the reservation cannot be dated and will not be retired" >&2; return 1; }
  now="$(date -u +%s)"
  age=$((now - cutover))
  ((age >= window)) ||
    { echo "slot reserved for rollback $((window - age))s longer: the last cutover was ${age}s ago and the retention window is ${window}s" >&2; return 1; }
  echo "rollback retention satisfied: the last cutover was ${age}s ago, the window is ${window}s"
}

# Nothing still points at the slot about to be retired.
#
# Distinct from "target != active", which drain-old.sh already enforces: during
# a partial cutover there is no single active slot, and a per-Service check is
# the only one that is answerable then. It is also the stronger statement --
# one Service left behind is one Service whose traffic the scale-to-zero would
# drop.
no_stable_service_selects() {
  local mapping="$1" slot="$2" holders
  holders="$(awk -v s="$slot" '$2 == s { print $1 }' <<<"$mapping" | tr '\n' ' ')"
  [[ -z "$holders" ]] ||
    { echo "slot $slot is still selected by: $holders" >&2; return 1; }
}

# The release now serving was proved through the stable Services.
#
# Readiness and a consistent release say the slot is *running* something
# coherent; they say nothing about whether it works. The post-cutover smoke is
# what asks that, and a release whose smoke failed is precisely the release
# whose rollback must stay available -- so "the smoke has not been recorded" is
# a refusal, not a gap to step over.
#
# Compared against the release the active slot is carrying right now, not
# against a boolean: a slot redeployed after it was smoked carries a different
# identity, and the evidence stops matching. That is the same rule the
# candidate evidence follows.
require_post_traffic_smoke() {
  local record="$1" active="$2" release="$3" recorded
  recorded="$(release_state_field "$record" post_cutover_smoke)"
  if [[ "$recorded" == "$release" ]]; then
    echo "the release serving on $active passed its post-traffic smoke ($release)"
    return 0
  fi
  if [[ -z "$recorded" ]]; then
    echo "no post-traffic smoke is recorded for the release now serving on $active." >&2
  else
    echo "the recorded post-traffic smoke is for '$recorded', but $active is serving '$release'." >&2
  fi
  echo "Retiring the rollback slot would remove the way back from a release nobody" >&2
  echo "has proved through the stable Services. Run:" >&2
  echo "  scripts/deploy/nchat-prod/record-traffic-smoke.sh --target $active --after cutover" >&2
  echo "and investigate rather than re-running it until it passes." >&2
  return 1
}

# Everything that must hold before the reserved slot may be scaled to zero.
#
# Retiring it is the step that turns rollback from seconds into a redeploy, so
# the release that would be rolled back *to* is not the thing being checked --
# the release that is running *now* is. If it is unhealthy, mixed, or was never
# proved to work, the fast way back is exactly what must not be removed.
require_retirement_preconditions() {
  local record="$1" mapping="$2" active="$3" target="$4" release
  [[ "$target" != "$active" ]] ||
    prod_fail "slot $target is serving production traffic; it is not a retirement target"
  all_services_on_slot "$mapping" "$active" ||
    { printf '%s\n' "$mapping" >&2; prod_fail "the stable Services have not converged on $active; retiring $target during a mixed state removes the way back from it"; }
  no_stable_service_selects "$mapping" "$target" || return 1
  slot_ready "$active" ||
    prod_fail "active slot $active is not fully Ready; keep $target available"
  release="$(require_consistent_release "$active")" || return 1
  echo "active slot $active is Ready and carries one release ($release)"
  require_post_traffic_smoke "$record" "$active" "$release" || return 1
}

# Proved after drain-old.sh, from the cluster rather than from its exit code.
#
# `.spec.replicas` and not `.status`: the assertion is that the desired state
# was recorded, which is what makes the slot reusable. Pods terminating on
# their grace period are expected and are not a failure.
require_slot_scaled_to_zero() {
  local slot="$1" service desired
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    desired="$(kubectl get deployment "$service-$slot" -n "$NCHAT_PROD_NAMESPACE" \
      -o jsonpath='{.spec.replicas}' 2>/dev/null)" ||
      prod_fail "deployment/$service-$slot is not readable after the drain"
    [[ "$desired" == "0" ]] ||
      prod_fail "deployment/$service-$slot still wants $desired replica(s) after the drain"
  done
  echo "every workload of slot $slot is scaled to zero; its Deployments remain and can be reused"
}

# --- preparation -----------------------------------------------------------

# Whether the idle slot may receive the next candidate, as one word on stdout:
#
#   FREE      no reservation stands against it; deploy into it
#   RETIRE    it is reserved, the window has expired, and every retirement
#             precondition holds; drain it, then deploy into it
#
# and a non-zero exit with a reason on stderr for every other case. There is no
# third word for "probably fine": a reservation that cannot be evaluated is a
# reservation that stands, and a record that could not be read is not a record
# that says there is none.
#
# `status` is the reader's, and it is a parameter rather than something
# re-derived here so that the decision is taken from one reading of the
# cluster.
candidate_slot_disposition() {
  local record="$1" status="$2" mapping="$3" active="$4" target="$5" reserved
  require_consistent_record "$record" "$status" "$active" >&2 || return 1
  reserved="$(release_state_field "$record" rollback_reserved_slot)"
  if [[ "$reserved" != "$target" ]]; then
    printf 'FREE'
    return 0
  fi
  echo "slot $target is reserved as the rollback for the release now on $active" >&2
  retention_elapsed "$(release_state_field "$record" cutover_at)" >&2 || return 1
  require_retirement_preconditions "$record" "$mapping" "$active" "$target" >&2 || return 1
  printf 'RETIRE'
}

# --- the record's own invariants -------------------------------------------

# A record that contradicts the cluster, or itself, stops the release.
#
# This is what "the cluster wins" has to mean operationally. Reading each field
# and cross-checking it at the point of use is not enough: a record can be
# internally impossible -- a reservation on the slot that is serving, a
# half-written candidate, an active slot the selectors disagree with -- and
# every one of those means something wrote it that this pipeline did not, or
# wrote it and died. None of them is a state to reason forward from.
#
# An absent record is not a contradiction. It is the namespace's first release
# under this scheme. A record that is present but incomplete is the opposite:
# something wrote it that this pipeline did not, or wrote it and died, and a
# missing `rollback_reserved_slot` there reads as "no reservation" when the
# truth is "nobody knows".
# Takes the reader's status, not just its text, because "there is no record"
# and "the record could not be read" are the same empty string and opposite
# answers. Only the first is a bootstrap.
require_consistent_record() {
  local record="$1" status="$2" active="$3"
  case "$status" in
    0) ;;
    "$NCHAT_PROD_RELEASE_STATE_ABSENT")
      echo "no lifecycle record exists yet; treating this as the namespace's first release"
      return 0
      ;;
    *)
      echo "the lifecycle record could not be read, so nothing is known about the rollback" >&2
      echo "reservation or the last cutover. Refusing rather than assuming there is none." >&2
      return 1
      ;;
  esac
  require_release_state_contract "$record" || return 1
  require_record_agrees_with_cluster "$record" "$active" || return 1
  require_record_candidate_complete "$record"
}

# The two claims the record makes about where production is, checked against
# where it actually is. Empty means "not claimed" and passes: a record written
# before the first cutover has no active slot to be wrong about.
require_record_agrees_with_cluster() {
  local record="$1" active="$2" claimed reserved
  claimed="$(release_state_field "$record" active_slot)"
  [[ -z "$claimed" || "$claimed" == "$active" ]] ||
    { echo "the lifecycle record claims slot '$claimed' is active, but the stable Services select '$active'; investigate before releasing" >&2; return 1; }
  reserved="$(release_state_field "$record" rollback_reserved_slot)"
  [[ -z "$reserved" || "$reserved" == "$(opposite_slot "$active")" ]] ||
    { echo "the lifecycle record reserves slot '$reserved' for rollback while '$active' is serving; a reservation can only be on the idle slot" >&2; return 1; }
}

# --- promotion -------------------------------------------------------------

# The candidate a cutover may promote, as "<slot> <release> <run id>".
#
# Derived rather than typed: the operator's action is the decision to promote,
# not the transcription of four identifiers. What is derived here is only a
# *claim* about which candidate was prepared; the caller proves it against the
# cluster with require_slot_release_identity before anything is patched, and
# refuses the promotion when the two disagree.
#
# Every field must be present. A partially written record describes a
# preparation that did not finish, and promoting the slot it names would
# promote whatever happens to be sitting there.
require_prepared_candidate() {
  local record="$1" slot release ready_at run_id
  slot="$(release_state_field "$record" candidate_slot)"
  release="$(release_state_field "$record" candidate_release)"
  ready_at="$(release_state_field "$record" candidate_ready_at)"
  run_id="$(release_state_field "$record" prepare_run_id)"
  is_valid_slot "$slot" ||
    prod_fail "no prepared candidate is recorded (candidate_slot='$slot'); run CD / Prepare Production, or the last cutover already consumed it"
  [[ "$release" =~ ^[a-f0-9]{40}:[a-f0-9]{64}$ ]] ||
    prod_fail "the recorded candidate release '$release' is not a <sha>:<release-id> pair"
  [[ "$run_id" =~ ^[1-9][0-9]{0,18}$ ]] ||
    prod_fail "the recorded preparation run id '$run_id' is not a run id"
  instant_to_epoch "$ready_at" >/dev/null ||
    prod_fail "the recorded candidate has no usable candidate_ready_at ('$ready_at')"
  printf '%s %s %s' "$slot" "$release" "$run_id"
}

# Twenty-four hours. A candidate prepared yesterday morning and promoted after
# this evening's incident review is the flow this supports; a candidate from
# last week is one whose smoke predates changes nobody correlated with it.
#
# ":-", for the same reason as the retention window: an unset environment
# variable reaches the runner as the empty string.
NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS="${NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS:-86400}"

candidate_max_age_seconds() {
  bounded_seconds NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS \
    "$NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS"
}

# The candidate has not been sitting unpromoted for longer than the evidence
# behind it is worth.
#
# Separate from the identity proof and weaker than it: the identity catches a
# candidate that was *replaced*, this catches one that was not. A slot nobody
# touched for a week is still bit-identical to what was smoked, and its
# dependencies, configuration and database are not. Failing closed here costs
# one preparation run; not failing costs a promotion nobody validated recently.
require_candidate_not_stale() {
  local ready_at="$1" window ready age
  window="$(candidate_max_age_seconds)" || return 1
  ready="$(instant_to_epoch "$ready_at")" || return 1
  age=$(( $(date -u +%s) - ready ))
  candidate_age_within_window "$age" "$window" || return 1
  echo "candidate evidence is ${age}s old, within the ${window}s limit"
}

# The comparison itself, with no clock in it.
#
# Split out so the boundary is testable at all: asserting that an age of
# exactly `window` passes is impossible against a live clock, because the
# second it takes to build the fixture and start the script moves the age. The
# rule is inclusive -- `age == window` is within the window -- and that is the
# kind of off-by-one that is invisible until the day it matters.
#
# A minute of clock skew is allowed in the other direction. Without it, dating
# a candidate forward would make it permanently fresh.
candidate_age_within_window() {
  local age="$1" window="$2"
  ((age >= -60)) ||
    { echo "the candidate is dated ${age#-}s in the future; the record is not trustworthy" >&2; return 1; }
  ((age <= window)) ||
    { echo "the candidate was prepared ${age}s ago and the limit is ${window}s; prepare it again rather than promoting evidence this old" >&2; return 1; }
}
