#!/usr/bin/env bash
# Smoke production through the stable Services, after the traffic has moved
# (issue #933).
#
#   stable-smoke.sh --target green --after cutover
#   stable-smoke.sh --target blue  --after rollback
#
# A separate command from smoke.sh, and deliberately not a flag on it.
#
# smoke.sh validates a *candidate*: it talks to the per-slot Services, and its
# central assertion is that the slot carries no production traffic. That
# assertion is the reason it is trustworthy, and it is exactly false here.
# Adding an --isolated=false to it would have meant the one gate that keeps a
# candidate smoke honest could be turned off by an argument, on the same code
# path, one typo away from a release that validated nothing.
#
# So the isolation rule is inverted rather than disabled: this command requires
# the target to be what every stable Service selects, and it reaches the
# workloads the way production reaches them -- through the stable Service
# names, not through <service>-<slot>. That is what makes it a post-traffic
# check: the thing being proved is that the selectors, the pods behind them and
# the release they carry are one coherent, serving system.
#
# What it does not do is replace the authenticated release smoke. It is a shell
# against in-cluster Services; it cannot sign in, cannot watch a message arrive
# for a second account and cannot upload a file past authorization. It reports
# what it checked and nothing more.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

FAILURES=0
# cutover or rollback. It changes no assertion -- both states must satisfy the
# same conditions -- and appears only in the report, so the evidence says which
# operation it was taken after.
AFTER=""

record() {
  local outcome="$1" detail="$2"
  if [[ "$outcome" == ok ]]; then
    printf '  [OK]   %s\n' "$detail"
    return 0
  fi
  printf '  [FAIL] %s\n' "$detail" >&2
  FAILURES=$((FAILURES + 1))
}

# The inverse of smoke.sh's check_isolation, and the reason the two are
# separate files. Every stable Service must select the target: one that did not
# move is a partial cutover, and reporting a pass over it would describe a
# namespace serving two releases as healthy.
check_converged() {
  local target="$1" mapping stragglers
  mapping="$(collect_service_slots)"
  stragglers="$(awk -v s="$target" '$2 != s { print $1 "=" $2 }' <<<"$mapping" | tr '\n' ' ')"
  if [[ -n "$stragglers" ]]; then
    record fail "stable Services are not all on $target: $stragglers"
    return
  fi
  record ok "every stable Service selects $target"
}

check_release_identity() {
  local target="$1" state
  state="$(slot_release_state "$target")" || {
    record fail "cannot read the release identity of slot $target"
    return
  }
  case "$state" in
    CONSISTENT\ *) record ok "slot $target serves one release across every workload (${state#CONSISTENT })" ;;
    *) record fail "slot $target is serving traffic while its release state is $state" ;;
  esac
}

check_readiness() {
  local target="$1" service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if deployment_ready "$service-$target"; then
      record ok "deployment/$service-$target all replicas Ready"
    else
      record fail "deployment/$service-$target not all replicas Ready"
    fi
  done
}

# Through the stable Service, not the per-slot one. That is the difference that
# makes this a check of what users reach: it exercises the selector as well as
# the pod, so a Service pointing at a slot with no matching pods fails here and
# would pass a per-slot probe.
probe_stable_service() {
  local service="$1" path="$2" attempt delay=1
  for attempt in 1 2 3 4 5; do
    if kubectl get --request-timeout=10s --raw \
      "/api/v1/namespaces/$NCHAT_PROD_NAMESPACE/services/http:$service:http/proxy$path" \
      >/dev/null 2>&1; then
      return 0
    fi
    [[ "$attempt" -lt 5 ]] || return 1
    sleep "$delay"
    delay=$((delay * 2))
  done
}

check_probes() {
  local path="$1" service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if probe_stable_service "$service" "$path"; then
      record ok "$service $path"
    else
      record fail "$service $path"
    fi
  done
}

print_verdict() {
  local target="$1"
  echo
  echo "Target slot                : $target"
  echo "Operation                  : post-$AFTER"
  if [[ "$FAILURES" -eq 0 ]]; then
    echo "Post-$AFTER smoke        : PASS"
    return 0
  fi
  echo "Post-$AFTER smoke        : FAIL ($FAILURES check(s))"
  echo
  echo "Production is serving this slot. Nothing here rolls back, and nothing here"
  echo "retires the other slot: it is still running and is the way back."
  return 1
}

parse_after() {
  case "${1:-}" in
    --after) ;;
    *) prod_fail "usage: stable-smoke.sh --target <blue|green> --after <cutover|rollback>" ;;
  esac
  case "${2:-}" in
    cutover | rollback) AFTER="$2" ;;
    *) prod_fail "--after must be cutover or rollback, got '${2:-}'" ;;
  esac
}

main() {
  local target
  target="$(require_target_slot "$@")"
  shift 2
  parse_after "$@"
  require_context
  require_namespace
  echo "=== post-$AFTER smoke: production on slot $target ==="
  echo "traffic convergence:"
  check_converged "$target"
  echo "release identity:"
  check_release_identity "$target"
  echo "workload readiness:"
  check_readiness "$target"
  echo "liveness through the stable Services:"
  check_probes /healthz
  echo "readiness through the stable Services:"
  check_probes /readyz
  print_verdict "$target"
}

main "$@"
