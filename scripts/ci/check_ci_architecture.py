#!/usr/bin/env python3
"""One gate, one owner, one execution.

Issue #931 replaced four overlapping workflows with one job per gate. That
architecture is only worth anything while it holds, and every regression it can
suffer is easy to reintroduce by hand: a gate copied back into a second job, a
mega-command like `pnpm ci` calling everything again from inside Actions, the
aggregation job quietly starting to run tests, or SonarQube's Quality Gate
becoming blocking again.

This check reads the workflows and refuses those four regressions. It is
deliberately literal: it compares whole, stripped `run:` lines against the
canonical command of each gate, so a gate folded into a compound line reads as
"no owner" rather than silently passing.

Usage: check_ci_architecture.py [workflow-directory]
Exit code 0 when the architecture holds; 1 with one short reason per violation.
"""

from __future__ import annotations

import pathlib
import re
import sys

import yaml

SCRIPT_DIRECTORY = pathlib.Path(__file__).parent
if str(SCRIPT_DIRECTORY) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIRECTORY))

from check_required_gates import REQUIRED_GATES

ROOT = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW_DIRECTORY = ROOT / ".github" / "workflows"

AGGREGATOR_JOB = "required"
SONARQUBE_JOB = "quality-sonarqube"

# The canonical command of every gate and its job owner. Each key is the whole
# `run:` line of the step that owns it.
OWNED_GATES = {
    "pnpm format:check:web": "static-web",
    "pnpm lint:web": "static-web",
    "pnpm typecheck:web": "static-web",
    "pnpm format:check:admin-web": "static-admin",
    "pnpm lint:admin-web": "static-admin",
    "pnpm typecheck:admin-web": "static-admin",
    "pnpm format:check:docs": "static-repository",
    "bash scripts/ci/go-fmt-check.sh": "static-go",
    "bash scripts/ci/go-vet.sh": "static-go",
    "bash scripts/ci/go-lint.sh": "static-go",
    "pnpm test:coverage:web": "tests-web-unit",
    "pnpm test:coverage:admin-web": "tests-admin-unit",
    "bash scripts/ci/go-test.sh": "tests-go-unit",
    "bash scripts/ci/go-test.sh -race": "tests-go-race",
    "bash scripts/ci/go-integration-test.sh": "tests-go-integration",
    "bash scripts/ci/go-coverage-check.sh": "tests-go-coverage",
    "pnpm test:e2e:web": "e2e-web",
    "pnpm test:e2e:admin-web": "e2e-admin",
    "pnpm build:web": "build-web",
    "pnpm build:admin-web": "build-admin",
    "pnpm k8s:ci": "infra-kubernetes",
    "pnpm migrations:check": "infra-migrations",
    "pnpm web:livekit-integration-check": "infra-web-image",
}

# Local convenience aggregators. Useful to a developer, redundant inside Actions,
# where every gate they chain already has its own job.
FORBIDDEN_AGGREGATORS = ("pnpm ci", "pnpm run ci", "make ci")

# The gates `CI / Required` consolidates. SonarQube is deliberately absent: it is
# advisory, and a failed Quality Gate must not block merge or CD.
# What the aggregation job may do: check out the repository and read the results
# of its dependencies. Anything that smells like real work is a violation.
AGGREGATOR_FORBIDDEN = ("pnpm", "go test", "make ", "trivy", "sonar", "playwright", "vitest")

# The SonarQube enforcement #931 removes. The scan and its upload stay; only the
# blocking wait on the server-side Quality Gate goes.
QUALITY_GATE_ENFORCEMENT = "sonar.qualitygate.wait"

# The two scripts allowed to activate an opt-in PostgreSQL suite. One owns
# `Tests / Go Integration`, the other is what `Tests / Go Coverage` calls.
GO_DATABASE_GATE_SCRIPTS = (
    "scripts/ci/go-integration-test.sh",
    "scripts/ci/link-safety-postgres-coverage.sh",
)
DATABASE_VARIABLE = re.compile(r"\b([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*_TEST_DATABASE_URL)\b")
SONARQUBE_SCAN_ACTION = "SonarSource/sonarqube-scan-action"


def load_workflows(directory: pathlib.Path) -> dict[str, dict]:
    """Every workflow in the directory, parsed, keyed by file name."""
    return {path.name: yaml.safe_load(path.read_text()) or {} for path in sorted(directory.glob("*.yml"))}


def run_lines(job: dict) -> list[str]:
    """Every non-empty shell line the job's steps execute."""
    lines = []
    for step in job.get("steps") or []:
        for line in str(step.get("run", "")).splitlines():
            stripped = line.strip()
            if stripped:
                lines.append(stripped)
    return lines


def all_jobs(workflows: dict[str, dict]) -> list[tuple[str, str, dict]]:
    """Every (workflow, job id, job) across the parsed workflows."""
    return [
        (name, job_id, job)
        for name, workflow in workflows.items()
        for job_id, job in (workflow.get("jobs") or {}).items()
        if isinstance(job, dict)
    ]


def check_single_ownership(jobs: list[tuple[str, str, dict]]) -> list[str]:
    """Each gate must be executed by exactly one job."""
    owners: dict[str, list[str]] = {gate: [] for gate in OWNED_GATES}
    for workflow, job_id, job in jobs:
        for line in run_lines(job):
            if line in owners:
                owners[line].append(f"{workflow}:{job_id}")

    violations = []
    for gate, found in owners.items():
        if not found:
            violations.append(f"Gate has no owner: {gate}")
        elif len(found) > 1:
            violations.append(f"Gate has {len(found)} owners ({', '.join(found)}): {gate}")
        elif found[0].split(":", 1)[1] != OWNED_GATES[gate]:
            violations.append(f"Gate must be owned by {OWNED_GATES[gate]}: {gate}")
    return violations


def check_no_aggregators(jobs: list[tuple[str, str, dict]]) -> list[str]:
    """No workflow may re-run the local mega-gate."""
    return [
        f"{workflow}:{job_id} runs the local aggregator '{line}'; it duplicates the specialised jobs."
        for workflow, job_id, job in jobs
        for line in run_lines(job)
        if line in FORBIDDEN_AGGREGATORS
    ]


def check_no_continue_on_error(jobs: list[tuple[str, str, dict]]) -> list[str]:
    """A gate that cannot fail is not a gate."""
    return [
        f"{workflow}:{job_id} sets continue-on-error; a gate must be able to fail."
        for workflow, job_id, job in jobs
        if job.get("continue-on-error")
    ]


def find_job(jobs: list[tuple[str, str, dict]], job_id: str) -> dict | None:
    for _, candidate, job in jobs:
        if candidate == job_id:
            return job
    return None


def aggregator_dependencies(needs: set[str]) -> list[str]:
    """The aggregation must consolidate the required gates, and only those."""
    violations = [
        f"{AGGREGATOR_JOB} does not depend on the required gate '{missing}'."
        for missing in sorted(REQUIRED_GATES - needs)
    ]
    violations += [
        f"{AGGREGATOR_JOB} depends on '{extra}', which is not a required gate."
        for extra in sorted(needs - REQUIRED_GATES)
    ]
    if SONARQUBE_JOB in needs:
        violations.append(f"{AGGREGATOR_JOB} depends on '{SONARQUBE_JOB}', which is advisory.")
    return violations


def aggregator_work(job: dict) -> list[str]:
    """The aggregation reads results; it must never produce them."""
    return [
        f"{AGGREGATOR_JOB} executes work rather than reading results: {line}"
        for line in run_lines(job)
        for forbidden in AGGREGATOR_FORBIDDEN
        if forbidden in line
    ]


def check_aggregator(jobs: list[tuple[str, str, dict]]) -> list[str]:
    """`CI / Required` consolidates exactly the required gates and runs nothing."""
    job = find_job(jobs, AGGREGATOR_JOB)
    if job is None:
        return [f"The aggregation job '{AGGREGATOR_JOB}' is missing."]
    return aggregator_dependencies(set(job.get("needs") or [])) + aggregator_work(job)


def check_sonarqube_is_advisory(jobs: list[tuple[str, str, dict]]) -> list[str]:
    """The scan keeps running; only the blocking Quality Gate wait is gone."""
    job = find_job(jobs, SONARQUBE_JOB)
    if job is None:
        return [f"The advisory job '{SONARQUBE_JOB}' is missing; the analysis must keep running."]

    violations = []
    text = yaml.safe_dump(job)
    if QUALITY_GATE_ENFORCEMENT in text:
        violations.append(
            f"{SONARQUBE_JOB} still enforces {QUALITY_GATE_ENFORCEMENT}; the Quality Gate is advisory."
        )
    if SONARQUBE_SCAN_ACTION not in text:
        violations.append(
            f"{SONARQUBE_JOB} no longer runs {SONARQUBE_SCAN_ACTION}; advisory means reported, not removed."
        )
    return violations


def database_variables(paths: list[pathlib.Path]) -> set[str]:
    """Every opt-in database variable named anywhere in these files."""
    found: set[str] = set()
    for path in paths:
        found |= set(DATABASE_VARIABLE.findall(path.read_text(errors="ignore")))
    return found


def check_go_database_variable_coverage(root: pathlib.Path) -> list[str]:
    """Every database variable the Go suites read is named by some gate script.

    A Go suite that needs a real database opts in by reading a
    `*_TEST_DATABASE_URL` and calling `t.Skip` when it is unset. A variable that
    no gate script so much as mentions therefore cannot be set by anything, and
    every suite reading it skips while reporting success. That is the one thing
    this check catches, and it catches it by comparing variable *names*.

    It deliberately does NOT prove that those suites execute. It does not read
    `ci.yml`, so it cannot say the workflow supplies the variable; it does not
    parse Go, so it cannot say which packages or test functions a gate selects;
    and a name mentioned only in a comment counts the same as one in a command.

    Real execution is proved elsewhere, and on purpose: by the gate scripts
    themselves, which refuse to run with a missing DSN instead of skipping
    quietly; by the unit tests of those scripts; and by the CI run. Widening this
    check to assert execution would need a GitHub Actions parser and a Go parser,
    which is a far more fragile thing to trust than the scripts' own failure.
    """
    gates = [root / relative for relative in GO_DATABASE_GATE_SCRIPTS]
    absent = [gate for gate in gates if not gate.is_file()]
    if absent:
        # Reported rather than raised: a deleted gate must read as a violation,
        # not as a checker that crashed and told nobody what it had covered.
        return [f"The database gate script is missing: {gate.name}" for gate in absent]

    read_by_suites = database_variables(sorted((root / "services").rglob("*_test.go")))
    named_by_gates = database_variables(gates)
    return [
        f"{variable} is read by a Go suite but named by no gate script, "
        f"so nothing can set it and every suite reading it skips."
        for variable in sorted(read_by_suites - named_by_gates)
    ]


CHECKS = (
    check_single_ownership,
    check_no_aggregators,
    check_no_continue_on_error,
    check_aggregator,
    check_sonarqube_is_advisory,
)


def audit(directory: pathlib.Path, root: pathlib.Path = ROOT) -> list[str]:
    jobs = all_jobs(load_workflows(directory))
    violations = [violation for check in CHECKS for violation in check(jobs)]
    return violations + check_go_database_variable_coverage(root)


def main(argv: list[str]) -> int:
    directory = pathlib.Path(argv[1]) if len(argv) > 1 else WORKFLOW_DIRECTORY
    violations = audit(directory)
    for violation in violations:
        print(violation, file=sys.stderr)
    if violations:
        return 1
    print("CI architecture check passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
