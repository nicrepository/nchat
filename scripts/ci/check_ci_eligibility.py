#!/usr/bin/env python3
"""Is this commit eligible for delivery? (issues #931, #933)

Continuous delivery consumes commits that passed `CI / Required`, and only
that. It is a deliberately narrower rule than "every check on the SHA is
green", because `Quality / SonarQube` is advisory (#931): a failed Quality Gate
must not hold back a release, while a scanner that could not run at all stays
visible as its own red job.

That distinction is exactly what makes the obvious implementation wrong. The CI
run's own `conclusion` is `failure` whenever *any* job in it failed, SonarQube
included, so a `workflow_run` handler that trusted it would turn the advisory
gate back into a blocking one -- through the CD pipeline, which is the one
place #931 says it must not reach. The verdict therefore has to be read from
the `CI / Required` job inside the run, by name.

The other half of the job is binding. A CD run is handed a run id by the event
that woke it, and it will deploy a commit; those must be the same commit, and
"the run that passed" and "the code being shipped" must not be allowed to drift
apart between the two. So the run's own metadata is checked as well: the
workflow it belongs to, the branch it ran on, the event that started it, its
attempt number, and above all its head SHA.

The attempt matters because a CI run is not immutable. `/runs/{id}` and
`/runs/{id}/jobs` answer for the *latest* attempt, so a re-run started while an
earlier attempt's handler is still in flight silently moves the ground under
it: the handler woken by attempt 1 would read attempt 2's verdict. Both
directions are wrong -- it could deploy on a pass nobody's event announced, or
refuse a commit that did pass because the newer attempt is still running. The
caller therefore fetches the attempt-scoped endpoints and the attempt number is
compared here, so the run that is read is the run that fired the event.

Reads the `GET /repos/{owner}/{repo}/actions/runs/{run_id}` payload and that
run's `.../jobs` payload, both as files, and the expectations as arguments. It
performs no network access of its own: fetching is the caller's job, and
keeping it out of here is what lets every refusal below be tested from a
fixture.

Usage:
  check_ci_eligibility.py --run RUN.json --jobs JOBS.json \\
      --expect-sha <40 hex> --expect-branch <branch> --expect-attempt <n> \\
      [--expect-workflow CI]

Exit code 0 when the commit is eligible; 1 with one reason per line on stderr
when it is not.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys

# The aggregation job of ci.yml, by its `name:`. Matched exactly: a run holding
# a job called "CI / Required (retry)" has not satisfied this.
REQUIRED_JOB = "CI / Required"
ACCEPTED_CONCLUSION = "success"
# Delivery follows a branch, so the run must be the one a push produced. A
# `pull_request` run of the same tree is a different commit with a different
# merge parent, and `workflow_dispatch` is somebody re-running CI by hand.
ACCEPTED_EVENT = "push"
COMMIT_SHA = re.compile(r"^[0-9a-f]{40}$")


def load(path: str) -> object:
    """One JSON document, or raise ValueError naming the file."""
    try:
        return json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"{path} could not be read as JSON: {error}") from error


def run_problems(run: object, expected: argparse.Namespace) -> list[str]:
    """Everything wrong with the run's own metadata."""
    if not isinstance(run, dict):
        return ["the run payload is not an object"]
    checks = (
        ("name", expected.expect_workflow, "workflow"),
        ("head_branch", expected.expect_branch, "branch"),
        ("head_sha", expected.expect_sha, "head SHA"),
        ("event", ACCEPTED_EVENT, "event"),
        ("run_attempt", expected.expect_attempt, "attempt"),
    )
    return [
        f"the run's {label} is {run.get(key)!r}, expected {want!r}"
        for key, want, label in checks
        if run.get(key) != want
    ]


def required_job(jobs: object) -> dict | str:
    """The `CI / Required` job of the run, or a sentence saying why not."""
    if not isinstance(jobs, dict) or not isinstance(jobs.get("jobs"), list):
        return "the jobs payload carries no job list"
    # A listing that reports more jobs than it contains is a first page. The
    # aggregation could be on the second one, and "it is not in what I was
    # given" would then read as "it did not run".
    total = jobs.get("total_count")
    if isinstance(total, int) and total > len(jobs["jobs"]):
        return f"the jobs listing is partial: {len(jobs['jobs'])} of {total} jobs"
    named = [job for job in jobs["jobs"] if isinstance(job, dict) and job.get("name") == REQUIRED_JOB]
    if not named:
        return f"the run has no {REQUIRED_JOB!r} job; it did not reach the aggregation"
    if len(named) > 1:
        return f"the run reports {len(named)} jobs named {REQUIRED_JOB!r}; the verdict is ambiguous"
    return named[0]


def job_problems(jobs: object) -> list[str]:
    """Everything wrong with the aggregation job's verdict."""
    job = required_job(jobs)
    if isinstance(job, str):
        return [job]
    if job.get("status") != "completed":
        return [f"{REQUIRED_JOB} is {job.get('status')!r}, not completed"]
    if job.get("conclusion") != ACCEPTED_CONCLUSION:
        return [f"{REQUIRED_JOB} concluded {job.get('conclusion')!r}, not {ACCEPTED_CONCLUSION!r}"]
    return []


def parse_arguments(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", required=True)
    parser.add_argument("--jobs", required=True)
    parser.add_argument("--expect-sha", required=True)
    parser.add_argument("--expect-branch", required=True)
    # The attempt the event announced. An int, because that is what the API
    # reports and a string "1" must not compare equal to it.
    parser.add_argument("--expect-attempt", required=True, type=int)
    parser.add_argument("--expect-workflow", default="CI")
    return parser.parse_args(argv)


def problems(expected: argparse.Namespace) -> list[str]:
    """Every reason the commit is not eligible, or an empty list."""
    if not COMMIT_SHA.match(expected.expect_sha):
        return [f"--expect-sha is not a full commit SHA: {expected.expect_sha!r}"]
    if expected.expect_attempt < 1:
        return [f"--expect-attempt is not an attempt number: {expected.expect_attempt!r}"]
    try:
        run, jobs = load(expected.run), load(expected.jobs)
    except ValueError as error:
        return [str(error)]
    return run_problems(run, expected) + job_problems(jobs)


def main(argv: list[str]) -> int:
    expected = parse_arguments(argv)
    refusals = problems(expected)
    if refusals:
        print("This commit is not eligible for delivery:", file=sys.stderr)
        for refusal in refusals:
            print(f"  - {refusal}", file=sys.stderr)
        return 1
    print(
        f"{REQUIRED_JOB} succeeded for {expected.expect_sha} on {expected.expect_branch}"
        f" (attempt {expected.expect_attempt})."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
