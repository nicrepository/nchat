#!/usr/bin/env bash
# Return production traffic to a named slot (issue #626).
#
#   rollback.sh --target blue "5xx on /api/chat after cutover"
#
# The target is named for the same reason cutover's is, and here the cost of
# getting it wrong is higher: a rollback that derived its destination from the
# current state would, on a second run, send production back to the release it
# had just been rescued from. Naming it means running this twice is a no-op.
#
# No build, no image, no migration — it moves the same nine selectors, which is
# why it takes seconds, and why keeping the old slot running through the
# observation window is what makes it possible at all.
#
# It does not touch the database. A schema rollback is a separate, deliberate
# procedure; the release contract is that migrations stay compatible with the
# previous slot precisely so this command never needs one.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

# Everything that must hold before traffic may be pointed at a slot.
#
# Readiness alone is not it, and that was the gap: a slot whose nine Deployments
# are all Ready can still be carrying two different releases, because a deploy
# that failed part-way leaves the workloads it never reached running and healthy
# on the previous version. Rolling onto that serves users a combination nobody
# built. cutover.sh already refused it; this is the same gate, from the same
# helper, so the two cannot drift apart.
#
# Prints the release the slot carries.
# `|| return 1` on every command substitution, deliberately.
#
# When a failure two levels deep exits its subshell, bash does not reliably
# propagate that through an enclosing assignment under `set -e`: the message
# lands on stderr and the caller carries on with an empty value. That is how a
# refusal to promote a mixed slot became a rollback that promoted it anyway.
# Stating the propagation is the fix; the shell option is not enough on its own.
assert_target_serviceable() {
  local target="$1" release
  release="$(require_consistent_release "$target")" || return 1
  slot_ready "$target" ||
    prod_fail "slot $target is not Ready; rolling back to it would not restore service"
  printf '%s' "$release"
}

# Re-reads the cluster after the selectors moved.
#
# Nothing here is assumed from the precheck: between then and now a workload can
# have been redeployed or a Service patched by someone else, and a rollback that
# reported success on the strength of a stale reading is the failure mode this
# whole command exists to avoid.
verify_converged() {
  local target="$1" mapping actual
  mapping="$(collect_service_slots)"
  if ! actual="$(resolve_active_slot "$mapping" 2>/dev/null)"; then
    echo "Services are still mixed after the rollback:" >&2
    printf '%s\n' "$mapping" >&2
    prod_fail "rollback did not converge; re-run with the same --target $target"
  fi
  [[ "$actual" == "$target" ]] ||
    prod_fail "rollback ended on slot $actual, expected $target"
  assert_target_serviceable "$target" || return 1
}

report_no_op() {
  local target="$1" release="$2"
  echo "Every stable Service already selects slot $target; nothing to move."
  echo "Slot $target is active, Ready and carrying release $release. No change made."
}

main() {
  local target reason mapping release final
  target="$(require_target_slot "$@")"
  shift 2
  reason="${1:-}"
  [[ -n "$reason" ]] ||
    prod_fail "usage: rollback.sh --target <blue|green> <reason>  (the reason is recorded in the release log)"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  print_context_banner "$mapping"
  echo "rollback target: $target"
  echo "reason         : $reason"
  # Before the no-op decision, not after: a slot that is already active but
  # carrying a mixed release is a fault to report, never a success to confirm.
  # Never pick a different slot because this one fails — rolling traffic onto a
  # slot that cannot serve it turns one incident into two.
  release="$(assert_target_serviceable "$target")" || return 1
  echo "target release : $release"
  if all_services_on_slot "$mapping" "$target"; then
    report_no_op "$target" "$release"
    return 0
  fi
  confirm "Move production traffic back to slot $target"
  if ! switch_services_to_slot "$target"; then
    echo "Rollback stopped part-way; production is mixed." >&2
    echo "Re-run 'rollback.sh --target $target \"$reason\"' to finish converging." >&2
    return 1
  fi
  final="$(verify_converged "$target")" || return 1
  echo
  echo "Production serves slot $target again, on release $final."
  echo "The failed slot is left running for investigation; do not delete it before"
  echo "its logs and events have been captured."
  echo "Record in the release log: rollback to $target, reason: $reason"
}

main "$@"
