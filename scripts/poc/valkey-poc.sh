#!/usr/bin/env bash
# TASK-16 — Valkey PoC: Pub/Sub, Streams, SETNX, TTL and sliding window validation.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"

# ---------------------------------------------------------------------------
# Load environment (without printing secrets)
# ---------------------------------------------------------------------------
if [ ! -f "$ENV_FILE" ]; then
  echo "[INFO] .env.dev not found — copying from .env.dev.example"
  cp "$ENV_EXAMPLE" "$ENV_FILE"
fi
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

VALKEY_PASSWORD="${VALKEY_PASSWORD:-}"
VALKEY_HOST_PORT="${VALKEY_HOST_PORT:-6379}"
VALKEY_POC_ITERATIONS="${VALKEY_POC_ITERATIONS:-10}"

# ---------------------------------------------------------------------------
# Output directories and run id
# ---------------------------------------------------------------------------
RESULTS_DIR="$ROOT_DIR/poc-results/valkey"
mkdir -p "$RESULTS_DIR"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
SUMMARY_FILE="$RESULTS_DIR/${RUN_ID}-summary.md"
METRICS_FILE="$RESULTS_DIR/${RUN_ID}-metrics.json"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "======================================================================"
echo " TASK-16 — Valkey PoC"
echo " Run: $RUN_ID"
echo " Iterations: $VALKEY_POC_ITERATIONS"
echo "======================================================================"

# ---------------------------------------------------------------------------
# Pre-checks
# ---------------------------------------------------------------------------
for cmd in docker python3; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "[ERROR] Required command not found: $cmd" >&2
    exit 1
  fi
done

if ! docker compose version &>/dev/null; then
  echo "[ERROR] docker compose plugin not available" >&2
  exit 1
fi

COMPOSE_CMD=(docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE")

# ---------------------------------------------------------------------------
# Ensure Valkey is running
# ---------------------------------------------------------------------------
VALKEY_RUNNING="$("${COMPOSE_CMD[@]}" ps valkey --format '{{.State}}' 2>/dev/null || echo "")"
if ! echo "$VALKEY_RUNNING" | grep -qi "running"; then
  echo "[INFO] Valkey not running — starting..."
  "${COMPOSE_CMD[@]}" up -d valkey
  echo "[WAIT] Waiting for Valkey to be ready..."
  sleep 5
fi

# ---------------------------------------------------------------------------
# Valkey CLI wrapper (hides password)
# ---------------------------------------------------------------------------
vcli() {
  "${COMPOSE_CMD[@]}" exec -T valkey valkey-cli -a "$VALKEY_PASSWORD" "$@" 2>/dev/null
}

# Verify connectivity
if ! vcli ping | grep -q "PONG"; then
  echo "[ERROR] Cannot connect to Valkey" >&2
  exit 1
fi
echo "[OK] Valkey is reachable"

# ---------------------------------------------------------------------------
# Helper: milliseconds timestamp
# ---------------------------------------------------------------------------
ms_now() { python3 -c "import time; print(int(time.time() * 1000))"; }

# ---------------------------------------------------------------------------
# Test results tracking
# ---------------------------------------------------------------------------
declare -A RESULTS
declare -A LATENCIES
OVERALL_PASS=true

pass() { RESULTS["$1"]="PASS"; echo "[PASS] $1"; }
fail() { RESULTS["$1"]="FAIL: $2"; OVERALL_PASS=false; echo "[FAIL] $1 — $2"; }

# ---------------------------------------------------------------------------
# TEST 1: PING
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 1: PING ---"
T0="$(ms_now)"
PONG="$(vcli ping)"
T1="$(ms_now)"
LATENCIES["ping_ms"]="$((T1 - T0))"
if [ "$PONG" = "PONG" ]; then
  pass "ping"
else
  fail "ping" "expected PONG got: $PONG"
fi
echo "  ping_ms=${LATENCIES[ping_ms]}"

# ---------------------------------------------------------------------------
# TEST 2: SET/GET basic
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 2: SET/GET ---"
vcli SET nchat:poc:string "ok" > /dev/null
VAL="$(vcli GET nchat:poc:string)"
if [ "$VAL" = "ok" ]; then
  pass "set_get"
else
  fail "set_get" "expected 'ok' got '$VAL'"
fi
vcli DEL nchat:poc:string > /dev/null || true

# ---------------------------------------------------------------------------
# TEST 3: TTL/EXPIRE
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 3: TTL/EXPIRE ---"
vcli SET nchat:poc:ttl "expires" EX 5 > /dev/null
TTL_VAL="$(vcli TTL nchat:poc:ttl)"
if [ "$TTL_VAL" -gt 0 ] && [ "$TTL_VAL" -le 5 ]; then
  pass "ttl"
else
  fail "ttl" "TTL expected 1-5, got $TTL_VAL"
fi
vcli DEL nchat:poc:ttl > /dev/null || true

# ---------------------------------------------------------------------------
# TEST 4: SETNX lock
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 4: SETNX lock ---"
vcli DEL nchat:poc:lock > /dev/null || true
LOCK1="$(vcli SET nchat:poc:lock owner1 NX EX 10)"
if [ "$LOCK1" = "OK" ]; then
  pass "setnx_acquire"
else
  fail "setnx_acquire" "expected OK got $LOCK1"
fi

LOCK2="$(vcli SET nchat:poc:lock owner2 NX EX 10)"
if [ -z "$LOCK2" ] || [ "$LOCK2" = "" ]; then
  pass "setnx_reject_duplicate"
else
  fail "setnx_reject_duplicate" "expected nil/empty got $LOCK2"
fi

OWNER="$(vcli GET nchat:poc:lock)"
if [ "$OWNER" = "owner1" ]; then
  pass "setnx_owner_preserved"
else
  fail "setnx_owner_preserved" "expected owner1 got $OWNER"
fi
vcli DEL nchat:poc:lock > /dev/null || true

# ---------------------------------------------------------------------------
# TEST 5: Streams
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 5: Streams ---"
vcli DEL nchat:poc:stream > /dev/null || true

XADD_ID="$(vcli XADD nchat:poc:stream '*' type message channel infra body hello)"
if [ -n "$XADD_ID" ]; then
  pass "xadd"
else
  fail "xadd" "XADD returned empty id"
fi

XRANGE_OUT="$(vcli XRANGE nchat:poc:stream - +)"
if echo "$XRANGE_OUT" | grep -q "message"; then
  pass "xrange"
else
  fail "xrange" "XRANGE output missing expected field: $XRANGE_OUT"
fi

XREAD_OUT="$(vcli XREAD COUNT 1 STREAMS nchat:poc:stream 0)"
if echo "$XREAD_OUT" | grep -q "nchat:poc:stream"; then
  pass "xread"
else
  fail "xread" "XREAD output unexpected: $XREAD_OUT"
fi
vcli DEL nchat:poc:stream > /dev/null || true

# ---------------------------------------------------------------------------
# TEST 6: Pub/Sub
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 6: Pub/Sub ---"
PUBSUB_OUT="$TMP_DIR/pubsub.txt"
PUBSUB_KEY="nchat:poc:pubsub"

# Start subscriber in background inside container with a 5s timeout
# Use a separate exec that subscribes and then exits after receiving one message or timing out
docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" \
  exec -T valkey \
  timeout 8 valkey-cli -a "$VALKEY_PASSWORD" SUBSCRIBE "$PUBSUB_KEY" > "$PUBSUB_OUT" 2>&1 &
SUB_PID=$!

sleep 1

# Publish a message
RECEIVERS="$(vcli PUBLISH "$PUBSUB_KEY" "hello-pubsub")"
echo "  publish receivers: $RECEIVERS"

# Wait for subscriber process (it will exit on timeout)
wait "$SUB_PID" 2>/dev/null || true

if grep -q "hello-pubsub" "$PUBSUB_OUT" 2>/dev/null; then
  pass "pubsub"
else
  # Check if at least one receiver got it (server-side confirmation)
  if [ "${RECEIVERS:-0}" -ge 1 ] 2>/dev/null; then
    RESULTS["pubsub"]="PASS (server confirmed delivery; capture timed out)"
    echo "[PASS] pubsub (server confirmed $RECEIVERS receiver(s))"
  else
    fail "pubsub" "no receivers and message not found in subscriber output"
  fi
fi

# ---------------------------------------------------------------------------
# TEST 7: Sliding window (sorted set based)
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 7: Sliding window rate limit ---"
vcli DEL nchat:poc:sliding > /dev/null || true

WINDOW_MS=60000
LIMIT=3
ALLOWED=0
BLOCKED=0

for i in 1 2 3 4; do
  NOW_MS="$(python3 -c "import time; print(int(time.time() * 1000))")"
  CUTOFF="$((NOW_MS - WINDOW_MS))"
  # Remove expired entries
  vcli ZREMRANGEBYSCORE nchat:poc:sliding 0 "$CUTOFF" > /dev/null
  # Add current request
  vcli ZADD nchat:poc:sliding "$NOW_MS" "req-${RUN_ID}-${i}" > /dev/null
  # Count
  COUNT="$(vcli ZCARD nchat:poc:sliding)"
  # Refresh TTL
  vcli EXPIRE nchat:poc:sliding 60 > /dev/null

  if [ "$COUNT" -le "$LIMIT" ]; then
    ALLOWED=$((ALLOWED + 1))
    echo "  request $i: allowed (count=$COUNT)"
  else
    BLOCKED=$((BLOCKED + 1))
    echo "  request $i: blocked (count=$COUNT)"
  fi
done

if [ "$ALLOWED" -eq 3 ] && [ "$BLOCKED" -eq 1 ]; then
  pass "sliding_window"
else
  fail "sliding_window" "expected 3 allowed + 1 blocked, got allowed=$ALLOWED blocked=$BLOCKED"
fi

SLIDE_TTL="$(vcli TTL nchat:poc:sliding)"
echo "  sliding window TTL: $SLIDE_TTL"
vcli DEL nchat:poc:sliding > /dev/null || true

# ---------------------------------------------------------------------------
# TEST 8: Latency measurement
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 8: Latency measurement (${VALKEY_POC_ITERATIONS} iterations) ---"

measure_latency() {
  local op_name="$1" total=0
  shift
  for _ in $(seq 1 "$VALKEY_POC_ITERATIONS"); do
    T0="$(ms_now)"
    "$@" > /dev/null
    T1="$(ms_now)"
    total=$((total + T1 - T0))
  done
  local avg=$((total / VALKEY_POC_ITERATIONS))
  LATENCIES["${op_name}_avg_ms"]="$avg"
  echo "  $op_name avg=${avg}ms over $VALKEY_POC_ITERATIONS iterations"
}

measure_latency "ping" vcli ping
measure_latency "set" vcli SET nchat:poc:bench:key "value"
measure_latency "get" vcli GET nchat:poc:bench:key

# XADD latency
vcli DEL nchat:poc:bench:stream > /dev/null || true
measure_latency "xadd" vcli XADD nchat:poc:bench:stream '*' field value
measure_latency "xread" vcli XREAD COUNT 1 STREAMS nchat:poc:bench:stream 0

# Cleanup bench keys
vcli DEL nchat:poc:bench:key > /dev/null || true
vcli DEL nchat:poc:bench:stream > /dev/null || true

# ---------------------------------------------------------------------------
# Write Markdown summary
# ---------------------------------------------------------------------------
NOW_HUMAN="$(date '+%Y-%m-%d %H:%M:%S %Z')"
cat > "$SUMMARY_FILE" <<EOF
# TASK-16 — Valkey PoC Summary

**Run:** ${RUN_ID}
**Date:** ${NOW_HUMAN}
**Iterations for latency:** ${VALKEY_POC_ITERATIONS}

## Results

| Test | Result |
|------|--------|
| ping | ${RESULTS[ping]:-NOT RUN} |
| set_get | ${RESULTS[set_get]:-NOT RUN} |
| ttl | ${RESULTS[ttl]:-NOT RUN} |
| setnx_acquire | ${RESULTS[setnx_acquire]:-NOT RUN} |
| setnx_reject_duplicate | ${RESULTS[setnx_reject_duplicate]:-NOT RUN} |
| setnx_owner_preserved | ${RESULTS[setnx_owner_preserved]:-NOT RUN} |
| xadd | ${RESULTS[xadd]:-NOT RUN} |
| xrange | ${RESULTS[xrange]:-NOT RUN} |
| xread | ${RESULTS[xread]:-NOT RUN} |
| pubsub | ${RESULTS[pubsub]:-NOT RUN} |
| sliding_window | ${RESULTS[sliding_window]:-NOT RUN} |

## Latencies (avg over ${VALKEY_POC_ITERATIONS} iterations)

| Operation | avg ms |
|-----------|--------|
| ping | ${LATENCIES[ping_avg_ms]:-N/A} |
| set | ${LATENCIES[set_avg_ms]:-N/A} |
| get | ${LATENCIES[get_avg_ms]:-N/A} |
| xadd | ${LATENCIES[xadd_avg_ms]:-N/A} |
| xread | ${LATENCIES[xread_avg_ms]:-N/A} |

## Limitations
- Not a production benchmark
- Does not test cluster or Sentinel
- Does not test high concurrency
- Does not test failover
- Pub/Sub subscriber capture relies on timeout; server-side delivery confirmation is used as fallback

## Overall: $([ "$OVERALL_PASS" = "true" ] && echo "PASS" || echo "FAIL")
EOF

# ---------------------------------------------------------------------------
# Write JSON metrics
# ---------------------------------------------------------------------------
python3 - <<PYEOF
import json
data = {
    "run_id": "${RUN_ID}",
    "iterations": ${VALKEY_POC_ITERATIONS},
    "results": {
$(for k in "${!RESULTS[@]}"; do echo "        \"$k\": \"${RESULTS[$k]}\","; done)
    },
    "latencies_avg_ms": {
$(for k in "${!LATENCIES[@]}"; do echo "        \"$k\": \"${LATENCIES[$k]}\","; done)
    },
    "overall_pass": $( [ "$OVERALL_PASS" = "true" ] && echo "True" || echo "False" )
}
with open("${METRICS_FILE}", "w") as f:
    json.dump(data, f, indent=2)
print(f"Metrics written to ${METRICS_FILE}")
PYEOF

echo ""
echo "======================================================================"
echo " Summary: $SUMMARY_FILE"
echo " Metrics:  $METRICS_FILE"
echo "======================================================================"

if [ "$OVERALL_PASS" = "true" ]; then
  echo "[RESULT] PASS"
  exit 0
else
  echo "[RESULT] FAIL — review results above"
  exit 1
fi
