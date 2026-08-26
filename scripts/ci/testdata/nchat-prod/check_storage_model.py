#!/usr/bin/env python3
"""ephemeral-storage must use the same current/desired/surge model as CPU.

The issue requires capacity to cover ephemeral-storage, and file-service really
does request it. Treating the dimension as "the whole candidate is new" would
reproduce, for storage, the redeploy over-estimate already fixed for CPU.
"""
import importlib.util
import sys

spec = importlib.util.spec_from_file_location("preflight", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

MB = 1024 * 1024


def workload(replicas, storage, surge=1):
    return module.Workload("alpha-blue", replicas, 100, 128 * MB, surge, storage)


def current(replicas, storage):
    return module.CurrentWorkload(replicas, 100, 128 * MB, storage)


CASES = [
    ("a new slot costs its whole storage request",
     workload(2, 64 * MB), None, 128 * MB),
    ("a redeploy at the same size costs one surge pod",
     workload(2, 64 * MB), current(2, 64 * MB), 64 * MB),
    ("a release asking for more storage is costed against what it replaces",
     workload(2, 512 * MB), current(2, 64 * MB), 2 * 512 * MB + 64 * MB - 2 * 64 * MB),
    ("a release asking for less is not charged as all new",
     workload(2, 64 * MB), current(2, 512 * MB), 64 * MB),
    ("a workload declaring no ephemeral-storage costs nothing",
     workload(2, 0), current(2, 0), 0),
]

failed = False
for name, desired, held, want in CASES:
    _, _, storage, _ = module._additional(desired, held)
    if storage != want:
        print(f"  [FAIL] {name}: got {storage}, expected {want}", file=sys.stderr)
        failed = True

# A malformed quantity must be refused, never silently read as zero.
try:
    module.parse_memory("not-a-size")
except ValueError:
    pass
else:
    print("  [FAIL] a malformed storage quantity was accepted", file=sys.stderr)
    failed = True

sys.exit(1 if failed else 0)
