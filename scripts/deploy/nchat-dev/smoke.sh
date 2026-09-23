#!/usr/bin/env bash
# Operational proof that the nchat-dev deploy actually delivered something
# (issue #933).
#
#   scripts/deploy/nchat-dev/smoke.sh
#
# Separate from deploy.sh, and that separation is the point: deploy.sh applies
# manifests and waits for rollouts, which proves Kubernetes accepted the change.
# It does not prove the release works. A pod can be Ready while the route in
# front of it 404s, the migration Job can be gone rather than complete, and a
# container can be restarting on a loop between two readiness probes.
#
# It is a smoke, not the E2E suite. The Playwright suites already ran against
# this commit in `E2E / Web` and `E2E / Admin`; re-running them here would cost
# minutes to re-answer a question CI answered, and would still not check the
# thing only a deployed environment can be asked -- whether *this cluster* is
# serving *this release*.
#
# Every check is deterministic. No fixed sleeps, no retries that paper over a
# race: the waits below are polls with a deadline against a condition that is
# either true or not, and a check that cannot be answered fails.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-dev/lib.sh
source "$SCRIPT_DIR/lib.sh"

NAMESPACE=nchat-dev
FAILURES=0

record() {
  local outcome="$1" detail="$2"
  if [[ "$outcome" == ok ]]; then
    printf '  [OK]   %s\n' "$detail"
    return 0
  fi
  printf '  [FAIL] %s\n' "$detail" >&2
  FAILURES=$((FAILURES + 1))
}

# --- workload state --------------------------------------------------------

check_rollouts() {
  local workload
  for workload in "${NCHAT_DEV_APPLICATION_DEPLOYMENTS[@]}"; do
    if kubectl rollout status "deployment/$workload" -n "$NAMESPACE" --timeout=60s >/dev/null 2>&1; then
      record ok "deployment/$workload rollout complete"
    else
      record fail "deployment/$workload rollout not complete"
    fi
  done
}

# A container that is Ready right now can still be in a crash loop: readiness
# is sampled, and CrashLoopBackOff is a container that has already failed
# repeatedly and is being restarted on a back-off. Between two probes it looks
# healthy, which is exactly why a deploy gate that only reads readiness passes
# over it.
#
# Every waiting reason is read, not just the loop: ImagePullBackOff and
# CreateContainerConfigError are the same class of "this will never come up"
# and a deploy that shipped one must not be reported as delivered.
check_no_restart_loops() {
  local reasons stuck
  if ! reasons="$(kubectl get pods -n "$NAMESPACE" \
    -o jsonpath='{range .items[*]}{range .status.containerStatuses[*]}{.state.waiting.reason}{"\n"}{end}{end}')"; then
    record fail "the pods of $NAMESPACE could not be read"
    return
  fi
  # awk, not grep: "no container is waiting" is the healthy answer and grep
  # reports it as exit 1, which under pipefail is a failure to distinguish
  # from kubectl having failed.
  stuck="$(awk '/CrashLoopBackOff|ImagePullBackOff|ErrImagePull|CreateContainerConfigError/' <<<"$reasons" |
    LC_ALL=C sort -u | tr '\n' ' ')"
  if [[ -n "$stuck" ]]; then
    record fail "containers are stuck: $stuck"
    return
  fi
  record ok "no container is in a restart or image back-off"
}

# The migration Job of this deploy, by its Complete condition rather than by
# its absence. A Job that was deleted, never created, or failed all read as
# "not Complete" here; only a Job the API reports as Complete passes.
check_migrations_applied() {
  local condition
  condition="$(kubectl get job/nchat-migrations -n "$NAMESPACE" \
    -o 'jsonpath={.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null)" || condition=""
  if [[ "$condition" == "True" ]]; then
    record ok "job/nchat-migrations reports Complete"
    return
  fi
  record fail "job/nchat-migrations does not report Complete (condition: ${condition:-absent})"
}

# --- service reachability --------------------------------------------------

# Through the cluster API's Service proxy, the same way the production smoke
# reaches its slots. It exercises the Service and its endpoints, so a Service
# with no matching pods fails here.
probe_service() {
  local service="$1" path="$2" attempt delay=1
  for attempt in 1 2 3 4 5; do
    if kubectl get --request-timeout=10s --raw \
      "/api/v1/namespaces/$NAMESPACE/services/http:$service:http/proxy$path" >/dev/null 2>&1; then
      return 0
    fi
    [[ "$attempt" -lt 5 ]] || return 1
    sleep "$delay"
    delay=$((delay * 2))
  done
}

check_service_endpoints() {
  local path="$1" service
  for service in "${NCHAT_DEV_GO_SERVICES[@]}"; do
    if probe_service "$service" "$path"; then
      record ok "$service $path"
    else
      record fail "$service $path"
    fi
  done
}

# --- the public surface ----------------------------------------------------

# The status of one request to the public host. Fails when the request could
# not be made at all, so a caller distinguishes "answered wrongly" from "did
# not answer"; every caller below turns the second into an empty status it
# reports verbatim.
#
# --max-time rather than a retry loop: the rollout already completed, so a host
# that does not answer within ten seconds is a finding and not a race.
public_status() {
  local path="$1" extra=("${@:2}")
  curl --silent --output /dev/null --max-time 10 \
    --write-out '%{http_code}' "${extra[@]}" "https://$NCHAT_DEV_HOST$path"
}

check_web_served() {
  local status
  status="$(public_status /)" || status=""
  if [[ "$status" == "200" ]]; then
    record ok "https://$NCHAT_DEV_HOST/ serves the application (200)"
    return
  fi
  record fail "https://$NCHAT_DEV_HOST/ returned '${status:-no response}', expected 200"
}

# Authentication is proved by a refusal, not by a sign-in.
#
# A smoke that logged in would need a credential, and a credential in the
# deploy pipeline is a credential in the deploy pipeline. The refusal proves
# more of what actually breaks anyway: the route exists, the gateway reaches
# the service, and the auth middleware is wired in front of it. The failure
# this catches is a protected API answering 200 to nobody -- which is how an
# unwired middleware ships -- and a 404, which is how a broken route does.
check_protected_api_refuses_anonymous() {
  local path="$1" status
  status="$(public_status "$path")" || status=""
  if [[ "$status" == "401" ]]; then
    record ok "$path refuses an unauthenticated request (401)"
    return
  fi
  record fail "$path returned '${status:-no response}' to an unauthenticated request, expected 401"
}

# The WebSocket route, asked the one question a shell can answer about it
# honestly.
#
# A real session would need a token, a second account and a message to watch
# arrive; that is the authenticated smoke and it is not automated here. What is
# checked is that the upgrade endpoint is routed and guarded: the handshake
# headers go up, and the answer must be 401 from the auth chain rather than a
# 404 from a missing route or a 502 from a gateway that does not know how to
# carry an upgrade. That distinction is the realtime failure that has actually
# shipped.

# RFC 6455 wants base64 of exactly sixteen bytes, and that is all it wants:
# the server echoes a hash of it and never treats it as a credential. So it is
# built here from a sixteen-character phrase rather than written out as the
# base64 it becomes.
#
# Two reasons, and only one of them is the scanner. The encoded form is an
# opaque high-entropy string that secret scanners read as a leaked key, which
# is a false positive somebody has to re-triage on every run -- and the answer
# to that is to stop writing a secret-shaped literal, not to teach the scanner
# to ignore one. The other reason is that the literal it replaces decoded to
# seventeen bytes, so it never satisfied the RFC in the first place; a phrase
# whose length is visible makes that checkable by counting.
websocket_probe_key() {
  local phrase='nchat-dev-smoke!'
  [[ "${#phrase}" -eq 16 ]] ||
    { echo "the WebSocket probe key must be 16 bytes before encoding, got ${#phrase}" >&2; return 1; }
  printf '%s' "$phrase" | base64 | tr -d '\n'
}

check_websocket_route_guarded() {
  local status key
  key="$(websocket_probe_key)" || return 1
  status="$(public_status /api/chat/ws \
    --header 'Connection: Upgrade' \
    --header 'Upgrade: websocket' \
    --header 'Sec-WebSocket-Version: 13' \
    --header "Sec-WebSocket-Key: $key")" || status=""
  if [[ "$status" == "401" ]]; then
    record ok "/api/chat/ws is routed and rejects an unauthenticated upgrade (401)"
    return
  fi
  record fail "/api/chat/ws answered '${status:-no response}' to an unauthenticated upgrade, expected 401"
}

print_verdict() {
  echo
  if [[ "$FAILURES" -eq 0 ]]; then
    echo "nchat-dev smoke: PASS"
    return 0
  fi
  echo "nchat-dev smoke: FAIL ($FAILURES check(s))"
  return 1
}

main() {
  [[ -n "${NCHAT_DEV_HOST:-}" ]] ||
    { echo "NCHAT_DEV_HOST must name the public host of the environment" >&2; return 1; }
  [[ "$(kubectl config current-context)" == nchat-dev-deployer ]] ||
    { echo "the current kube context is not nchat-dev-deployer" >&2; return 1; }
  echo "=== nchat-dev smoke ==="
  echo "rollouts:"
  check_rollouts
  echo "container state:"
  check_no_restart_loops
  echo "migrations:"
  check_migrations_applied
  echo "liveness:"
  check_service_endpoints /healthz
  echo "readiness:"
  check_service_endpoints /readyz
  echo "public surface:"
  check_web_served
  check_protected_api_refuses_anonymous /api/chat/sidebar
  check_websocket_route_guarded
  print_verdict
}

main "$@"
