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

fail_evidence() {
  echo "  [FAIL] $*" >&2
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
cpu, memory = module.parse_cpu, module.parse_memory
cases = [
    (cpu, "250m", 250), (cpu, "2", 2000),
    (cpu, "0.5", 500), (cpu, "1500m", 1500),
    (memory, "128Mi", 134217728), (memory, "1Gi", 1073741824),
    (memory, "64Ki", 65536), (memory, "1M", 1000000),
    (memory, "1024", 1024),
    # The whole decimal SI range Kubernetes defines, including the three
    # sub-unit suffixes and the four large ones the old parser did not know.
    # 'm' is the one that mattered in production: 400m is an ordinary CPU
    # request, and "quantity is not a number" stopped a deploy over it.
    (memory, "1n", 1), (memory, "1u", 1), (memory, "400m", 1),
    (memory, "1k", 1000), (memory, "1K", 1000), (memory, "1G", 10**9),
    (memory, "1T", 10**12), (memory, "1P", 10**15), (memory, "1E", 10**18),
    (cpu, "400m", 400), (cpu, "1e0", 1000), (cpu, "1000m", 1000), (cpu, "1", 1000),
    # Binary SI to the top of the range. 1Ei is 2^60 bytes, which an int64 holds
    # and a float does not represent exactly.
    (memory, "1Mi", 1048576), (memory, "1Ti", 2**40),
    (memory, "1Pi", 2**50), (memory, "1Ei", 2**60),
    # Exponents, in either case, and never combined with a suffix.
    (memory, "129e6", 129000000), (memory, "129E6", 129000000),
    (memory, "1e3", 1000), (memory, "1.5e3", 1500),
    # Bytes are whole, and a fraction of one costs a whole one -- the rounding
    # Kubernetes' own Value() does. Truncating gave 2.5 bytes as 2 and quietly
    # under-counted every fractional request.
    (memory, "2.5", 3), (memory, "0.5", 1), (memory, "2.5Gi", 2684354560),
    # A positive quantity below one unit is one unit, however far below. These
    # are ordinary quantities, and a guard on the size of the exponent refused
    # them for being small -- the same rounding mistake as 2.5 -> 2, in the
    # direction that reads as less demand than there is.
    (memory, "1e-89", 1), (memory, "1e-100", 1),
    (memory, "1e-999999999999999999", 1), (cpu, "1e-89", 1),
    # Zero is the one quantity that is not one unit, at any exponent.
    (memory, "0", 0), (memory, "0e999999999999999999", 0), (cpu, "0", 0),
]
bad = [(fn, raw, want, fn(raw)) for fn, raw, want in cases if fn(raw) != want]
for fn, raw, want, got in bad:
    print(f"  [FAIL] {fn.__name__}({raw!r}) = {got}, expected {want}", file=sys.stderr)

# Fail-closed is the other half: a quantity outside the grammar must not be
# absorbed as a zero. '1e3Ki' is here because a suffix is one of the three forms
# and never a combination, which an optional-groups regex would have allowed.
refused = ["-1", "-1m", "-1Gi", "nan", "inf", "Infinity", "not-a-quantity",
           "1e309", "1e3Ki", "1Xi", "1.2.3", "", "0x10",
           # Short to write and impossible to write out: Decimal holds this in
           # three fields, and expanding it would build an integer with a
           # quintillion digits. Refused on its exponent, before anything tries.
           # Its negative-exponent twin is deliberately NOT here: that one is a
           # valid, very small quantity, and it is in the table above.
           "1e999999999999999999"]
for raw in refused:
    try:
        memory(raw)
    except module.InvalidQuantity:
        continue
    print(f"  [FAIL] parse_memory({raw!r}) was accepted", file=sys.stderr)
    bad.append(raw)
# int64 is the size policy and it is applied to the finished value; the exponent
# guard only refuses what cannot be expanded at all. So the largest quantity
# Kubernetes can hold is accepted, the one above it is refused for being too
# large, and everything between there and the guard is refused by that same
# exact check -- the guard acts only past 10^88, where nothing legitimate is.
boundary = [
    ("9223372036854775807", None),            # 2^63-1
    ("9223372036854775808", "too large"),     # one more: the exact check
    ("1e88", "too large"),                    # far out, still the exact check
    ("1e89", "exponent"),                     # only here does the guard act
    ("1e-89", None),                          # and it never acts downward
]
for raw, expected in boundary:
    try:
        memory(raw)
        got = None
    except module.InvalidQuantity as error:
        got = str(error)
    if (expected is None) != (got is None) or (expected and expected not in got):
        print(f"  [FAIL] parse_memory({raw!r}): {got!r} does not match {expected!r}",
              file=sys.stderr)
        bad.append(raw)

print("  [OK]   the Kubernetes quantity grammar converts exactly and fails closed"
      if not bad else "", end="")
sys.exit(1 if bad else 0)
PY
[[ "$?" -eq 0 ]] || FAILURES=$((FAILURES + 1))
echo
echo

echo "--- extreme exponents, at the two costs they actually have ---"
# An exponent is eight bytes of text either way, and the two directions are not
# the same problem. Upward, the digits have to be written out: 1e999999999999999999
# asks Python for an integer with a quintillion of them and the process stops
# answering. Downward, nothing needs writing out -- a positive value below one
# unit is one unit -- so it is an ordinary quantity and gets an ordinary verdict.
#
# `timeout` bounds these cases; it does not decide them. Exit 124 is the failure
# they exist to catch, so each demands a specific exit code of its own.
quota_with_cpu() {
  timeout 5 python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" \
    --quota-hard-cpu "$1" --quota-used-cpu 0 \
    --quota-hard-memory 8Gi --quota-used-memory 1Gi \
    --quota-hard-pods 20 --quota-used-pods 5 \
    --quota-hard-storage 50Gi --quota-used-storage 1Gi \
    --node-allocatable-file "$WORK/nodes-ok.txt" \
    --cluster-requests-file "$WORK/requests-ok.txt"
}
printf '8 32Gi 200Gi 110\n' >"$WORK/nodes-ok.txt"
printf 'Running|node-a|500m 2Gi 64Mi\n' >"$WORK/requests-ok.txt"

check "an exponent too large to expand is refused rather than chewed on" \
  "$BAD_INPUT" quota_with_cpu 1e999999999999999999

# A quota of one millicore is read, and then it is simply too small for the
# candidate: INSUFFICIENT, not BAD INPUT. The quantity was understood.
check "an exponent far below the point is read, not refused" \
  "$INSUFFICIENT" quota_with_cpu 1e-999999999999999999

# And a small-but-sane quota behaves like any other: 1e-89 cores is one
# millicore, which still cannot hold the candidate.
check "a very small quota is one unit, and evaluated as one" \
  "$INSUFFICIENT" quota_with_cpu 1e-89

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

# A request is an amount of a resource, and there is no negative amount of one.
# It is not an odd input either: it subtracts from the demand, so a candidate
# carrying one reported LESS demand than it has -- the manifest below used to
# print "cpu=-18950m" and pass.
cat >"$WORK/candidate-negative.yaml" <<'MANIFEST'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: alpha-blue
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: alpha
          resources:
            requests:
              cpu: -1900m
              memory: 128Mi
MANIFEST
check "a manifest asking for a negative request is refused" "$BAD_INPUT" \
  python3 "$PREFLIGHT" --manifest "$WORK/candidate-negative.yaml" "${QUOTA_OK[@]}"
grep -q "quantity is negative" "$WORK/err.txt" ||
  { echo "  [FAIL] did not name the negative quantity" >&2; FAILURES=$((FAILURES + 1)); }
! grep -q "Traceback" "$WORK/err.txt" ||
  { echo "  [FAIL] a negative request left by way of a traceback" >&2; FAILURES=$((FAILURES + 1)); }

echo
echo "=== capacity evidence ==="
#
# The production deploy identity is namespaced: it cannot read Nodes and it
# cannot list Pods across namespaces, and the node these workloads share is
# 94% committed by other namespaces, so a namespace-only view would report room
# that is not there. Collection therefore happens under a trusted read-only
# context and the deployer evaluates the snapshot.
#
# The claim under test is the whole point of the split: with both cluster-wide
# reads refused, the gate still reaches a verdict from evidence — and every way
# the evidence can be wrong stops the deploy instead of passing it.

FAKE_BIN="$(mktemp -d "${TMPDIR:-/tmp}/nchat-capacity-fakebin.XXXXXX")"
trap 'rm -rf "$WORK" "$FAKE_BIN"' EXIT
cp "$FIXTURES/fake-kubectl" "$FAKE_BIN/kubectl"
chmod +x "$FAKE_BIN/kubectl"
PATH="$FAKE_BIN:$PATH"
export PATH

# The gate stops the operation with 1, the same status the evaluator uses for a
# proven shortfall. Named apart because they are different claims: one is "the
# cluster cannot hold this", the other "this snapshot may not be believed".
REFUSED=1

LIB="$ROOT_DIR/scripts/deploy/nchat-prod/lib.sh"
COLLECTOR="$ROOT_DIR/scripts/deploy/nchat-prod/capacity-evidence.sh"
STATE="$WORK/cluster"
mkdir -p "$STATE/quota"
printf 'read-only-admin' >"$STATE/context"
printf 'nchat-prod' >"$STATE/namespace"
printf '8 32Gi 200Gi 110\n' >"$STATE/node-allocatable"
printf 'Running|node-a|500m 2Gi 64Mi\n' >"$STATE/cluster-pods"
printf 'Running|node-a\n' >"$STATE/cluster-pod-slots"
printf '16' >"$STATE/quota/hard-cpu"; printf '1' >"$STATE/quota/used-cpu"
printf '32Gi' >"$STATE/quota/hard-memory"; printf '2Gi' >"$STATE/quota/used-memory"
printf '80' >"$STATE/quota/hard-pods"; printf '10' >"$STATE/quota/used-pods"
printf '500Gi' >"$STATE/quota/hard-storage"; printf '10Gi' >"$STATE/quota/used-storage"
# The slot the candidate replaces, read live from the namespace on every run.
printf 'alpha-blue|2|100m|128Mi|64Mi\nbeta-blue|1|1|1Gi|\n' >"$STATE/current-slot"

collect() {
  FAKE_STATE_DIR="$STATE" bash "$COLLECTOR" "$@"
}

# Runs the shell half of the gate exactly as deploy.sh does, against the fake.
# NCHAT_PROD_CAPACITY_EVIDENCE_DIR empty is live mode, as in production.
gate() {
  gate_within 60 "$@"
}

# The gate, with a ceiling on how long it may take. The ceiling is not the
# assertion -- every caller still asserts an exit code -- it only turns "this
# never returned" into a failure instead of a suite that stops responding.
# 60 seconds for the ordinary cases, which take milliseconds; the case that
# exists to prove a quantity is refused rather than expanded asks for less.
gate_within() {
  local seconds="$1" evidence="$2" workdir
  workdir="$(mktemp -d "$WORK/gate.XXXXXX")"
  FAKE_STATE_DIR="$STATE" NCHAT_PROD_CAPACITY_EVIDENCE_DIR="$evidence" \
    timeout "$seconds" \
    bash -c 'set -Eeuo pipefail; source "$1"; run_capacity_preflight "$2" "$3" blue' \
    _ "$LIB" "$FIXTURES/candidate-small.yaml" "$workdir"
}

echo
echo "--- collecting ---"
EVIDENCE="$WORK/evidence"
check "a trusted context publishes a complete snapshot" "$SUFFICIENT" collect "$EVIDENCE"
for name in node-allocatable.txt cluster-requests.txt cluster-pods.txt sha256sums.txt metadata; do
  [[ -s "$EVIDENCE/$name" ]] || { echo "  [FAIL] evidence is missing $name" >&2; FAILURES=$((FAILURES + 1)); }
done
grep -qx 'schema=nchat-prod-capacity-evidence/v1' "$EVIDENCE/metadata" ||
  { echo "  [FAIL] evidence metadata declares no schema" >&2; FAILURES=$((FAILURES + 1)); }

# A collection that could not read the cluster must leave nothing a deployer
# would accept. Publishing the files and failing afterwards would leave a
# directory that looks like a snapshot of an empty cluster.
printf '1' >"$STATE/deny-cluster-wide"
check "a refused collection publishes nothing" "$REFUSED" collect "$WORK/denied"
[[ ! -e "$WORK/denied/metadata" ]] ||
  { echo "  [FAIL] a failed collection still published metadata" >&2; FAILURES=$((FAILURES + 1)); }
rm -f "$STATE/deny-cluster-wide"

# An empty answer is a failed read, not an empty cluster: there is always at
# least one node and at least one Pod.
: >"$STATE/node-allocatable"
check "a collection with an empty input publishes nothing" "$REFUSED" collect "$WORK/empty"
[[ ! -e "$WORK/empty/metadata" ]] ||
  { echo "  [FAIL] an incomplete collection still published metadata" >&2; FAILURES=$((FAILURES + 1)); }
printf '8 32Gi 200Gi 110\n' >"$STATE/node-allocatable"

# The metadata record is built from two commands, and a command substitution
# inside printf hands its exit status to printf. A kubectl that could not reach
# the API therefore wrote an empty collector_context and returned success, and
# the collector carried on to publish a snapshot of an unidentified cluster.
echo
echo "--- metadata the collector could not gather is not published ---"
rm -rf "$WORK/evidence-nocontext"
printf '1' >"$STATE/context-fails"
check "a collector that cannot read its own context fails" "$REFUSED" \
  collect "$WORK/evidence-nocontext"
[[ ! -e "$WORK/evidence-nocontext/metadata" ]] ||
  { echo "  [FAIL] published metadata without a kube context" >&2; FAILURES=$((FAILURES + 1)); }
grep -q "kube context" "$WORK/err.txt" ||
  { echo "  [FAIL] did not name the kube context as the reason" >&2; FAILURES=$((FAILURES + 1)); }

# And it must not leave a previous publication standing either: the refresh was
# attempted, so what was there is withdrawn whatever the reason for the failure.
collect "$WORK/evidence-nocontext" >/dev/null 2>&1 && :
rm -f "$STATE/context-fails"
check "a good collection into the same directory recovers it" "$SUFFICIENT" \
  collect "$WORK/evidence-nocontext"
printf '1' >"$STATE/context-fails"
check "and a context failure then withdraws it again" "$REFUSED" \
  collect "$WORK/evidence-nocontext"
check "leaving the gate unable to read it" "$REFUSED" gate "$WORK/evidence-nocontext"
rm -f "$STATE/context-fails"

echo
echo "--- a refresh that fails withdraws what was published there ---"
#
# The dangerous sequence is not a failed collection. It is a failed collection
# into a directory that already holds a good one: the collector reported an
# error, the operator saw it, and the deploy would still have loaded yesterday's
# snapshot of the cluster with nothing to say so.
#
# The loader is exercised, not the files. "metadata is gone" is an
# implementation detail; "the gate will not read this" is the claim.
REFRESH="$WORK/evidence-refresh"

check "a first collection publishes evidence" "$SUFFICIENT" collect "$REFRESH"
check "and the gate reads it" "$SUFFICIENT" gate "$REFRESH"

printf '1' >"$STATE/deny-cluster-wide"
check "re-collecting into it fails when the cluster cannot be read" "$REFUSED" collect "$REFRESH"
check "and what was published there is no longer accepted" "$REFUSED" gate "$REFRESH"
rm -f "$STATE/deny-cluster-wide"

# And the invalidation is not a one-way door: the destination is reusable the
# moment a collection succeeds again.
check "a successful recollection makes it usable once more" "$SUFFICIENT" collect "$REFRESH"
check "and the gate reads it again" "$SUFFICIENT" gate "$REFRESH"

echo
echo "--- the gate under the namespaced deploy identity ---"
# From here on the fake refuses `get nodes` and `get pods --all-namespaces`,
# exactly as the API refuses nchat-prod-deployer.
printf '1' >"$STATE/deny-cluster-wide"
rm -f "$STATE/cluster-wide-log"

check "live mode cannot reach a verdict for this identity" "$INCONCLUSIVE" gate ""
[[ -f "$STATE/cluster-wide-log" ]] ||
  { echo "  [FAIL] live mode did not even attempt the cluster-wide reads" >&2; FAILURES=$((FAILURES + 1)); }
rm -f "$STATE/cluster-wide-log"

check "valid evidence reaches a verdict" "$SUFFICIENT" gate "$EVIDENCE"

[[ ! -f "$STATE/cluster-wide-log" ]] ||
  fail_evidence "evidence mode read a cluster-wide resource: $(tr '\n' ';' <"$STATE/cluster-wide-log")"
# The slot's own workloads are namespaced, so they stay live: the run must have
# priced this as a redeploy, not as a first deploy of the slot.
grep -q "redeploy over 2 existing workload(s)" "$WORK/out.txt" ||
  fail_evidence "the current slot was not read live from the namespace"

echo
echo "--- evidence that must not be trusted ---"
# A private copy of the valid evidence, for a case to spoil in exactly one way.
mutate() {
  rm -rf "$WORK/mutated"
  cp -r "$EVIDENCE" "$WORK/mutated"
}

check "absent evidence stops the gate" "$REFUSED" gate "$WORK/no-such-evidence"

mutate; rm "$WORK/mutated/cluster-pods.txt"
check "incomplete evidence stops the gate" "$REFUSED" gate "$WORK/mutated"

mutate; printf 'schema=nchat-prod-capacity-evidence/v99\n' >"$WORK/mutated/metadata"
check "an unknown schema stops the gate" "$REFUSED" gate "$WORK/mutated"

mutate; : >"$WORK/mutated/metadata"
check "evidence with no metadata at all stops the gate" "$REFUSED" gate "$WORK/mutated"

# Dated outside the freshness window. No sleep: the stamp is simply written old.
mutate
sed -i "s/^collected_at=.*/collected_at=$(date -u -d "@$(( $(date -u +%s) - 4000 ))" +%Y-%m-%dT%H:%M:%SZ)/" \
  "$WORK/mutated/metadata"
check "stale evidence stops the gate" "$REFUSED" gate "$WORK/mutated"

# Dated forward, which is how a stale snapshot would be made to look fresh.
mutate
sed -i "s/^collected_at=.*/collected_at=$(date -u -d "@$(( $(date -u +%s) + 4000 ))" +%Y-%m-%dT%H:%M:%SZ)/" \
  "$WORK/mutated/metadata"
check "evidence stamped in the future stops the gate" "$REFUSED" gate "$WORK/mutated"

mutate; sed -i 's/^namespace=.*/namespace=nchat-dev/' "$WORK/mutated/metadata"
check "evidence collected for another namespace stops the gate" "$REFUSED" gate "$WORK/mutated"

# Truncation and editing are what the checksums are for. They are integrity, not
# authenticity: this case edits the file and leaves the recorded sum alone.
mutate; printf 'Running|node-a|9000m 90Gi 90Gi\n' >>"$WORK/mutated/cluster-requests.txt"
check "evidence that does not match its checksums stops the gate" "$REFUSED" gate "$WORK/mutated"

# Emptied *and* re-summed, so what is refused here is the emptiness itself. No
# allocatable capacity must never read as a cluster with room to spare, and no
# committed requests must never read as a cluster with nothing on it.
mutate; : >"$WORK/mutated/node-allocatable.txt"
(cd "$WORK/mutated" && sha256sum node-allocatable.txt cluster-requests.txt cluster-pods.txt >sha256sums.txt)
check "empty node allocatable stops the gate" "$REFUSED" gate "$WORK/mutated"

mutate; : >"$WORK/mutated/cluster-requests.txt"
(cd "$WORK/mutated" && sha256sum node-allocatable.txt cluster-requests.txt cluster-pods.txt >sha256sums.txt)
check "empty cluster requests stops the gate" "$REFUSED" gate "$WORK/mutated"

mutate; : >"$WORK/mutated/cluster-pods.txt"
(cd "$WORK/mutated" && sha256sum node-allocatable.txt cluster-requests.txt cluster-pods.txt >sha256sums.txt)
check "empty cluster pods stops the gate" "$REFUSED" gate "$WORK/mutated"

# A link in the evidence directory would let something that looks like a
# snapshot read from somewhere else entirely.
mutate; rm "$WORK/mutated/cluster-pods.txt"
ln -s "$EVIDENCE/cluster-pods.txt" "$WORK/mutated/cluster-pods.txt"
check "a symlinked evidence file stops the gate" "$REFUSED" gate "$WORK/mutated"

# And the gate still fails on a real shortfall in evidence mode: a snapshot is
# an input to the same arithmetic, not a way around it.
printf '1' >"$STATE/quota/hard-cpu"; printf '900m' >"$STATE/quota/used-cpu"
check "insufficient capacity is still refused from evidence" "$INSUFFICIENT" gate "$EVIDENCE"
grep -q "\[FAIL\] namespace quota requests.cpu" "$WORK/out.txt" ||
  fail_evidence "the shortfall came from the gate refusing the evidence, not from the arithmetic"
printf '16' >"$STATE/quota/hard-cpu"; printf '1' >"$STATE/quota/used-cpu"

echo
echo "--- malformed evidence is unusable input, never an overridable gap ---"
#
# The review's reproduction: valid metadata, valid freshness, checksums
# recalculated after the corruption -- and `not-a-pod-record` still passed,
# because a line without the delimiter parsed as a Pod with no node name, which
# reads as a Pod holding nothing. A file of them described an empty cluster.
#
# Every case here re-signs the directory after corrupting it, so what is under
# test is the content and never the integrity check. The verdict has to be
# BAD INPUT and not INCONCLUSIVE: the operator override reaches the second, and
# "the input is broken" is not something anyone can verify by hand.
corrupt() {
  local name="$1" content="$2"
  mutate
  printf '%s\n' "$content" >"$WORK/mutated/$name"
  (cd "$WORK/mutated" &&
    sha256sum node-allocatable.txt cluster-requests.txt cluster-pods.txt >sha256sums.txt)
}

expect_message() {
  grep -q "$1" "$WORK/err.txt" || fail_evidence "did not report '$1'"
}

corrupt cluster-pods.txt 'not-a-pod-record'
check "the reviewer's malformed pod record is unusable input" "$BAD_INPUT" gate "$WORK/mutated"
expect_message "invalid cluster pod input"

# A phase Kubernetes does not define did not come from a Pod listing either.
corrupt cluster-pods.txt 'Runnnig|node-a'
check "a pod line with no real phase is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt node-allocatable.txt 'not-a-node-record'
check "a node line with one field is unusable input" "$BAD_INPUT" gate "$WORK/mutated"
expect_message "invalid node allocatable input"

corrupt node-allocatable.txt '8 32Gi 200Gi 110 extra'
check "a node line with too many fields is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt node-allocatable.txt '8 32Gi 200Gi many'
check "a non-numeric pod ceiling is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt node-allocatable.txt 'eight 32Gi 200Gi 110'
check "an impossible node quantity is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt cluster-requests.txt 'Running|node-a'
check "a request line missing its resources column is unusable input" "$BAD_INPUT" gate "$WORK/mutated"
expect_message "invalid cluster request input"

corrupt cluster-requests.txt 'Running|node-a|1x2 1Gi 64Mi'
check "an impossible request quantity is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt cluster-requests.txt 'Running|node-a|100m 1Gi 64Mi 5Gi'
check "a request line with a fourth quantity is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

# A quantity is an amount of a resource. A negative one does not read as an
# error to arithmetic that subtracts committed from allocatable -- it reads as a
# cluster with more room than it has, which is the shape of a false pass. The
# reviewer's reproduction reported "free 18000m of 8000m" and exited 0.
corrupt cluster-requests.txt 'Running|node-a|-100m 256Mi 1Gi'
check "a negative cpu request is unusable input" "$BAD_INPUT" gate "$WORK/mutated"
expect_message "quantity is negative"

corrupt cluster-requests.txt 'Running|node-a|100m -256Mi 1Gi'
check "a negative memory request is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt cluster-requests.txt 'Running|node-a|100m 256Mi -1Gi'
check "a negative ephemeral-storage request is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt node-allocatable.txt '-8 32Gi 200Gi 110'
check "a negative node allocatable is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

# 9223372036854775808 is 2^63: one byte more than a Kubernetes Quantity can
# hold, and refused on exactly that bound. It is the size policy doing the work
# here -- the number expands to nineteen digits without trouble.
#
# The float parser used to turn quantities of this magnitude into infinity and
# surface the OverflowError as exit 1, "the cluster cannot hold this", for an
# input it had in fact failed to read.
corrupt cluster-requests.txt 'Running|node-a|9223372036854775808 2Gi 64Mi'
check "a quantity too large for a Kubernetes quantity is unusable input" \
  "$BAD_INPUT" gate "$WORK/mutated"
expect_message "quantity is too large"

# Far enough out that expanding it is pointless rather than merely useless. Same
# verdict, different reason, and the reason is the one that says so.
corrupt cluster-requests.txt 'Running|node-a|1e309 2Gi 64Mi'
check "a quantity beyond any Kubernetes exponent is unusable input" \
  "$BAD_INPUT" gate "$WORK/mutated"
expect_message "exponent"
! grep -qE "Traceback|OverflowError" "$WORK/err.txt" ||
  fail_evidence "an overflowing quantity left by way of a traceback"

corrupt cluster-requests.txt 'Running|node-a|nan 2Gi 64Mi'
check "a quantity that is not a number is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

corrupt cluster-requests.txt 'Running|node-a|inf 2Gi 64Mi'
check "an infinite quantity is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

# Validation happens before the filter, so a record that holds no capacity is
# still a record that has to follow the contract. Both of these used to be
# dropped before anything looked at their quantities, which meant a file of
# malformed lines passed as long as every Pod in it was terminal or unscheduled.
corrupt cluster-requests.txt 'Succeeded|node-a|not-a-quantity'
check "a terminal Pod with an unreadable quantity is unusable input" "$BAD_INPUT" \
  gate "$WORK/mutated"

corrupt cluster-requests.txt 'Pending||-100m 256Mi 1Gi'
check "an unscheduled Pod with a negative quantity is unusable input" "$BAD_INPUT" \
  gate "$WORK/mutated"

echo
echo "--- and Kubernetes that is merely sparse is still valid evidence ---"
# The overcorrection guard. An empty field is not a missing delimiter: a Pod the
# scheduler has not bound anywhere has no nodeName, and a container that
# declares no requests reserves nothing. Both are ordinary, and both were
# already the contract before the parsers were tightened.
corrupt cluster-pods.txt 'Running|node-a
Pending|
Unknown|node-a'
check "a Pod with no node, and every real phase, stay valid" "$SUFFICIENT" gate "$WORK/mutated"

corrupt cluster-requests.txt 'Running|node-a|100m
Running|node-a| 128Mi
Running|node-a|500m 1Gi 64Mi
Pending||3 6Gi 64Mi'
check "containers that declare no requests stay valid" "$SUFFICIENT" gate "$WORK/mutated"

# And validating them first did not make them count. The node holds 8 CPU; the
# three records below ask for 24 between them, and every one of them is either
# terminal or unbound, so the candidate still fits.
corrupt cluster-requests.txt 'Succeeded|node-a|8 30Gi 190Gi
Failed|node-a|8 30Gi 190Gi
Pending||8 30Gi 190Gi'
check "valid terminal and unscheduled Pods are read and still hold nothing" \
  "$SUFFICIENT" gate "$WORK/mutated"

# The reviewer's reproduction, end to end. Every quantity below is an ordinary
# Kubernetes one that the old parser did not know, so a container with a
# sub-unit memory request, a decimal 'k' or an exponent stopped the deploy with
# "invalid cluster request input" -- a parse failure reported as though the
# evidence itself were corrupt. The first line is the reproduction verbatim.
corrupt cluster-requests.txt 'Running|node-a|500m 400m 64Mi
Running|node-a|250m 1k 129e6
Running|node-a|1e0 2.5 1Mi'
check "quantities the old parser rejected are read as the Kubernetes values they are" \
  "$SUFFICIENT" gate "$WORK/mutated"
! grep -q "invalid cluster request input" "$WORK/err.txt" ||
  fail_evidence "a valid Kubernetes quantity was reported as invalid evidence"

# And reading them is not the same as waving them through. 1Pi of committed
# ephemeral-storage against 200Gi of allocatable is a real shortfall, so the
# same file that no longer fails to parse still fails to fit -- exit 1, not the
# exit 3 a parse error would give.
corrupt cluster-requests.txt 'Running|node-a|500m 400m 1Pi'
check "a large binary quantity is counted against the nodes, not refused" \
  "$INSUFFICIENT" gate "$WORK/mutated"
grep -q "\[FAIL\] cluster allocatable ephemeral-storage" "$WORK/out.txt" ||
  fail_evidence "1Pi of committed storage was not counted against the nodes"

# Two of the four fields is a node that reported no ephemeral-storage and no pod
# ceiling: structurally sound, and two dimensions the cluster did not answer.
# That is still INCONCLUSIVE, not unusable input.
corrupt node-allocatable.txt '8 32Gi'
check "a node reporting only cpu and memory is inconclusive, not unusable" "$INCONCLUSIVE" \
  gate "$WORK/mutated"

# The positions are fixed, and an empty one in the middle is not a missing
# separator. "8 32Gi  110" is a node that did not report ephemeral-storage and
# does report a ceiling of 110 pods; splitting on runs of whitespace slid the
# ceiling into the storage position, so 110 was read as 110 BYTES of allocatable
# storage and the run reported a storage shortfall that does not exist, while
# calling the pod dimension unknown. Both halves are asserted here.
corrupt node-allocatable.txt '8 32Gi  110'
check "an empty storage position leaves storage unknown, not tiny" "$INCONCLUSIVE" \
  gate "$WORK/mutated"
grep -q "\[INCONCLUSIVE\] cluster allocatable ephemeral-storage" "$WORK/out.txt" ||
  fail_evidence "an unreported ephemeral-storage was given a value"
grep -q "\[OK\]   cluster allocatable pods: need 2, free 109 of 110" "$WORK/out.txt" ||
  fail_evidence "the pod ceiling did not survive the empty storage position"

# The mirror image: a node that reports storage and no ceiling. The trailing
# position is empty rather than absent, and neither value moves.
corrupt node-allocatable.txt '8 32Gi 200Gi '
check "an empty pods position leaves pods unknown, not storage" "$INCONCLUSIVE" \
  gate "$WORK/mutated"
grep -q "\[OK\]   cluster allocatable ephemeral-storage" "$WORK/out.txt" ||
  fail_evidence "the ephemeral-storage figure did not survive the empty pods position"
grep -q "\[INCONCLUSIVE\] cluster allocatable pods" "$WORK/out.txt" ||
  fail_evidence "an unreported pod ceiling was given a value"

corrupt node-allocatable.txt '8 32Gi 200Gi 110 extra more'
check "a node line with more than four positions is unusable input" "$BAD_INPUT" \
  gate "$WORK/mutated"

corrupt node-allocatable.txt ' 32Gi 200Gi 110'
check "a node line with no cpu position is unusable input" "$BAD_INPUT" gate "$WORK/mutated"

# Evidence and the live collection share the parser, so the same eight bytes of
# text reach the gate through a snapshot. It has to answer, and answer the same
# way: unusable input, before the migration and before any apply. `timeout` here
# bounds the case rather than deciding it -- exit 124 is the failure this exists
# to catch, so the check demands the preflight's own BAD INPUT.
corrupt cluster-requests.txt 'Running|node-a|1e999999999999999999 2Gi 64Mi'
check "an unexpandable exponent in evidence stops the gate instead of hanging it" \
  "$BAD_INPUT" gate_within 5 "$WORK/mutated"
expect_message "exponent"

# The other direction, through the same chain: evidence -> loader ->
# candidate-capacity.py -> parsing -> evaluation. A container asking for 1e-89
# of a core is asking for a real, tiny amount, and the gate has to price it and
# carry on. It reached BAD INPUT for a while, which would have stopped a deploy
# over a container that was doing nothing wrong.
corrupt cluster-requests.txt 'Running|node-a|1e-89 2Gi 64Mi
Running|node-a|1e-999999999999999999 1Gi 32Mi'
check "a vanishingly small request in evidence is priced, not refused" \
  "$SUFFICIENT" gate_within 5 "$WORK/mutated"
! grep -qE "invalid cluster request input|InvalidQuantity" "$WORK/err.txt" ||
  fail_evidence "a valid tiny quantity was reported as invalid evidence"
# Priced as one millicore each, not dropped: 3Gi of the two memory requests is
# what the run must have counted.
grep -q "cluster allocatable memory: need .* free 31138512896B" "$WORK/out.txt" ||
  fail_evidence "the two tiny-CPU containers' memory was not counted"

# The same quantity in live mode, where the collection comes from the API rather
# than from a snapshot. Both modes reach the same evaluator, and this is the
# case that says so out loud.
rm -f "$STATE/deny-cluster-wide"
printf 'Running|node-a|1e-89 2Gi 64Mi\n' >"$STATE/cluster-pods"
check "live mode prices the same vanishingly small request" "$SUFFICIENT" gate ""
! grep -qE "invalid cluster request input|InvalidQuantity" "$WORK/err.txt" ||
  fail_evidence "live mode reported a valid tiny quantity as invalid"
printf 'Running|node-a|500m 2Gi 64Mi\n' >"$STATE/cluster-pods"
printf '1' >"$STATE/deny-cluster-wide"
# Live mode reads Nodes and every namespace -- that is what live mode is. The
# log has to go back to empty so the least-privilege assertions that follow are
# still asserting about evidence mode and not about this case.
rm -f "$STATE/cluster-wide-log"

echo
echo "--- the freshness limit is configuration, and is validated as one ---"
# It reaches an arithmetic context, where bash resolves a bare word as a
# variable name: the limit used to end the run with its own "unbound variable".
with_max_age() {
  local value="$1" expected="$2" name="$3"
  export NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS="$value"
  check "$name" "$expected" gate "$EVIDENCE"
  ! grep -q "unbound variable" "$WORK/err.txt" ||
    fail_evidence "$name reported a bash error instead of a configuration one"
  unset NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS
}

# The validator on its own, where a limit of 0 has a deterministic answer that a
# freshness comparison against a snapshot collected moments ago would not, and
# where the digit cap can be read directly off what it returns.
max_age_reads_as() {
  local value="$1" expected="$2" got
  if ! got="$(NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS="$value" bash -c     'set -Eeuo pipefail; source "$1"; capacity_evidence_max_age' _ "$LIB" 2>"$WORK/err.txt")"; then
    got=REFUSED
  fi
  [[ "$got" == "$expected" ]] ||
    fail_evidence "a freshness limit of '$value' read as '$got', expected '$expected'"
  ! grep -qE "value too great for base|unbound variable|syntax error" "$WORK/err.txt" ||
    fail_evidence "a freshness limit of '$value' reached bash arithmetic"
}

# Bash arithmetic is signed 64-bit and wraps in silence: these two used to come
# back as -8446744073709551617 and 0, and a limit of zero refuses every snapshot
# for a reason nobody could see. Eighteen digits is the cap, checked as text.
max_age_reads_as 0 0
max_age_reads_as 999999999999999999 999999999999999999
max_age_reads_as 9999999999999999999 REFUSED
max_age_reads_as 18446744073709551616 REFUSED
echo "  [OK]   the freshness limit is bounded to 18 digits, before any arithmetic"

# The default is what applies when the variable is not set at all.
check "the default freshness limit admits fresh evidence" "$SUFFICIENT" gate "$EVIDENCE"

with_max_age oops "$REFUSED" "a non-numeric freshness limit is refused, not evaluated"
expect_message "invalid NCHAT_PROD_CAPACITY_EVIDENCE_MAX_AGE_SECONDS"
with_max_age -1 "$REFUSED" "a negative freshness limit is refused"
with_max_age 1+1 "$REFUSED" "a freshness limit that is an expression is refused"
with_max_age "" "$REFUSED" "an explicitly empty freshness limit is refused, not defaulted"
with_max_age 900 "$SUFFICIENT" "the ordinary freshness limit still admits fresh evidence"
# Leading zeros are read as decimal, never as octal.
with_max_age 000900 "$SUFFICIENT" "a zero-padded freshness limit is read in base ten"

[[ ! -f "$STATE/cluster-wide-log" ]] ||
  fail_evidence "evidence mode read a cluster-wide resource: $(tr '\n' ';' <"$STATE/cluster-wide-log")"

echo
echo "=== what the collectors actually produce ==="
#
# Everything above starts from files already in the contract's shape. These
# cases start one step earlier, at the kubectl queries that produce them: the
# gate reached BAD INPUT and INCONCLUSIVE in production while every fixture here
# agreed it was fine, because nothing exercised the queries themselves.
#
# Live mode, so the collectors run.
rm -f "$STATE/deny-cluster-wide" "$STATE/cluster-wide-log"

# One lib.sh function, against the fake.
lib_call() {
  FAKE_STATE_DIR="$STATE" bash -c 'set -Eeuo pipefail; source "$1"; shift; "$@"' _ "$LIB" "$@"
}

expect_value() {
  local name="$1" expected="$2" got="$3"
  if [[ "$got" == "$expected" ]]; then
    echo "  [OK]   $name"
    return 0
  fi
  echo "  [FAIL] $name: got '$got', expected '$expected'" >&2
  FAILURES=$((FAILURES + 1))
}

echo
echo "--- every quota dimension is read, dotted keys included ---"
#
# Three of the four keys are literal names with a dot in them. Asked for
# unescaped, kubectl reads the dot as a step into a nested object no
# ResourceQuota has and answers with nothing at all -- so cpu, memory and
# ephemeral-storage arrived as "the namespace declares no such limit" and only
# `pods`, the one key without a dot, was ever judged. The values here are the
# ones the fake holds; what is under test is that they arrive at all.
expect_value "hard requests.cpu is read" 16 "$(lib_call quota_status_field hard requests.cpu)"
expect_value "used requests.cpu is read" 1 "$(lib_call quota_status_field used requests.cpu)"
expect_value "hard requests.memory is read" 32Gi "$(lib_call quota_status_field hard requests.memory)"
expect_value "used requests.memory is read" 2Gi "$(lib_call quota_status_field used requests.memory)"
expect_value "hard requests.ephemeral-storage is read" 500Gi \
  "$(lib_call quota_status_field hard requests.ephemeral-storage)"
expect_value "used requests.ephemeral-storage is read" 10Gi \
  "$(lib_call quota_status_field used requests.ephemeral-storage)"
expect_value "hard pods is read" 80 "$(lib_call quota_status_field hard pods)"
expect_value "used pods is read" 10 "$(lib_call quota_status_field used pods)"

# A dimension the quota does not declare stays unread. It must not acquire a
# zero on the way through, which would read as a limit of nothing and fail every
# deploy, nor a default, which would read as room nobody granted.
: >"$STATE/quota/hard-cpu"
expect_value "an undeclared limit stays empty" "" "$(lib_call quota_status_field hard requests.cpu)"
printf '16' >"$STATE/quota/hard-cpu"

echo
echo "--- one line per container, each carrying its own Pod's phase and node ---"
#
# kubectl answers the request query per Pod: the Pod's phase and node once, then
# its containers. The Pod cannot be reached from inside the container loop --
# `$` in jsonpath is the current object, not the document root -- so asking for
# it there produced "||<requests>" for every container in the cluster and the
# evaluator refused the file at line 1.
#
# The Pods below cover the states the sum has to tell apart, and the last of
# them holds an app container followed by an initContainer: both are collected,
# because the kubelet reserves the larger of the two and counting both can only
# overstate a Pod.
KUBECTL_PODS=(
  'Running|node-a|500m 1Gi 64Mi;100m 256Mi 64Mi;'
  'Pending|node-b|250m 512Mi 32Mi;'
  'Pending||3 6Gi 1Gi;'
  'Succeeded|node-a|2 4Gi 1Gi;'
  'Failed|node-a|2 4Gi 1Gi;'
  'Running|node-b| 128Mi 32Mi;'
  'Running|node-b|100m  32Mi;'
  'Running|node-b|100m 128Mi ;'
  'Running|node-c|  ;'
  'Running|node-c|50m 64Mi 16Mi;200m 256Mi 64Mi;'
)
EXPANDED=(
  'Running|node-a|500m 1Gi 64Mi'
  'Running|node-a|100m 256Mi 64Mi'
  'Pending|node-b|250m 512Mi 32Mi'
  'Pending||3 6Gi 1Gi'
  'Succeeded|node-a|2 4Gi 1Gi'
  'Failed|node-a|2 4Gi 1Gi'
  'Running|node-b| 128Mi 32Mi'
  'Running|node-b|100m  32Mi'
  'Running|node-b|100m 128Mi '
  'Running|node-c|  '
  'Running|node-c|50m 64Mi 16Mi'
  'Running|node-c|200m 256Mi 64Mi'
)
printf '%s\n' "${KUBECTL_PODS[@]}" >"$STATE/cluster-pods"
expect_value "each container becomes its own line, phase and node intact" \
  "$(printf '%s\n' "${EXPANDED[@]}")" "$(lib_call cluster_container_request_lines)"

# And what comes out is what the evaluator reads, not merely what looks like it.
lib_call cluster_container_request_lines >"$WORK/collected-requests.txt"
check "the collected requests are accepted by the evaluator" "$SUFFICIENT" \
  python3 "$PREFLIGHT" --manifest "$FIXTURES/candidate-small.yaml" \
  "${QUOTA_OK[@]}" --node-allocatable-file <(printf '8 32Gi 200Gi 110\n') \
  --cluster-requests-file "$WORK/collected-requests.txt" \
  --cluster-pods-file "$FIXTURES/cluster-pod-slots.txt"

# A Pod the query could not describe is not quietly dropped: it reaches the
# evaluator, which refuses the file. Losing it would take its commitment out of
# the sum, which is the direction that reads as a cluster with room.
expect_value "a record the query could not fill is passed on, not dropped" \
  '||500m 1Gi 64Mi' "$(printf '||500m 1Gi 64Mi;\n' | lib_call expand_pod_container_requests)"
expect_value "a Pod listing no container at all is still a Pod" \
  'Running|node-a|' "$(printf 'Running|node-a|\n' | lib_call expand_pod_container_requests)"

echo
echo "--- the gate, end to end, over what the collectors returned ---"
#
# The shell half and the Python half against the same cluster, one case per exit
# code the contract defines. Nothing here reads a file written by hand.
live_gate() {
  gate ""
}

check "a cluster with room admits the candidate" "$SUFFICIENT" live_gate

# Terminal and unscheduled Pods hold nothing. Counted, the 7 CPU they ask for
# would put this candidate 4.35 CPU past a 4-CPU cluster; released, it fits.
printf '4 16Gi 200Gi 110\n' >"$STATE/node-allocatable"
check "Pods that have finished or were never scheduled do not hold capacity" \
  "$SUFFICIENT" live_gate

printf '2 4Gi 200Gi 110\n' >"$STATE/node-allocatable"
check "a cluster without room refuses it" "$INSUFFICIENT" live_gate
printf '8 32Gi 200Gi 110\n' >"$STATE/node-allocatable"

# A dimension the cluster did not declare is unknown, and unknown is not a pass.
for name in hard-cpu used-cpu hard-memory used-memory hard-storage used-storage; do
  mv "$STATE/quota/$name" "$STATE/quota/$name.kept"
done
check "a quota dimension the namespace did not declare is inconclusive" \
  "$INCONCLUSIVE" live_gate
for name in hard-cpu used-cpu hard-memory used-memory hard-storage used-storage; do
  mv "$STATE/quota/$name.kept" "$STATE/quota/$name"
done

# A phase the cluster cannot have reported, and a quantity that is not one.
printf 'Terminating|node-a|500m 1Gi 64Mi;\n' >"$STATE/cluster-pods"
check "a line whose phase is not one Kubernetes defines is unusable input" \
  "$BAD_INPUT" live_gate
printf 'Running|node-a|not-a-quantity 1Gi 64Mi;\n' >"$STATE/cluster-pods"
check "a request that is not a quantity is unusable input" "$BAD_INPUT" live_gate
printf '%s\n' "${KUBECTL_PODS[@]}" >"$STATE/cluster-pods"

# A read that came back with nothing is a failed read, never an empty cluster.
: >"$STATE/cluster-pods"
check "a request query that answered nothing is not a cluster with nothing on it" \
  "$INCONCLUSIVE" live_gate
printf '%s\n' "${KUBECTL_PODS[@]}" >"$STATE/cluster-pods"

echo
echo "--- the fake refuses a request query production could not use ---"
#
# The collector's query is protected by the fake's shape check, not by the
# fake's topic. Matching on "resources.requests" alone handed the correct
# fixture to the incident's own jsonpath, so a regression could reintroduce it
# and leave the suite green. These cases ask the fake directly.
REQUEST_TAIL="{.resources.requests.cpu}{' '}{.resources.requests.memory}{' '}{.resources.requests.ephemeral-storage}{';'}"

ask_for_requests() {
  FAKE_STATE_DIR="$STATE" "$FAKE_BIN/kubectl" get pods --all-namespaces -o "jsonpath=$1"
}

refuse_query() {
  local name="$1" query="$2" status=0
  ask_for_requests "$query" >"$WORK/out.txt" 2>"$WORK/err.txt" || status=$?
  if [[ "$status" -eq 0 ]]; then
    echo "  [FAIL] $name: the fake answered a query production gets nothing from" >&2
    FAILURES=$((FAILURES + 1))
    return 0
  fi
  grep -q "capacity request query" "$WORK/err.txt" ||
    fail_evidence "$name: refused without saying what was wrong with the query"
  echo "  [OK]   $name"
}

# The incident. `$` is the current object, so both fields resolve against the
# container and the collector emits "||<requests>" for every Pod in the cluster.
refuse_query "the Pod's fields read from inside the container range are refused" \
  "{range .items[*]}{range .spec.containers[*]}{\$.status.phase}|{\$.spec.nodeName}|$REQUEST_TAIL{end}{range .spec.initContainers[*]}{\$.status.phase}|{\$.spec.nodeName}|$REQUEST_TAIL{end}{end}"

# The same mistake written without the '$', which resolves no better.
refuse_query "a phase read after the container range has opened is refused" \
  "{range .items[*]}{range .spec.containers[*]}{.status.phase}|{.spec.nodeName}|$REQUEST_TAIL{end}{range .spec.initContainers[*]}$REQUEST_TAIL{end}{end}"

# initContainers dropped: the kubelet reserves the larger of (max init request,
# sum of app requests), so a collection without them can understate a Pod.
refuse_query "a query that never ranges over initContainers is refused" \
  "{range .items[*]}{.status.phase}|{.spec.nodeName}|{range .spec.containers[*]}$REQUEST_TAIL{end}{'\\n'}{end}"

# And ranging over them without reading what they ask for is the same loss.
refuse_query "an initContainer range missing a request is refused" \
  "{range .items[*]}{.status.phase}|{.spec.nodeName}|{range .spec.containers[*]}$REQUEST_TAIL{end}{range .spec.initContainers[*]}{.resources.requests.cpu}{' '}{.resources.requests.memory}{end}{'\\n'}{end}"

# The query the collector actually sends stays answerable, and what it collects
# is unchanged -- the checks above are a gate on shape, not a rewrite of it.
check "the collector's own query is still answered" "$SUFFICIENT" \
  lib_call cluster_container_request_lines
expect_value "and still returns the same evidence" \
  "$(printf '%s\n' "${EXPANDED[@]}")" "$(lib_call cluster_container_request_lines)"

echo
if [ "$FAILURES" -gt 0 ]; then
  echo "capacity preflight tests failed with $FAILURES failure(s)." >&2
  exit 1
fi
echo "capacity preflight tests passed."
