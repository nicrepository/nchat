#!/usr/bin/env bash
# CI-only config check for the WebRTC office network validation QA script
# (TASK-158 / issue #187). Does NOT touch the network or start containers.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
ERRORS=0
SELF_TEST_TMP_DIR="$(mktemp -d)"
cleanup_self_tests() {
  rm -rf "$SELF_TEST_TMP_DIR"
}
trap cleanup_self_tests EXIT

print_expected_actual() {
  local expected="$1" actual="$2"
  printf '    expected message:\n%s\n' "$expected" >&2
  printf '    actual output:\n%s\n' "${actual:-<empty>}" >&2
}

echo "=== WebRTC office network validation — config check ==="

# ---------------------------------------------------------------------------
# 1. Required files exist
# ---------------------------------------------------------------------------
echo
echo "--- Required files ---"
REQUIRED_FILES=(
  "scripts/qa/validate-webrtc-office-network.sh"
  "scripts/qa/lib/webrtc-office-network-result.sh"
  "docs/runbooks/task-158-webrtc-office-network-validation.md"
  "poc-results/webrtc-office-network/RESULT-TEMPLATE.md"
)
for f in "${REQUIRED_FILES[@]}"; do
  if [ -f "$ROOT_DIR/$f" ]; then
    echo "  [OK]   $f"
  else
    echo "  [FAIL] missing: $f" >&2
    ERRORS=$((ERRORS + 1))
  fi
done

# ---------------------------------------------------------------------------
# 2. Bash syntax check
# ---------------------------------------------------------------------------
echo
echo "--- Script syntax check (bash -n) ---"
SCRIPT="$ROOT_DIR/scripts/qa/validate-webrtc-office-network.sh"
RESULT_LIB="$ROOT_DIR/scripts/qa/lib/webrtc-office-network-result.sh"
if [ -f "$SCRIPT" ] && bash -n "$SCRIPT" 2>/dev/null; then
  echo "  [OK]   scripts/qa/validate-webrtc-office-network.sh"
else
  echo "  [FAIL] syntax error (or missing): scripts/qa/validate-webrtc-office-network.sh" >&2
  ERRORS=$((ERRORS + 1))
fi
if [ -f "$RESULT_LIB" ] && bash -n "$RESULT_LIB" 2>/dev/null; then
  echo "  [OK]   scripts/qa/lib/webrtc-office-network-result.sh"
else
  echo "  [FAIL] syntax error (or missing): scripts/qa/lib/webrtc-office-network-result.sh" >&2
  ERRORS=$((ERRORS + 1))
fi

# ---------------------------------------------------------------------------
# 3. poc-results/webrtc-office-network is gitignored (except the template)
# ---------------------------------------------------------------------------
echo
echo "--- Raw results must not be versioned ---"
cd "$ROOT_DIR"
if git check-ignore "poc-results/webrtc-office-network/$(date +%s)-summary.md" > /dev/null 2>&1; then
  echo "  [OK]   poc-results/webrtc-office-network/*-summary.md is gitignored"
else
  echo "  [FAIL] poc-results/webrtc-office-network/ summary files are NOT gitignored — update .gitignore" >&2
  ERRORS=$((ERRORS + 1))
fi
if git ls-files --error-unmatch "poc-results/webrtc-office-network/RESULT-TEMPLATE.md" > /dev/null 2>&1; then
  echo "  [OK]   RESULT-TEMPLATE.md is tracked (sanitized template, versionable)"
elif git check-ignore "poc-results/webrtc-office-network/RESULT-TEMPLATE.md" > /dev/null 2>&1; then
  echo "  [FAIL] RESULT-TEMPLATE.md is gitignored — it must be a versionable exception" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   RESULT-TEMPLATE.md is not gitignored (versionable once committed)"
fi

# ---------------------------------------------------------------------------
# 4. No secrets hardcoded in the new script/docs
# ---------------------------------------------------------------------------
echo
echo "--- Security checks ---"
# The only allowed occurrence is the safe idiom used consistently across the
# media scripts: assigning the variable from itself for `docker run -e`
# (e.g. LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET"), never a literal value.
SECRET_CANDIDATES=$(grep -rnE "(LIVEKIT_API_SECRET|COTURN_STATIC_AUTH_SECRET)\s*=" \
  "$ROOT_DIR/scripts/qa/validate-webrtc-office-network.sh" \
  "$ROOT_DIR/docs/runbooks/task-158-webrtc-office-network-validation.md" \
  "$ROOT_DIR/poc-results/webrtc-office-network/RESULT-TEMPLATE.md" \
  2>/dev/null || true)
UNSAFE_SECRET_LINES=$(echo "$SECRET_CANDIDATES" | grep -vE '="\$(LIVEKIT_API_SECRET|COTURN_STATIC_AUTH_SECRET)"' | grep -vE '^\s*$' || true)
if [ -n "$UNSAFE_SECRET_LINES" ]; then
  echo "  [FAIL] potential hardcoded LiveKit/coturn secret found:" >&2
  echo "$UNSAFE_SECRET_LINES" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   no LiveKit/coturn secret hardcoded (only safe var-from-var references)"
fi

if grep -qE '^\s*[A-Za-z0-9._-]*token[A-Za-z0-9._-]*\s*=\s*["'"'"'][^"'"'"']+' \
  "$ROOT_DIR/scripts/qa/validate-webrtc-office-network.sh" 2>/dev/null; then
  echo "  [FAIL] potential hardcoded token found in validation script" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   no hardcoded token in validation script"
fi

# ---------------------------------------------------------------------------
# 5. Script validates its own arguments (no eval, bounded values)
# ---------------------------------------------------------------------------
echo
echo "--- Script safety checks ---"
if grep -q "eval " "$SCRIPT" 2>/dev/null; then
  echo "  [FAIL] script uses eval — not allowed" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   script does not use eval"
fi
for marker in "validate_positive_int" "validate_host" "set -euo pipefail"; do
  if grep -qF "$marker" "$SCRIPT" 2>/dev/null; then
    echo "  [OK]   script has: $marker"
  else
    echo "  [FAIL] script is missing: $marker" >&2
    ERRORS=$((ERRORS + 1))
  fi
done

# ---------------------------------------------------------------------------
# 5b. Scenario E must be reclassified as connectivity/presence-only, not
#     proof of media/relay/second-device (Code Quality Review finding #2)
# ---------------------------------------------------------------------------
echo
echo "--- Scenario E/F reclassification checks ---"
if grep -qF "E_two_participants" "$SCRIPT" 2>/dev/null; then
  echo "  [FAIL] script still uses the old 'E_two_participants' key — must be 'E_room_connectivity'" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "  [OK]   old 'E_two_participants' key not present"
fi
if grep -qF "E_room_connectivity" "$SCRIPT" 2>/dev/null; then
  echo "  [OK]   script uses the reclassified 'E_room_connectivity' key"
else
  echo "  [FAIL] script is missing the reclassified 'E_room_connectivity' key" >&2
  ERRORS=$((ERRORS + 1))
fi
if grep -qF "WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED" "$SCRIPT" 2>/dev/null; then
  echo "  [OK]   script requires explicit manual evidence confirmation for APPROVED"
else
  echo "  [FAIL] script is missing WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED handling" >&2
  ERRORS=$((ERRORS + 1))
fi

# ---------------------------------------------------------------------------
# 5c. Cleanup must track room creation and not mask the original exit code
#     (Code Quality Review finding "Cleanup")
# ---------------------------------------------------------------------------
echo
echo "--- Cleanup checks ---"
for marker in "ROOM_CREATED" "local ec=\$?" "exit \"\$ec\""; do
  if grep -qF "$marker" "$SCRIPT" 2>/dev/null; then
    echo "  [OK]   script has: $marker"
  else
    echo "  [FAIL] script is missing: $marker" >&2
    ERRORS=$((ERRORS + 1))
  fi
done

# ---------------------------------------------------------------------------
# 5d. LAN override support must be additive/opt-in (Code Quality Review
#     finding #3) — default media_compose() behavior must be unchanged.
# ---------------------------------------------------------------------------
echo
echo "--- LAN override wiring checks ---"
MEDIA_ENV_HELPER="$ROOT_DIR/scripts/dev/_media_env.sh"
if grep -qF "MEDIA_COMPOSE_EXTRA_FILE" "$MEDIA_ENV_HELPER" 2>/dev/null; then
  echo "  [OK]   scripts/dev/_media_env.sh supports optional MEDIA_COMPOSE_EXTRA_FILE"
else
  echo "  [FAIL] scripts/dev/_media_env.sh is missing MEDIA_COMPOSE_EXTRA_FILE support" >&2
  ERRORS=$((ERRORS + 1))
fi
if grep -qF "node_ip" "$SCRIPT" 2>/dev/null; then
  echo "  [OK]   script preflight-checks the rendered node_ip in LAN mode"
else
  echo "  [FAIL] script is missing the LAN node_ip preflight check" >&2
  ERRORS=$((ERRORS + 1))
fi

# ---------------------------------------------------------------------------
# 6. package.json / Makefile wiring
# ---------------------------------------------------------------------------
echo
echo "--- package.json / Makefile wiring ---"
PACKAGE_JSON="$ROOT_DIR/package.json"
for target in "qa:webrtc-office-network" "webrtc-office-network:config-check"; do
  if grep -q "\"$target\"" "$PACKAGE_JSON"; then
    echo "  [OK]   package.json has: $target"
  else
    echo "  [FAIL] package.json missing script: $target" >&2
    ERRORS=$((ERRORS + 1))
  fi
done

MAKEFILE="$ROOT_DIR/Makefile"
for target in "qa-webrtc-office-network" "webrtc-office-network-config-check"; do
  if grep -q "^${target}:" "$MAKEFILE"; then
    echo "  [OK]   Makefile has: $target"
  else
    echo "  [FAIL] Makefile missing target: $target" >&2
    ERRORS=$((ERRORS + 1))
  fi
done

# ---------------------------------------------------------------------------
# 7. Result aggregation self-test (deterministic, no network/Docker).
# ---------------------------------------------------------------------------
echo
echo "--- Result aggregation self-test (network-independent) ---"
if [ -f "$RESULT_LIB" ]; then
  # shellcheck source=../qa/lib/webrtc-office-network-result.sh
  source "$RESULT_LIB"

  assert_final_result() {
    local case_name="$1" manual_evidence="$2" loopback="$3" expected_result="$4" expected_exit="$5"
    shift 5
    local -a scenario_results=("$@")
    local -a reasons=()
    compute_final_result scenario_results "$manual_evidence" "$loopback" reasons
    if [ "$FINAL_RESULT" = "$expected_result" ] && [ "$FINAL_EXIT_CODE" -eq "$expected_exit" ]; then
      echo "  [OK]   $case_name -> $FINAL_RESULT (exit $FINAL_EXIT_CODE)"
    else
      echo "  [FAIL] $case_name -> expected ${expected_result}/${expected_exit}, got ${FINAL_RESULT}/${FINAL_EXIT_CODE}" >&2
      ERRORS=$((ERRORS + 1))
    fi
  }

  ALL_PASS_RESULTS=(
    "A_reachability|OK (HTTP 200)"
    "B_stun_binding|OK"
    "C_turn_udp|OK"
    "C_turn_tcp|OK"
    "C_turn_tls|OK"
    "E_room_connectivity|OK"
    "F_stability|OK (0 drops / 60s)"
  )

  assert_final_result "PENDING (no results recorded)" "1" "0" "PENDING" "3"
  assert_final_result "PENDING (mandatory results missing)" "1" "0" "PENDING" "3" \
    "A_reachability|OK (HTTP 200)" "B_stun_binding|OK"
  assert_final_result "PENDING (manual evidence missing)" "0" "0" "PENDING" "3" \
    "${ALL_PASS_RESULTS[@]}"
  assert_final_result "APPROVED (all criteria met)" "1" "0" "APPROVED" "0" \
    "${ALL_PASS_RESULTS[@]}"
  assert_final_result "FAILED (failure takes precedence over missing)" "1" "0" "FAILED" "1" \
    "B_stun_binding|FAIL" "C_turn_udp|OK"
  assert_final_result "PENDING (missing takes precedence over partial)" "1" "0" "PENDING" "3" \
    "A_reachability|OK (HTTP 200)" "C_turn_tcp|FAIL"

  PARTIAL_RESULTS=("${ALL_PASS_RESULTS[@]}")
  PARTIAL_RESULTS[6]="F_stability|PARTIAL (1 drop / 60s)"
  assert_final_result "PARTIAL (all results present, stability partial)" "1" "0" "PARTIAL" "2" \
    "${PARTIAL_RESULTS[@]}"

  EMPTY_RESULT=("${ALL_PASS_RESULTS[@]}")
  EMPTY_RESULT[1]="B_stun_binding|"
  assert_final_result "PENDING (mandatory result empty)" "1" "0" "PENDING" "3" \
    "${EMPTY_RESULT[@]}"

  UNKNOWN_RESULT=("${ALL_PASS_RESULTS[@]}")
  UNKNOWN_RESULT[1]="unknown_scenario|OK"
  assert_final_result "PENDING (unknown name does not satisfy mandatory result)" "1" "0" "PENDING" "3" \
    "${UNKNOWN_RESULT[@]}"

  REPEATED_FIRST=("${ALL_PASS_RESULTS[@]}")
  REPEATED_FIRST[1]="B_stun_binding|FAIL"
  assert_final_result "FAILED (first repeated invocation)" "1" "0" "FAILED" "1" \
    "${REPEATED_FIRST[@]}"
  assert_final_result "APPROVED (second invocation has no residual state)" "1" "0" "APPROVED" "0" \
    "${ALL_PASS_RESULTS[@]}"

  SKIPPED_RESULT=("${ALL_PASS_RESULTS[@]}")
  SKIPPED_RESULT[1]="B_stun_binding|SKIPPED"
  assert_final_result "PENDING (mandatory result skipped)" "1" "0" "PENDING" "3" \
    "${SKIPPED_RESULT[@]}"

  UNKNOWN_STATE_RESULT=("${ALL_PASS_RESULTS[@]}")
  UNKNOWN_STATE_RESULT[1]="B_stun_binding|UNKNOWN"
  assert_final_result "PENDING (mandatory result has unknown state)" "1" "0" "PENDING" "3" \
    "${UNKNOWN_STATE_RESULT[@]}"

  assert_final_result "APPROVED (first invocation before empty results)" "1" "0" "APPROVED" "0" \
    "${ALL_PASS_RESULTS[@]}"
  assert_final_result "PENDING (empty second invocation has no residual state)" "1" "0" "PENDING" "3"

  assert_final_result "PENDING (manual evidence empty)" "" "0" "PENDING" "3" \
    "${ALL_PASS_RESULTS[@]}"
  assert_final_result "PENDING (manual evidence zero)" "0" "0" "PENDING" "3" \
    "${ALL_PASS_RESULTS[@]}"

  assert_manual_evidence_value() {
    local case_name="$1" value="$2" expected_exit="$3"
    local actual_exit
    if validate_manual_evidence_confirmation "$value" >/dev/null 2>&1; then
      actual_exit=0
    else
      actual_exit=$?
    fi
    if [ "$actual_exit" -eq "$expected_exit" ]; then
      echo "  [OK]   $case_name -> exit $actual_exit"
    else
      echo "  [FAIL] $case_name -> expected exit $expected_exit, got $actual_exit" >&2
      ERRORS=$((ERRORS + 1))
    fi
  }
  assert_manual_evidence_value "invalid manual evidence numeric value" "2" "1"
  assert_manual_evidence_value "invalid manual evidence text value" "true" "1"

  echo
  echo "--- LAN node_ip parser/preflight integration self-test (network-independent) ---"
  PREFLIGHT_FIXTURES="$SELF_TEST_TMP_DIR/preflight"
  mkdir -p "$PREFLIGHT_FIXTURES"
  EMPTY_YAML="$PREFLIGHT_FIXTURES/empty.yaml"
  NO_NODE_IP_YAML="$PREFLIGHT_FIXTURES/no-node-ip.yaml"
  EMPTY_NODE_IP_YAML="$PREFLIGHT_FIXTURES/empty-node-ip.yaml"
  LOOPBACK_NODE_IP_YAML="$PREFLIGHT_FIXTURES/loopback-node-ip.yaml"
  DIVERGENT_NODE_IP_YAML="$PREFLIGHT_FIXTURES/divergent-node-ip.yaml"
  CORRECT_NODE_IP_YAML="$PREFLIGHT_FIXTURES/correct-node-ip.yaml"
  MULTIPLE_NODE_IP_YAML="$PREFLIGHT_FIXTURES/multiple-node-ip.yaml"
  MISSING_NODE_IP_YAML="$PREFLIGHT_FIXTURES/missing.yaml"
  : > "$EMPTY_YAML"
  printf 'rtc:\n  use_external_ip: false\n' > "$NO_NODE_IP_YAML"
  printf 'rtc:\n  node_ip:\n' > "$EMPTY_NODE_IP_YAML"
  printf 'rtc:\n  node_ip: 127.0.0.1\n' > "$LOOPBACK_NODE_IP_YAML"
  printf 'rtc:\n  node_ip: 192.168.10.21\n' > "$DIVERGENT_NODE_IP_YAML"
  printf 'rtc:\n  node_ip: 192.168.10.20\n' > "$CORRECT_NODE_IP_YAML"
  printf 'rtc:\n  node_ip: 192.168.10.20\nother:\n  node_ip: 192.168.10.20\n' > "$MULTIPLE_NODE_IP_YAML"

  assert_node_ip_file_preflight() {
    local case_name="$1" yaml_file="$2" target_host="$3" target_is_loopback="$4"
    local livekit_node_ip="$5" expected_exit="$6" expected_message="${7:-}"
    local output_file="$SELF_TEST_TMP_DIR/preflight-output"
    local actual_exit output

    if WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=true bash -c '
      source "$1"
      rendered_node_ip=""
      expected_node_ip=""
      run_webrtc_office_network_preflight "$2" "$3" "$4" "$5" \
        rendered_node_ip expected_node_ip
    ' _ "$SCRIPT" "$yaml_file" "$target_host" "$target_is_loopback" "$livekit_node_ip" >"$output_file" 2>&1; then
      actual_exit=0
    else
      actual_exit=$?
    fi
    output="$(cat "$output_file")"

    if [ "$actual_exit" -ne "$expected_exit" ]; then
      echo "  [FAIL] $case_name -> expected exit $expected_exit, got $actual_exit" >&2
      ERRORS=$((ERRORS + 1))
    elif [ -n "$expected_message" ] && [[ "$output" != *"$expected_message"* ]]; then
      echo "  [FAIL] $case_name -> expected message was not found" >&2
      print_expected_actual "$expected_message" "$output"
      ERRORS=$((ERRORS + 1))
    else
      echo "  [OK]   $case_name -> exit $actual_exit"
    fi
  }

  assert_node_ip_file_preflight "missing rendered YAML" "$MISSING_NODE_IP_YAML" "192.168.10.20" "0" "192.168.10.20" "1" \
    "rendered LiveKit configuration file not found"
  assert_node_ip_file_preflight "empty rendered YAML" "$EMPTY_YAML" "192.168.10.20" "0" "192.168.10.20" "1" \
    "rendered LiveKit configuration file is empty"
  assert_node_ip_file_preflight "rendered YAML without node_ip" "$NO_NODE_IP_YAML" "192.168.10.20" "0" "192.168.10.20" "1" \
    "rendered LiveKit configuration must contain exactly one node_ip entry"
  assert_node_ip_file_preflight "rendered YAML with empty node_ip" "$EMPTY_NODE_IP_YAML" "192.168.10.20" "0" "192.168.10.20" "1" \
    "rendered node_ip is missing"
  assert_node_ip_file_preflight "LAN target + loopback node_ip" "$LOOPBACK_NODE_IP_YAML" "192.168.10.20" "0" "192.168.10.20" "1" \
    "rendered node_ip is loopback and cannot be used for LAN validation"
  assert_node_ip_file_preflight "LAN target + divergent node_ip" "$DIVERGENT_NODE_IP_YAML" "192.168.10.20" "0" "192.168.10.20" "1" \
    "rendered node_ip differs from the expected LAN node IP"
  assert_node_ip_file_preflight "LAN target + expected node_ip" "$CORRECT_NODE_IP_YAML" "192.168.10.20" "0" "192.168.10.20" "0" \
    "rendered node_ip is non-loopback and suitable for LAN validation"
  assert_node_ip_file_preflight "multiple rendered node_ip entries" "$MULTIPLE_NODE_IP_YAML" "192.168.10.20" "0" "192.168.10.20" "1" \
    "rendered LiveKit configuration must contain exactly one node_ip entry"
  assert_node_ip_file_preflight "DNS target without LIVEKIT_NODE_IP" "$CORRECT_NODE_IP_YAML" "media.office.test" "0" "" "1" \
    "expected LAN node IP is missing"
  assert_node_ip_file_preflight "DNS target + matching LIVEKIT_NODE_IP" "$CORRECT_NODE_IP_YAML" "media.office.test" "0" "192.168.10.20" "0" \
    "rendered node_ip is non-loopback and suitable for LAN validation"
  assert_node_ip_file_preflight "local target + loopback node_ip" "$LOOPBACK_NODE_IP_YAML" "127.0.0.1" "1" "127.0.0.1" "0"

  echo
  echo "--- Public entrypoint and source behavior self-test (network-independent) ---"
  FAKE_BIN="$SELF_TEST_TMP_DIR/fake-bin"
  mkdir -p "$FAKE_BIN"
  printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$*" >> "${WEBRTC_QA_DOCKER_MARKER:?}"\nexit 0\n' > "$FAKE_BIN/docker"
  printf '#!/usr/bin/env bash\nprintf "000"\nexit 7\n' > "$FAKE_BIN/curl"
  chmod +x "$FAKE_BIN/docker" "$FAKE_BIN/curl"

  assert_source_is_side_effect_free() {
    local case_name="source does not execute main"
    local cleanup_parent="$SELF_TEST_TMP_DIR/source-cleanup"
    local results_dir="$SELF_TEST_TMP_DIR/source-results"
    local output_file="$SELF_TEST_TMP_DIR/source-output"
    local docker_marker="$SELF_TEST_TMP_DIR/source-docker-calls"
    local actual_exit output residual_count summary_count
    mkdir -p "$cleanup_parent" "$results_dir"

    if PATH="$FAKE_BIN:$PATH" TMPDIR="$cleanup_parent" \
      WEBRTC_QA_DOCKER_MARKER="$docker_marker" \
      WEBRTC_QA_RESULTS_DIR="$results_dir" \
      WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=true \
      bash -c 'source "$1"; printf "__SOURCE_RETURNED__:%s\n" "$(type -t run_webrtc_office_network_preflight)"' \
      _ "$SCRIPT" >"$output_file" 2>&1; then
      actual_exit=0
    else
      actual_exit=$?
    fi
    output="$(cat "$output_file")"
    residual_count="$(find "$cleanup_parent" -mindepth 1 -maxdepth 1 | wc -l)"
    summary_count="$(find "$results_dir" -type f | wc -l)"

    if [ "$actual_exit" -ne 0 ] || [[ "$output" != *"__SOURCE_RETURNED__:function"* ]]; then
      echo "  [FAIL] $case_name -> source did not return control with the shared function loaded" >&2
      print_expected_actual "__SOURCE_RETURNED__:function" "$output"
      ERRORS=$((ERRORS + 1))
    elif [ "$residual_count" -ne 0 ] || [ "$summary_count" -ne 0 ] || [ -e "$docker_marker" ]; then
      echo "  [FAIL] $case_name -> source created a TMP item, summary, or Docker call" >&2
      ERRORS=$((ERRORS + 1))
    else
      echo "  [OK]   $case_name -> no main, summary, Docker call, or TMP residue"
    fi
  }

  assert_legacy_mode_rejected() {
    local value_label="$1" value="$2"
    local results_dir="$SELF_TEST_TMP_DIR/legacy-results-${value_label}"
    local output_file="$SELF_TEST_TMP_DIR/legacy-output-${value_label}"
    local docker_marker="$SELF_TEST_TMP_DIR/legacy-docker-${value_label}"
    local expected_message="WEBRTC_QA_CONFIG_CHECK_PREFLIGHT_ONLY is no longer supported; use the CI config-check command"
    local actual_exit output summary_count
    mkdir -p "$results_dir"

    if env "WEBRTC_QA_CONFIG_CHECK_PREFLIGHT_ONLY=$value" \
      PATH="$FAKE_BIN:$PATH" \
      WEBRTC_QA_DOCKER_MARKER="$docker_marker" \
      WEBRTC_QA_RESULTS_DIR="$results_dir" \
      bash "$SCRIPT" >"$output_file" 2>&1; then
      actual_exit=0
    else
      actual_exit=$?
    fi
    output="$(cat "$output_file")"
    summary_count="$(find "$results_dir" -type f | wc -l)"

    if [ "$actual_exit" -ne 1 ]; then
      echo "  [FAIL] legacy preflight-only value $value_label -> expected exit 1, got $actual_exit" >&2
      ERRORS=$((ERRORS + 1))
    elif [[ "$output" != *"$expected_message"* ]]; then
      echo "  [FAIL] legacy preflight-only value $value_label -> expected message was not found" >&2
      print_expected_actual "$expected_message" "$output"
      ERRORS=$((ERRORS + 1))
    elif [ "$summary_count" -ne 0 ] || [ -e "$docker_marker" ]; then
      echo "  [FAIL] legacy preflight-only value $value_label -> created a summary or invoked Docker" >&2
      ERRORS=$((ERRORS + 1))
    else
      echo "  [OK]   legacy preflight-only value $value_label -> rejected with exit 1"
    fi
  }

  assert_direct_main_once() {
    local output_file="$SELF_TEST_TMP_DIR/direct-main-output"
    local actual_exit output message_count
    if PATH="$FAKE_BIN:$PATH" WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=true \
      bash "$SCRIPT" >"$output_file" 2>&1; then
      actual_exit=0
    else
      actual_exit=$?
    fi
    output="$(cat "$output_file")"
    message_count="$(grep -c "Invalid WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED" "$output_file" || true)"
    if [ "$actual_exit" -ne 1 ] || [ "$message_count" -ne 1 ]; then
      echo "  [FAIL] direct entrypoint -> expected main validation exactly once and exit 1" >&2
      print_expected_actual "one Invalid WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED message" "$output"
      ERRORS=$((ERRORS + 1))
    else
      echo "  [OK]   direct entrypoint -> main executed exactly once"
    fi
  }

  assert_main_preflight_cleanup() {
    local case_name="$1" yaml_file="$2" expected_message="$3"
    local cleanup_parent="$SELF_TEST_TMP_DIR/cleanup-${case_name//[^A-Za-z0-9]/-}"
    local results_dir="$SELF_TEST_TMP_DIR/results-${case_name//[^A-Za-z0-9]/-}"
    local output_file="$SELF_TEST_TMP_DIR/main-preflight-output"
    local actual_exit output residual_count
    mkdir -p "$cleanup_parent" "$results_dir"

    if PATH="$FAKE_BIN:$PATH" TMPDIR="$cleanup_parent" \
      WEBRTC_QA_DOCKER_MARKER="$SELF_TEST_TMP_DIR/cleanup-docker-calls" \
      WEBRTC_QA_LIVEKIT_RUNTIME_YAML="$yaml_file" \
      WEBRTC_QA_TARGET_HOST=192.168.10.20 \
      WEBRTC_QA_RESULTS_DIR="$results_dir" \
      bash "$SCRIPT" >"$output_file" 2>&1; then
      actual_exit=0
    else
      actual_exit=$?
    fi
    output="$(cat "$output_file")"
    residual_count="$(find "$cleanup_parent" -mindepth 1 -maxdepth 1 | wc -l)"

    if [ "$actual_exit" -ne 1 ]; then
      echo "  [FAIL] $case_name -> expected exit 1, got $actual_exit" >&2
      ERRORS=$((ERRORS + 1))
    elif [[ "$output" != *"$expected_message"* ]]; then
      echo "  [FAIL] $case_name -> expected message was not found" >&2
      print_expected_actual "$expected_message" "$output"
      ERRORS=$((ERRORS + 1))
    elif [ "$residual_count" -ne 0 ]; then
      echo "  [FAIL] $case_name -> TMPDIR has $residual_count residual item(s)" >&2
      ERRORS=$((ERRORS + 1))
    else
      echo "  [OK]   $case_name -> exit $actual_exit, TMP_DIR removed"
    fi
  }

  assert_source_is_side_effect_free
  assert_legacy_mode_rejected "1" "1"
  assert_legacy_mode_rejected "0" "0"
  assert_legacy_mode_rejected "true" "true"
  assert_legacy_mode_rejected "empty" ""
  assert_direct_main_once
  assert_main_preflight_cleanup "failed LAN preflight cleanup" "$MISSING_NODE_IP_YAML" \
    "rendered LiveKit configuration file not found"
else
  echo "  [FAIL] cannot self-test: $RESULT_LIB not found" >&2
  ERRORS=$((ERRORS + 1))
fi

echo
echo "--- MEDIA_COMPOSE_EXTRA_FILE resolution self-test (network-independent) ---"
# shellcheck source=../dev/_media_env.sh
source "$MEDIA_ENV_HELPER"
SPACE_OVERRIDE="$SELF_TEST_TMP_DIR/override with spaces.yml"
: > "$SPACE_OVERRIDE"
ABSOLUTE_OVERRIDE="$ROOT_DIR/infra/compose/compose.media.lan-office-test.override.example.yml"
RELATIVE_OVERRIDE="infra/compose/compose.media.lan-office-test.override.example.yml"

assert_extra_file_resolution() {
  local case_name="$1" input_path="$2" call_dir="$3"
  local expected_exit="$4" expected_path="$5" expected_message="${6:-}"
  local original_dir="$PWD" output_file="$SELF_TEST_TMP_DIR/extra-file-output"
  local actual_exit output

  if [ "$input_path" = "__UNSET__" ]; then
    unset MEDIA_COMPOSE_EXTRA_FILE
  else
    MEDIA_COMPOSE_EXTRA_FILE="$input_path"
  fi
  MEDIA_COMPOSE_EXTRA_FILE_RESOLVED="residual"
  cd "$call_dir"
  if resolve_media_compose_extra_file >"$output_file" 2>&1; then
    actual_exit=0
  else
    actual_exit=$?
  fi
  cd "$original_dir"
  output="$(cat "$output_file")"

  if [ "$actual_exit" -ne "$expected_exit" ]; then
    echo "  [FAIL] $case_name -> expected exit $expected_exit, got $actual_exit" >&2
    ERRORS=$((ERRORS + 1))
  elif [ "$MEDIA_COMPOSE_EXTRA_FILE_RESOLVED" != "$expected_path" ]; then
    echo "  [FAIL] $case_name -> expected path '$expected_path', got '$MEDIA_COMPOSE_EXTRA_FILE_RESOLVED'" >&2
    ERRORS=$((ERRORS + 1))
  elif [ -n "$expected_message" ] && [[ "$output" != *"$expected_message"* ]]; then
    echo "  [FAIL] $case_name -> expected message was not found" >&2
    print_expected_actual "$expected_message" "$output"
    ERRORS=$((ERRORS + 1))
  else
    echo "  [OK]   $case_name -> exit $actual_exit"
  fi
}

assert_extra_file_resolution "override variable unset" "__UNSET__" "$ROOT_DIR" "0" ""
assert_extra_file_resolution "absolute override path" "$ABSOLUTE_OVERRIDE" "$ROOT_DIR" "0" "$ABSOLUTE_OVERRIDE"
assert_extra_file_resolution "relative override from repository root" "$RELATIVE_OVERRIDE" "$ROOT_DIR" "0" "$ABSOLUTE_OVERRIDE"
assert_extra_file_resolution "relative override from /tmp" "$RELATIVE_OVERRIDE" "/tmp" "0" "$ABSOLUTE_OVERRIDE"
assert_extra_file_resolution "override path with spaces" "$SPACE_OVERRIDE" "$ROOT_DIR" "0" "$SPACE_OVERRIDE"
assert_extra_file_resolution "missing override path" "infra/compose/missing-office-override.yml" "$ROOT_DIR" "1" "" \
  "MEDIA_COMPOSE_EXTRA_FILE does not reference a file"

echo

if [ "$ERRORS" -gt 0 ]; then
  echo "WebRTC office network config check FAILED with $ERRORS error(s)." >&2
  exit 1
fi

echo "WebRTC office network config check passed."
