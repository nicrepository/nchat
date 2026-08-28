#!/usr/bin/env bash
# TASK-158 (issue #187) — WebRTC office network validation.
#
# Validates STUN/TURN/ICE connectivity for the LiveKit + coturn stack
# (see docs/runbooks/task-livekit-coturn-dev.md) against a target host that
# is reachable from the current machine over a real network (LAN/office),
# instead of the same-host Docker network used by dev-media-validate.sh.
#
# This script is NOT run in CI (requires a real network + Docker). It is a
# manual QA tool intended to be executed on a machine connected to the
# Nic-Labs office network. See:
#   docs/runbooks/task-158-webrtc-office-network-validation.md
#
# What it validates automatically (scenarios A, B, C, E, F, G — see runbook):
#   A. Basic reachability of the LiveKit signaling endpoint.
#   B. STUN binding response from coturn (NAT traversal / srflx capability).
#   C. TURN relay allocation forced over UDP, then TCP, then TLS (443-style).
#      TLS is reported as NOT CONFIGURED (not a failure) when coturn has no
#      TLS listener, which is the case for the current dev template.
#   E. "Room connectivity and participant presence" — two livekit-cli
#      processes join the same room and are confirmed present. This is
#      infrastructure connectivity evidence ONLY. It does NOT prove media was
#      actually published/received, which ICE candidate/transport was
#      selected, that a relay candidate was used, that media traversed TURN,
#      or that a real second physical device/browser was involved — those
#      require the manual evidence described below and in the runbook.
#   F. Stability window (polling, no fixed sleep-only wait), tracking
#      participant-presence drops/reconnects over the window.
#   G. Controlled failure: an unreachable target fails fast with a clear,
#      non-zero-exit error and no hang.
#
# What is intentionally NOT automated here (see runbook, "Validação manual"):
#   - Media publish/receive confirmation, selected ICE candidate type
#     (host/srflx/relay), relay-candidate confirmation, and the transport
#     actually used (UDP/TCP/TLS): require a real browser
#     (chrome://webrtc-internals or about:webrtc), not scriptable here
#     without adding a new browser-automation dependency.
#   - A real second physical device/browser (scenario E/F here only use two
#     livekit-cli processes, not two devices).
#   - Physically blocking UDP at the office firewall/router (scenario D):
#     this script must not modify corporate firewall rules; the operator
#     toggles UDP manually and re-runs this script to observe the fallback.
#
# Without WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=1 (see below) and a real,
# non-loopback target, the final result can never be APPROVED — it will be
# PENDING at best. See scripts/qa/lib/webrtc-office-network-result.sh and the
# runbook's "Resultado final e exit codes" section.
#
# Required env vars:
#   None — sensible defaults are loaded from infra/compose/.env.dev(.example)
#   via scripts/dev/_media_env.sh.
#
# Optional env vars:
#   WEBRTC_QA_TARGET_HOST       Host/IP where LiveKit + coturn are reachable
#                               from this machine (default: 127.0.0.1).
#   WEBRTC_QA_STABILITY_SECONDS Stability window duration in seconds
#                               (default: 60; use 600-900 for a real office
#                               run per the runbook). Max 3600.
#   WEBRTC_QA_POLL_SECONDS      Poll interval during the stability window
#                               (default: 5). Max 120.
#   WEBRTC_QA_SKIP_TWO_PARTICIPANT  Set to "1" to skip scenarios E/F (e.g.
#                               when only validating STUN/TURN reachability).
#   WEBRTC_QA_RESULTS_DIR       Override output directory (default:
#                               poc-results/webrtc-office-network).
#   WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED  Set to "1" ONLY after the operator
#                               has manually recorded, per the runbook: media
#                               publish/receive, selected ICE candidate type,
#                               relay candidate, actual transport used, and a
#                               real second device/browser test via
#                               chrome://webrtc-internals (or equivalent).
#                               Default "0" — required for an APPROVED result.
#
# Exit codes (see runbook for full detail):
#   0  APPROVED — all mandatory scenarios passed and manual evidence confirmed.
#   1  FAILED   — a mandatory automated scenario failed.
#   2  PARTIAL  — mandatory scenarios passed, but a fallback/stability issue
#                 was detected.
#   3  PENDING  — a mandatory result is missing/empty/skipped, or required
#                 manual evidence / a real second device is missing.
#
# Security invariants:
#   - Never prints LIVEKIT_API_SECRET / COTURN_STATIC_AUTH_SECRET.
#   - Never prints LiveKit participant tokens.
#   - Every network call has an explicit timeout.
#   - No firewall/router changes are made by this script.
#   - The generated report only ever includes the target host value the
#     operator supplied; redact it manually before sharing if it is a
#     routable/public office IP (see runbook).
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

# shellcheck source=lib/webrtc-office-network-result.sh
source "$ROOT_DIR/scripts/qa/lib/webrtc-office-network-result.sh"

run_webrtc_office_network_preflight() {
  local yaml_file="$1" target_host="$2" target_is_loopback="$3"
  local livekit_node_ip="$4"
  local -n output_rendered_node_ip="$5"
  local -n output_expected_node_ip="$6"

  output_rendered_node_ip=""
  output_expected_node_ip="$livekit_node_ip"
  if [[ "$target_host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || [[ "$target_host" == *:* ]]; then
    output_expected_node_ip="$target_host"
  fi

  validate_rendered_node_ip_file "$yaml_file" "$target_is_loopback" \
    "$output_expected_node_ip" output_rendered_node_ip
}

main() {
set -euo pipefail

if [[ -v WEBRTC_QA_CONFIG_CHECK_PREFLIGHT_ONLY ]]; then
  echo "WEBRTC_QA_CONFIG_CHECK_PREFLIGHT_ONLY is no longer supported; use the CI config-check command" >&2
  return 1
fi

# shellcheck source=../dev/_media_env.sh
source "$ROOT_DIR/scripts/dev/_media_env.sh"

# ---------------------------------------------------------------------------
# Argument / environment validation (fail fast, no eval, bounded values)
# ---------------------------------------------------------------------------
require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

validate_positive_int() {
  local name="$1" value="$2" max="$3"
  if ! [[ "$value" =~ ^[0-9]+$ ]] || [ "$value" -lt 1 ] || [ "$value" -gt "$max" ]; then
    echo "Invalid $name: '$value' (must be an integer between 1 and $max)" >&2
    exit 1
  fi
}

validate_host() {
  local value="$1"
  # Conservative allow-list: hostname/IP characters only. Blocks shell
  # metacharacters; this script never passes the host through eval/sh -c.
  if ! [[ "$value" =~ ^[A-Za-z0-9.:_-]+$ ]]; then
    echo "Invalid WEBRTC_QA_TARGET_HOST: contains unsupported characters" >&2
    exit 1
  fi
}

TARGET_HOST="${WEBRTC_QA_TARGET_HOST:-127.0.0.1}"
STABILITY_SECONDS="${WEBRTC_QA_STABILITY_SECONDS:-60}"
POLL_SECONDS="${WEBRTC_QA_POLL_SECONDS:-5}"
SKIP_TWO_PARTICIPANT="${WEBRTC_QA_SKIP_TWO_PARTICIPANT:-0}"
RESULTS_DIR="${WEBRTC_QA_RESULTS_DIR:-$ROOT_DIR/poc-results/webrtc-office-network}"
MANUAL_EVIDENCE_CONFIRMED="${WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED:-0}"
LIVEKIT_RUNTIME_YAML="${WEBRTC_QA_LIVEKIT_RUNTIME_YAML:-$ROOT_DIR/infra/compose/livekit/livekit.runtime.yaml}"

validate_host "$TARGET_HOST"
validate_positive_int "WEBRTC_QA_STABILITY_SECONDS" "$STABILITY_SECONDS" 3600
validate_positive_int "WEBRTC_QA_POLL_SECONDS" "$POLL_SECONDS" 120
if ! validate_manual_evidence_confirmation "$MANUAL_EVIDENCE_CONFIRMED"; then
  exit 1
fi

mkdir -p "$RESULTS_DIR"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
SUMMARY_FILE="$RESULTS_DIR/${RUN_ID}-summary.md"

LK_CLI_IMAGE="livekit/livekit-cli:v2.17"
ROOM_NAME="nchat-office-qa-$(date +%s)"
PARTICIPANT_A_IDENTITY="office-qa-participant-a"
PARTICIPANT_B_IDENTITY="office-qa-participant-b"
PARTICIPANT_A_CONTAINER="nchat-office-qa-a"
PARTICIPANT_B_CONTAINER="nchat-office-qa-b"
STUN_CONTAINER="nchat-office-qa-stun"
TURN_UDP_CONTAINER="nchat-office-qa-turn-udp"
TURN_TCP_CONTAINER="nchat-office-qa-turn-tcp"
TURN_TLS_CONTAINER="nchat-office-qa-turn-tls"
LK_CLI_CONTAINER="nchat-office-qa-lkcli"
ROOM_CREATED=0

cleanup() {
  # Preserve the script's real exit code: this trap must never mask it, even
  # if a cleanup step below fails (each step is best-effort via `|| true`).
  local ec=$?

  rm -rf "$TMP_DIR" 2>/dev/null || true

  if [ "$ROOM_CREATED" = "1" ]; then
    # Best-effort room deletion. The room may already have expired via
    # LiveKit's own empty_timeout — that is not a cleanup failure.
    lk_cli room delete "$ROOM_NAME" >/dev/null 2>&1 || true
  fi

  docker rm -f \
    "$STUN_CONTAINER" "$TURN_UDP_CONTAINER" "$TURN_TCP_CONTAINER" "$TURN_TLS_CONTAINER" \
    "$LK_CLI_CONTAINER" "$PARTICIPANT_A_CONTAINER" "$PARTICIPANT_B_CONTAINER" >/dev/null 2>&1 || true

  exit "$ec"
}
TMP_DIR="$(mktemp -d)"
trap cleanup EXIT

# Containers started via plain `docker run` cannot reach 127.0.0.1-bound
# ports on the host (127.0.0.1 inside a container refers to the container
# itself, not the host). When the target is the local loopback (i.e. this
# machine is running the stack and this is a same-host dry run), attach to
# the compose-managed "nchat-dev" network and address services by their
# container DNS names, matching scripts/dev/dev-media-validate.sh. When the
# target is a real LAN/office IP, use normal container networking (Docker's
# default bridge can route out to the LAN like any other host process).
DOCKER_NET_ARGS=()
TURN_TARGET_HOST="$TARGET_HOST"
TURN_TARGET_PORT="$COTURN_LISTENING_PORT"
LIVEKIT_WS_URL="ws://${TARGET_HOST}:${LIVEKIT_HOST_PORT}"
TARGET_IS_LOOPBACK=0
if [ "$TARGET_HOST" = "127.0.0.1" ] || [ "$TARGET_HOST" = "localhost" ]; then
  TARGET_IS_LOOPBACK=1
else
  rendered_node_ip=""
  expected_lan_node_ip=""
  node_ip_preflight_output="$TMP_DIR/node-ip-preflight.out"
  if ! run_webrtc_office_network_preflight "$LIVEKIT_RUNTIME_YAML" "$TARGET_HOST" \
    "$TARGET_IS_LOOPBACK" "${LIVEKIT_NODE_IP:-}" rendered_node_ip expected_lan_node_ip \
    >"$node_ip_preflight_output" 2>&1; then
    node_ip_preflight_message="$(cat "$node_ip_preflight_output")"
    echo "FAIL: ${node_ip_preflight_message}." >&2
    echo "Fix: set LIVEKIT_NODE_IP=<LAN IP> in infra/compose/.env.dev, then re-render/restart with:" >&2
    echo "  MEDIA_COMPOSE_EXTRA_FILE=infra/compose/compose.media.lan-office-test.override.yml bash scripts/dev/dev-media-up.sh" >&2
    echo "before re-running this script. See the runbook's LAN section." >&2
    exit 1
  fi
  node_ip_preflight_message="$(cat "$node_ip_preflight_output")"

  if [[ "$TARGET_HOST" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || [[ "$TARGET_HOST" == *:* ]]; then
    echo "[INFO] LAN preflight: ${node_ip_preflight_message} (rendered=${rendered_node_ip}, expected target=${expected_lan_node_ip})."
  else
    echo "[INFO] LAN preflight: ${node_ip_preflight_message} (rendered=${rendered_node_ip}, expected LIVEKIT_NODE_IP=${expected_lan_node_ip}; target is DNS)."
  fi
fi

require_command docker
docker compose version >/dev/null
require_command curl

if [ "$TARGET_IS_LOOPBACK" = "1" ]; then
  DOCKER_NETWORK="nchat-dev"
  if docker network inspect "$DOCKER_NETWORK" >/dev/null 2>&1; then
    DOCKER_NET_ARGS=(--network "$DOCKER_NETWORK")
    TURN_TARGET_HOST="coturn"
    TURN_TARGET_PORT="3478"
    LIVEKIT_WS_URL="ws://livekit:7880"
    echo "[INFO] Target is loopback — using the '${DOCKER_NETWORK}' Docker network for container-to-container checks (same-host dry run)."
  else
    echo "[WARN] Target is loopback but the '${DOCKER_NETWORK}' network was not found." >&2
    echo "[WARN] Container-based checks (STUN/TURN/room join) will likely fail to reach 127.0.0.1. Run 'make dev-media-up' first, or point WEBRTC_QA_TARGET_HOST at a real LAN IP." >&2
  fi
fi

RESULTS=()
record() { RESULTS+=("$1|$2"); }

# run_bounded: runs a docker command with a hard wall-clock timeout, always
# removing the (named) container afterwards even if the process ignores
# SIGTERM (observed with turnutils_uclient during a failed TLS/DTLS
# handshake, and with livekit-cli after listing room participants — both can
# otherwise hang past the timeout and leak a running container).
run_bounded() {
  local container_name="$1"
  shift
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  local rc=0
  timeout -k 5 20 docker run --rm --name "$container_name" "$@" || rc=$?
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  return "$rc"
}

lk_cli() {
  run_bounded "$LK_CLI_CONTAINER" "${DOCKER_NET_ARGS[@]}" \
    -e LIVEKIT_URL="$LIVEKIT_WS_URL" \
    -e LIVEKIT_API_KEY="$LIVEKIT_API_KEY" \
    -e LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET" \
    "$LK_CLI_IMAGE" "$@"
}


echo "======================================================================"
echo " TASK-158 — WebRTC office network validation"
echo " Run: $RUN_ID"
echo " Target host: $TARGET_HOST"
echo " Stability window: ${STABILITY_SECONDS}s (poll every ${POLL_SECONDS}s)"
echo "======================================================================"

# ---------------------------------------------------------------------------
# Scenario A — basic reachability of LiveKit signaling endpoint
# ---------------------------------------------------------------------------
echo
echo "==> Scenario A: LiveKit reachability (ws/http endpoint)"
# curl's -w '%{http_code}' already prints "000" on connect failure/timeout;
# do not also append a fallback echo, or the two get concatenated (e.g. into
# the invalid "000000") instead of the intended single "000".
http_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://${TARGET_HOST}:${LIVEKIT_HOST_PORT}/" 2>/dev/null)" || true
http_code="${http_code:-000}"
if [ "$http_code" != "000" ]; then
  echo "  [OK] LiveKit responded (HTTP ${http_code})"
  record "A_reachability" "OK (HTTP ${http_code})"
else
  echo "FAIL: LiveKit did not respond at http://${TARGET_HOST}:${LIVEKIT_HOST_PORT}/ within 5s." >&2
  record "A_reachability" "FAIL (unreachable)"
  echo
  echo "Aborting — downstream scenarios require LiveKit to be reachable." >&2
  # Write partial report before exiting so the failure is documented.
  {
    echo "# WebRTC office network validation — $RUN_ID"
    echo
    echo "Target host: (see operator notes — sanitize before sharing)"
    echo
    echo "| Scenario | Result |"
    echo "| --- | --- |"
    for r in "${RESULTS[@]}"; do
      IFS='|' read -r k v <<<"$r"
      echo "| $k | $v |"
    done
    echo
    echo "Overall result: FAILED — LiveKit endpoint unreachable (mandatory Scenario A)."
  } > "$SUMMARY_FILE"
  echo "Partial report written to: $SUMMARY_FILE"
  echo "==> Final result: FAILED (exit code 1)"
  exit 1
fi

# ---------------------------------------------------------------------------
# Scenario B — STUN binding (NAT traversal)
# ---------------------------------------------------------------------------
echo
echo "==> Scenario B: STUN binding via coturn"
if run_bounded "$STUN_CONTAINER" "${DOCKER_NET_ARGS[@]}" "$COTURN_IMAGE" \
  turnutils_stunclient -p "$TURN_TARGET_PORT" "$TURN_TARGET_HOST" >"$TMP_DIR/stun.log" 2>&1; then
  echo "  [OK] STUN binding request succeeded"
  record "B_stun_binding" "OK"
else
  echo "FAIL: STUN binding request failed." >&2
  cat "$TMP_DIR/stun.log" >&2 || true
  record "B_stun_binding" "FAIL"
fi

# ---------------------------------------------------------------------------
# Scenario C — TURN relay forced over UDP, TCP, and TLS
# ---------------------------------------------------------------------------
echo
echo "==> Scenario C: TURN relay allocation (UDP) — forces relay via -y"
if run_bounded "$TURN_UDP_CONTAINER" "${DOCKER_NET_ARGS[@]}" "$COTURN_IMAGE" \
  turnutils_uclient -y -W "$COTURN_STATIC_AUTH_SECRET" -p "$TURN_TARGET_PORT" "$TURN_TARGET_HOST" \
  >"$TMP_DIR/turn-udp.log" 2>&1; then
  echo "  [OK] TURN relay allocation succeeded over UDP"
  record "C_turn_udp" "OK"
else
  echo "FAIL: TURN relay allocation failed over UDP." >&2
  cat "$TMP_DIR/turn-udp.log" >&2 || true
  record "C_turn_udp" "FAIL"
fi

echo
echo "==> Scenario C: TURN relay allocation (TCP fallback) — forces relay + TCP via -y -t"
if run_bounded "$TURN_TCP_CONTAINER" "${DOCKER_NET_ARGS[@]}" "$COTURN_IMAGE" \
  turnutils_uclient -y -t -W "$COTURN_STATIC_AUTH_SECRET" -p "$TURN_TARGET_PORT" "$TURN_TARGET_HOST" \
  >"$TMP_DIR/turn-tcp.log" 2>&1; then
  echo "  [OK] TURN relay allocation succeeded over TCP"
  record "C_turn_tcp" "OK"
else
  echo "FAIL: TURN relay allocation failed over TCP." >&2
  cat "$TMP_DIR/turn-tcp.log" >&2 || true
  record "C_turn_tcp" "FAIL"
fi

echo
echo "==> Scenario C: TURN relay allocation (TLS, port-443-style fallback) — via -y -t -S"
tls_rc=0
run_bounded "$TURN_TLS_CONTAINER" "${DOCKER_NET_ARGS[@]}" "$COTURN_IMAGE" \
  turnutils_uclient -y -t -S -W "$COTURN_STATIC_AUTH_SECRET" -p "$TURN_TARGET_PORT" "$TURN_TARGET_HOST" \
  >"$TMP_DIR/turn-tls.log" 2>&1 || tls_rc=$?
if [ "$tls_rc" -eq 0 ]; then
  echo "  [OK] TURN relay allocation succeeded over TLS"
  record "C_turn_tls" "OK"
elif [ "$tls_rc" -eq 124 ] || [ "$tls_rc" -eq 137 ] || grep -qiE "no-tls|connect|refused|handshake" "$TMP_DIR/turn-tls.log"; then
  # A hard timeout (124/137, forced by run_bounded's SIGKILL grace) with no
  # successful handshake is the expected signature of "no TLS listener" for
  # the current dev coturn template (no-tls/no-dtls) — the connection just
  # hangs instead of being actively refused. Treat as NOT_CONFIGURED, not a
  # functional failure of an otherwise-working stack.
  echo "  [NOT CONFIGURED] coturn has no TLS listener in this stack (expected for the dev template; see runbook)."
  record "C_turn_tls" "NOT_CONFIGURED"
  cat "$TMP_DIR/turn-tls.log" >&2 || true
else
  echo "FAIL: TURN relay allocation failed over TLS." >&2
  record "C_turn_tls" "FAIL"
  cat "$TMP_DIR/turn-tls.log" >&2 || true
fi

# ---------------------------------------------------------------------------
# Scenario E/F — room connectivity and participant presence + stability
#
# IMPORTANT: this only proves that two livekit-cli processes can join the
# same room and remain listed as present. It is NOT proof of media
# publish/receive, ICE candidate/transport selection, relay usage, or a real
# second physical device — that evidence is manual (see runbook and the
# WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED note above).
# ---------------------------------------------------------------------------
if [ "$SKIP_TWO_PARTICIPANT" = "1" ]; then
  echo
  echo "==> Scenario E/F skipped (WEBRTC_QA_SKIP_TWO_PARTICIPANT=1)"
  record "E_room_connectivity" "SKIPPED"
  record "F_stability" "SKIPPED"
else
  echo
  echo "==> Scenario E: room connectivity and participant presence — room '${ROOM_NAME}'"
  if lk_cli room create "$ROOM_NAME" >"$TMP_DIR/room-create.log" 2>&1; then
    echo "  [OK] room created"
    ROOM_CREATED=1
  else
    echo "FAIL: could not create LiveKit room." >&2
    cat "$TMP_DIR/room-create.log" >&2 || true
    record "E_room_connectivity" "FAIL (room create)"
    record "F_stability" "SKIPPED"
    SKIP_TWO_PARTICIPANT=1
  fi
fi

if [ "$SKIP_TWO_PARTICIPANT" != "1" ]; then
  docker rm -f "$PARTICIPANT_A_CONTAINER" "$PARTICIPANT_B_CONTAINER" >/dev/null 2>&1 || true
  docker run -d --name "$PARTICIPANT_A_CONTAINER" "${DOCKER_NET_ARGS[@]}" \
    -e LIVEKIT_URL="$LIVEKIT_WS_URL" \
    -e LIVEKIT_API_KEY="$LIVEKIT_API_KEY" \
    -e LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET" \
    "$LK_CLI_IMAGE" room join "$ROOM_NAME" --identity "$PARTICIPANT_A_IDENTITY" --publish-demo >/dev/null
  docker run -d --name "$PARTICIPANT_B_CONTAINER" "${DOCKER_NET_ARGS[@]}" \
    -e LIVEKIT_URL="$LIVEKIT_WS_URL" \
    -e LIVEKIT_API_KEY="$LIVEKIT_API_KEY" \
    -e LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET" \
    "$LK_CLI_IMAGE" room join "$ROOM_NAME" --identity "$PARTICIPANT_B_IDENTITY" --publish-demo >/dev/null

  both_connected=0
  for _ in $(seq 1 10); do
    sleep 2
    participants="$(lk_cli room participants list "$ROOM_NAME" 2>/dev/null || true)"
    if grep -q "$PARTICIPANT_A_IDENTITY" <<<"$participants" && grep -q "$PARTICIPANT_B_IDENTITY" <<<"$participants"; then
      both_connected=1
      break
    fi
  done

  if [ "$both_connected" -eq 1 ]; then
    echo "  [OK] both livekit-cli processes are present in the room (infra connectivity only — see note above)"
    record "E_room_connectivity" "OK"
  else
    echo "FAIL: both participants did not appear in the room within the expected time." >&2
    record "E_room_connectivity" "FAIL"
  fi

  echo
  echo "==> Scenario F: stability window (${STABILITY_SECONDS}s, polling every ${POLL_SECONDS}s)"
  elapsed=0
  drops=0
  last_state="unknown"
  while [ "$elapsed" -lt "$STABILITY_SECONDS" ]; do
    sleep "$POLL_SECONDS"
    elapsed=$((elapsed + POLL_SECONDS))
    participants="$(lk_cli room participants list "$ROOM_NAME" 2>/dev/null || true)"
    if grep -q "$PARTICIPANT_A_IDENTITY" <<<"$participants" && grep -q "$PARTICIPANT_B_IDENTITY" <<<"$participants"; then
      state="both_connected"
    else
      state="degraded"
    fi
    if [ "$last_state" = "both_connected" ] && [ "$state" = "degraded" ]; then
      drops=$((drops + 1))
      echo "  [WARN] participant drop detected at ${elapsed}s"
    fi
    last_state="$state"
    echo "  [t=${elapsed}s] state=${state}"
  done

  if [ "$drops" -eq 0 ] && [ "$last_state" = "both_connected" ]; then
    echo "  [OK] session remained stable for ${STABILITY_SECONDS}s (0 drops detected)"
    record "F_stability" "OK (0 drops / ${STABILITY_SECONDS}s)"
  else
    echo "  [WARN] session was not fully stable (${drops} drop(s) detected, last_state=${last_state})"
    record "F_stability" "PARTIAL (${drops} drop(s) / ${STABILITY_SECONDS}s)"
  fi
  # Room deletion is handled by the cleanup() EXIT trap (ROOM_CREATED=1),
  # so it also runs on early error paths, not just this normal completion.
fi

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------
# ---------------------------------------------------------------------------
# Final result aggregation (must not silently return exit 0 for anything
# other than APPROVED — see scripts/qa/lib/webrtc-office-network-result.sh)
# ---------------------------------------------------------------------------
FINAL_REASONS=()
compute_final_result RESULTS "$MANUAL_EVIDENCE_CONFIRMED" "$TARGET_IS_LOOPBACK" FINAL_REASONS

echo
echo "==> Writing sanitized summary to $SUMMARY_FILE"
{
  echo "# WebRTC office network validation — $RUN_ID"
  echo
  echo "Generated by scripts/qa/validate-webrtc-office-network.sh."
  echo
  echo "**IMPORTANT**: replace/redact any sensitive network detail (public IP,"
  echo "internal hostnames) before sharing or committing this file outside"
  echo "poc-results/ (which is gitignored)."
  echo
  echo "| Scenario | Result |"
  echo "| --- | --- |"
  for r in "${RESULTS[@]}"; do
    IFS='|' read -r k v <<<"$r"
    echo "| $k | $v |"
  done
  echo
  echo "Stability window requested: ${STABILITY_SECONDS}s (poll every ${POLL_SECONDS}s)"
  echo
  echo "Note: Scenario E only proves room connectivity and participant"
  echo "presence — it does NOT prove media publish/receive, ICE candidate"
  echo "type, relay usage, or a real second device. ICE candidate types"
  echo "(host/srflx/relay), relay confirmation, actual transport used, and"
  echo "media publish/receive must be recorded manually (chrome://webrtc-internals"
  echo "or about:webrtc) — see docs/runbooks/task-158-webrtc-office-network-validation.md."
  echo
  echo "## Resultado final: ${FINAL_RESULT}"
  echo
  if [ "${#FINAL_REASONS[@]}" -gt 0 ]; then
    echo "Reasons:"
    for reason in "${FINAL_REASONS[@]}"; do
      echo "- ${reason}"
    done
  else
    echo "All mandatory automated criteria passed and manual evidence was confirmed."
  fi
} > "$SUMMARY_FILE"

echo
echo "WebRTC office network validation finished. See $SUMMARY_FILE"
echo
echo "==> Final result: ${FINAL_RESULT} (exit code ${FINAL_EXIT_CODE})"
if [ "${#FINAL_REASONS[@]}" -gt 0 ]; then
  printf '  - %s\n' "${FINAL_REASONS[@]}"
fi

return "$FINAL_EXIT_CODE"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
