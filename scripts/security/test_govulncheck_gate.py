#!/usr/bin/env python3

"""The gate's whole value is that it is narrower than "ignore this module".

These tests exist to keep it that way: an accepted advisory must not carry any
other advisory through with it, and must stop being accepted once its exception
expires.
"""

import datetime
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).with_name("govulncheck_gate.py")
SPEC = importlib.util.spec_from_file_location("govulncheck_gate", SCRIPT_PATH)
assert SPEC and SPEC.loader
govulncheck_gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(govulncheck_gate)

REPO_ROOT = Path(__file__).resolve().parents[2]
ACCEPTED_ADVISORY = "GO-2026-6452"


def finding(advisory: str, *, called: bool) -> dict:
    """One govulncheck finding. A frame with a function is a real call site."""
    frame = {"module": "example.com/dep", "package": "example.com/dep"}
    if called:
        frame = {**frame, "function": "Vulnerable"}
    return {"finding": {"osv": advisory, "trace": [frame]}}


def report(*findings: dict) -> str:
    """govulncheck writes concatenated objects, so the gate must not assume NDJSON."""
    return "\n".join(json.dumps(item, indent=2) for item in findings)


class DecodeStreamTest(unittest.TestCase):
    def test_reads_concatenated_pretty_printed_objects(self) -> None:
        messages = govulncheck_gate.decode_stream(
            report(finding("GO-0000-0001", called=True), finding("GO-0000-0002", called=False))
        )
        self.assertEqual(len(messages), 2)

    def test_empty_report_is_not_an_error(self) -> None:
        self.assertEqual(govulncheck_gate.decode_stream("  \n "), [])


class CalledAdvisoriesTest(unittest.TestCase):
    def test_counts_only_advisories_reaching_a_call_site(self) -> None:
        messages = govulncheck_gate.decode_stream(
            report(finding("GO-0000-0001", called=True), finding("GO-0000-0002", called=False))
        )
        self.assertEqual(govulncheck_gate.called_advisories(messages), {"GO-0000-0001"})


class ExceptionsTest(unittest.TestCase):
    def load(self, body: str, today: datetime.date):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "ignore.yaml"
            path.write_text(body)
            return govulncheck_gate.load_exceptions(path, today)

    def test_accepts_a_complete_unexpired_entry(self) -> None:
        accepted, problems = self.load(
            "vulnerabilities:\n"
            "  - id: GO-0000-0001\n"
            "    statement: why\n"
            "    tracking: https://example.com/1\n"
            "    expired_at: 2099-01-01\n",
            datetime.date(2026, 9, 17),
        )
        self.assertEqual((accepted, problems), ({"GO-0000-0001"}, []))

    def test_expired_entry_stops_being_accepted(self) -> None:
        accepted, problems = self.load(
            "vulnerabilities:\n"
            "  - id: GO-0000-0001\n"
            "    statement: why\n"
            "    tracking: https://example.com/1\n"
            "    expired_at: 2020-01-01\n",
            datetime.date(2026, 9, 17),
        )
        self.assertEqual(accepted, set())
        self.assertTrue(problems)

    def test_entry_without_justification_or_tracking_is_refused(self) -> None:
        accepted, problems = self.load(
            "vulnerabilities:\n  - id: GO-0000-0001\n    expired_at: 2099-01-01\n",
            datetime.date(2026, 9, 17),
        )
        self.assertEqual(accepted, set())
        self.assertTrue(problems)

    def test_missing_file_accepts_nothing(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            accepted, problems = govulncheck_gate.load_exceptions(
                Path(directory) / "absent.yaml", datetime.date(2026, 9, 17)
            )
        self.assertEqual((accepted, problems), (set(), []))


class GateVerdictTest(unittest.TestCase):
    def run_gate(self, body: str) -> int:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            path.write_text(body)
            return govulncheck_gate.main(["govulncheck_gate.py", str(path)])

    def test_repository_exception_lets_its_own_advisory_through(self) -> None:
        self.assertEqual(self.run_gate(report(finding(ACCEPTED_ADVISORY, called=True))), 0)

    # The one that matters: accepting an advisory must not blunt the gate.
    def test_a_different_called_advisory_still_fails(self) -> None:
        self.assertEqual(self.run_gate(report(finding("GO-2099-9999", called=True))), 1)

    def test_accepted_advisory_does_not_carry_a_second_one(self) -> None:
        self.assertEqual(
            self.run_gate(
                report(
                    finding(ACCEPTED_ADVISORY, called=True),
                    finding("GO-2099-9999", called=True),
                )
            ),
            1,
        )

    def test_uncalled_advisory_is_not_a_failure(self) -> None:
        self.assertEqual(self.run_gate(report(finding("GO-2099-9999", called=False))), 0)

    def test_clean_report_passes(self) -> None:
        self.assertEqual(self.run_gate(""), 0)


class RepositoryExceptionFileTest(unittest.TestCase):
    """The committed file itself, so a careless edit is caught here."""

    def test_accepts_exactly_the_documented_advisory(self) -> None:
        accepted, problems = govulncheck_gate.load_exceptions(
            REPO_ROOT / ".govulncheckignore.yaml", datetime.date.today()
        )
        self.assertEqual(problems, [])
        self.assertEqual(accepted, {ACCEPTED_ADVISORY})


if __name__ == "__main__":
    unittest.main()
