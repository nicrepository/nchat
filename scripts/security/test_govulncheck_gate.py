#!/usr/bin/env python3

"""The gate's whole value is that it is narrower than "ignore this module".

These tests exist to keep it that way: an accepted advisory must not carry any
other advisory through with it, and must stop being accepted once its exception
expires.
"""

import datetime
import importlib.util
import json
import os
import subprocess
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


class ScanRetryTest(unittest.TestCase):
    def run_scan(self, outcomes):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / "attempts"
            scanner = root / "scanner"
            scanner.write_text(
                "#!/usr/bin/env python3\n"
                "import json, pathlib, sys\n"
                f"state = pathlib.Path({str(state)!r})\n"
                "attempt = int(state.read_text()) if state.exists() else 0\n"
                "state.write_text(str(attempt + 1))\n"
                f"outcomes = {outcomes!r}\n"
                "status, message = outcomes[min(attempt, len(outcomes) - 1)]\n"
                "print(json.dumps({'attempt': attempt + 1}))\n"
                "print(message, file=sys.stderr)\n"
                "sys.exit(status)\n"
            )
            scanner.chmod(0o755)
            sleeper = root / "sleep"
            sleeper.write_text("#!/bin/sh\nexit 0\n")
            sleeper.chmod(0o755)
            report_path = root / "report.json"
            helper = REPO_ROOT / "scripts/security/govulncheck-retry.sh"
            result = subprocess.run(
                ["bash", "-c", 'source "$1"; govulncheck_retry "$2" "$3" "$4"',
                 "retry-test", str(helper), str(report_path), str(root / "stderr"), str(scanner)],
                env={**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"]},
                capture_output=True, text=True, check=False,
            )
            return result.returncode, int(state.read_text()), json.loads(report_path.read_text())

    def test_transient_fetch_recovers_with_a_fresh_report(self):
        transient = "govulncheck: fetching vulnerabilities: Get URL: connection reset by peer"
        self.assertEqual(self.run_scan([(1, transient), (0, "")]), (0, 2, {"attempt": 2}))
        self.assertEqual(self.run_scan([(1, transient), (3, "findings")]), (3, 2, {"attempt": 2}))

    def test_errors_remain_fail_closed(self):
        for status, message, attempts in [
            (1, "govulncheck: fetching vulnerabilities: connection reset by peer", 3),
            (1, "package loading failed", 1),
            (1, "connection reset by peer while loading packages", 1),
            (3, "vulnerabilities found", 1),
            (2, "invalid arguments", 1),
        ]:
            with self.subTest(message=message):
                self.assertEqual(self.run_scan([(status, message)]),
                                 (status, attempts, {"attempt": attempts}))


if __name__ == "__main__":
    unittest.main()
