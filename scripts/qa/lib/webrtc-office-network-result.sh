#!/usr/bin/env bash
# scripts/qa/lib/webrtc-office-network-result.sh
# TASK-158 (issue #187) — network-independent result aggregation and rendered
# LiveKit node_ip preflight helpers.
#
# Source this file (do not execute directly). It defines validation helpers
# with no network side effects, so they can be exercised deterministically in
# scripts/ci/webrtc-office-network-config-check.sh without Docker or a real
# network — see the "Result aggregation self-test" section of that script.
#
# Final result states and exit codes (documented in the runbook):
#   APPROVED  exit 0  — all mandatory automated scenarios passed, no partial
#                       fallback/stability issues, and manual evidence
#                       (media pub/rx, ICE candidate types, relay usage,
#                       transport used, real second device) was confirmed.
#   FAILED    exit 1  — a mandatory automated scenario failed (LiveKit
#                       reachability, STUN, TURN/UDP relay, or room
#                       connectivity).
#   PARTIAL   exit 2  — mandatory scenarios passed, but a fallback
#                       (TURN/TCP, TURN/TLS) or the stability window reported
#                       a real problem.
#   PENDING   exit 3  — a mandatory result is missing/empty/skipped, required
#                       manual evidence is missing, and/or the run target was
#                       loopback (no real second device involved).
#
# Precedence when more than one condition applies: FAILED > PENDING > PARTIAL
# > APPROVED. Manual evidence / loopback gaps are treated as PENDING rather
# than PARTIAL because, without them, we cannot actually distinguish PARTIAL
# from APPROVED (e.g. we cannot confirm media/relay were real) — see
# docs/runbooks/task-158-webrtc-office-network-validation.md.

# These are the automated scenarios that must be present before aggregation
# can yield APPROVED. TCP/TLS fallback and stability remain additional
# results: they can lower a complete run to PARTIAL or PENDING, but their
# failure is not classified as a mandatory-scenario failure by the runbook.
WEBRTC_OFFICE_MANDATORY_SCENARIOS=(
  "A_reachability"
  "B_stun_binding"
  "C_turn_udp"
  "E_room_connectivity"
)

# validate_manual_evidence_confirmation: accepts only the documented 0/1
# values. The main script defaults an unset/empty environment value to 0
# before calling this helper.
validate_manual_evidence_confirmation() {
  local value="$1"
  if [ "$value" = "0" ] || [ "$value" = "1" ]; then
    return 0
  fi
  echo "Invalid WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED: '$value' (must be 0 or 1)" >&2
  return 1
}

# read_rendered_node_ip: extracts exactly one node_ip entry from the rendered
# LiveKit YAML and stores it in the named output variable.
read_rendered_node_ip() {
  local yaml_file="$1"
  local -n output_value="$2"
  local -a node_ip_values=()
  output_value=""

  if [ ! -e "$yaml_file" ]; then
    echo "rendered LiveKit configuration file not found: $yaml_file" >&2
    return 1
  fi
  if [ ! -f "$yaml_file" ]; then
    echo "rendered LiveKit configuration path is not a file: $yaml_file" >&2
    return 1
  fi
  if [ ! -s "$yaml_file" ]; then
    echo "rendered LiveKit configuration file is empty: $yaml_file" >&2
    return 1
  fi

  mapfile -t node_ip_values < <(
    awk '
      /^[[:space:]]*node_ip[[:space:]]*:/ {
        value = $0
        sub(/^[[:space:]]*node_ip[[:space:]]*:[[:space:]]*/, "", value)
        sub(/[[:space:]]+$/, "", value)
        print value
      }
    ' "$yaml_file"
  )

  if [ "${#node_ip_values[@]}" -ne 1 ]; then
    echo "rendered LiveKit configuration must contain exactly one node_ip entry (found ${#node_ip_values[@]}): $yaml_file" >&2
    return 1
  fi

  output_value="${node_ip_values[0]}"
}

# validate_rendered_node_ip: validates an extracted node_ip for the target.
#
# Args:
#   $1  "1" for a loopback/local target; "0" for LAN.
#   $2  expected LAN node IP (LIVEKIT_NODE_IP loaded from .env.dev).
#   $3  node_ip extracted from the rendered LiveKit YAML.
validate_rendered_node_ip() {
  local target_is_loopback="$1"
  local expected_node_ip="$2"
  local rendered_node_ip="$3"

  expected_node_ip="${expected_node_ip//[[:space:]]/}"
  expected_node_ip="${expected_node_ip#\"}"
  expected_node_ip="${expected_node_ip%\"}"
  rendered_node_ip="${rendered_node_ip//[[:space:]]/}"
  rendered_node_ip="${rendered_node_ip#\"}"
  rendered_node_ip="${rendered_node_ip%\"}"

  if [ -z "$rendered_node_ip" ]; then
    echo "rendered node_ip is missing" >&2
    return 1
  fi

  if [ "$target_is_loopback" = "1" ]; then
    return 0
  fi

  if [[ "$rendered_node_ip" == 127.* ]] || [ "$rendered_node_ip" = "::1" ] || [ "$rendered_node_ip" = "localhost" ]; then
    echo "rendered node_ip is loopback and cannot be used for LAN validation" >&2
    return 1
  fi

  if [ -z "$expected_node_ip" ]; then
    echo "expected LAN node IP is missing" >&2
    return 1
  fi

  if [ "$rendered_node_ip" != "$expected_node_ip" ]; then
    echo "rendered node_ip differs from the expected LAN node IP" >&2
    return 1
  fi

  echo "rendered node_ip is non-loopback and suitable for LAN validation"
}

# validate_rendered_node_ip_file: shared production/test integration from the
# rendered YAML file through parsing and node_ip validation.
#
# Args:
#   $1  rendered LiveKit YAML path.
#   $2  "1" for a loopback/local target; "0" for LAN.
#   $3  expected LAN node IP.
#   $4  name of the variable that receives the parsed node_ip.
validate_rendered_node_ip_file() {
  local yaml_file="$1"
  local target_is_loopback="$2"
  local expected_node_ip="$3"
  local -n output_node_ip="$4"
  local parsed_node_ip=""
  output_node_ip=""

  if ! read_rendered_node_ip "$yaml_file" parsed_node_ip; then
    return 1
  fi
  output_node_ip="$parsed_node_ip"
  validate_rendered_node_ip "$target_is_loopback" "$expected_node_ip" "$parsed_node_ip"
}

# compute_final_result: sets the globals FINAL_RESULT, FINAL_EXIT_CODE.
#
# Args:
#   $1  name of an array variable containing "scenario_key|value" strings
#       (same format as the main script's RESULTS array).
#   $2  "1" if the operator has explicitly confirmed the required manual
#       evidence (media publish/receive, selected ICE candidate type, relay
#       candidate confirmed, actual transport used, real second device/
#       browser test, chrome://webrtc-internals or equivalent inspection);
#       "0" (or unset) otherwise.
#   $3  "1" if the run target was loopback/localhost (same-host dry run,
#       cannot prove a real second device); "0" otherwise.
#   $4  name of an array variable that will receive the human-readable
#       reasons contributing to the final result (cleared and repopulated).
compute_final_result() {
  local -n _wrocr_results="$1"
  local manual_evidence_confirmed="${2:-0}"
  local target_is_loopback="${3:-0}"
  local -n _wrocr_reasons="$4"

  local required_fail=0
  local partial=0
  local pending=0
  local -A mandatory_seen=()
  _wrocr_reasons=()

  local entry key value
  for entry in "${_wrocr_results[@]}"; do
    IFS='|' read -r key value <<<"$entry"
    case "$key" in
      A_reachability)
        mandatory_seen["$key"]="$value"
        if [[ "$value" == FAIL* ]]; then
          required_fail=1
          _wrocr_reasons+=("LiveKit reachability failed (A)")
        elif [[ "$value" == PARTIAL* ]]; then
          partial=1
          _wrocr_reasons+=("LiveKit reachability was partial (A)")
        elif [[ "$value" == SKIPPED* ]]; then
          pending=1
          _wrocr_reasons+=("LiveKit reachability scenario skipped (A)")
        fi
        ;;
      B_stun_binding)
        mandatory_seen["$key"]="$value"
        if [[ "$value" == FAIL* ]]; then
          required_fail=1
          _wrocr_reasons+=("STUN binding failed (B)")
        elif [[ "$value" == PARTIAL* ]]; then
          partial=1
          _wrocr_reasons+=("STUN binding was partial (B)")
        elif [[ "$value" == SKIPPED* ]]; then
          pending=1
          _wrocr_reasons+=("STUN binding scenario skipped (B)")
        fi
        ;;
      C_turn_udp)
        mandatory_seen["$key"]="$value"
        if [[ "$value" == FAIL* ]]; then
          required_fail=1
          _wrocr_reasons+=("TURN relay allocation over UDP failed (C-UDP)")
        elif [[ "$value" == PARTIAL* ]]; then
          partial=1
          _wrocr_reasons+=("TURN relay allocation over UDP was partial (C-UDP)")
        elif [[ "$value" == SKIPPED* ]]; then
          pending=1
          _wrocr_reasons+=("TURN relay allocation over UDP skipped (C-UDP)")
        fi
        ;;
      C_turn_tcp)
        if [[ "$value" == FAIL* ]]; then
          partial=1
          _wrocr_reasons+=("TURN TCP fallback failed (C-TCP)")
        fi
        ;;
      C_turn_tls)
        if [[ "$value" == FAIL* ]]; then
          partial=1
          _wrocr_reasons+=("TURN TLS/443 fallback failed (C-TLS)")
        elif [[ "$value" == NOT_CONFIGURED* ]]; then
          partial=1
          _wrocr_reasons+=("TURN TLS/443 fallback not configured in this stack (C-TLS)")
        fi
        ;;
      E_room_connectivity)
        mandatory_seen["$key"]="$value"
        if [[ "$value" == FAIL* ]]; then
          required_fail=1
          _wrocr_reasons+=("Room connectivity / participant presence check failed (E)")
        elif [[ "$value" == SKIPPED* ]]; then
          pending=1
          _wrocr_reasons+=("Room connectivity / participant presence scenario skipped (E)")
        fi
        ;;
      F_stability)
        if [[ "$value" == PARTIAL* ]]; then
          partial=1
          _wrocr_reasons+=("Stability window reported participant drop(s) (F)")
        elif [[ "$value" == SKIPPED* ]]; then
          pending=1
          _wrocr_reasons+=("Stability scenario skipped (F)")
        fi
        ;;
    esac
  done

  local mandatory_key mandatory_value
  for mandatory_key in "${WEBRTC_OFFICE_MANDATORY_SCENARIOS[@]}"; do
    if [ ! -v 'mandatory_seen[$mandatory_key]' ]; then
      pending=1
      _wrocr_reasons+=("Mandatory automated result missing: $mandatory_key")
      continue
    fi

    mandatory_value="${mandatory_seen[$mandatory_key]}"
    if [ -z "$mandatory_value" ]; then
      pending=1
      _wrocr_reasons+=("Mandatory automated result empty: $mandatory_key")
    elif [[ "$mandatory_value" != OK* ]] &&
      [[ "$mandatory_value" != FAIL* ]] &&
      [[ "$mandatory_value" != PARTIAL* ]] &&
      [[ "$mandatory_value" != SKIPPED* ]]; then
      pending=1
      _wrocr_reasons+=("Mandatory automated result has an unknown state: $mandatory_key")
    fi
  done

  if [ "$manual_evidence_confirmed" != "1" ]; then
    pending=1
    _wrocr_reasons+=("Required manual evidence not confirmed: media publish/receive, selected ICE candidate type, relay candidate, actual transport used, real second device/browser, chrome://webrtc-internals (or equivalent) inspection — see runbook")
  fi

  if [ "$target_is_loopback" = "1" ]; then
    pending=1
    _wrocr_reasons+=("Target host is loopback (same-host dry run) — physical office/LAN second-device test not performed")
  fi

  if [ "$required_fail" -eq 1 ]; then
    FINAL_RESULT="FAILED"
    FINAL_EXIT_CODE=1
  elif [ "$pending" -eq 1 ]; then
    FINAL_RESULT="PENDING"
    FINAL_EXIT_CODE=3
  elif [ "$partial" -eq 1 ]; then
    FINAL_RESULT="PARTIAL"
    FINAL_EXIT_CODE=2
  else
    FINAL_RESULT="APPROVED"
    FINAL_EXIT_CODE=0
  fi
}
