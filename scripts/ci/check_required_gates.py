#!/usr/bin/env python3
"""Fail-closed aggregation of the required CI gates.

`CI / Required` is the single check branch protection is meant to require, so it
must never be greener than the gates it aggregates. GitHub runs it with
`if: always()`, which means it also runs when a dependency failed, was cancelled
or was skipped -- and a job that merely *ran* reports success. The verdict
therefore has to be read from the `needs` context explicitly.

Every required gate in this repository runs unconditionally on `pull_request`
and on `push`, so `success` is the only acceptable result. `failure`,
`cancelled`, `skipped`, an unknown result, an empty payload and malformed JSON
are all refusals: a gate whose outcome cannot be read must never be reported as
a gate that passed.

Reads the serialised `needs` context from REQUIRED_GATES. Exit code 0 when every
gate succeeded, 1 otherwise.
"""

from __future__ import annotations

import json
import os
import sys

ACCEPTED_RESULT = "success"
ENVIRONMENT_VARIABLE = "REQUIRED_GATES"
REQUIRED_GATES = frozenset(
    {
        "static-web",
        "static-admin",
        "static-go",
        "static-repository",
        "tests-web-unit",
        "tests-admin-unit",
        "tests-go-unit",
        "tests-go-race",
        "tests-go-integration",
        "tests-go-coverage",
        "e2e-web",
        "e2e-admin",
        "build-web",
        "build-admin",
        "infra-config",
        "infra-kubernetes",
        "infra-migrations",
        "infra-release-safety",
        "infra-web-image",
        "security",
        "governance",
    }
)


def parse_payload(payload: str) -> dict:
    """The serialised `needs` context as a mapping, or raise ValueError."""
    try:
        gates = json.loads(payload)
    except json.JSONDecodeError as error:
        raise ValueError(f"{ENVIRONMENT_VARIABLE} is not valid JSON: {error}") from error

    if not isinstance(gates, dict) or not gates:
        raise ValueError(f"{ENVIRONMENT_VARIABLE} carries no gate results.")
    return gates


def verify_gate_set(names: set[str]) -> None:
    """The aggregation must observe exactly the canonical gates, or raise.

    A gate dropped from the job's `needs` would otherwise disappear silently:
    the aggregation would pass on a smaller set and still report success.
    """
    missing = REQUIRED_GATES - names
    unexpected = names - REQUIRED_GATES
    if not missing and not unexpected:
        return

    details = []
    if missing:
        details.append(f"missing: {', '.join(sorted(missing))}")
    if unexpected:
        details.append(f"unexpected: {', '.join(sorted(unexpected))}")
    raise ValueError(f"{ENVIRONMENT_VARIABLE} has the wrong gate set ({'; '.join(details)}).")


def read_results(payload: str) -> dict[str, str]:
    """Map each canonical gate to its reported result, or raise ValueError."""
    gates = parse_payload(payload)
    verify_gate_set(set(gates.keys()))
    return {
        name: outcome.get("result", "") if isinstance(outcome, dict) else ""
        for name, outcome in gates.items()
    }


def failures(results: dict[str, str]) -> list[str]:
    """Names of the gates that did not report success."""
    return sorted(name for name, result in results.items() if result != ACCEPTED_RESULT)


def report(results: dict[str, str]) -> None:
    for name in sorted(results):
        print(f"{name}: {results[name] or 'unknown'}")


def main() -> int:
    try:
        results = read_results(os.environ.get(ENVIRONMENT_VARIABLE, ""))
    except ValueError as error:
        print(f"{error} Refusing to report success.", file=sys.stderr)
        return 1

    report(results)

    refused = failures(results)
    if refused:
        print(f"Required gates did not succeed: {', '.join(refused)}", file=sys.stderr)
        return 1

    print(f"All {len(results)} required gates succeeded.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
