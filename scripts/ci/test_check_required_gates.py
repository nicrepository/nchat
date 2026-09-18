#!/usr/bin/env python3

import importlib.util
import unittest
from pathlib import Path

SCRIPT_PATH = Path(__file__).with_name("check_required_gates.py")
SPEC = importlib.util.spec_from_file_location("check_required_gates", SCRIPT_PATH)
assert SPEC and SPEC.loader
check_required_gates = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_required_gates)


def payload(**gates: str) -> str:
    import json

    results = {name: "success" for name in check_required_gates.REQUIRED_GATES}
    results.update(gates)
    return json.dumps({name: {"result": result} for name, result in results.items()})


class ReadResultsTest(unittest.TestCase):
    def test_reads_every_reported_result(self) -> None:
        results = check_required_gates.read_results(
            payload(**{"static-web": "success", "e2e-web": "failure"})
        )
        self.assertEqual(results["static-web"], "success")
        self.assertEqual(results["e2e-web"], "failure")

    def test_refuses_malformed_json(self) -> None:
        with self.assertRaises(ValueError):
            check_required_gates.read_results("{not json")

    def test_refuses_empty_payload(self) -> None:
        for empty in ("", "   ", "{}", "null", "[]"):
            with self.subTest(payload=empty):
                with self.assertRaises(ValueError):
                    check_required_gates.read_results(empty)

    def test_refuses_a_missing_required_gate(self) -> None:
        import json

        results = {name: {"result": "success"} for name in check_required_gates.REQUIRED_GATES}
        del results["e2e-web"]
        with self.assertRaises(ValueError):
            check_required_gates.read_results(json.dumps(results))

    def test_refuses_an_unexpected_gate(self) -> None:
        import json

        results = json.loads(payload())
        results["quality-sonarqube"] = {"result": "success"}
        with self.assertRaises(ValueError):
            check_required_gates.read_results(json.dumps(results))

    def test_treats_a_result_less_gate_as_unknown(self) -> None:
        import json

        gates = json.loads(payload())
        gates["static-web"] = {}
        results = check_required_gates.read_results(json.dumps(gates))
        self.assertEqual(results["static-web"], "")


class FailuresTest(unittest.TestCase):
    def test_accepts_only_success(self) -> None:
        self.assertEqual(check_required_gates.failures({"a": "success"}), [])

    def test_refuses_every_other_conclusion(self) -> None:
        # A required gate that failed, was cancelled, was skipped or reported
        # nothing must never let the aggregation pass.
        for result in ("failure", "cancelled", "skipped", "", "neutral"):
            with self.subTest(result=result):
                self.assertEqual(check_required_gates.failures({"gate": result}), ["gate"])

    def test_names_every_refused_gate(self) -> None:
        self.assertEqual(
            check_required_gates.failures(
                {"e2e-web": "failure", "static-go": "success", "build-web": "cancelled"}
            ),
            ["build-web", "e2e-web"],
        )


class MainTest(unittest.TestCase):
    def run_main(self, value: str | None) -> int:
        import os
        from unittest.mock import patch

        environment = {} if value is None else {"REQUIRED_GATES": value}
        with patch.dict(os.environ, environment, clear=True):
            return check_required_gates.main()

    def test_passes_when_every_gate_succeeded(self) -> None:
        self.assertEqual(self.run_main(payload()), 0)

    def test_fails_when_any_gate_did_not_succeed(self) -> None:
        self.assertEqual(self.run_main(payload(**{"e2e-web": "skipped"})), 1)

    def test_fails_when_the_context_is_missing(self) -> None:
        self.assertEqual(self.run_main(None), 1)


if __name__ == "__main__":
    unittest.main()
