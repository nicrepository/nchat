#!/usr/bin/env bash
# Fixture tests for the capacity preflight (issue #626).
#
# The preflight replaced a `kubectl apply --dry-run=server` that could only ever
# pass, so the thing worth testing is that it can now fail — and that it says
# INCONCLUSIVE rather than OK when the cluster did not give it the numbers.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
PREFLIGHT="$ROOT_DIR/scripts/deploy/nchat-prod/candidate-capacity.py"
FIXTURES="$ROOT_DIR/scripts/ci/testdata/nchat-prod"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nchat-capacity-tests.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

FAILURES=0

# Exit codes, from the preflight's contract.
SUFFICIENT=0
INSUFFICIENT=1
INCONCLUSIVE=2
BAD_INPUT=3

check() {
  local name="$1" expected="$2"; shift 2
  local status=0
  "$@" >"$WORK/out.txt" 2>"$WORK/err.txt" || status=$?
  if [[ "$status" == "$expected" ]]; then
    echo "  [OK]   $name"
    return 0
  fi
  echo "  [FAIL] $name: exit $status, expected $expected" >&2
  sed 's/^/         /' "$WORK/out.txt" "$WORK/err.txt" >&2
  FAILURES=$((FAILURES + 1))
}

preflight() {
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" \
    --cluster-pods-file "$FIXTURES/cluster-pod-slots.txt" "$@"
}

# The fixture asks for 1250m, 1344Mi and 3 pods.
QUOTA_OK=(--quota-hard-cpu 4 --quota-used-cpu 1 --quota-hard-memory 8Gi
  --quota-used-memory 1Gi --quota-hard-pods 20 --quota-used-pods 5 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi
  --quota-hard-storage 50Gi --quota-used-storage 1Gi)

echo "=== capacity preflight ==="
echo
echo "--- unit handling ---"
python3 - "$PREFLIGHT" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("preflight", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
cases = [
    (module.parse_cpu, "250m", 250), (module.parse_cpu, "2", 2000),
    (module.parse_cpu, "0.5", 500), (module.parse_cpu, "1500m", 1500),
    (module.parse_memory, "128Mi", 134217728), (module.parse_memory, "1Gi", 1073741824),
    (module.parse_memory, "64Ki", 65536), (module.parse_memory, "1M", 1000000),
    (module.parse_memory, "1024", 1024),
]
bad = [(fn, raw, want, fn(raw)) for fn, raw, want in cases if fn(raw) != want]
for fn, raw, want, got in bad:
    print(f"  [FAIL] {fn.__name__}({raw!r}) = {got}, expected {want}", file=sys.stderr)
print("  [OK]   millicores and Ki/Mi/Gi/K/M suffixes convert exactly" if not bad else "", end="")
sys.exit(1 if bad else 0)
PY
[[ "$?" -eq 0 ]] || FAILURES=$((FAILURES + 1))
echo
echo

echo "--- quota dimensions ---"
check "sufficient capacity passes" "$SUFFICIENT" preflight "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') --cluster-requests-file <(printf 'Running|node-a|500m 2Gi 64Mi\n')
check "insufficient cpu quota fails" "$INSUFFICIENT" preflight \
  --quota-hard-cpu 2 --quota-used-cpu 1500m --quota-hard-memory 8Gi --quota-used-memory 1Gi \
  --quota-hard-pods 20 --quota-used-pods 5 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') --cluster-requests-file <(printf 'Running|node-a|0 0 0\n')
check "insufficient memory quota fails" "$INSUFFICIENT" preflight \
  --quota-hard-cpu 4 --quota-used-cpu 1 --quota-hard-memory 2Gi --quota-used-memory 1Gi \
  --quota-hard-pods 20 --quota-used-pods 5 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') --cluster-requests-file <(printf 'Running|node-a|0 0 0\n')
check "insufficient pod quota fails" "$INSUFFICIENT" preflight \
  --quota-hard-cpu 4 --quota-used-cpu 1 --quota-hard-memory 8Gi --quota-used-memory 1Gi \
  --quota-hard-pods 6 --quota-used-pods 5 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') --cluster-requests-file <(printf 'Running|node-a|0 0 0\n')

echo
echo "--- node capacity ---"
check "a cluster with no room left fails" "$INSUFFICIENT" preflight "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '2 4Gi 200Gi 110\n') --cluster-requests-file <(printf 'Running|node-a|1900m 3900Mi 64Mi\n')
check "containers without declared requests reserve nothing" "$SUFFICIENT" preflight "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') --cluster-requests-file <(printf 'Running|node-a| \nRunning|node-a|100m\nRunning|node-a| 128Mi\n')

echo
echo "--- pods already bound to a node hold capacity ---"
# The cluster has 4 CPU / 8Gi allocatable; the candidate needs 1250m / 1344Mi.
#
# A real file, not a process substitution: an array assignment opens the
# substitution's descriptor immediately and it is gone by the time the array is
# expanded, which reads back as "the cluster reported nothing".
printf '4 8Gi 200Gi 40\n' >"$WORK/node-allocatable.txt"
NODES=(--node-allocatable-file "$WORK/node-allocatable.txt")

check "the candidate fits alongside the running pods" "$SUFFICIENT" preflight "${QUOTA_OK[@]}" \
  "${NODES[@]}" --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

# The same cluster plus one Pending pod the scheduler has already bound. Its
# requests are reserved on node-a even though it is not Running yet, so the
# candidate no longer fits -- and counting only Running pods would have admitted
# it and then let the kubelet evict something.
check "a scheduled Pending pod is counted and the candidate no longer fits" "$INSUFFICIENT" \
  preflight "${QUOTA_OK[@]}" "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-with-pending.txt"

# The same pod with no nodeName has been scheduled nowhere and reserves nothing.
check "an unscheduled Pending pod reserves nothing" "$SUFFICIENT" preflight "${QUOTA_OK[@]}" \
  "${NODES[@]}" --cluster-requests-file "$FIXTURES/cluster-pods-unschedulable.txt"

# Succeeded and Failed pods stay listed by the API but have released their
# reservation.
check "terminal pods on a node do not hold capacity" "$SUFFICIENT" preflight "${QUOTA_OK[@]}" \
  "${NODES[@]}" --cluster-requests-file "$FIXTURES/cluster-pods-terminal.txt"

echo
echo "--- first deploy vs redeploy of an existing slot ---"
# The fixture wants 1250m / 1344Mi / 3 pods when the slot does not exist. Rolling
# it over a slot that already runs it costs only the surge: one extra pod per
# workload, at the NEW per-pod requests.
printf 'alpha-blue|2|100m|128Mi|64Mi\nbeta-blue|1|1|1Gi|\nbeta-blue|1|50m|64Mi|\n' >"$WORK/current-same.txt"

check "a slot that does not exist costs a whole slot" "$INSUFFICIENT" preflight \
  --quota-hard-pods 2 --quota-used-pods 0 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

check "rolling over an existing slot costs only the surge" "$SUFFICIENT" preflight \
  --quota-hard-pods 2 --quota-used-pods 0 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi --quota-hard-cpu 4 --quota-used-cpu 1 \
  --quota-hard-memory 8Gi --quota-used-memory 1Gi "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --current-slot-file "$WORK/current-same.txt"

check "an existing slot is not charged for a second full copy" "$SUFFICIENT" preflight \
  --quota-hard-cpu 2 --quota-used-cpu 500m --quota-hard-memory 4Gi --quota-used-memory 1Gi \
  --quota-hard-pods 10 --quota-used-pods 3 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --current-slot-file "$WORK/current-same.txt"

# Room for both slots as they stand, but not for the surge pods on top.
check "a cluster with no headroom for the surge is refused" "$INSUFFICIENT" preflight \
  --quota-hard-cpu 4 --quota-used-cpu 3 --quota-hard-memory 8Gi --quota-used-memory 1Gi \
  --quota-hard-pods 10 --quota-used-pods 3 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --current-slot-file "$WORK/current-same.txt"

# Scaling up: the extra replicas are additional demand on top of the surge.
printf 'alpha-blue|1|100m|128Mi|64Mi\nbeta-blue|1|1|1Gi|\nbeta-blue|1|50m|64Mi|\n' >"$WORK/current-fewer.txt"
check "extra replicas in the new release are counted" "$INSUFFICIENT" preflight \
  --quota-hard-pods 2 --quota-used-pods 0 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --current-slot-file "$WORK/current-fewer.txt"

# Scaling down: the cluster already holds more than the rollout's peak, so the
# release adds nothing. That is a pass, not a malformed manifest.
printf 'alpha-blue|5|100m|128Mi|64Mi\nbeta-blue|5|1|1Gi|\nbeta-blue|5|50m|64Mi|\n' >"$WORK/current-more.txt"
check "a release that scales a slot down adds no demand" "$SUFFICIENT" preflight \
  --quota-hard-pods 1 --quota-used-pods 0 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi --quota-hard-cpu 4 --quota-used-cpu 1 \
  --quota-hard-memory 8Gi --quota-used-memory 1Gi "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --current-slot-file "$WORK/current-more.txt"

# The regression the fifth review found, end to end: a cluster sized for the old
# model's answer (1000m) but not for the real peak (1900m) must now be refused.
printf 'alpha-blue|2|100m|128Mi|64Mi\n' >"$WORK/current-cheap.txt"
cat >"$WORK/candidate-expensive.yaml" <<'MANIFEST'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: alpha-blue
spec:
  replicas: 2
  strategy:
    rollingUpdate:
      maxSurge: 1
  template:
    spec:
      containers:
        - name: alpha
          resources:
            requests:
              cpu: 1000m
              memory: 128Mi
MANIFEST
check "a release that raises CPU is refused when only the old model would fit" \
  "$INSUFFICIENT" python3 "$PREFLIGHT" --manifest "$WORK/candidate-expensive.yaml" \
  --quota-hard-cpu 4 --quota-used-cpu 2800m --quota-hard-memory 8Gi --quota-used-memory 1Gi \
  --quota-hard-pods 20 --quota-used-pods 5 \
  --quota-hard-storage 50Gi --quota-used-storage 1Gi "${NODES[@]}" \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --current-slot-file "$WORK/current-cheap.txt"

echo
echo "--- maxSurge parsing ---"
if python3 "$ROOT_DIR/scripts/ci/testdata/nchat-prod/check_max_surge.py" "$PREFLIGHT"; then
  echo "  [OK]   maxSurge is read as an integer or a percentage"
else
  FAILURES=$((FAILURES + 1))
fi

echo
echo "--- rollout peak is costed in resources, not pod counts ---"
if python3 "$ROOT_DIR/scripts/ci/testdata/nchat-prod/check_rollout_peak.py" "$PREFLIGHT"; then
  echo "  [OK]   raised requests, lowered requests and replica changes are all priced correctly"
else
  FAILURES=$((FAILURES + 1))
fi

echo
echo "--- ephemeral-storage ---"
# The fixture's alpha container asks for 64Mi of ephemeral-storage per pod, two
# replicas. file-service does the same in production, and the preflight used to
# ignore the dimension entirely.

check "enough ephemeral-storage passes" "$SUFFICIENT" preflight "${QUOTA_OK[@]}" \
  "${NODES[@]}" --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

check "a node without room for ephemeral-storage is refused" "$INSUFFICIENT" preflight \
  "${QUOTA_OK[@]}" --node-allocatable-file <(printf '8 32Gi 100Mi 110\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

check "an ephemeral-storage quota that cannot fit the candidate is refused" "$INSUFFICIENT" \
  preflight --quota-hard-cpu 4 --quota-used-cpu 1 --quota-hard-memory 8Gi \
  --quota-used-memory 1Gi --quota-hard-pods 20 --quota-used-pods 5 \
  --quota-hard-storage 100Mi --quota-used-storage 90Mi \
  "${NODES[@]}" --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

# An absent quota for this dimension is INCONCLUSIVE, never an implicit pass:
# production must declare requests.ephemeral-storage for the gate to be able to
# answer.
check "an absent ephemeral-storage quota is inconclusive" "$INCONCLUSIVE" preflight \
  --quota-hard-cpu 4 --quota-used-cpu 1 --quota-hard-memory 8Gi --quota-used-memory 1Gi \
  --quota-hard-pods 20 --quota-used-pods 5 \
  "${NODES[@]}" --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

# Nodes that do not report allocatable ephemeral-storage cannot be judged.
check "nodes without an ephemeral-storage figure are inconclusive" "$INCONCLUSIVE" preflight \
  "${QUOTA_OK[@]}" --node-allocatable-file <(printf '8 32Gi\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

# A redeploy is charged the rollout peak here too, not a whole second slot.
check "a redeploy is charged only the surge of ephemeral-storage" "$SUFFICIENT" preflight \
  --quota-hard-cpu 4 --quota-used-cpu 1 --quota-hard-memory 8Gi --quota-used-memory 1Gi \
  --quota-hard-pods 20 --quota-used-pods 5 \
  --quota-hard-storage 200Mi --quota-used-storage 64Mi \
  "${NODES[@]}" --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --current-slot-file "$WORK/current-same.txt"

echo
echo "--- ephemeral-storage arithmetic ---"
if python3 "$ROOT_DIR/scripts/ci/testdata/nchat-prod/check_storage_model.py" "$PREFLIGHT"; then
  echo "  [OK]   storage follows the same current/desired/surge model as CPU"
else
  FAILURES=$((FAILURES + 1))
fi

echo
echo "--- cluster pod slots ---"
# The namespace quota is not the only ceiling on pods: the kubelet caps each node
# at status.allocatable.pods, so a cluster can be out of slots while the quota
# still has room. Both conditions have to pass.

check "quota and cluster both allow the candidate" "$SUFFICIENT" preflight "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

# Five slots on the nodes, three already taken, candidate needs three.
check "quota allows but the nodes are out of pod slots" "$INSUFFICIENT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '5\n' | sed 's/^/8 32Gi 200Gi /') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --cluster-pods-file "$FIXTURES/cluster-pod-slots-full.txt"

check "an insufficient quota fails even when the cluster has slots" "$INSUFFICIENT" preflight \
  --quota-hard-cpu 4 --quota-used-cpu 1 --quota-hard-memory 8Gi --quota-used-memory 1Gi \
  --quota-hard-pods 6 --quota-used-pods 5 --quota-hard-storage 50Gi --quota-used-storage 1Gi \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

# Four slots, three taken (two Running plus one Pending already bound): the
# candidate's three do not fit.
check "a Pending pod bound to a node holds its slot" "$INSUFFICIENT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 4\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --cluster-pods-file "$FIXTURES/cluster-pod-slots.txt"

# The same four slots, but the two Pending pods are unscheduled and hold none.
check "a Pending pod with no node holds no slot" "$SUFFICIENT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 4\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --cluster-pods-file "$FIXTURES/cluster-pod-slots-unscheduled.txt"

# Succeeded and Failed pods have released their slots.
check "terminal pods hold no slot" "$SUFFICIENT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 4\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --cluster-pods-file "$FIXTURES/cluster-pod-slots-terminal.txt"

check "nodes that do not report allocatable.pods are inconclusive" "$INCONCLUSIVE" preflight \
  "${QUOTA_OK[@]}" --node-allocatable-file <(printf '8 32Gi 200Gi\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt"

check "an unreadable pod listing is inconclusive" "$INCONCLUSIVE" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --cluster-pods-file "$WORK/no-such-pods.txt"

# A redeploy adds only the surge, so three slots are ample.
check "a redeploy is charged only its surge of pod slots" "$SUFFICIENT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" "${QUOTA_OK[@]}" \
  --node-allocatable-file <(printf '8 32Gi 200Gi 6\n') \
  --cluster-requests-file "$FIXTURES/cluster-pods-scheduled.txt" \
  --cluster-pods-file "$FIXTURES/cluster-pod-slots.txt" \
  --current-slot-file "$WORK/current-same.txt"

echo
echo "--- missing data is never a pass ---"
check "an absent quota is inconclusive" "$INCONCLUSIVE" preflight \
  --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') --cluster-requests-file <(printf 'Running|node-a|0 0 0\n')
check "unavailable node data is inconclusive" "$INCONCLUSIVE" preflight "${QUOTA_OK[@]}"
check "an unreadable metrics file is inconclusive, not fatal" "$INCONCLUSIVE" preflight "${QUOTA_OK[@]}" \
  --node-allocatable-file "$WORK/does-not-exist" --cluster-requests-file "$WORK/also-missing"
check "a real shortfall outranks an inconclusive dimension" "$INSUFFICIENT" preflight \
  --quota-hard-cpu 2 --quota-used-cpu 1900m

echo
echo "--- unusable input ---"
check "a manifest with no Deployment is refused" "$BAD_INPUT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-no-deployment.yaml" "${QUOTA_OK[@]}"
check "a malformed quantity is refused, not silently skipped" "$BAD_INPUT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-malformed.yaml" "${QUOTA_OK[@]}"
check "an absent manifest is refused" "$BAD_INPUT" \
  python3 "$PREFLIGHT" --manifest "$WORK/nope.yaml" "${QUOTA_OK[@]}"

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "capacity preflight tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "capacity preflight tests passed."
