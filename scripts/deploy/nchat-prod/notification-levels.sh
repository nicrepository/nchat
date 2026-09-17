#!/usr/bin/env bash
# Open or close the granular conversation notification levels (issue #136).
#
#   notification-levels.sh --status
#   notification-levels.sh --set true
#   notification-levels.sh --set false
#
# # Why this is a command and not a ConfigMap edit
#
# CHAT_CONVERSATION_NOTIFICATION_LEVELS_ENABLED reaches chat-service through
# `envFrom: configMapRef`, which Kubernetes resolves once, when a container
# starts. Editing nchat-config therefore changes what the *next* pod will read
# and nothing about the pods already running. An operator who edited the
# ConfigMap and moved on would believe the gate was open while every serving
# process still refused the granular mode — and would find out from users.
#
# So the operation is the whole sequence: patch, restart, wait, and prove the
# value the new pods actually loaded. Any step failing stops it, loudly.
#
# # Why both slots
#
# Production keeps two slots deployed through the observation window, and a
# cutover promotes whichever one is idle. Restarting only the active slot would
# leave the idle one holding pods started against the previous value, so the
# next cutover would silently move production back to the old behaviour. Both
# are restarted; a slot scaled to zero has no pods to restart and is reported as
# such rather than skipped quietly.
#
# Only chat-service is touched. It is the only service that reads this key: the
# gate lives in its SidebarService, and the web app learns the capability from
# chat-service's own sidebar payload.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

NCHAT_PROD_NOTIFICATION_LEVELS_KEY=CHAT_CONVERSATION_NOTIFICATION_LEVELS_ENABLED
NCHAT_PROD_CONFIGMAP=nchat-config
# The one workload that reads the key.
NCHAT_PROD_NOTIFICATION_LEVELS_SERVICE=chat-service
ROLLOUT_TIMEOUT="${NCHAT_PROD_ROLLOUT_TIMEOUT:-300s}"

usage() {
  cat >&2 <<'USAGE'
usage:
  notification-levels.sh --status
  notification-levels.sh --set <true|false>
USAGE
  exit 1
}

# The requested value, validated against the only two the application accepts.
#
# A third value is refused here rather than written: chat-service's
# Config.Validate refuses to start on a non-boolean, so writing one would take
# production down at the next restart instead of failing this command.
parse_args() {
  local mode="" value=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --status) mode=status ;;
      --set)
        mode=set
        value="${2:-}"
        shift
        ;;
      *) usage ;;
    esac
    shift
  done
  [[ -n "$mode" ]] || usage
  if [[ "$mode" == set ]]; then
    [[ "$value" == "true" || "$value" == "false" ]] ||
      prod_fail "--set takes 'true' or 'false', got '${value:-}'"
  fi
  printf '%s %s' "$mode" "$value"
}

# What nchat-config currently says. Empty output means the key is absent, which
# the application reads as false.
configured_value() {
  kubectl get configmap "$NCHAT_PROD_CONFIGMAP" -n "$NCHAT_PROD_NAMESPACE" \
    -o "jsonpath={.data.$NCHAT_PROD_NOTIFICATION_LEVELS_KEY}" 2>/dev/null
}

# Whether a slot has a chat-service Deployment at all.
#
# deployment_component is the probe because it reads the Deployment itself: it
# answers empty for a Deployment that does not exist, which is the same signal
# ready_pod_of already relies on.
slot_is_deployed() {
  local slot="$1" component
  component="$(deployment_component "$NCHAT_PROD_NOTIFICATION_LEVELS_SERVICE-$slot")" || return 1
  [[ -n "$component" ]]
}

# A Ready pod of one slot's chat-service, or nothing.
#
# Labels are derived from the Deployment's own selector by deployment_component,
# exactly as deployment_observed_releases does it, rather than guessed here: the
# component label is `chat`, not the Deployment's name, and a selector written
# from the name would match nothing and report every slot as having no pod.
ready_pod_of() {
  local slot="$1" deployment component template names name
  deployment="$NCHAT_PROD_NOTIFICATION_LEVELS_SERVICE-$slot"
  component="$(deployment_component "$deployment")" || return 0
  [[ -n "$component" ]] || return 0
  template='{range .items[?(@.status.conditions[?(@.type=="Ready")].status=="True")]}'
  template+="{.metadata.name}{'\n'}{end}"
  names="$(kubectl get pods -n "$NCHAT_PROD_NAMESPACE" \
    -l "app.kubernetes.io/component=$component,$NCHAT_PROD_SLOT_LABEL=$slot" \
    -o "jsonpath=$template" 2>/dev/null)" || return 0
  # The first non-blank name, read in the shell rather than through
  # `| head -1`: head closes the pipe after one line, the writer takes SIGPIPE,
  # and `set -o pipefail` then reports the whole pipeline as failed — which read
  # as "this slot has no pod" for a slot that has several.
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    printf '%s' "$name"
    return 0
  done <<<"$names"
  return 0
}

# The value a Ready pod of one slot actually has in its environment.
#
# This is the only check that answers the question the operator cares about: the
# ConfigMap is what the *next* pod will read, and `printenv` is what this pod
# did read. Prints nothing when the slot has no Ready pod.
loaded_value() {
  local slot="$1" pod
  pod="$(ready_pod_of "$slot")"
  [[ -n "$pod" ]] || return 0
  # `|| true`: an absent variable makes printenv exit non-zero, which is a
  # reading — "this pod has no such value" — and not a failure of this command.
  kubectl exec -n "$NCHAT_PROD_NAMESPACE" "$pod" -- \
    printenv "$NCHAT_PROD_NOTIFICATION_LEVELS_KEY" 2>/dev/null || true
}

# The effective value of a slot, normalised so an absent variable and an absent
# pod are distinguishable in the report.
slot_report() {
  local slot="$1" value
  value="$(loaded_value "$slot" | tr -d '\r\n')"
  if [[ -z "$value" ]]; then
    printf 'no Ready pod, or the value is unset'
    return 0
  fi
  printf '%s' "$value"
}

print_status() {
  local slot configured
  configured="$(configured_value)"
  echo "configmap    : $NCHAT_PROD_CONFIGMAP.$NCHAT_PROD_NOTIFICATION_LEVELS_KEY=${configured:-<unset>}"
  for slot in "${NCHAT_PROD_SLOTS[@]}"; do
    echo "slot $slot pods: $(slot_report "$slot")"
  done
}

apply_value() {
  local value="$1" observed
  kubectl patch configmap "$NCHAT_PROD_CONFIGMAP" -n "$NCHAT_PROD_NAMESPACE" \
    --type merge \
    -p "{\"data\":{\"$NCHAT_PROD_NOTIFICATION_LEVELS_KEY\":\"$value\"}}" ||
    prod_fail "could not patch $NCHAT_PROD_CONFIGMAP; nothing was restarted"
  # Read back rather than trusting the patch: a merge that silently did nothing
  # would otherwise be followed by a restart that changes nothing, and the
  # command would report success.
  observed="$(configured_value)"
  [[ "$observed" == "$value" ]] ||
    prod_fail "$NCHAT_PROD_CONFIGMAP.$NCHAT_PROD_NOTIFICATION_LEVELS_KEY reads '${observed:-<unset>}' after the patch, expected '$value'; nothing was restarted"
}

# Restart every deployed slot's chat-service and wait for each rollout.
#
# `rollout restart` and not `delete pod`: it goes through the Deployment, so the
# replacement is admitted by the same readiness gate a release is, and a slot
# scaled to zero is annotated now and starts with the new value whenever it is
# scaled up.
restart_slots() {
  local slot deployment
  for slot in "${NCHAT_PROD_SLOTS[@]}"; do
    deployment="deployment/$NCHAT_PROD_NOTIFICATION_LEVELS_SERVICE-$slot"
    # A slot that has never been deployed has no Deployment to restart — the
    # state between bootstrap and the first release into the other slot. It is
    # reported rather than skipped silently, and rather than failing the
    # command: there is nothing there holding a stale value, and whatever is
    # deployed into it later starts against the ConfigMap as it is then.
    if ! slot_is_deployed "$slot"; then
      echo "slot $slot : not deployed; nothing to restart"
      continue
    fi
    echo "restarting $deployment"
    kubectl rollout restart "$deployment" -n "$NCHAT_PROD_NAMESPACE" ||
      prod_fail "could not restart $deployment; the ConfigMap already carries the new value, so re-run this command"
    if ! kubectl rollout status "$deployment" -n "$NCHAT_PROD_NAMESPACE" --timeout="$ROLLOUT_TIMEOUT"; then
      kubectl describe "$deployment" -n "$NCHAT_PROD_NAMESPACE" || true
      kubectl get pods -n "$NCHAT_PROD_NAMESPACE" -l "$NCHAT_PROD_SLOT_LABEL=$slot" -o wide || true
      prod_fail "$deployment did not become Ready within $ROLLOUT_TIMEOUT; the gate may be half-applied — re-run this command once the rollout is healthy"
    fi
  done
}

# Prove the running processes carry the requested value.
#
# A slot with no running pod is not a failure — it is a slot scaled to zero, and
# it will read the ConfigMap when it starts — but a slot whose pods carry the
# *previous* value is: that is the stale-slot case a later cutover would
# promote.
verify_slots() {
  local value="$1" slot observed stale=0 verified=0
  for slot in "${NCHAT_PROD_SLOTS[@]}"; do
    observed="$(loaded_value "$slot" | tr -d '\r\n')"
    if [[ -z "$observed" ]]; then
      echo "slot $slot : no Ready pod; it will read '$value' when scaled up"
      continue
    fi
    if [[ "$observed" == "$value" ]]; then
      echo "slot $slot : pods carry $NCHAT_PROD_NOTIFICATION_LEVELS_KEY=$observed"
      verified=$((verified + 1))
      continue
    fi
    echo "slot $slot : pods carry '$observed', expected '$value'" >&2
    stale=1
  done
  [[ "$stale" -eq 0 ]] ||
    prod_fail "at least one slot is still running pods with a stale value; a cutover to it would change behaviour silently"
  [[ "$verified" -gt 0 ]] ||
    prod_fail "no slot had a Ready pod to verify; the ConfigMap carries '$value' but nothing was proved to have loaded it"
}

main() {
  local parsed mode value mapping active
  parsed="$(parse_args "$@")"
  mode="${parsed%% *}"
  value="${parsed#* }"
  require_context
  require_namespace
  mapping="$(collect_service_slots)"
  active="$(resolve_active_slot "$mapping")"
  print_context_banner "$mapping"
  echo "active slot  : $active"
  print_status
  if [[ "$mode" == status ]]; then
    return 0
  fi
  echo
  echo "This patches $NCHAT_PROD_CONFIGMAP and restarts $NCHAT_PROD_NOTIFICATION_LEVELS_SERVICE in BOTH slots."
  if [[ "$value" == "true" ]]; then
    echo "Precondition: no reader from before issue #136 is left — neither an HTTP"
    echo "slot, nor a realtime connection, nor a notification worker."
  fi
  confirm "Set $NCHAT_PROD_NOTIFICATION_LEVELS_KEY=$value in production"
  apply_value "$value"
  restart_slots
  verify_slots "$value"
  echo
  echo "$NCHAT_PROD_NOTIFICATION_LEVELS_KEY=$value is applied and loaded."
  print_status
}

main "$@"