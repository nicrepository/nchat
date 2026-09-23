#!/usr/bin/env python3
"""Behaviour tests for the continuous delivery workflow contracts.

An exact-contract checker has one failure mode that matters: it passes because
it is not looking, rather than because the workflows are right. A green run
against the real files proves nothing on its own -- the same green appears if
every comparison is against a dictionary nobody reads.

So what is tested here is refusal. Each case takes the real workflows, breaks
exactly one invariant in one of them, and requires the checker to say no. The
cases are the invariants of issues #626, #714, #801, #931 and #933 stated as
the mutations that would violate them: a promotion added to the automatic
preparation, a third job, a weakened concurrency lock, a rollback that runs a
migration, a schema gate removed, an input that stops being required.

The baseline case -- the real workflows, unmutated -- runs last, so a suite
that could only ever pass is visible as a suite in which nothing was refused.
"""

from __future__ import annotations

import copy
import pathlib
import sys
import tempfile
import unittest

import yaml

SCRIPT_DIRECTORY = pathlib.Path(__file__).resolve().parent
if str(SCRIPT_DIRECTORY) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIRECTORY))

import check_cd_workflows as checker  # noqa: E402

ROOT = SCRIPT_DIRECTORY.parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"
PREPARE = checker.PREPARE
CUTOVER = checker.CUTOVER
ROLLBACK = checker.ROLLBACK
DEVELOP = checker.DEVELOP
DEV_DEPLOY = checker.DEV_DEPLOY
DEPLOY_RUNNER = checker.DEPLOY_RUNNER


def triggers(workflow: dict) -> dict:
    """`on:` is YAML 1.1 for True, which is how PyYAML keys it."""
    return workflow[True] if True in workflow else workflow["on"]


def dispatch_inputs(workflow: dict) -> dict:
    return triggers(workflow)["workflow_dispatch"]["inputs"]


class ProductionWorkflowContracts(unittest.TestCase):
    """Every case mutates one real workflow and requires a refusal."""

    @classmethod
    def setUpClass(cls) -> None:
        cls.source = {
            name: yaml.safe_load((WORKFLOWS / name).read_text(encoding="utf-8"))
            for name in (PREPARE, CUTOVER, ROLLBACK, DEVELOP, DEV_DEPLOY)
        }

    def verdict(self, mutations: dict) -> int:
        """The checker's exit code against the workflows with `mutations` applied."""
        with tempfile.TemporaryDirectory() as directory:
            target = pathlib.Path(directory)
            for name, document in self.source.items():
                document = copy.deepcopy(document)
                if name in mutations:
                    mutations[name](document)
                (target / name).write_text(yaml.safe_dump(document, sort_keys=False), encoding="utf-8")
            return checker.run(target)

    def refuses(self, name: str, mutate, because: str) -> None:
        self.assertEqual(1, self.verdict({name: mutate}), because)

    # --- the automatic half may not promote --------------------------------

    def test_preparation_may_not_call_cutover(self):
        def mutate(workflow):
            workflow["jobs"]["candidate"]["steps"].append(
                {"run": 'scripts/deploy/nchat-prod/cutover.sh --target "$CANDIDATE_SLOT"'}
            )

        self.refuses(PREPARE, mutate, "an automatic run must never promote")

    def test_preparation_may_not_call_rollback(self):
        def mutate(workflow):
            workflow["jobs"]["candidate"]["steps"].append(
                {"run": 'scripts/deploy/nchat-prod/rollback.sh --target blue "auto"'}
            )

        self.refuses(PREPARE, mutate, "an automatic run must never roll back")

    def test_preparation_may_not_patch_a_stable_service(self):
        def mutate(workflow):
            workflow["jobs"]["candidate"]["steps"][9]["run"] = (
                "kubectl patch service nchat-web -n nchat-prod -p '{}'"
            )

        self.refuses(PREPARE, mutate, "selector patches belong only to the canonical scripts")

    def test_preparation_may_not_grow_a_job(self):
        def mutate(workflow):
            workflow["jobs"]["promote"] = {
                "needs": "candidate",
                "runs-on": DEPLOY_RUNNER,
                "steps": [{"run": "true"}],
            }

        self.refuses(PREPARE, mutate, "a fourth job is a lever nobody reviewed")

    def test_preparation_may_not_be_dispatched_by_hand(self):
        def mutate(workflow):
            triggers(workflow)["workflow_dispatch"] = None

        self.refuses(PREPARE, mutate, "preparation follows CI, it is not an operator action")

    # --- the CI boundary ----------------------------------------------------

    def test_preparation_may_not_drop_the_eligibility_gate(self):
        def mutate(workflow):
            del workflow["jobs"]["eligibility"]
            workflow["jobs"]["build"]["with"]["sha"] = "${{ github.sha }}"

        self.refuses(PREPARE, mutate, "CD consumes commits that passed CI / Required")

    def test_preparation_may_not_build_an_unproved_commit(self):
        def mutate(workflow):
            workflow["jobs"]["build"]["with"]["require_main"] = False

        self.refuses(PREPARE, mutate, "a production build proves reachability from main")

    def test_preparation_may_not_build_a_different_commit(self):
        def mutate(workflow):
            workflow["jobs"]["build"]["with"]["sha"] = "${{ github.event.workflow_run.head_sha }}"

        self.refuses(PREPARE, mutate, "the deployed commit is the one eligibility returned")

    # --- issue #714: least privilege ----------------------------------------

    def test_capacity_may_not_be_collected_by_the_deployer(self):
        def mutate(workflow):
            workflow["jobs"]["capacity"]["runs-on"] = DEPLOY_RUNNER

        self.refuses(PREPARE, mutate, "the deployer must stay namespaced")

    def test_the_deployer_may_not_gain_write_permission(self):
        def mutate(workflow):
            workflow["jobs"]["candidate"]["permissions"]["packages"] = "write"

        self.refuses(PREPARE, mutate, "the candidate job reads; it publishes nothing")

    def test_capacity_evidence_may_not_come_from_an_unverified_path(self):
        def mutate(workflow):
            step = workflow["jobs"]["candidate"]["steps"][9]
            step["env"]["NCHAT_PROD_CAPACITY_EVIDENCE_DIR"] = (
                "${{ vars.NCHAT_PROD_CAPACITY_EVIDENCE_DIR }}"
            )

        self.refuses(PREPARE, mutate, "the evidence is this run's collection, not a standing directory")

    def test_the_capacity_collection_may_not_be_dropped(self):
        def mutate(workflow):
            del workflow["jobs"]["capacity"]

        self.refuses(PREPARE, mutate, "no evidence must never become enough capacity")

    # --- the candidate's own proofs -----------------------------------------

    def test_the_candidate_smoke_may_not_be_dropped(self):
        def mutate(workflow):
            del workflow["jobs"]["candidate"]["steps"][10]

        self.refuses(PREPARE, mutate, "a candidate nobody smoked is not promotable")

    def test_the_selector_invariant_may_not_be_dropped(self):
        def mutate(workflow):
            del workflow["jobs"]["candidate"]["steps"][11]

        self.refuses(PREPARE, mutate, "preparation must prove it moved no traffic")

    def test_the_lifecycle_gate_may_not_be_bypassed(self):
        def mutate(workflow):
            step = workflow["jobs"]["candidate"]["steps"][8]
            step["run"] = (
                'echo "candidate=$(scripts/deploy/nchat-prod/status.sh | tail -1)" >>"$GITHUB_OUTPUT"'
            )

        self.refuses(PREPARE, mutate, "the reserved rollback slot must not be overwritten")

    # --- the shared mutation lock -------------------------------------------

    def test_cutover_may_not_leave_the_shared_lock(self):
        def mutate(workflow):
            workflow["concurrency"]["group"] = "nchat-prod-cutover"

        self.refuses(CUTOVER, mutate, "a cutover must not run beside a candidate deploy")

    def test_rollback_may_not_leave_the_shared_lock(self):
        def mutate(workflow):
            workflow["concurrency"]["group"] = "nchat-prod-rollback"

        self.refuses(ROLLBACK, mutate, "a rollback must not run beside a cutover")

    def test_preparation_may_not_leave_the_shared_lock(self):
        def mutate(workflow):
            workflow["jobs"]["candidate"]["concurrency"]["group"] = "nchat-prod-candidate"

        self.refuses(PREPARE, mutate, "a candidate deploy must not run beside a rollback")

    def test_preparation_may_not_drop_the_lock_entirely(self):
        def mutate(workflow):
            del workflow["jobs"]["candidate"]["concurrency"]

        self.refuses(PREPARE, mutate, "the mutating job must hold the production lock")

    def test_preparation_may_not_hold_the_lock_across_the_build(self):
        """The lock belongs on the mutating job, not on the whole workflow.

        Taking it at the root would hold it through eligibility, an image build
        and the capacity collection -- twenty minutes in which nchat-prod is
        not touched at all, and in which an incident rollback would be queued
        behind a build.
        """

        def mutate(workflow):
            del workflow["jobs"]["candidate"]["concurrency"]
            workflow["concurrency"] = {
                "group": "nchat-prod-release-mutation",
                "cancel-in-progress": False,
            }

        self.refuses(PREPARE, mutate, "an incident rollback must not wait for an image build")

    def test_a_quoted_false_does_not_disable_cancellation(self):
        def mutate(workflow):
            workflow["concurrency"]["cancel-in-progress"] = "false"

        self.refuses(CUTOVER, mutate, "the string 'false' is truthy to the expression evaluator")

    def test_a_quoted_false_does_not_disable_cancellation_on_the_candidate(self):
        def mutate(workflow):
            workflow["jobs"]["candidate"]["concurrency"]["cancel-in-progress"] = "false"

        self.refuses(PREPARE, mutate, "a half-built candidate must not be cancelled")

    # --- the cutover ---------------------------------------------------------

    def test_cutover_may_not_drop_the_production_environment(self):
        def mutate(workflow):
            del workflow["jobs"]["cutover"]["environment"]

        self.refuses(CUTOVER, mutate, "the environment is the governance boundary")

    def test_cutover_may_not_promote_without_revalidating(self):
        def mutate(workflow):
            del workflow["jobs"]["cutover"]["steps"][2]

        self.refuses(CUTOVER, mutate, "a candidate is revalidated against the cluster first")

    def test_cutover_may_not_take_its_target_from_an_input(self):
        def mutate(workflow):
            triggers(workflow)["workflow_dispatch"] = {
                "inputs": {"slot": {"required": True, "type": "string"}}
            }
            workflow["jobs"]["cutover"]["steps"][5]["env"]["CANDIDATE_SLOT"] = "${{ inputs.slot }}"

        self.refuses(CUTOVER, mutate, "the candidate is derived and proved, never typed")

    def test_cutover_may_not_reuse_the_candidate_smoke(self):
        def mutate(workflow):
            workflow["jobs"]["cutover"]["steps"][10]["run"] = (
                'scripts/deploy/nchat-prod/smoke.sh --target "$CANDIDATE_SLOT"'
            )

        self.refuses(CUTOVER, mutate, "the candidate smoke requires isolation and cannot follow traffic")

    def test_cutover_may_not_smoke_without_recording_the_result(self):
        """The retirement gate reads this record; an unrecorded pass blocks it.

        Calling stable-smoke.sh directly still smokes, and still fails the run
        on a bad release -- but it leaves no evidence, so the next release
        could not tell a release that passed from one nobody proved.
        """

        def mutate(workflow):
            workflow["jobs"]["cutover"]["steps"][10]["run"] = (
                'scripts/deploy/nchat-prod/stable-smoke.sh --target "$CANDIDATE_SLOT" --after cutover'
            )

        self.refuses(CUTOVER, mutate, "the post-cutover smoke result must be recorded")

    def test_rollback_may_not_smoke_without_recording_the_result(self):
        def mutate(workflow):
            workflow["jobs"]["rollback"]["steps"][8]["run"] = (
                'scripts/deploy/nchat-prod/stable-smoke.sh --target "$TARGET_SLOT" --after rollback'
            )

        self.refuses(ROLLBACK, mutate, "the post-rollback smoke result must be recorded")

    def test_cutover_may_not_drop_the_post_cutover_smoke(self):
        def mutate(workflow):
            del workflow["jobs"]["cutover"]["steps"][10]

        self.refuses(CUTOVER, mutate, "a promotion nobody probed is not a promotion anyone proved")

    def test_cutover_may_not_drain_the_previous_slot(self):
        def mutate(workflow):
            workflow["jobs"]["cutover"]["steps"].append(
                {"run": 'scripts/deploy/nchat-prod/drain-old.sh --target "$ROLLBACK_SLOT"'}
            )

        self.refuses(CUTOVER, mutate, "the previous slot is the rollback and stays running")

    def test_cutover_may_not_roll_back_automatically(self):
        def mutate(workflow):
            workflow["jobs"]["cutover"]["steps"].append(
                {
                    "if": "failure()",
                    "run": 'scripts/deploy/nchat-prod/rollback.sh --target "$ROLLBACK_SLOT" "smoke failed"',
                }
            )

        self.refuses(CUTOVER, mutate, "a failing smoke is not a second unattended traffic move")

    def test_an_after_step_may_not_be_gated_loosely(self):
        def mutate(workflow):
            workflow["jobs"]["cutover"]["steps"][6]["if"] = "${{ !cancelled() }}"

        self.refuses(CUTOVER, mutate, "a step skipped after a failure must not query production")

    # --- the rollback (issue #801) -------------------------------------------

    def test_rollback_is_manual_only(self):
        def mutate(workflow):
            triggers(workflow)["workflow_run"] = {"workflows": ["CI"], "types": ["completed"]}

        self.refuses(ROLLBACK, mutate, "a rollback is an operator's decision")

    def test_rollback_requires_a_reason(self):
        def mutate(workflow):
            dispatch_inputs(workflow)["reason"]["required"] = False

        self.refuses(ROLLBACK, mutate, "the reason is the audit record")

    def test_rollback_target_may_not_have_a_default(self):
        def mutate(workflow):
            dispatch_inputs(workflow)["target_slot"]["default"] = "blue"

        self.refuses(ROLLBACK, mutate, "a defaulted target is a rollback nobody chose")

    def test_rollback_target_stays_an_allowlist(self):
        def mutate(workflow):
            dispatch_inputs(workflow)["target_slot"] = {"required": True, "type": "string"}

        self.refuses(ROLLBACK, mutate, "the slot is blue or green and nothing else")

    def test_rollback_may_not_evaluate_the_reason(self):
        def mutate(workflow):
            workflow["jobs"]["rollback"]["steps"][4]["run"] = (
                'eval "scripts/deploy/nchat-prod/rollback.sh --target $TARGET_SLOT $REASON"'
            )

        self.refuses(ROLLBACK, mutate, "a dispatch input is never a shell fragment")

    def test_rollback_may_not_skip_the_schema_gate(self):
        def mutate(workflow):
            del workflow["jobs"]["rollback"]["steps"][3]

        self.refuses(ROLLBACK, mutate, "Ready is not the same property as schema-compatible")

    def test_rollback_may_not_run_a_migration(self):
        def mutate(workflow):
            workflow["jobs"]["rollback"]["steps"].insert(4, {"run": "pnpm migrations:down"})

        self.refuses(ROLLBACK, mutate, "application rollback is not database rollback")

    def test_rollback_may_not_build(self):
        def mutate(workflow):
            workflow["jobs"]["rollback"]["steps"].insert(
                2, {"uses": "./.github/workflows/images.yml", "with": {"sha": "${{ github.sha }}"}}
            )

        self.refuses(ROLLBACK, mutate, "a rollback moves selectors and builds nothing")

    def test_rollback_may_not_drop_the_convergence_proof(self):
        def mutate(workflow):
            del workflow["jobs"]["rollback"]["steps"][6]

        self.refuses(ROLLBACK, mutate, "a mixed state must fail explicitly")

    def test_rollback_may_not_drop_the_post_rollback_smoke(self):
        def mutate(workflow):
            del workflow["jobs"]["rollback"]["steps"][8]

        self.refuses(ROLLBACK, mutate, "the restored release is proved, not assumed")

    # --- supply chain --------------------------------------------------------

    def test_actions_stay_pinned_by_commit(self):
        def mutate(workflow):
            workflow["jobs"]["cutover"]["steps"][1]["uses"] = "actions/checkout@v4"

        self.refuses(CUTOVER, mutate, "a moved tag is a different action")

    def test_production_checkouts_do_not_keep_credentials(self):
        def mutate(workflow):
            workflow["jobs"]["cutover"]["steps"][1]["with"]["persist-credentials"] = True

        self.refuses(CUTOVER, mutate, "no token is left in the workspace of a production job")

    def test_production_workflows_refuse_pull_request_target(self):
        def mutate(workflow):
            triggers(workflow)["pull_request_target"] = {"branches": ["main"]}

        self.refuses(CUTOVER, mutate, "pull_request_target runs untrusted code with a write token")

    def test_a_shell_default_may_not_wrap_every_step(self):
        def mutate(workflow):
            workflow["defaults"] = {"run": {"shell": "bash -c 'true; {0}'"}}

        self.refuses(CUTOVER, mutate, "the root is an allowlist because defaults can rewrite every run")

    # --- CD / Develop ---------------------------------------------------------

    def test_develop_may_not_deploy_without_the_eligibility_gate(self):
        """#933: a develop commit is deployed only after CI / Required passed.

        Before this, `images.yml` built on push and raced the CI of the same
        commit, so a commit CI then failed could already be on nchat-dev.
        """

        def mutate(workflow):
            del workflow["jobs"]["eligibility"]
            workflow["jobs"]["build"]["with"]["sha"] = "${{ github.sha }}"
            workflow["jobs"]["deploy"]["with"]["sha"] = "${{ github.sha }}"

        self.refuses(DEVELOP, mutate, "dev CD consumes commits that passed CI / Required")

    def test_develop_may_not_deploy_a_commit_other_than_the_eligible_one(self):
        def mutate(workflow):
            workflow["jobs"]["deploy"]["with"]["sha"] = "${{ github.sha }}"

        self.refuses(DEVELOP, mutate, "a workflow_run's github.sha is not the commit CI passed")

    def test_develop_may_not_build_and_deploy_different_commits(self):
        def mutate(workflow):
            workflow["jobs"]["build"]["with"]["sha"] = "${{ github.event.workflow_run.head_sha }}"

        self.refuses(DEVELOP, mutate, "the images deployed are the ones this run built")

    def test_develop_may_not_deploy_before_the_build(self):
        def mutate(workflow):
            workflow["jobs"]["deploy"]["needs"] = "eligibility"

        self.refuses(DEVELOP, mutate, "the deploy consumes the build's digest artifacts")

    def test_develop_may_not_claim_a_production_build(self):
        def mutate(workflow):
            workflow["jobs"]["build"]["with"]["require_main"] = True

        self.refuses(DEVELOP, mutate, "develop is not reachable from main")

    def test_develop_may_not_build_on_push(self):
        def mutate(workflow):
            triggers(workflow)["push"] = {"branches": ["develop"]}

        self.refuses(DEVELOP, mutate, "building on push races the CI of the same commit")

    def test_develop_may_not_share_the_production_lock(self):
        def mutate(workflow):
            workflow["concurrency"]["group"] = "nchat-prod-release-mutation"

        self.refuses(DEVELOP, mutate, "a dev deploy must not delay a production rollback")

    def test_develop_may_not_cancel_a_running_deploy(self):
        def mutate(workflow):
            workflow["concurrency"]["cancel-in-progress"] = True

        self.refuses(DEVELOP, mutate, "a run mid-migration must not be cancelled for a newer commit")

    def test_develop_may_not_grow_a_blue_green_job(self):
        def mutate(workflow):
            workflow["jobs"]["cutover"] = {
                "needs": "deploy",
                "runs-on": "ubuntu-latest",
                "steps": [{"run": "scripts/deploy/nchat-prod/cutover.sh --target green"}],
            }

        self.refuses(DEVELOP, mutate, "nchat-dev is single-slot")

    def test_the_dev_deploy_may_not_drop_the_smoke(self):
        def mutate(workflow):
            del workflow["jobs"]["deploy"]["steps"][6]

        self.refuses(DEV_DEPLOY, mutate, "rollout completion is not delivery")

    def test_the_dev_deploy_may_not_drop_the_summary(self):
        def mutate(workflow):
            del workflow["jobs"]["deploy"]["steps"][7]

        self.refuses(DEV_DEPLOY, mutate, "the run must report what it deployed")

    def test_the_dev_smoke_may_not_be_made_advisory(self):
        def mutate(workflow):
            workflow["jobs"]["deploy"]["steps"][6]["continue-on-error"] = True

        self.refuses(DEV_DEPLOY, mutate, "a failed smoke must fail the delivery")

    def test_the_dev_deploy_may_not_deploy_an_unvalidated_sha(self):
        def mutate(workflow):
            del workflow["jobs"]["deploy"]["steps"][0]

        self.refuses(DEV_DEPLOY, mutate, "the SHA is validated before it reaches a command")

    def test_the_dev_deploy_may_not_check_out_a_branch(self):
        def mutate(workflow):
            workflow["jobs"]["deploy"]["steps"][1]["with"]["ref"] = "develop"

        self.refuses(DEV_DEPLOY, mutate, "the commit deployed is the one that was proved")

    def test_the_dev_deploy_may_not_run_on_the_production_runner(self):
        def mutate(workflow):
            workflow["jobs"]["deploy"]["runs-on"] = DEPLOY_RUNNER

        self.refuses(DEV_DEPLOY, mutate, "dev and prod identities stay separate")

    # --- and the real thing --------------------------------------------------

    def test_the_real_workflows_satisfy_their_contracts(self):
        self.assertEqual(0, self.verdict({}), "the workflows as committed must pass")


if __name__ == "__main__":
    unittest.main()
