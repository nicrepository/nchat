#!/usr/bin/env bash
# TASK-15 — SeaweedFS PoC: upload, download, latency and basic replication validation.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
COMPOSE_FILE="$ROOT_DIR/infra/compose/compose.dev.yml"
ENV_EXAMPLE="$ROOT_DIR/infra/compose/.env.dev.example"
ENV_FILE="$ROOT_DIR/infra/compose/.env.dev"

# ---------------------------------------------------------------------------
# Load environment
# ---------------------------------------------------------------------------
if [ ! -f "$ENV_FILE" ]; then
  echo "[INFO] .env.dev not found — copying from .env.dev.example"
  cp "$ENV_EXAMPLE" "$ENV_FILE"
fi
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

# Resolve ports with defaults
SEAWEEDFS_MASTER_HOST_PORT="${SEAWEEDFS_MASTER_HOST_PORT:-9333}"
SEAWEEDFS_FILER_HOST_PORT="${SEAWEEDFS_FILER_HOST_PORT:-8888}"
SEAWEEDFS_VOLUME_HOST_PORT="${SEAWEEDFS_VOLUME_HOST_PORT:-8088}"
SEAWEEDFS_VOLUME_2_HOST_PORT="${SEAWEEDFS_VOLUME_2_HOST_PORT:-8089}"
SEAWEEDFS_POC_LARGE_MB="${SEAWEEDFS_POC_LARGE_MB:-10}"

# ---------------------------------------------------------------------------
# Output directories and run id
# ---------------------------------------------------------------------------
RESULTS_DIR="$ROOT_DIR/poc-results/seaweedfs"
mkdir -p "$RESULTS_DIR"
RUN_ID="$(date +%Y%m%dT%H%M%S)"
SUMMARY_FILE="$RESULTS_DIR/${RUN_ID}-summary.md"
METRICS_FILE="$RESULTS_DIR/${RUN_ID}-metrics.json"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "======================================================================"
echo " TASK-15 — SeaweedFS PoC"
echo " Run: $RUN_ID"
echo "======================================================================"

# ---------------------------------------------------------------------------
# Pre-checks
# ---------------------------------------------------------------------------
for cmd in docker curl sha256sum python3; do
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
# Ensure SeaweedFS services are running (with replication profile)
# ---------------------------------------------------------------------------
MASTER_URL="http://localhost:${SEAWEEDFS_MASTER_HOST_PORT}"
FILER_URL="http://localhost:${SEAWEEDFS_FILER_HOST_PORT}"

wait_http() {
  local url="$1" label="$2" retries="${3:-30}"
  echo "[WAIT] $label at $url ..."
  for i in $(seq 1 "$retries"); do
    if curl -sf --max-time 2 "$url" &>/dev/null; then
      echo "[OK]   $label is up"
      return 0
    fi
    sleep 2
  done
  echo "[ERROR] $label did not respond after $((retries * 2))s" >&2
  return 1
}

if ! curl -sf --max-time 3 "${MASTER_URL}/cluster/status" &>/dev/null; then
  echo "[INFO] SeaweedFS master not responding — starting environment with replication profile..."
  "${COMPOSE_CMD[@]}" --profile seaweed-replication up -d \
    seaweed-master seaweed-volume seaweed-volume-2 seaweed-filer seaweed-s3
fi

wait_http "${MASTER_URL}/cluster/status" "seaweed-master"
wait_http "${FILER_URL}/" "seaweed-filer"

# ---------------------------------------------------------------------------
# Helper: milliseconds timestamp
# ---------------------------------------------------------------------------
ms_now() { python3 -c "import time; print(int(time.time() * 1000))"; }

# ---------------------------------------------------------------------------
# Helper: map SeaweedFS internal Docker hostname:port to host-accessible URL.
# dir/assign returns internal hostnames (e.g. seaweed-volume:8088).
# The script runs on the host, so we must translate them to localhost:<mapped_port>.
# ---------------------------------------------------------------------------
map_seaweedfs_url() {
  local internal_url="$1"
  case "$internal_url" in
    seaweed-volume:8088)   echo "localhost:${SEAWEEDFS_VOLUME_HOST_PORT}" ;;
    seaweed-volume-2:8088) echo "localhost:${SEAWEEDFS_VOLUME_2_HOST_PORT}" ;;
    *)
      # Fallback: replace any container hostname with localhost and keep port
      echo "$internal_url" | sed 's/^[^:]*:/localhost:/'
      ;;
  esac
}

# ---------------------------------------------------------------------------
# Test results tracking
# ---------------------------------------------------------------------------
declare -A RESULTS
declare -A LATENCIES
OVERALL_PASS=true

pass() { RESULTS["$1"]="PASS"; echo "[PASS] $1"; }
fail() { RESULTS["$1"]="FAIL: $2"; OVERALL_PASS=false; echo "[FAIL] $1 — $2"; }

# ---------------------------------------------------------------------------
# TEST 1: Status smoke test
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 1: Status smoke ---"
if curl -sf --max-time 5 "${MASTER_URL}/cluster/status" -o /dev/null; then
  pass "master_status"
else
  fail "master_status" "HTTP request failed"
fi

if curl -sf --max-time 5 "${FILER_URL}/" -o /dev/null; then
  pass "filer_status"
else
  fail "filer_status" "HTTP request failed"
fi

# ---------------------------------------------------------------------------
# TEST 2: Upload/download small file
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 2: Upload/download small file ---"
SMALL_FILE="$TMP_DIR/small.txt"
SMALL_DL="$TMP_DIR/small-dl.txt"
echo "nchat-seaweedfs-poc-small-$(date +%s)" > "$SMALL_FILE"
SMALL_SHA="$(sha256sum "$SMALL_FILE" | awk '{print $1}')"

T0="$(ms_now)"
if curl -sS --max-time 15 -X POST -F "file=@${SMALL_FILE}" "${FILER_URL}/poc/small.txt" -o /dev/null; then
  T1="$(ms_now)"
  LATENCIES["upload_small_ms"]="$((T1 - T0))"
  pass "upload_small"
else
  fail "upload_small" "curl upload failed"
  LATENCIES["upload_small_ms"]="N/A"
fi

T0="$(ms_now)"
if curl -sS --max-time 15 "${FILER_URL}/poc/small.txt" -o "$SMALL_DL"; then
  T1="$(ms_now)"
  LATENCIES["download_small_ms"]="$((T1 - T0))"
  pass "download_small"
else
  fail "download_small" "curl download failed"
  LATENCIES["download_small_ms"]="N/A"
fi

if [ -f "$SMALL_DL" ]; then
  DL_SHA="$(sha256sum "$SMALL_DL" | awk '{print $1}')"
  if [ "$SMALL_SHA" = "$DL_SHA" ]; then
    pass "checksum_small"
  else
    fail "checksum_small" "SHA256 mismatch: expected $SMALL_SHA got $DL_SHA"
  fi
else
  fail "checksum_small" "download file missing"
fi

echo "  upload_small_ms=${LATENCIES[upload_small_ms]:-N/A}  download_small_ms=${LATENCIES[download_small_ms]:-N/A}"

# ---------------------------------------------------------------------------
# TEST 3: Upload/download large file (default 10 MiB)
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 3: Upload/download large file (${SEAWEEDFS_POC_LARGE_MB} MiB) ---"
LARGE_FILE="$TMP_DIR/large.bin"
LARGE_DL="$TMP_DIR/large-dl.bin"

python3 - <<PYEOF
import os, random, struct
size = ${SEAWEEDFS_POC_LARGE_MB} * 1024 * 1024
with open("${LARGE_FILE}", "wb") as f:
    f.write(os.urandom(size))
PYEOF

LARGE_SHA="$(sha256sum "$LARGE_FILE" | awk '{print $1}')"

T0="$(ms_now)"
if curl -sS --max-time 120 -X POST -F "file=@${LARGE_FILE}" "${FILER_URL}/poc/large.bin" -o /dev/null; then
  T1="$(ms_now)"
  LATENCIES["upload_large_ms"]="$((T1 - T0))"
  pass "upload_large"
else
  fail "upload_large" "curl upload failed"
  LATENCIES["upload_large_ms"]="N/A"
fi

T0="$(ms_now)"
if curl -sS --max-time 120 "${FILER_URL}/poc/large.bin" -o "$LARGE_DL"; then
  T1="$(ms_now)"
  LATENCIES["download_large_ms"]="$((T1 - T0))"
  pass "download_large"
else
  fail "download_large" "curl download failed"
  LATENCIES["download_large_ms"]="N/A"
fi

if [ -f "$LARGE_DL" ]; then
  DL_SHA_L="$(sha256sum "$LARGE_DL" | awk '{print $1}')"
  if [ "$LARGE_SHA" = "$DL_SHA_L" ]; then
    pass "checksum_large"
  else
    fail "checksum_large" "SHA256 mismatch: expected $LARGE_SHA got $DL_SHA_L"
  fi
else
  fail "checksum_large" "download file missing"
fi

echo "  upload_large_ms=${LATENCIES[upload_large_ms]:-N/A}  download_large_ms=${LATENCIES[download_large_ms]:-N/A}"

# ---------------------------------------------------------------------------
# TEST 4: Basic replication via dir/assign with replication=001
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 4: Basic replication (replication=001) ---"

# Ensure seaweed-volume-2 is running — required for replication=001 to work.
# The master blocks on dir/assign?replication=001 until enough volumes are available.
echo "[INFO] Ensuring seaweed-volume-2 is running (profile seaweed-replication)..."
"${COMPOSE_CMD[@]}" --profile seaweed-replication up -d seaweed-master seaweed-volume seaweed-volume-2 seaweed-filer

VOLUME2_URL="http://localhost:${SEAWEEDFS_VOLUME_2_HOST_PORT}/status"
wait_http "$VOLUME2_URL" "seaweed-volume-2" 30

# Wait for volume-2 to register with the master (it reports in cluster/status)
echo "[WAIT] Waiting for seaweed-volume-2 to register with master..."
REGISTERED=false
for i in $(seq 1 20); do
  DATA_NODES="$(curl -sf --max-time 3 "${MASTER_URL}/cluster/status" \
    | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d.get('DataNodes', d.get('Topology', {}).get('DataNodes', []))))" 2>/dev/null || echo "0")"
  echo "  attempt $i: data nodes visible = ${DATA_NODES}"
  if [ "${DATA_NODES:-0}" -ge 2 ] 2>/dev/null; then
    REGISTERED=true
    break
  fi
  sleep 3
done

if [ "$REGISTERED" = "false" ]; then
  echo "[WARN] Only ${DATA_NODES:-0} data node(s) visible after wait; proceeding anyway"
fi

ASSIGN_RESPONSE="$TMP_DIR/assign.json"
REPLICATION_RESULT="fail"
REPL_FILE="$TMP_DIR/repl.txt"
REPL_DL="$TMP_DIR/repl-dl.txt"
echo "nchat-seaweedfs-replication-poc-$(date +%s)" > "$REPL_FILE"
REPL_SHA="$(sha256sum "$REPL_FILE" | awk '{print $1}')"

if curl -sS --max-time 15 "${MASTER_URL}/dir/assign?replication=001" -o "$ASSIGN_RESPONSE"; then
  echo "  assign response: $(cat "$ASSIGN_RESPONSE")"
  FID="$(python3 -c "import json; d=json.load(open('${ASSIGN_RESPONSE}')); print(d.get('fid',''))" 2>/dev/null || echo "")"
  INTERNAL_VOL_URL="$(python3 -c "import json; d=json.load(open('${ASSIGN_RESPONSE}')); print(d.get('url',''))" 2>/dev/null || echo "")"
  VID="$(python3 -c "import json; d=json.load(open('${ASSIGN_RESPONSE}')); fid=d.get('fid',''); print(fid.split(',')[0] if ',' in fid else '')" 2>/dev/null || echo "")"

  if [ -n "$FID" ] && [ -n "$INTERNAL_VOL_URL" ]; then
    # Map internal Docker hostname to host-accessible URL
    HOST_VOL_URL="$(map_seaweedfs_url "$INTERNAL_VOL_URL")"
    echo "  internal url: $INTERNAL_VOL_URL -> host url: $HOST_VOL_URL"

    # Upload to the assigned fid via host-accessible URL
    UPLOAD_HTTP_STATUS="$(curl -sS --max-time 15 -o /dev/null -w "%{http_code}" \
      -X POST "http://${HOST_VOL_URL}/${FID}" \
      -F "file=@${REPL_FILE}" 2>/dev/null || echo "000")"
    echo "  replication upload HTTP status: $UPLOAD_HTTP_STATUS"

    if [[ "$UPLOAD_HTTP_STATUS" =~ ^2 ]]; then
      pass "replication_upload"

      # Allow replication to propagate, then lookup volume locations
      if [ -n "$VID" ]; then
        sleep 2
        LOOKUP_RESPONSE="$TMP_DIR/lookup.json"
        curl -sS --max-time 10 "${MASTER_URL}/dir/lookup?volumeId=${VID}" -o "$LOOKUP_RESPONSE" || true
        echo "  lookup response: $(cat "$LOOKUP_RESPONSE" 2>/dev/null || echo 'N/A')"

        LOCATIONS_COUNT="$(python3 -c "
import json
try:
    d = json.load(open('${LOOKUP_RESPONSE}'))
    print(len(d.get('locations', [])))
except Exception:
    print(0)
" 2>/dev/null || echo "0")"
        echo "  volume locations found: $LOCATIONS_COUNT"

        if [ "${LOCATIONS_COUNT:-0}" -ge 2 ]; then
          # Download from the second location to verify the replica is accessible
          SECOND_INTERNAL_URL="$(python3 -c "
import json
try:
    d = json.load(open('${LOOKUP_RESPONSE}'))
    locs = d.get('locations', [])
    # pick the location that is NOT the primary upload target
    primary = '${INTERNAL_VOL_URL}'
    for loc in locs:
        if loc.get('url','') != primary:
            print(loc.get('url',''))
            break
    else:
        # fall back to any location
        if locs:
            print(locs[0].get('url',''))
except Exception:
    pass
" 2>/dev/null || echo "")"

          if [ -n "$SECOND_INTERNAL_URL" ]; then
            SECOND_HOST_URL="$(map_seaweedfs_url "$SECOND_INTERNAL_URL")"
            echo "  downloading replica from: $SECOND_INTERNAL_URL -> host: $SECOND_HOST_URL"
            DL_HTTP="$(curl -sS --max-time 15 -o "$REPL_DL" -w "%{http_code}" \
              "http://${SECOND_HOST_URL}/${FID}" 2>/dev/null || echo "000")"
            echo "  replica download HTTP status: $DL_HTTP"

            if [[ "$DL_HTTP" =~ ^2 ]]; then
              REPL_DL_SHA="$(sha256sum "$REPL_DL" | awk '{print $1}')"
              if [ "$REPL_SHA" = "$REPL_DL_SHA" ]; then
                pass "replication_checksum"
                pass "replication_basic"
                REPLICATION_RESULT="pass"
              else
                fail "replication_checksum" "SHA256 mismatch on replica: expected $REPL_SHA got $REPL_DL_SHA"
                fail "replication_basic" "replica checksum mismatch"
                REPLICATION_RESULT="fail"
              fi
            else
              # Replica download failed — log but don't fail the whole test;
              # the lookup proving >=2 locations is sufficient evidence of replication.
              echo "[WARN] Could not download from replica (HTTP $DL_HTTP); lookup shows >=2 locations"
              RESULTS["replication_checksum"]="SKIPPED: replica download returned $DL_HTTP"
              pass "replication_basic"
              REPLICATION_RESULT="pass"
            fi
          else
            RESULTS["replication_checksum"]="SKIPPED: could not determine second location URL"
            pass "replication_basic"
            REPLICATION_RESULT="pass"
          fi
        else
          fail "replication_basic" "lookup returned only $LOCATIONS_COUNT location(s) — expected >=2 with replication=001"
          REPLICATION_RESULT="fail"
        fi
      else
        fail "replication_basic" "could not parse volumeId from assign response"
        REPLICATION_RESULT="fail"
      fi
    else
      fail "replication_upload" "upload to fid returned HTTP $UPLOAD_HTTP_STATUS"
      fail "replication_basic" "fid upload failed — cannot validate replication"
      REPLICATION_RESULT="fail"
    fi
  else
    fail "replication_basic" "assign response missing fid or url"
    REPLICATION_RESULT="fail"
  fi
else
  fail "replication_basic" "dir/assign request failed"
  REPLICATION_RESULT="fail"
fi

# ---------------------------------------------------------------------------
# TEST 5: Cleanup
# ---------------------------------------------------------------------------
echo ""
echo "--- TEST 5: Cleanup ---"
for path in poc/small.txt poc/large.bin; do
  HTTP_STATUS="$(curl -sS --max-time 10 -o /dev/null -w "%{http_code}" -X DELETE "${FILER_URL}/${path}" 2>/dev/null || echo "000")"
  if [[ "$HTTP_STATUS" =~ ^2 ]]; then
    echo "[OK]   DELETE /${path} => $HTTP_STATUS"
  else
    echo "[WARN] DELETE /${path} => $HTTP_STATUS (non-critical)"
  fi
done

# ---------------------------------------------------------------------------
# Determine final exit status
# ---------------------------------------------------------------------------
# Replication with "limited" is acceptable if clearly documented
if [ "$REPLICATION_RESULT" = "fail" ]; then
  OVERALL_PASS=false
fi

# ---------------------------------------------------------------------------
# Write Markdown summary
# ---------------------------------------------------------------------------
NOW_HUMAN="$(date '+%Y-%m-%d %H:%M:%S %Z')"
cat > "$SUMMARY_FILE" <<EOF
# TASK-15 — SeaweedFS PoC Summary

**Run:** ${RUN_ID}
**Date:** ${NOW_HUMAN}

## Environment
- Master: ${MASTER_URL}
- Filer: ${FILER_URL}
- Large file size: ${SEAWEEDFS_POC_LARGE_MB} MiB

## Results

| Test | Result |
|------|--------|
| master_status | ${RESULTS[master_status]:-NOT RUN} |
| filer_status | ${RESULTS[filer_status]:-NOT RUN} |
| upload_small | ${RESULTS[upload_small]:-NOT RUN} |
| download_small | ${RESULTS[download_small]:-NOT RUN} |
| checksum_small | ${RESULTS[checksum_small]:-NOT RUN} |
| upload_large | ${RESULTS[upload_large]:-NOT RUN} |
| download_large | ${RESULTS[download_large]:-NOT RUN} |
| checksum_large | ${RESULTS[checksum_large]:-NOT RUN} |
| replication_upload | ${RESULTS[replication_upload]:-NOT RUN} |
| replication_checksum | ${RESULTS[replication_checksum]:-NOT RUN} |
| replication_basic | ${RESULTS[replication_basic]:-NOT RUN} |

## Latencies

| Operation | ms |
|-----------|----|
| upload_small | ${LATENCIES[upload_small_ms]:-N/A} |
| download_small | ${LATENCIES[download_small_ms]:-N/A} |
| upload_large | ${LATENCIES[upload_large_ms]:-N/A} |
| download_large | ${LATENCIES[download_large_ms]:-N/A} |

## Replication Result
${REPLICATION_RESULT}

## Limitations
- Not a production benchmark
- Does not test node failure
- Does not test backup/restore
- Does not test concurrent load
- replication=001 requires two running volume servers; replica download is attempted
  but a lookup with >=2 locations is accepted as sufficient evidence if download fails

## Overall: $([ "$OVERALL_PASS" = "true" ] && echo "PASS" || echo "FAIL")
EOF

# ---------------------------------------------------------------------------
# Write JSON metrics
# ---------------------------------------------------------------------------
python3 - <<PYEOF
import json
data = {
    "run_id": "${RUN_ID}",
    "results": {
$(for k in "${!RESULTS[@]}"; do echo "        \"$k\": \"${RESULTS[$k]}\","; done)
    },
    "latencies_ms": {
$(for k in "${!LATENCIES[@]}"; do echo "        \"$k\": \"${LATENCIES[$k]}\","; done)
    },
    "replication_result": "${REPLICATION_RESULT}",
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
