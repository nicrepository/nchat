#!/usr/bin/env bash
# Retire the slot that no longer serves traffic (issue #626).
#
#   drain-old.sh --target blue
#
# Separate from cutover, and never automatic: between the two there is an
# observation window during which the old slot is the rollback. Running this
# ends that window, so it asks for it explicitly.
#
# Scaling to zero, rather than deleting, is what makes the old slot's WebSockets
# close through the application's own shutdown path: each pod gets SIGTERM,
# stops reporting Ready, finishes in-flight requests and closes its hub
# connections within terminationGracePeriodSeconds. Clients then reconnect to
# the stable host, which already points at the new slot.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

scale_slot_to_zero() {
  local slot="$1" service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    kubectl scale "deployment/$service-$slot" -n "$NCHAT_PROD_NAMESPACE" --replicas=0
  done
}

main() {
  local target mapping active
  # Named, like every other production mutation: scaling a slot to zero is the
  # step that removes the fast way back, so which slot it is must be the
  # operator's statement rather than an inference from the current selectors.
  target="$(require_target_slot "$@")"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  active="$(resolve_active_slot "$mapping")"
  print_context_banner "$mapping"
  echo "active slot: $active"
  echo "slot to retire: $target"
  [[ "$target" != "$active" ]] ||
    prod_fail "slot $target is serving production traffic; refusing to scale it to zero"
  # Refusing while the active slot is unhealthy is the point: retiring the old
  # slot removes the only fast way back.
  slot_ready "$active" || prod_fail "active slot $active is not fully Ready; keep $target available"
  echo
  echo "After this, rollback to $target requires redeploying it — it is no longer instant."
  confirm "Scale slot $target down to zero"
  scale_slot_to_zero "$target"
  echo
  echo "Slot $target is scaled to zero. Its Deployments and Services remain, so the"
  echo "next release can deploy into it without recreating anything."
}

main "$@"
