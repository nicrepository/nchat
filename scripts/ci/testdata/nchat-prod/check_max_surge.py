#!/usr/bin/env python3
"""maxSurge is what decides how much extra a redeploy costs, so both the integer
and the percentage form have to be read correctly, and a percentage has to round
up the way Kubernetes does."""
import importlib.util
import sys

spec = importlib.util.spec_from_file_location("preflight", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

CASES = [
    (["  replicas: 4", "      maxSurge: 2"], 4, 2, "integer maxSurge"),
    (["  replicas: 4", '      maxSurge: "25%"'], 4, 1, "percentage maxSurge"),
    (["  replicas: 3", '      maxSurge: "50%"'], 3, 2, "percentage rounds up"),
    (["  replicas: 2"], 2, 1, "absent maxSurge defaults to 1"),
]

failed = False
for lines, replicas, want, name in CASES:
    got = module._max_surge_of(lines, replicas)
    if got != want:
        print(f"  [FAIL] {name}: got {got}, expected {want}", file=sys.stderr)
        failed = True
sys.exit(1 if failed else 0)
