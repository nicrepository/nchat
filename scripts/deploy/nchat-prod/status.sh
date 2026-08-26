#!/usr/bin/env bash
# Where production actually is, derived from the cluster (issue #626).
#
# It always prints the whole picture before it decides anything. During a
# partial cutover — the moment this command matters most — some Services have
# moved and others have not, and one of them may not exist at all; stopping at
# the first problem would hide exactly the map the operator needs. So every
# stable Service and both slots are reported, and only then does the exit code
# reflect what was found.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

PROBLEMS=0

note_problem() { PROBLEMS=$((PROBLEMS + 1)); }

# Each Service reported with its own state. MISSING, UNSET and an unrecognised
# selector are three distinct operational conditions and none of them is "mixed":
# a deleted Service, one whose selector was never patched, and one pointing at a
# slot nobody recognises need three different responses.
report_services() {
  local mapping="$1" service state
  echo "stable services:"
  while read -r service state; do
    case "$state" in
      blue | green) printf '  %-22s -> %s\n' "$service" "$state" ;;
      MISSING) printf '  %-22s -> MISSING (the Service does not exist)\n' "$service"; note_problem ;;
      UNSET) printf '  %-22s -> UNSET (no release-slot selector)\n' "$service"; note_problem ;;
      *) printf '  %-22s -> INVALID (selector %s)\n' "$service" "$state"; note_problem ;;
    esac
  done <<<"$mapping"
}

report_release() {
  local slot="$1" state
  state="$(slot_release_state "$slot")" || {
    printf '  release   UNKNOWN (could not be read)\n'
    note_problem
    return
  }
  case "$state" in
    CONSISTENT\ *) printf '  release   %s\n  state     CONSISTENT\n' "${state#CONSISTENT }" ;;
    NOT_DEPLOYED) printf '  release   -\n  state     NOT DEPLOYED\n' ;;
    ROLLING_OUT)
      # Distinct from MIXED: the slot is not broken, it has simply not finished.
      # The per-workload lines show which releases its Ready pods are on.
      printf '  release   ROLLING OUT\n  state     INCOMPLETE\n'
      slot_workload_releases "$slot" | awk '{ printf "    %-22s observed=%s  %s\n", $1, $2, $3 }'
      note_problem
      ;;
    *)
      printf '  release   MIXED\n  state     INVALID\n'
      slot_workload_releases "$slot" | awk '{ printf "    %-22s %s  %s\n", $1, $2, $3 }'
      note_problem
      ;;
  esac
}

report_readiness() {
  local slot="$1"
  if slot_ready "$slot" 2>/dev/null; then
    echo "  readiness all workloads Ready"
  else
    echo "  readiness NOT all workloads Ready"
  fi
}

report_slot() {
  local slot="$1" role="$2"
  printf '%s (%s):\n' "$slot" "$role"
  report_release "$slot"
  report_readiness "$slot"
}

# The active slot when the Services agree, or the empty string. Not an error:
# disagreement is reported by report_services and must not stop the slot report.
active_slot_or_empty() {
  local mapping="$1" slots
  slots="$(distinct_slots "$mapping")"
  [[ "$(printf '%s\n' "$slots" | grep -c .)" -eq 1 ]] || return 0
  is_valid_slot "$slots" || return 0
  printf '%s' "$slots"
}

report_roles() {
  local active="$1"
  if [[ -z "$active" ]]; then
    echo "ACTIVE   : NONE (the stable Services do not agree)"
    echo "CANDIDATE: NONE"
    return
  fi
  echo "ACTIVE   : $active"
  echo "CANDIDATE: $(opposite_slot "$active")"
}

main() {
  local mapping active
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  echo "=== nchat production release slots ==="
  echo "kube context : $(kubectl config current-context)"
  echo "namespace    : $NCHAT_PROD_NAMESPACE"
  echo "environment  : production"
  echo
  active="$(active_slot_or_empty "$mapping")"
  report_roles "$active"
  echo
  report_services "$mapping"
  echo
  report_slot blue "$([[ "$active" == blue ]] && echo active || echo candidate)"
  echo
  report_slot green "$([[ "$active" == green ]] && echo active || echo candidate)"
  if [[ -z "$active" ]]; then
    echo
    echo "MIXED STATE: the stable Services do not all select the same slot." >&2
    echo "Converge with 'cutover.sh --target <slot>' or 'rollback.sh --target <slot> <reason>'." >&2
    note_problem
  fi
  [[ "$PROBLEMS" -eq 0 ]] || return 1
}

main "$@"
