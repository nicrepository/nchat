#!/usr/bin/env python3

import importlib.util
import pathlib
import tempfile
import unittest
from pathlib import Path

import yaml

SCRIPT_PATH = Path(__file__).with_name("check_ci_architecture.py")
SPEC = importlib.util.spec_from_file_location("check_ci_architecture", SCRIPT_PATH)
assert SPEC and SPEC.loader
check_ci_architecture = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_ci_architecture)

REQUIRED_GATES = check_ci_architecture.REQUIRED_GATES
OWNED_GATES = check_ci_architecture.OWNED_GATES


def healthy_workflow() -> dict:
    """The smallest workflow that satisfies every rule the check enforces."""
    jobs = {gate: {"steps": []} for gate in sorted(REQUIRED_GATES)}
    for command, owner in OWNED_GATES.items():
        jobs[owner]["steps"].append({"run": command})
    jobs["required"] = {
        "needs": sorted(REQUIRED_GATES),
        "steps": [{"run": "python3 scripts/ci/check_required_gates.py"}],
    }
    jobs["quality-sonarqube"] = {
        "steps": [{"uses": "SonarSource/sonarqube-scan-action@abc", "with": {"args": "-Dsonar.verbose=false"}}]
    }
    return {"name": "CI", "jobs": jobs}


class ArchitectureCheckTest(unittest.TestCase):
    def audit(self, workflow: dict) -> list[str]:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "ci.yml"
            path.write_text(yaml.safe_dump(workflow))
            return check_ci_architecture.audit(Path(directory))

    def test_accepts_a_healthy_architecture(self) -> None:
        self.assertEqual(self.audit(healthy_workflow()), [])

    def test_rejects_a_gate_owned_twice(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["build-web"]["steps"].append({"run": "pnpm build:web"})
        violations = self.audit(workflow)
        self.assertTrue(any("2 owners" in line and "pnpm build:web" in line for line in violations))

    def test_rejects_a_gate_left_without_an_owner(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["e2e-web"]["steps"] = [
            step for step in workflow["jobs"]["e2e-web"]["steps"] if step["run"] != "pnpm test:e2e:web"
        ]
        self.assertIn("Gate has no owner: pnpm test:e2e:web", self.audit(workflow))

    def test_rejects_a_gate_moved_to_the_wrong_owner(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["e2e-web"]["steps"] = [
            step for step in workflow["jobs"]["e2e-web"]["steps"] if step["run"] != "pnpm test:e2e:web"
        ]
        workflow["jobs"]["build-web"]["steps"] = [{"run": "pnpm test:e2e:web"}]
        violations = self.audit(workflow)
        self.assertTrue(any("must be owned by e2e-web" in line for line in violations))

    def test_distinguishes_the_go_unit_and_race_gates(self) -> None:
        # `go-test.sh` and `go-test.sh -race` are different gates with different
        # owners; a substring match would confuse them.
        workflow = healthy_workflow()
        self.assertEqual(
            [line for line in self.audit(workflow) if "go-test.sh" in line],
            [],
        )

    def test_rejects_the_local_mega_gate_inside_actions(self) -> None:
        for aggregator in ("pnpm ci", "pnpm run ci", "make ci"):
            with self.subTest(aggregator=aggregator):
                workflow = healthy_workflow()
                workflow["jobs"]["build-web"]["steps"] = [{"run": aggregator}]
                self.assertTrue(any(aggregator in line for line in self.audit(workflow)))

    def test_rejects_continue_on_error_on_a_gate(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["e2e-web"]["continue-on-error"] = True
        self.assertTrue(any("continue-on-error" in line for line in self.audit(workflow)))

    def test_rejects_a_required_gate_missing_from_the_aggregator(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["required"]["needs"] = sorted(REQUIRED_GATES - {"e2e-web"})
        self.assertIn(
            "required does not depend on the required gate 'e2e-web'.", self.audit(workflow)
        )

    def test_rejects_sonarqube_inside_the_aggregator(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["required"]["needs"] = sorted(REQUIRED_GATES) + ["quality-sonarqube"]
        violations = self.audit(workflow)
        self.assertTrue(any("advisory" in line for line in violations))

    def test_rejects_an_aggregator_that_does_real_work(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["required"]["steps"].append({"run": "pnpm build:web"})
        self.assertTrue(any("executes work" in line for line in self.audit(workflow)))

    def test_rejects_a_missing_aggregator(self) -> None:
        workflow = healthy_workflow()
        del workflow["jobs"]["required"]
        self.assertIn("The aggregation job 'required' is missing.", self.audit(workflow))

    def test_rejects_a_blocking_quality_gate(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["quality-sonarqube"]["steps"][0]["with"]["args"] = (
            "-Dsonar.qualitygate.wait=true"
        )
        self.assertTrue(any("qualitygate.wait" in line for line in self.audit(workflow)))

    def test_rejects_removing_the_scanner_instead_of_its_enforcement(self) -> None:
        workflow = healthy_workflow()
        workflow["jobs"]["quality-sonarqube"]["steps"] = [{"run": "echo skipped"}]
        self.assertTrue(any("advisory means reported" in line for line in self.audit(workflow)))


class GoDatabaseVariableCoverageTest(unittest.TestCase):
    """Names only: a variable no gate script mentions cannot be set by anything.

    The tests below pin both halves of that claim -- what the check catches, and
    what it deliberately does not. See the check's own docstring for why proving
    execution is left to the gate scripts instead.
    """

    def build(self, suite_body: str, *, gate_bodies: dict[str, str] | None = None) -> list[str]:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            suite = root / "services" / "demo-service" / "internal" / "storage"
            suite.mkdir(parents=True)
            (suite / "thing_postgres_test.go").write_text(suite_body)
            gates = root / "scripts" / "ci"
            gates.mkdir(parents=True)
            bodies = gate_bodies if gate_bodies is not None else {}
            for relative in check_ci_architecture.GO_DATABASE_GATE_SCRIPTS:
                name = pathlib.Path(relative).name
                (gates / name).write_text(bodies.get(name, ""))
            return check_ci_architecture.check_go_database_variable_coverage(root)

    def test_accepts_a_variable_an_integration_gate_exports(self) -> None:
        self.assertEqual(
            self.build(
                'dsn := os.Getenv("DEMO_TEST_DATABASE_URL")',
                gate_bodies={"go-integration-test.sh": "DEMO_TEST_DATABASE_URL|./internal/storage"},
            ),
            [],
        )

    def test_accepts_a_variable_the_coverage_gate_exports(self) -> None:
        self.assertEqual(
            self.build(
                'dsn := os.Getenv("DEMO_TEST_DATABASE_URL")',
                gate_bodies={
                    "link-safety-postgres-coverage.sh": 'dsn_var="DEMO_TEST_DATABASE_URL"'
                },
            ),
            [],
        )

    def test_rejects_a_suite_no_gate_script_mentions(self) -> None:
        violations = self.build('dsn := os.Getenv("DEMO_TEST_DATABASE_URL")')
        self.assertEqual(
            violations,
            [
                "DEMO_TEST_DATABASE_URL is read by a Go suite but named by no gate "
                "script, so nothing can set it and every suite reading it skips."
            ],
        )

    def test_rejects_a_variable_dropped_from_the_gate_script(self) -> None:
        # The regression this exists for: the suite keeps reading the variable,
        # someone removes the family from the script, and without this the
        # suite would go on reporting success by skipping.
        suite = 'dsn := os.Getenv("DEMO_TEST_DATABASE_URL")'
        wired = {"go-integration-test.sh": "DEMO_TEST_DATABASE_URL|./internal/storage"}
        self.assertEqual(self.build(suite, gate_bodies=wired), [])
        self.assertTrue(self.build(suite, gate_bodies={"go-integration-test.sh": ""}))

    def test_does_not_claim_the_suite_actually_executes(self) -> None:
        # Honesty test, pinning the documented limitation. The name appears only
        # in a comment, so nothing sets it and the suite really does skip -- yet
        # the check passes, because it compares names and nothing more. Anyone
        # tempted to rely on this check as proof of execution fails here first.
        self.assertEqual(
            self.build(
                'dsn := os.Getenv("DEMO_TEST_DATABASE_URL")',
                gate_bodies={"go-integration-test.sh": "# DEMO_TEST_DATABASE_URL is not used here"},
            ),
            [],
        )

    def test_ignores_a_suite_that_needs_no_database(self) -> None:
        self.assertEqual(self.build("func TestPureUnit(t *testing.T) {}"), [])

    def test_reports_a_deleted_gate_script_instead_of_crashing(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            violations = check_ci_architecture.check_go_database_variable_coverage(pathlib.Path(directory))
        self.assertTrue(violations)
        self.assertTrue(all("is missing" in line for line in violations))

    def test_every_variable_in_this_repository_is_named_by_a_gate(self) -> None:
        self.assertEqual(
            check_ci_architecture.check_go_database_variable_coverage(check_ci_architecture.ROOT), []
        )


class RealWorkflowsTest(unittest.TestCase):
    def test_the_repository_workflows_satisfy_the_architecture(self) -> None:
        self.assertEqual(check_ci_architecture.audit(check_ci_architecture.WORKFLOW_DIRECTORY), [])


if __name__ == "__main__":
    unittest.main()
