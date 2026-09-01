#!/usr/bin/env bash
# Automated smoke of one release slot, and an honest statement of what it did
# not cover (issue #626).
#
#   smoke.sh --target green
#
# It talks to the slot's own Services, never to the stable ones, so it can be
# run against a candidate that carries no production traffic — and running it
# can never promote anything.
#
# It does not print "smoke passed". A shell against an in-cluster Service cannot
# sign in through Keycloak, cannot watch a message arrive for a second user,
# cannot upload an attachment past authorization, cannot make a call and cannot
# reload a session. Those are the flows a release actually risks, and reporting
# a green result without them would turn the release gate into a rubber stamp —
# which is precisely how a broken upload path or a disabled realtime bus reaches
# production looking healthy. So the automated part reports as the automated
# part, and cutover eligibility stays BLOCKED until a person records the rest.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=scripts/deploy/nchat-prod/lib.sh
source "$SCRIPT_DIR/lib.sh"

AUTOMATED_FAILURES=0
# candidate (default) or baseline. Never inferred: a mode that turned itself on
# because the target happened to be live would be the isolation rule quietly
# disabling itself on exactly the release where it matters.
SMOKE_MODE=candidate

record() {
  local outcome="$1" detail="$2"
  if [[ "$outcome" == ok ]]; then
    printf '  [OK]   %s\n' "$detail"
    return 0
  fi
  printf '  [FAIL] %s\n' "$detail" >&2
  AUTOMATED_FAILURES=$((AUTOMATED_FAILURES + 1))
}

probe_service() {
  local service="$1" slot="$2" path="$3" attempt delay=1
  for attempt in 1 2 3 4 5; do
    if kubectl get --request-timeout=10s --raw \
      "/api/v1/namespaces/$NCHAT_PROD_NAMESPACE/services/http:$service-$slot:http/proxy$path" \
      >/dev/null 2>&1; then
      return 0
    fi
    [[ "$attempt" -lt 5 ]] || return 1
    sleep "$delay"
    delay=$((delay * 2))
  done
}

check_probes() {
  local slot="$1" path="$2" service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if probe_service "$service" "$slot" "$path"; then
      record ok "$service-$slot $path"
    else
      record fail "$service-$slot $path"
    fi
  done
}

check_readiness() {
  local slot="$1" service
  for service in "${NCHAT_PROD_STABLE_SERVICES[@]}"; do
    if deployment_ready "$service-$slot"; then
      record ok "deployment/$service-$slot all replicas Ready"
    else
      record fail "deployment/$service-$slot not all replicas Ready"
    fi
  done
}

# The candidate must not already be carrying production traffic: a smoke run
# against the live slot proves nothing about the release being validated.
check_isolation() {
  local slot="$1" mapping serving
  mapping="$(collect_service_slots)"
  serving="$(awk -v s="$slot" '$2 == s { print $1 }' <<<"$mapping")"
  if [[ -z "$serving" ]]; then
    record ok "slot $slot carries no production traffic"
    return
  fi
  record fail "slot $slot is already selected by: $(tr '\n' ' ' <<<"$serving")"
}

# The one situation in which smoking a slot that already holds traffic is not a
# mistake: the very first production release.
#
# bootstrap.sh establishes Blue and the stable Services select it immediately,
# because there is no previous slot to serve while Blue is validated. Requiring
# isolation there would make the documented first launch impossible to complete.
#
# This is deliberately not a general escape hatch. Every condition below must
# hold, and together they describe exactly one moment in the lifetime of the
# namespace: Blue is the baseline slot, it is what every stable Service selects,
# and Green has never been deployed. On any later release Green exists, so this
# mode simply stops being available and the candidate rules apply again.
check_baseline_preconditions() {
  local slot="$1" mapping active
  if [[ "$slot" != "$NCHAT_PROD_BASELINE_SLOT" ]]; then
    record fail "--baseline applies only to the baseline slot ($NCHAT_PROD_BASELINE_SLOT), not $slot"
    return
  fi
  mapping="$(collect_service_slots)"
  if ! active="$(resolve_active_slot "$mapping" 2>/dev/null)"; then
    record fail "the stable Services do not agree on a slot; this is not a baseline"
    return
  fi
  if [[ "$active" != "$slot" ]]; then
    record fail "slot $slot is not what the stable Services select ($active); this is not a baseline"
    return
  fi
  check_no_rival_slot "$slot"
}

# Green having workloads means the namespace is past its first release, and a
# baseline smoke would then be validating a live slot while another one waits —
# which is the candidate flow, with its own isolation rule.
check_no_rival_slot() {
  local slot="$1" other state
  other="$(opposite_slot "$slot")"
  state="$(slot_release_state "$other" 2>/dev/null)" || state=UNKNOWN
  if [[ "$state" != NOT_DEPLOYED ]]; then
    record fail "slot $other is deployed ($state); this namespace is past its baseline, use a candidate smoke"
    return
  fi
  record ok "baseline: $slot is the only deployed slot and is what the stable Services select"
}

check_traffic_boundary() {
  local slot="$1"
  if [[ "$SMOKE_MODE" == baseline ]]; then
    check_baseline_preconditions "$slot"
    return
  fi
  check_isolation "$slot"
}

# Configuration that decides whether whole features work at all. These are the
# keys whose absence produced a service that answered 200 on every probe while
# uploads were off and realtime was pod-local.
check_release_configuration() {
  local key value
  for key in VALKEY_WS_BROADCAST_ENABLED FILE_UPLOADS_ENABLED LIVEKIT_ENABLED; do
    value="$(kubectl get configmap nchat-config -n "$NCHAT_PROD_NAMESPACE" \
      -o "jsonpath={.data.$key}" 2>/dev/null)"
    if [[ "$value" == "true" ]]; then
      record ok "nchat-config $key=true"
    else
      record fail "nchat-config $key is '${value:-unset}', expected 'true'"
    fi
  done
  for key in FILE_MALWARE_SCANNER_ADDRESS NCHAT_WEB_LIVEKIT_CONNECT_SRC; do
    value="$(kubectl get configmap nchat-config -n "$NCHAT_PROD_NAMESPACE" \
      -o "jsonpath={.data.$key}" 2>/dev/null)"
    if [[ -n "$value" ]]; then
      record ok "nchat-config $key is set"
    else
      record fail "nchat-config $key is empty"
    fi
  done
}

# Establishes what is actually being validated. Everything below smokes a
# release, not a slot, so a candidate that does not carry exactly one release
# has nothing coherent to validate and the run stops meaning anything.
SMOKE_RELEASE=""

check_release_identity() {
  local slot="$1" state
  state="$(slot_release_state "$slot")" || {
    record fail "cannot read the release identity of slot $slot"
    return
  }
  case "$state" in
    CONSISTENT\ *)
      SMOKE_RELEASE="${state#CONSISTENT }"
      record ok "slot $slot carries one release across every workload ($SMOKE_RELEASE)"
      ;;
    NOT_DEPLOYED)
      record fail "slot $slot is not deployed; there is nothing to smoke"
      ;;
    ROLLING_OUT)
      # No evidence may be produced here. The pods answering probes are the ones
      # the release is replacing, so a PASS would describe the previous release.
      record fail "slot $slot has not finished rolling out; its Ready pods are not on the release it declares"
      slot_workload_releases "$slot" | awk '{ printf "         %-22s observed=%s  %s\n", $1, $2, $3 }' >&2
      ;;
    *)
      record fail "slot $slot carries more than one release; deploy it again before smoking it"
      slot_workload_releases "$slot" | awk '{ printf "         %-22s %s  %s\n", $1, $2, $3 }' >&2
      ;;
  esac
}

print_manual_checklist() {
  local slot="$1"
  cat <<EOF

--- AUTHENTICATED RELEASE SMOKE (manual, required): slot $slot ---
Not automated, and not claimed to be. Run from two accounts in two browser
profiles against the slot's preview host. Record each result in the release
ticket; an unrecorded item counts as failed.

  [ ] Keycloak sign-in completes and lands back on the application
  [ ] session survives a full page reload
  [ ] sidebar lists workspaces and conversations
  [ ] a DM opens and shows history
  [ ] a message sent by account A arrives for account B without a reload
  [ ] the browser reports a WebSocket connection, not polling
  [ ] account B's message arrives for account A (both directions, two replicas)
  [ ] a reaction applies and is seen by the second account
  [ ] a public channel opens; a private channel opens for a member
  [ ] a private channel is refused for a non-member
  [ ] a group conversation opens and receives messages
  [ ] presence shows the second account online, then offline
  [ ] an attachment uploads, downloads and previews
  [ ] an EICAR test file is rejected or quarantined by ClamAV
  [ ] search returns a known message (skip only if search is disabled here)
  [ ] a voice/video call connects between the two accounts
  [ ] screen share starts and is visible to the other account
  [ ] logout ends the session and a protected route is refused afterwards

Administrative console, on admin-$slot.preview.<host>:
  [ ] the console loads from the candidate and its assets resolve
  [ ] administrative sign-in completes
  [ ] the overview/bootstrap view renders data from the candidate Admin API
  [ ] one read-only listing (users or audit events) loads
  Do not perform administrative mutations as a smoke: no user changes, no
  deletions, no configuration edits.
--- end authenticated release smoke ---
EOF
}

print_verdict() {
  local slot="$1"
  echo
  if [[ "$AUTOMATED_FAILURES" -eq 0 ]]; then
    echo "Automated smoke            : PASS"
  else
    echo "Automated smoke            : FAIL ($AUTOMATED_FAILURES check(s))"
  fi
  echo "Release validated          : ${SMOKE_RELEASE:-NONE (candidate does not carry one release)}"
  echo "Authenticated release smoke: REQUIRED / NOT CONFIRMED BY THIS COMMAND"
  echo "Cutover eligibility        : BLOCKED until the checklist above is recorded"
  [[ "$AUTOMATED_FAILURES" -eq 0 ]] || return 1
  echo
  # The evidence names the release, not just the slot, and the release is the
  # commit AND the sealed build: rebuild or redeploy the candidate and this token
  # stops matching, so the next cutover asks for a fresh smoke.
  echo "When the checklist has been completed and recorded, promote with:"
  echo "  NCHAT_PROD_SMOKE_CONFIRMED=$slot:$SMOKE_RELEASE \\"
  echo "    NCHAT_PROD_RELEASE_MANIFEST_DIR=<dir holding the sealed release-manifest.json> \\"
  echo "    scripts/deploy/nchat-prod/cutover.sh --target $slot"
}

parse_mode() {
  case "${1:-}" in
    "") return 0 ;;
    --baseline) SMOKE_MODE=baseline ;;
    *) prod_fail "unknown argument: $1 (expected --target <slot> [--baseline])" ;;
  esac
}

main() {
  local slot
  slot="$(require_target_slot "$@")"
  shift 2
  parse_mode "$@"
  require_context
  require_namespace
  echo "=== automated smoke: slot $slot (mode: $SMOKE_MODE) ==="
  echo "traffic boundary:"
  check_traffic_boundary "$slot"
  echo "release identity:"
  check_release_identity "$slot"
  echo "workload readiness:"
  check_readiness "$slot"
  echo "liveness:"
  check_probes "$slot" /healthz
  echo "readiness endpoints:"
  check_probes "$slot" /readyz
  echo "release configuration:"
  check_release_configuration
  print_manual_checklist "$slot"
  print_verdict "$slot"
}

main "$@"
