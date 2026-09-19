#!/usr/bin/env bash
# Everything that must hold before a rollback may patch its first Service
# (issues #801, #933).
#
#   rollback-preflight.sh --target <blue|green> <selectors-snapshot-file>
#
# Prints, on stdout, the facts the rollback run needs and nothing else:
#
#   target=<slot>
#   target_release_sha=<40 hex>
#   target_release=<sha>:<id>
#   from_slot=<slot>
#   from_release_sha=<40 hex>
#
# in the `key=value` form GitHub Actions appends straight to $GITHUB_OUTPUT.
#
# Unlike the cutover, the target here is the operator's and is never derived.
# That is #801's rule and it is not a symmetry oversight: a rollback runs
# during an incident, it may be run twice, and the one thing it must never do
# is compute its destination from the state it is about to change. Derived,
# a second run would send production back to the release it had just been
# rescued from. Named, a second run converges.
#
# The two SHAs printed are what the schema gate is run over afterwards: the
# release the target carries, and the release currently in front of it. The
# second is read from the *opposite* slot rather than from "the active one",
# because during a partial cutover there is no active one and the question --
# "what has the schema been migrated for?" -- still has an answer.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

log() { printf '%s\n' "$*" >&2; }

# The release the slot being rolled back *off* is carrying.
#
# It is what the database was migrated for, so it is the other end of the
# schema comparison. A slot that carries no single release cannot answer the
# question, and an unanswerable schema question is a refusal: rolling back
# blind is the failure this whole gate exists to prevent, and "we could not
# tell" is not a reason to do it anyway.
serving_release_of() {
  local slot="$1" state
  state="$(slot_release_state "$slot" 2>/dev/null)" || state=UNKNOWN
  case "$state" in
    CONSISTENT\ *) printf '%s' "${state#CONSISTENT }" ;;
    *)
      log "slot $slot is $state, so the release production has been migrated for cannot be identified."
      log "Schema compatibility cannot be proved and the rollback is refused. Follow the"
      log "incident procedure in docs/runbooks/production-blue-green-deployment.md."
      return 1
      ;;
  esac
}

main() {
  local target snapshot mapping from_slot target_release from_release
  target="$(require_target_slot "$@")"
  shift 2
  snapshot="${1:-}"
  [[ -n "$snapshot" ]] ||
    prod_fail "usage: rollback-preflight.sh --target <blue|green> <selectors-snapshot-file>"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  printf '%s\n' "$mapping" >"$snapshot"
  print_context_banner "$mapping" >&2
  log "rollback target: $target"
  # A blue/green split passes, and must: converging a half-finished cutover on
  # an explicit target is a legitimate rollback. A selector this cannot
  # describe does not.
  require_promotable_selectors "$mapping" "$target"
  # Readiness and one consistent release, both before any mutation. A slot
  # whose workloads are all Ready can still be carrying two releases, because
  # a deploy that failed part-way leaves the ones it never reached on the
  # previous version; rolling onto that serves a combination nobody built.
  slot_ready "$target" ||
    prod_fail "slot $target is not Ready; rolling back to it would not restore service"
  target_release="$(require_consistent_release "$target")" || return 1
  log "target release : $target_release"
  from_slot="$(opposite_slot "$target")"
  from_release="$(serving_release_of "$from_slot")" || return 1
  log "current release: $from_release (slot $from_slot)"
  printf 'target=%s\ntarget_release_sha=%s\ntarget_release=%s\nfrom_slot=%s\nfrom_release_sha=%s\n' \
    "$target" "${target_release%%:*}" "$target_release" "$from_slot" "${from_release%%:*}"
}

main "$@"
