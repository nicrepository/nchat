#!/usr/bin/env python3
"""The rollout peak must be costed in resources, not in pod counts.

The regression this guards: an earlier model counted the surge as one extra pod
and priced it at the new per-pod requests. A release going from 2x100m to
2x1000m therefore reported 1000m of additional CPU when its final state alone
needs 1800m more than the cluster already holds. A cluster with, say, 1.2 CPU
free would have passed the preflight and then failed to roll out.
"""
import importlib.util
import sys

spec = importlib.util.spec_from_file_location("preflight", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

MB = 1024 * 1024


def current(replicas, cpu, memory=128 * MB):
    return module.CurrentWorkload(replicas, cpu, memory)


def workload(replicas, cpu, memory=128 * MB, surge=1):
    return module.Workload("alpha-blue", replicas, cpu, memory, surge)


# (name, desired workload, current state, expected cpu, expected pods)
CASES = [
    ("unchanged release costs one surge pod",
     workload(2, 100), current(2, 100), 100, 1),
    ("raised CPU is costed against what the pods cost now",
     workload(2, 1000), current(2, 100), 1900, 1),
    ("lowered CPU is not charged as if it were all new",
     workload(2, 100), current(2, 1000), 100, 1),
    ("scaling up counts the extra replicas and the surge",
     workload(4, 100), current(2, 100), 300, 3),
    ("scaling down is not credited during the rollout",
     workload(2, 100), current(4, 100), 0, 0),
    ("a slot that does not exist costs the whole slot",
     workload(2, 100), None, 200, 2),
    ("a larger surge costs more",
     workload(2, 100, surge=2), current(2, 100), 200, 2),
]

failed = False
for name, desired, held, want_cpu, want_pods in CASES:
    cpu, _, _, pods = module._additional(desired, held)
    if cpu != want_cpu or pods != want_pods:
        print(f"  [FAIL] {name}: cpu=+{cpu}m pods=+{pods}, "
              f"expected cpu=+{want_cpu}m pods=+{want_pods}", file=sys.stderr)
        failed = True

# Memory follows the same path; one explicit case keeps it honest.
cpu, memory, _, _ = module._additional(workload(2, 100, 512 * MB), current(2, 100, 128 * MB))
if memory != 2 * 512 * MB + 128 * MB - 2 * 128 * MB:
    print(f"  [FAIL] raised memory: got {memory}", file=sys.stderr)
    failed = True

sys.exit(1 if failed else 0)
