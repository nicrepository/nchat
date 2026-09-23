#!/usr/bin/env python3
"""Behaviour tests for the CI/CD eligibility gate (issues #931, #933).

Every case is a refusal except the two that must not be. The gate decides
whether a commit may be deployed at all, so what it has to get right is saying
no: to a run that failed, to one whose aggregation never finished, to a run of
a different branch or a different commit, and -- the case the obvious
implementation gets wrong -- it has to say *yes* to a run whose overall
conclusion is failure because the advisory SonarQube job failed while
`CI / Required` passed.
"""

from __future__ import annotations

import copy
import json
import pathlib
import sys
import tempfile
import unittest

SCRIPT_DIRECTORY = pathlib.Path(__file__).resolve().parent
if str(SCRIPT_DIRECTORY) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIRECTORY))

import check_ci_eligibility as gate  # noqa: E402

SHA = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"
OTHER_SHA = "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1"

# A CI run as the API reports it. `conclusion` is deliberately present and
# deliberately never read: the whole point of this gate is that the run's own
# conclusion is not the verdict.
RUN = {
    "id": 42,
    "name": "CI",
    "head_branch": "main",
    "head_sha": SHA,
    "event": "push",
    "run_attempt": 1,
    "status": "completed",
    "conclusion": "success",
}
JOBS = {
    "total_count": 3,
    "jobs": [
        {"name": "Static / Web", "status": "completed", "conclusion": "success"},
        {"name": "CI / Required", "status": "completed", "conclusion": "success"},
        {"name": "Quality / SonarQube", "status": "completed", "conclusion": "success"},
    ],
}


def job_named(jobs: dict, name: str) -> dict:
    return next(job for job in jobs["jobs"] if job["name"] == name)


class Eligibility(unittest.TestCase):
    def verdict(self, run=None, jobs=None, sha=SHA, branch="main", attempt="1") -> int:
        with tempfile.TemporaryDirectory() as directory:
            base = pathlib.Path(directory)
            run_path, jobs_path = base / "run.json", base / "jobs.json"
            run_path.write_text(json.dumps(RUN if run is None else run), encoding="utf-8")
            jobs_path.write_text(json.dumps(JOBS if jobs is None else jobs), encoding="utf-8")
            return gate.main(
                [
                    "--run", str(run_path),
                    "--jobs", str(jobs_path),
                    "--expect-sha", sha,
                    "--expect-branch", branch,
                    "--expect-attempt", attempt,
                ]
            )

    def with_run(self, **changes) -> dict:
        run = copy.deepcopy(RUN)
        run.update(changes)
        return run

    def with_required(self, **changes) -> dict:
        jobs = copy.deepcopy(JOBS)
        job_named(jobs, "CI / Required").update(changes)
        return jobs

    # --- the two acceptances -------------------------------------------------

    def test_a_passing_run_is_eligible(self):
        self.assertEqual(0, self.verdict())

    def test_a_failed_sonarqube_does_not_block_delivery(self):
        """#931: the Quality Gate is advisory and must not reach the CD gate.

        The run's own conclusion is `failure` here, which is exactly what a
        `workflow_run` handler would have seen. Reading it would turn the
        advisory gate into a blocking one through the CD pipeline.
        """
        jobs = copy.deepcopy(JOBS)
        job_named(jobs, "Quality / SonarQube").update(conclusion="failure")
        self.assertEqual(
            0, self.verdict(run=self.with_run(conclusion="failure"), jobs=jobs)
        )

    # --- the aggregation's verdict -------------------------------------------

    def test_a_failed_aggregation_is_refused(self):
        self.assertEqual(1, self.verdict(jobs=self.with_required(conclusion="failure")))

    def test_a_cancelled_aggregation_is_refused(self):
        self.assertEqual(1, self.verdict(jobs=self.with_required(conclusion="cancelled")))

    def test_a_skipped_aggregation_is_refused(self):
        self.assertEqual(1, self.verdict(jobs=self.with_required(conclusion="skipped")))

    def test_an_unfinished_aggregation_is_refused(self):
        self.assertEqual(
            1, self.verdict(jobs=self.with_required(status="in_progress", conclusion=None))
        )

    def test_a_missing_aggregation_is_refused(self):
        jobs = copy.deepcopy(JOBS)
        jobs["jobs"] = [job for job in jobs["jobs"] if job["name"] != "CI / Required"]
        jobs["total_count"] = len(jobs["jobs"])
        self.assertEqual(1, self.verdict(jobs=jobs))

    def test_a_lookalike_aggregation_is_refused(self):
        jobs = copy.deepcopy(JOBS)
        job_named(jobs, "CI / Required")["name"] = "CI / Required (retry)"
        self.assertEqual(1, self.verdict(jobs=jobs))

    def test_two_aggregations_are_ambiguous_and_refused(self):
        jobs = copy.deepcopy(JOBS)
        jobs["jobs"].append({"name": "CI / Required", "status": "completed", "conclusion": "failure"})
        jobs["total_count"] = len(jobs["jobs"])
        self.assertEqual(1, self.verdict(jobs=jobs))

    def test_a_partial_job_listing_is_refused(self):
        """The aggregation could be on the page that was not fetched."""
        jobs = copy.deepcopy(JOBS)
        jobs["total_count"] = 120
        self.assertEqual(1, self.verdict(jobs=jobs))

    # --- the binding between the run and the commit ---------------------------

    def test_a_different_commit_is_refused(self):
        self.assertEqual(1, self.verdict(sha=OTHER_SHA))

    def test_a_run_reporting_a_different_head_is_refused(self):
        self.assertEqual(1, self.verdict(run=self.with_run(head_sha=OTHER_SHA)))

    def test_a_run_of_another_branch_is_refused(self):
        self.assertEqual(1, self.verdict(run=self.with_run(head_branch="develop")))

    def test_a_develop_run_is_not_eligible_for_a_main_delivery(self):
        self.assertEqual(1, self.verdict(branch="develop"))

    def test_a_pull_request_run_is_refused(self):
        """A PR run is a different commit with a different merge parent."""
        self.assertEqual(1, self.verdict(run=self.with_run(event="pull_request")))

    def test_a_hand_dispatched_ci_run_is_refused(self):
        self.assertEqual(1, self.verdict(run=self.with_run(event="workflow_dispatch")))

    def test_a_run_of_another_workflow_is_refused(self):
        self.assertEqual(1, self.verdict(run=self.with_run(name="Nightly")))

    def test_an_abbreviated_sha_is_refused(self):
        self.assertEqual(1, self.verdict(sha=SHA[:12]))

    def test_an_uppercase_sha_is_refused(self):
        self.assertEqual(1, self.verdict(sha=SHA.upper()))

    # --- the attempt that fired the event -------------------------------------

    def test_a_later_attempt_is_refused(self):
        """A re-run must not answer for the attempt whose event woke the handler.

        `/runs/{id}` and `/runs/{id}/jobs` report the latest attempt, so a
        re-run started while this handler is in flight would have it read a
        verdict its own event never announced.
        """
        self.assertEqual(1, self.verdict(run=self.with_run(run_attempt=2), attempt="1"))

    def test_an_earlier_attempt_is_refused(self):
        self.assertEqual(1, self.verdict(run=self.with_run(run_attempt=1), attempt="2"))

    def test_a_matching_later_attempt_is_eligible(self):
        """Attempt 2 passing, read by attempt 2's own handler, is a release."""
        self.assertEqual(0, self.verdict(run=self.with_run(run_attempt=2), attempt="2"))

    def test_a_run_reporting_no_attempt_is_refused(self):
        run = copy.deepcopy(RUN)
        del run["run_attempt"]
        self.assertEqual(1, self.verdict(run=run))

    def test_a_string_attempt_does_not_match_the_integer_the_api_reports(self):
        self.assertEqual(1, self.verdict(run=self.with_run(run_attempt="1")))

    def test_attempt_zero_is_not_an_attempt(self):
        self.assertEqual(1, self.verdict(run=self.with_run(run_attempt=0), attempt="0"))

    # --- unreadable input is never eligibility --------------------------------

    def test_an_unreadable_run_payload_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            base = pathlib.Path(directory)
            (base / "jobs.json").write_text(json.dumps(JOBS), encoding="utf-8")
            self.assertEqual(
                1,
                gate.main(
                    [
                        "--run", str(base / "absent.json"),
                        "--jobs", str(base / "jobs.json"),
                        "--expect-sha", SHA,
                        "--expect-branch", "main",
                        "--expect-attempt", "1",
                    ]
                ),
            )

    def test_a_jobs_payload_without_a_job_list_is_refused(self):
        self.assertEqual(1, self.verdict(jobs={"message": "Not Found"}))

    def test_an_empty_run_payload_is_refused(self):
        self.assertEqual(1, self.verdict(run={}))


if __name__ == "__main__":
    unittest.main()
