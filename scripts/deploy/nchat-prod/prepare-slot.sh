#!/usr/bin/env bash
# Make the idle slot available to the next candidate, or refuse (issue #933).
#
#   prepare-slot.sh <selectors-snapshot-file>
#
# Prints, on stdout, the two facts the caller needs and nothing else:
#
#   active=<slot>
#   candidate=<slot>
#
# in the `key=value` form GitHub Actions appends straight to $GITHUB_OUTPUT,
# the same way scripts/deploy/nchat-dev/image-matrix.sh already does. The
# snapshot of the stable Service selectors is written to the file named in $1,
# because the deploy that follows has to prove they did not move and both
# halves of that comparison must come from the same reader.
#
# What this exists for is the step that did not exist before #933: the idle
# slot is no longer free by definition. After a cutover the demoted slot is the
# rollback, and it is kept running until the next release is prepared. So
# "deploy into the opposite slot" is now a question with three answers --
# it is free, it is reserved but the window has passed, or it is reserved --
# and the third one must stop the release rather than overwrite the only fast
# way back from what is currently in production.
#
# The decision is lifecycle.sh's and the retirement is drain-old.sh's. This
# file is the orchestration between them, and it deliberately holds neither:
# a second copy of the eligibility rule or of the scale-to-zero would be the
# copy that drifts.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"
# shellcheck source=scripts/deploy/nchat-prod/release-state.sh
source "$SCRIPT_DIR/release-state.sh"
# shellcheck source=scripts/deploy/nchat-prod/lifecycle.sh
source "$SCRIPT_DIR/lifecycle.sh"

# Everything this prints that is not one of the two output lines goes to
# stderr: stdout is a machine-readable record the caller appends to a GitHub
# command file, and a stray line of prose in it becomes a job output.
log() { printf '%s\n' "$*" >&2; }

retire_reserved_slot() {
  local target="$1"
  log "retiring the expired rollback reservation on slot $target"
  NCHAT_PROD_ASSUME_YES=1 "$SCRIPT_DIR/drain-old.sh" --target "$target" >&2
  require_slot_scaled_to_zero "$target" >&2
}

main() {
  local snapshot="${1:-}" mapping active candidate record disposition record_status=0
  [[ -n "$snapshot" ]] ||
    prod_fail "usage: prepare-slot.sh <selectors-snapshot-file>"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  printf '%s\n' "$mapping" >"$snapshot"
  active="$(resolve_active_slot "$mapping")"
  candidate="$(opposite_slot "$active")"
  print_context_banner "$mapping" >&2
  log "active slot   : $active"
  log "candidate slot: $candidate"
  # The status matters as much as the text: an absent record is a bootstrap, a
  # record that could not be read is a refusal, and both arrive as an empty
  # string.
  record="$(release_state_read)" || record_status=$?
  disposition="$(candidate_slot_disposition "$record" "$record_status" "$mapping" "$active" "$candidate")" ||
    prod_fail "slot $candidate cannot be reused for this release; see the reason above"
  case "$disposition" in
    FREE) log "slot $candidate carries no rollback reservation" ;;
    RETIRE) retire_reserved_slot "$candidate" ;;
    *) prod_fail "unrecognised lifecycle disposition '$disposition'" ;;
  esac
  printf 'active=%s\ncandidate=%s\n' "$active" "$candidate"
}

main "$@"
