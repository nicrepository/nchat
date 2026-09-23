#!/usr/bin/env python3
"""The five continuous delivery workflows are exactly what is written here.

The guarantee this file exists to hold is one sentence: exactly two execution
paths in this repository change a stable Service selector, they are
`cutover.sh` and `rollback.sh`, each is called from its own manually
dispatched workflow, and neither the automatic preparation of a production
release nor the development delivery can reach either.

An earlier version of this checker tried to prove that by reading the shell in
each `run:` and looking for dangerous commands. That is the wrong shape for the
problem. Shell has unboundedly many ways to spell one invocation -- `bash x.sh`,
`env bash x.sh`, a variable holding the path, `kubectl patch svc` instead of
`kubectl patch service` -- so a denylist is a list of the spellings someone
happened to think of, and anything unrecognised is read as harmless. That is
fail-open, and it is not fixable by adding more patterns.

The second guarantee is narrower and just as easy to lose: continuous
delivery consumes commits that passed `CI / Required`, and the shape that
enforces it -- an eligibility job every other job depends on -- is one `needs:`
away from being decorative.

So the direction is inverted. These workflows are small, they are
security-critical, and they should stay both: every job and step they are
allowed to contain is written out below, and each workflow must match its
contract exactly -- same jobs, same steps, same order, same commands, same
wiring. Nothing has to be recognised as dangerous, because nothing unlisted is
permitted at all. A promotion added to the preparation workflow is refused for
the same reason `echo hello` is: no such step is in the contract. A fourth job
-- called `promote`, `finish`, or anything else -- is refused for the same
reason: the contract names exactly the jobs it names.

That is also what confines each mutation to the one place it belongs.
`cutover.sh` appears in exactly one step of one job of one workflow;
`rollback.sh` in exactly one step of one job of another; `drain-old.sh` in
neither, because retirement happens inside prepare-slot.sh under the lifecycle
preconditions and never as a workflow step of its own.

Two consequences worth stating. The contract is the executable surface, so a
comment inside a `run:` is normalised away and may be edited freely, while a
changed command is a violation. And the contract is a ratchet: adding a step to
a workflow means adding it here, in a diff a reviewer will see.

Usage: check_cd_workflows.py <workflow-directory>
Exit code 0 when every contract holds; 1 with one short reason per violation on
stderr when one does not.
"""

from __future__ import annotations

import pathlib
import sys

import yaml

# ---------------------------------------------------------------- shared ---

CHECKOUT = "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683"
DOWNLOAD = "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093"
UPLOAD = "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02"

PREPARE = "cd-prepare-production.yml"
CUTOVER = "cutover-nchat-prod.yml"
ROLLBACK = "rollback-nchat-prod.yml"
DEVELOP = "cd-develop.yml"
DEV_DEPLOY = "deploy-nchat-dev.yml"

# One mutation of nchat-prod at a time, across all three workflows. They share
# the group deliberately: a candidate deployed underneath a cutover, or a
# rollback racing the drain inside a preparation, is a namespace nobody can
# reason about afterwards.
#
# cancel-in-progress must be false, and false as a boolean: the string "false"
# is truthy to the expression evaluator, so a quoted one would cancel exactly
# the run that is holding a half-finished mutation.
MUTATION_CONCURRENCY = {
    "group": "nchat-prod-release-mutation",
    "cancel-in-progress": False,
}
DEPLOY_RUNNER = ["self-hosted", "linux", "x64", "nchat-prod-deploy"]
DEV_RUNNER = ["self-hosted", "linux", "x64", "nchat-dev-deploy"]
# nchat-dev has its own exclusion. It shares no lock with production: the two
# environments have nothing to serialise against each other, and a shared group
# would make a development deploy able to delay a production rollback.
DEV_DELIVERY_CONCURRENCY = {"group": "nchat-dev-delivery", "cancel-in-progress": False}
DEV_DEPLOY_CONCURRENCY = {"group": "nchat-dev-deploy", "cancel-in-progress": False}
# Issue #714: a separate label because it is a separate identity. The capacity
# collector reads Nodes and Pods across namespaces; the deployer must never be
# able to.
CAPACITY_RUNNER = ["self-hosted", "linux", "x64", "nchat-prod-capacity"]
READ_ONLY = {"actions": "read", "contents": "read"}
CONTENTS_ONLY = {"contents": "read"}
WORKFLOW_PERMISSIONS = CONTENTS_ONLY

# The root of a manual workflow, whose one job is the mutation. The
# preparation workflow declares no `concurrency` at the root -- see
# PREPARE_JOBS -- so its root is this set without it.
ROOT_KEYS = {"name", "on", "permissions", "concurrency", "jobs"}
ROOT_KEYS_WITHOUT_LOCK = ROOT_KEYS - {"concurrency"}
STEP_KEYS = {"name", "id", "if", "uses", "with", "env", "run"}
CALLED_JOB_KEYS = {"needs", "permissions", "uses", "with", "if"}
STEP_JOB_KEYS = {
    "name", "needs", "permissions", "runs-on", "timeout-minutes", "steps", "concurrency",
}
ENVIRONMENT_JOB_KEYS = STEP_JOB_KEYS | {"environment"}

SELECTORS_BEFORE = "${{ runner.temp }}/stable-selectors-before.txt"
SELECTORS_AFTER = "${{ runner.temp }}/stable-selectors-after.txt"

# The one conditional form the contract permits on a step that records what a
# mutation left behind.
#
# It is an allowlist of the two conclusions that mean the mutation actually
# ran, and the allowlist form is the requirement rather than the wording.
# Naming any status function stops Actions inserting the implicit `success()`,
# so every looser spelling runs the step when nothing was mutated:
# `conclusion != ''` is satisfied by `skipped`, which is exactly what a step
# reports when an earlier one failed, and `!cancelled()` on its own is
# satisfied by everything. Both are refused below, by name.
AFTER_CONCLUSIONS = ("success", "failure")
AFTER_CONDITION_REJECTED = ("!= ''", '!= ""', "!= 'skipped'", '!= "skipped"')


def after_condition(step_id: str) -> str:
    return (
        f"${{{{ !cancelled() && (steps.{step_id}.conclusion == 'success'"
        f" || steps.{step_id}.conclusion == 'failure') }}}}"
    )


def summary_condition(step_id: str) -> str:
    return f"${{{{ !cancelled() && steps.{step_id}.conclusion != '' && steps.{step_id}.conclusion != 'skipped' }}}}"


# ------------------------------------------------- CD / Prepare Production ---

# Everything the preparation workflow derives, and the expressions it derives
# them through. Comparing these strings exactly is what stops a hardcoded
# `green`, a workflow input, or another step's output standing in.
ELIGIBLE_SHA = "${{ needs.eligibility.outputs.sha }}"
CANDIDATE_SLOT = "${{ steps.slot.outputs.candidate }}"
ACTIVE_SLOT = "${{ steps.slot.outputs.active }}"
PREPARED_RELEASE = "${{ needs.eligibility.outputs.sha }}:${{ steps.release.outputs.release_id }}"

PREPARE_JOBS = {
    # The eligibility gate, and it must be the first thing in the graph: every
    # other job depends on the SHA it returns, so a commit whose
    # `CI / Required` did not pass is never built and never deployed.
    "eligibility": {
        "kind": "called",
        "if": "github.event.workflow_run.head_branch == 'main' && github.event.workflow_run.event == 'push'",
        "permissions": READ_ONLY,
        "uses": "./.github/workflows/ci-eligibility.yml",
        "with": {"branch": "main"},
    },
    # Build once. `require_main: true` is the gate that refuses a commit main
    # cannot reach, and it is compared as the boolean: a string here would be
    # passed to a boolean input and rejected at parse time, or worse, coerced.
    "build": {
        "kind": "called",
        "needs": "eligibility",
        "permissions": {"actions": "read", "contents": "read", "packages": "write"},
        "uses": "./.github/workflows/images.yml",
        "with": {"sha": ELIGIBLE_SHA, "require_main": True},
    },
    # Issue #714. Its own runner label, and no `packages` or write permission
    # of any kind: it reads the cluster and uploads what it read.
    "capacity": {
        "kind": "steps",
        "needs": ["eligibility", "build"],
        "permissions": CONTENTS_ONLY,
        "runs-on": CAPACITY_RUNNER,
        "timeout-minutes": 10,
        "steps": [
            {
                "uses": CHECKOUT,
                "with": {"ref": ELIGIBLE_SHA, "persist-credentials": False},
            },
            {"run": ['scripts/deploy/nchat-prod/capacity-evidence.sh "$RUNNER_TEMP/capacity-evidence"']},
            {
                "uses": UPLOAD,
                "with": {
                    "name": "capacity-evidence",
                    "path": "${{ runner.temp }}/capacity-evidence",
                    "retention-days": 1,
                    "if-no-files-found": "error",
                },
            },
        ],
    },
    # The candidate. Read down the job: prove the SHA, check out the proved
    # commit, prove it is reachable from main, fetch this run's sealed
    # manifest, pin the digests it seals, derive the release identity, fetch
    # the capacity evidence, reserve the idle slot under the lifecycle rules,
    # deploy into it, smoke it, prove the stable Services still select what the
    # snapshot recorded, prove the candidate is running the release this run
    # built, record it, report.
    #
    # Every dependency in that sentence is an ordering the contract holds by
    # position: digests are bound before anything is deployed, the snapshot is
    # taken before the deploy that must not disturb it, the deploy finishes
    # before the smoke that validates it, both invariants are read after all of
    # it, and the record is written only once they have passed.
    #
    # It declares no `environment`. An approval attached to preparation would
    # gate the phase with nothing to approve and pull the production
    # environment's secrets into a job that moves no traffic.
    "candidate": {
        "kind": "steps",
        "needs": ["eligibility", "build", "capacity"],
        # The lock is on this job and not on the workflow, and that placement
        # is the contract. The three jobs before it -- eligibility, an image
        # build and a read-only capacity collection -- touch nchat-prod not at
        # all and take twenty minutes; holding the production mutation lock
        # across them would make an incident rollback wait for an image build,
        # which is exactly what "rollback stays available" has to rule out.
        "concurrency": MUTATION_CONCURRENCY,
        "permissions": READ_ONLY,
        "runs-on": DEPLOY_RUNNER,
        "timeout-minutes": 45,
        "steps": [
            {
                "env": {"RELEASE_SHA": ELIGIBLE_SHA},
                "run": ['[[ "$RELEASE_SHA" =~ ^[a-f0-9]{40}$ ]]'],
            },
            {
                "uses": CHECKOUT,
                "with": {"ref": ELIGIBLE_SHA, "fetch-depth": 0, "persist-credentials": False},
            },
            {
                "env": {"RELEASE_SHA": ELIGIBLE_SHA},
                "run": ['scripts/deploy/nchat-prod/require-main-sha.sh "$RELEASE_SHA"'],
            },
            # No run-id and no token: this run's own artifact, so the manifest
            # can only be the one this run's build sealed.
            {
                "uses": DOWNLOAD,
                "with": {"name": "release-manifest", "path": "release-manifest"},
            },
            {
                "env": {"NCHAT_PROD_RELEASE_SHA": ELIGIBLE_SHA},
                "run": ["scripts/deploy/nchat-prod/release-digests.sh release-manifest artifacts"],
            },
            {
                "id": "release",
                "run": ['echo "release_id=$(cat artifacts/release-id.txt)" >>"$GITHUB_OUTPUT"'],
            },
            {
                "uses": DOWNLOAD,
                "with": {"name": "capacity-evidence", "path": "capacity-evidence"},
            },
            # One read of the cluster feeding the snapshot, the slot decision
            # and the lifecycle gate. Written as one script because they must
            # describe the same instant: separate readers could snapshot a
            # state no deploy was ever planned from, and the invariant would
            # then prove nothing.
            {
                "id": "slot",
                "env": {
                    "SELECTORS_BEFORE": SELECTORS_BEFORE,
                    "NCHAT_PROD_ROLLBACK_RETENTION_SECONDS": "${{ vars.NCHAT_PROD_ROLLBACK_RETENTION_SECONDS }}",
                },
                "run": ['scripts/deploy/nchat-prod/prepare-slot.sh "$SELECTORS_BEFORE" >>"$GITHUB_OUTPUT"'],
            },
            {
                "env": {
                    "ARTIFACTS_DIR": "${{ github.workspace }}/artifacts",
                    # The evidence the capacity job collected, not a directory
                    # an operator filled by hand: a repository variable here
                    # would be a path the pipeline never verified was refreshed.
                    "NCHAT_PROD_CAPACITY_EVIDENCE_DIR": "${{ github.workspace }}/capacity-evidence",
                    # Compared exactly: the deploy must build the slot the
                    # reservation step resolved, not one it derives for itself.
                    "NCHAT_PROD_CANDIDATE_SLOT": CANDIDATE_SLOT,
                    "NCHAT_PROD_RELEASE_SHA": ELIGIBLE_SHA,
                    "NCHAT_PROD_TOPOLOGY_FILE": "${{ vars.NCHAT_PROD_TOPOLOGY_FILE }}",
                    "NCHAT_PROD_ASSUME_YES": "1",
                },
                "run": ["scripts/deploy/nchat-prod/deploy.sh"],
            },
            {
                "env": {"CANDIDATE_SLOT": CANDIDATE_SLOT},
                "run": ['scripts/deploy/nchat-prod/smoke.sh --target "$CANDIDATE_SLOT"'],
            },
            # `diff` is the assertion. Under `set -e` a difference ends the
            # step, which is the whole behaviour: this step detects that
            # something moved a stable Service, and it must never be the thing
            # that puts one back.
            {
                "env": {"SELECTORS_BEFORE": SELECTORS_BEFORE, "SELECTORS_AFTER": SELECTORS_AFTER},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    'collect_service_slots >"$SELECTORS_AFTER"',
                    'echo "stable Service selectors after the deploy:"',
                    'cat "$SELECTORS_AFTER"',
                    'diff -u "$SELECTORS_BEFORE" "$SELECTORS_AFTER"',
                    'echo "The stable Services select exactly what they selected before this run."',
                ],
            },
            {
                "env": {"CANDIDATE_SLOT": CANDIDATE_SLOT, "EXPECTED_RELEASE": PREPARED_RELEASE},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE"',
                    "echo",
                    'echo "Slot $CANDIDATE_SLOT is running exactly $EXPECTED_RELEASE."',
                ],
            },
            # After both invariants, never before. The record is what lets the
            # cutover run hours later with nothing transcribed, and writing it
            # earlier would record a candidate that had not been proved.
            {
                "env": {
                    "CANDIDATE_SLOT": CANDIDATE_SLOT,
                    "EXPECTED_RELEASE": PREPARED_RELEASE,
                    "PREPARE_RUN_ID": "${{ github.run_id }}",
                },
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    "source scripts/deploy/nchat-prod/release-state.sh",
                    'record_candidate_ready "$CANDIDATE_SLOT" "$EXPECTED_RELEASE" "$PREPARE_RUN_ID"',
                ],
            },
            {
                "if": "${{ !cancelled() }}",
                "env": {
                    "ACTIVE_SLOT": ACTIVE_SLOT,
                    "CANDIDATE_SLOT": CANDIDATE_SLOT,
                    "EXPECTED_RELEASE": PREPARED_RELEASE,
                },
                "run": [
                    "set -euo pipefail",
                    "{",
                    'echo "## Production Candidate Ready"',
                    "echo",
                    'echo "| | |"',
                    'echo "|---|---|"',
                    'echo "| Current active | \\`${ACTIVE_SLOT:-unknown}\\` |"',
                    'echo "| Candidate | \\`${CANDIDATE_SLOT:-unknown}\\` |"',
                    'echo "| Candidate release | \\`$EXPECTED_RELEASE\\` |"',
                    "echo",
                    'echo "| Gate | Result |"',
                    'echo "|---|---|"',
                    'echo "| Capacity | PASS |"',
                    'echo "| Migrations | PASS |"',
                    'echo "| Rollout | PASS |"',
                    'echo "| Candidate smoke | PASS |"',
                    'echo "| Release identity | PASS |"',
                    'echo "| Stable selectors | UNCHANGED |"',
                    "echo",
                    'echo "Production traffic remains on \\`${ACTIVE_SLOT:-unknown}\\`."',
                    'echo "Next action: **Actions -> Production / Cutover**."',
                    '} >>"$GITHUB_STEP_SUMMARY"',
                ],
            },
        ],
    },
}

# ----------------------------------------------------- Production / Cutover ---

PROMOTED_SLOT = "${{ steps.candidate.outputs.candidate }}"
PROMOTED_SHA = "${{ steps.candidate.outputs.release_sha }}"
PROMOTED_RELEASE = "${{ steps.candidate.outputs.release_sha }}:${{ steps.candidate.outputs.release_id }}"
ROLLBACK_TARGET = "${{ steps.candidate.outputs.rollback_target }}"

CUTOVER_JOBS = {
    "cutover": {
        "kind": "steps",
        # No job-level lock: this workflow's one job is the mutation, so the
        # root-level group already covers exactly it.
        "concurrency": None,
        "environment": "production",
        "permissions": READ_ONLY,
        "runs-on": DEPLOY_RUNNER,
        "timeout-minutes": 20,
        "steps": [
            # A dispatch carries the workflow file of the ref it was started
            # from, so a feature branch would run its own copy of these gates.
            {"run": ['[[ "$GITHUB_REF" == "refs/heads/main" ]]']},
            {
                "uses": CHECKOUT,
                "with": {"ref": "main", "fetch-depth": 0, "persist-credentials": False},
            },
            # Derivation and revalidation in one place. Everything after this
            # is about a candidate already proved to exist as claimed.
            {
                "id": "candidate",
                "env": {
                    "SELECTORS_BEFORE": SELECTORS_BEFORE,
                    "NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS": "${{ vars.NCHAT_PROD_CANDIDATE_MAX_AGE_SECONDS }}",
                },
                "run": ['scripts/deploy/nchat-prod/cutover-preflight.sh "$SELECTORS_BEFORE" >>"$GITHUB_OUTPUT"'],
            },
            {
                "env": {"RELEASE_SHA": PROMOTED_SHA},
                "run": ['scripts/deploy/nchat-prod/require-main-sha.sh "$RELEASE_SHA"'],
            },
            # From the preparation run named in the lifecycle record. The run
            # id is derived, never typed, and a manifest from another run seals
            # a different release id, which cutover.sh compares against the
            # cluster.
            {
                "uses": DOWNLOAD,
                "with": {
                    "name": "release-manifest",
                    "path": "release-manifest",
                    "run-id": "${{ steps.candidate.outputs.prepare_run_id }}",
                    "github-token": "${{ secrets.GITHUB_TOKEN }}",
                },
            },
            # The mutation, and the only one in this workflow.
            {
                "id": "promote",
                "env": {
                    "CANDIDATE_SLOT": PROMOTED_SLOT,
                    "NCHAT_PROD_SMOKE_CONFIRMED": f"{PROMOTED_SLOT}:{PROMOTED_RELEASE}",
                    "NCHAT_PROD_RELEASE_MANIFEST_DIR": "release-manifest",
                    "NCHAT_PROD_ASSUME_YES": "1",
                },
                "run": ['scripts/deploy/nchat-prod/cutover.sh --target "$CANDIDATE_SLOT"'],
            },
            {
                "if": after_condition("promote"),
                "env": {"SELECTORS_AFTER": SELECTORS_AFTER},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    'collect_service_slots >"$SELECTORS_AFTER"',
                    'echo "stable Service selectors after the cutover:"',
                    'cat "$SELECTORS_AFTER"',
                ],
            },
            {
                "env": {"CANDIDATE_SLOT": PROMOTED_SLOT, "SELECTORS_AFTER": SELECTORS_AFTER},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$CANDIDATE_SLOT"',
                    'echo "Every stable Service selects slot $CANDIDATE_SLOT."',
                ],
            },
            {
                "env": {"CANDIDATE_SLOT": PROMOTED_SLOT, "EXPECTED_RELEASE": PROMOTED_RELEASE},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE"',
                    "echo",
                    'echo "Production serves $EXPECTED_RELEASE on slot $CANDIDATE_SLOT."',
                ],
            },
            # Before the smoke, deliberately: the previous slot is the rollback
            # from the moment traffic moved, and a failing smoke is exactly
            # when that reservation matters most.
            {
                "env": {"CANDIDATE_SLOT": PROMOTED_SLOT, "ROLLBACK_SLOT": ROLLBACK_TARGET},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    "source scripts/deploy/nchat-prod/release-state.sh",
                    'record_cutover "$CANDIDATE_SLOT" "$ROLLBACK_SLOT"',
                ],
            },
            # The post-cutover profile, not the candidate one: `smoke.sh`
            # requires the slot to carry no traffic and is structurally unable
            # to validate a slot that now carries all of it.
            #
            # Through record-traffic-smoke.sh, which runs that smoke and
            # records the result only when it passes. The record is what the
            # next release's retirement gate reads: without it, a cutover whose
            # post-cutover smoke failed would still have its rollback retired
            # once the retention window elapsed. Calling stable-smoke.sh
            # directly here is refused for that reason.
            {
                "id": "smoke",
                "env": {"CANDIDATE_SLOT": PROMOTED_SLOT},
                "run": ['scripts/deploy/nchat-prod/record-traffic-smoke.sh --target "$CANDIDATE_SLOT" --after cutover'],
            },
            {
                "if": summary_condition("promote"),
                "env": {
                    "PREVIOUS_SLOT": ROLLBACK_TARGET,
                    "CANDIDATE_SLOT": PROMOTED_SLOT,
                    "EXPECTED_RELEASE": PROMOTED_RELEASE,
                    "PROMOTE_RESULT": "${{ steps.promote.conclusion }}",
                    "SMOKE_RESULT": "${{ steps.smoke.conclusion }}",
                    "RETENTION": "${{ vars.NCHAT_PROD_ROLLBACK_RETENTION_SECONDS }}",
                },
                "run": [
                    "set -euo pipefail",
                    "{",
                    'echo "## Production Cutover"',
                    "echo",
                    'echo "| | |"',
                    'echo "|---|---|"',
                    'echo "| Previous active | \\`$PREVIOUS_SLOT\\` |"',
                    'echo "| New active | \\`$CANDIDATE_SLOT\\` |"',
                    'echo "| Release | \\`$EXPECTED_RELEASE\\` |"',
                    'echo "| Selector patch | $PROMOTE_RESULT |"',
                    'echo "| Post-cutover smoke | ${SMOKE_RESULT:-not reached} |"',
                    "echo",
                    'echo "Rollback target: \\`$PREVIOUS_SLOT\\`, still running and untouched."',
                    'echo "It is reserved for rollback for ${RETENTION:-1800}s from now and is retired"',
                    'echo "by the next **CD / Prepare Production** run, never by this one."',
                    "echo",
                    'echo "Roll back with **Actions -> Production / Rollback**, target \\`$PREVIOUS_SLOT\\`."',
                    '} >>"$GITHUB_STEP_SUMMARY"',
                ],
            },
        ],
    }
}

# ---------------------------------------------------- Production / Rollback ---

TARGET_SLOT = "${{ inputs.target_slot }}"
REASON = "${{ inputs.reason }}"

ROLLBACK_JOBS = {
    "rollback": {
        "kind": "steps",
        "concurrency": None,
        "permissions": CONTENTS_ONLY,
        "runs-on": DEPLOY_RUNNER,
        "timeout-minutes": 20,
        "steps": [
            # Both inputs are untrusted strings, and both are validated before
            # either reaches a command. `target_slot` is a choice input, but a
            # choice is a form affordance and not an enforcement boundary --
            # the API accepts any string for it.
            {
                "env": {"TARGET_SLOT": TARGET_SLOT, "REASON": REASON},
                "run": [
                    "set -euo pipefail",
                    '[[ "$GITHUB_REF" == "refs/heads/main" ]]',
                    '[[ "$TARGET_SLOT" == blue || "$TARGET_SLOT" == green ]]',
                    '[[ -n "$REASON" ]] || { echo "a reason is required" >&2; exit 1; }',
                    '[[ "${#REASON}" -le 200 ]] || { echo "the reason must be at most 200 characters" >&2; exit 1; }',
                    '[[ "$REASON" =~ ^[[:print:]]+$ ]] ||',
                    '{ echo "the reason must be one line of printable characters" >&2; exit 1; }',
                ],
            },
            {
                "uses": CHECKOUT,
                "with": {"ref": "main", "fetch-depth": 0, "persist-credentials": False},
            },
            {
                "id": "target",
                "env": {"TARGET_SLOT": TARGET_SLOT, "SELECTORS_BEFORE": SELECTORS_BEFORE},
                "run": [
                    "set -euo pipefail",
                    "scripts/deploy/nchat-prod/rollback-preflight.sh \\",
                    '--target "$TARGET_SLOT" "$SELECTORS_BEFORE" >>"$GITHUB_OUTPUT"',
                ],
            },
            # "Target Ready" and "the database can still serve it" are
            # different properties and the second is the one that gets assumed.
            {
                "id": "schema",
                "env": {
                    "TARGET_SHA": "${{ steps.target.outputs.target_release_sha }}",
                    "FROM_SHA": "${{ steps.target.outputs.from_release_sha }}",
                },
                "run": ['scripts/deploy/nchat-prod/rollback-schema-gate.sh "$TARGET_SHA" "$FROM_SHA"'],
            },
            # The mutation, and the only one in this workflow. The reason is a
            # quoted argument from the environment; nothing builds a command
            # string from it.
            {
                "id": "revert",
                "env": {"TARGET_SLOT": TARGET_SLOT, "REASON": REASON, "NCHAT_PROD_ASSUME_YES": "1"},
                "run": ['scripts/deploy/nchat-prod/rollback.sh --target "$TARGET_SLOT" "$REASON"'],
            },
            {
                "if": after_condition("revert"),
                "env": {"SELECTORS_AFTER": SELECTORS_AFTER},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    'collect_service_slots >"$SELECTORS_AFTER"',
                    'echo "stable Service selectors after the rollback:"',
                    'cat "$SELECTORS_AFTER"',
                ],
            },
            {
                "env": {"TARGET_SLOT": TARGET_SLOT, "SELECTORS_AFTER": SELECTORS_AFTER},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$TARGET_SLOT" || {',
                    'echo "Production is mixed. Re-run this workflow with the same target ($TARGET_SLOT)." >&2',
                    "exit 1",
                    "}",
                    'echo "Every stable Service selects slot $TARGET_SLOT."',
                ],
            },
            {
                "env": {"TARGET_SLOT": TARGET_SLOT},
                "run": [
                    "set -euo pipefail",
                    "source scripts/deploy/nchat-prod/lib.sh",
                    "source scripts/deploy/nchat-prod/release-state.sh",
                    'record_rollback "$TARGET_SLOT"',
                ],
            },
            {
                "id": "smoke",
                "env": {"TARGET_SLOT": TARGET_SLOT},
                "run": ['scripts/deploy/nchat-prod/record-traffic-smoke.sh --target "$TARGET_SLOT" --after rollback'],
            },
            {
                "if": summary_condition("revert"),
                "env": {
                    "FROM_SLOT": "${{ steps.target.outputs.from_slot }}",
                    "TARGET_SLOT": TARGET_SLOT,
                    "TARGET_RELEASE": "${{ steps.target.outputs.target_release }}",
                    "REASON": REASON,
                    "REVERT_RESULT": "${{ steps.revert.conclusion }}",
                    "SCHEMA_RESULT": "${{ steps.schema.conclusion }}",
                    "SMOKE_RESULT": "${{ steps.smoke.conclusion }}",
                },
                "run": [
                    "set -euo pipefail",
                    "{",
                    'echo "## Production Rollback"',
                    "echo",
                    'echo "| | |"',
                    'echo "|---|---|"',
                    'echo "| From | \\`$FROM_SLOT\\` |"',
                    'echo "| To | \\`$TARGET_SLOT\\` |"',
                    'echo "| Target release | \\`$TARGET_RELEASE\\` |"',
                    # printf with the reason as an argument, never inside the
                    # format string: an operator-supplied `%s` in an `echo`
                    # would be harmless, but the habit is not.
                    "printf '| Reason | %s |\\n' \"$REASON\"",
                    'echo "| Schema compatibility | ${SCHEMA_RESULT:-not reached} |"',
                    'echo "| Selector patch | $REVERT_RESULT |"',
                    'echo "| Post-rollback smoke | ${SMOKE_RESULT:-not reached} |"',
                    "echo",
                    'echo "No build, no image publication and no migration ran in this workflow."',
                    'echo "Slot \\`$FROM_SLOT\\` is left running for investigation; capture its logs and"',
                    'echo "events before anything else touches it."',
                    '} >>"$GITHUB_STEP_SUMMARY"',
                ],
            },
        ],
    }
}

# The dispatch form is part of the contract, not decoration. An input that went
# missing, became optional, or changed type would leave CI green while the only
# way to roll back was broken or quietly weakened -- an optional `reason`
# defaults to the empty string, which the validation step would then be
# refusing instead of a mistake anyone meant to make.
#
# `default` is refused for every input: it turns a required gate into a value
# the dispatcher can leave alone, and a rollback with a default target is a
# rollback somebody can start by pressing one button with no thought.
ROLLBACK_INPUTS = {
    "target_slot": {"required": True, "type": "choice", "options": ["blue", "green"]},
    "reason": {"required": True, "type": "string"},
}
INPUT_KEYS = {"required", "type", "description", "options"}


# ------------------------------------------------------------ CD / Develop ---

# nchat-dev is one environment with one copy of each workload. The two-slot
# machinery exists in production because production cannot afford a rollback
# that is a redeploy, and nchat-dev can -- so what is written out here is a
# single-slot delivery, and a slot, a cutover or an approval appearing in it
# would be refused for not being in the contract.
DEV_SHA = "${{ needs.eligibility.outputs.sha }}"
DEV_INPUT_SHA = "${{ inputs.sha }}"
DEV_HOST_ENV = {
    "NCHAT_DEV_NODE_IP": "${{ vars.NCHAT_DEV_NODE_IP }}",
    "NCHAT_DEV_NODE_CIDR": "${{ vars.NCHAT_DEV_NODE_CIDR }}",
    "NCHAT_DEV_HOST": "${{ vars.NCHAT_DEV_HOST }}",
    "NCHAT_DEV_TURN_EXTERNAL_IP": "${{ vars.NCHAT_DEV_TURN_EXTERNAL_IP }}",
}

DEVELOP_JOBS = {
    # Same gate as production's, on develop. Nothing is built before it.
    "eligibility": {
        "kind": "called",
        "if": "github.event.workflow_run.head_branch == 'develop' && github.event.workflow_run.event == 'push'",
        "permissions": READ_ONLY,
        "uses": "./.github/workflows/ci-eligibility.yml",
        "with": {"branch": "develop"},
    },
    # `require_main: false` as the boolean: develop is not reachable from main,
    # and a development build must not claim to be a production one.
    "build": {
        "kind": "called",
        "needs": "eligibility",
        "permissions": {"actions": "read", "contents": "read", "packages": "write"},
        "uses": "./.github/workflows/images.yml",
        "with": {"sha": DEV_SHA, "require_main": False},
    },
    # The commit eligibility returned, not `github.sha`: a `workflow_run`
    # handler's own `github.sha` is the default branch's head at dispatch time,
    # which is not necessarily the commit whose CI passed.
    "deploy": {
        "kind": "called",
        "needs": ["eligibility", "build"],
        "permissions": READ_ONLY,
        "uses": "./.github/workflows/deploy-nchat-dev.yml",
        "with": {"sha": DEV_SHA},
    },
}

DEV_DEPLOY_JOBS = {
    "deploy": {
        "kind": "steps",
        "environment": "nchat-dev",
        "runs-on": DEV_RUNNER,
        "timeout-minutes": 20,
        "steps": [
            {
                "env": {"DEPLOY_SHA": DEV_INPUT_SHA},
                "run": ['[[ "$DEPLOY_SHA" =~ ^[a-f0-9]{40}$ ]]'],
            },
            {"uses": CHECKOUT, "with": {"ref": DEV_INPUT_SHA}},
            # This run's own digests, so the images deployed are the ones this
            # run built.
            {
                "uses": DOWNLOAD,
                "with": {"pattern": "digest-*", "path": "artifacts", "merge-multiple": True},
            },
            {"run": ["scripts/deploy/nchat-dev/install-kustomize.sh"]},
            {"env": DEV_HOST_ENV, "run": ["scripts/ci/nchat-dev-deployment-check.sh"]},
            {
                "id": "apply",
                "env": {
                    "ARTIFACTS_DIR": "${{ github.workspace }}/artifacts",
                    "DEPLOY_SHA": DEV_INPUT_SHA,
                    **DEV_HOST_ENV,
                },
                "run": ["scripts/deploy/nchat-dev/deploy.sh"],
            },
            # Rollout completion is not delivery. Dropping this step is the
            # regression the contract exists to refuse: a deploy that applied
            # cleanly and serves nothing would then report success.
            {
                "id": "smoke",
                "env": {"NCHAT_DEV_HOST": "${{ vars.NCHAT_DEV_HOST }}"},
                "run": ["scripts/deploy/nchat-dev/smoke.sh"],
            },
            {
                "if": "${{ !cancelled() }}",
                "env": {
                    "DEPLOY_SHA": DEV_INPUT_SHA,
                    "APPLY_RESULT": "${{ steps.apply.conclusion }}",
                    "SMOKE_RESULT": "${{ steps.smoke.conclusion }}",
                    "DIGEST_DIR": "${{ github.workspace }}/artifacts",
                },
                "run": [
                    "set -euo pipefail",
                    "{",
                    'echo "## CD / Develop"',
                    "echo",
                    'echo "| | |"',
                    'echo "|---|---|"',
                    'echo "| SHA | \\`$DEPLOY_SHA\\` |"',
                    'echo "| Images | $(find "$DIGEST_DIR" -name \'digest-*.txt\' -type f | wc -l) pinned by OCI digest |"',
                    'echo "| Migrations, rollout, readiness | ${APPLY_RESULT:-not reached} |"',
                    'echo "| Smoke | ${SMOKE_RESULT:-not reached} |"',
                    "echo",
                    'echo "Single-slot deployment; no Blue/Green and no traffic switch in this environment."',
                    '} >>"$GITHUB_STEP_SUMMARY"',
                ],
            },
        ],
    }
}

CONTRACTS = {
    PREPARE: {
        "triggers": {"workflow_run"},
        "jobs": PREPARE_JOBS,
        "inputs": None,
        # The lock lives on the `candidate` job instead.
        "concurrency": None,
        "root_keys": ROOT_KEYS_WITHOUT_LOCK,
    },
    CUTOVER: {
        "triggers": {"workflow_dispatch"},
        "jobs": CUTOVER_JOBS,
        "inputs": {},
        "concurrency": MUTATION_CONCURRENCY,
        "root_keys": ROOT_KEYS,
    },
    ROLLBACK: {
        "triggers": {"workflow_dispatch"},
        "jobs": ROLLBACK_JOBS,
        "inputs": ROLLBACK_INPUTS,
        "concurrency": MUTATION_CONCURRENCY,
        "root_keys": ROOT_KEYS,
    },
    DEVELOP: {
        "triggers": {"workflow_run"},
        "jobs": DEVELOP_JOBS,
        "inputs": None,
        "concurrency": DEV_DELIVERY_CONCURRENCY,
        "root_keys": ROOT_KEYS,
        "permissions": CONTENTS_ONLY,
    },
    DEV_DEPLOY: {
        "triggers": {"workflow_call"},
        "jobs": DEV_DEPLOY_JOBS,
        "inputs": None,
        "concurrency": DEV_DEPLOY_CONCURRENCY,
        "root_keys": ROOT_KEYS,
        # Declared at the root rather than on the job, which is why the job
        # itself declares none: it reads this run's digest artifacts.
        "permissions": READ_ONLY,
    },
}


# ----------------------------------------------------------------- reading ---


def load(path: pathlib.Path):
    with open(path, encoding="utf-8") as handle:
        return yaml.safe_load(handle)


def normalize_run(run) -> list[str]:
    """The executable lines of a `run:`, comments and blank lines removed.

    Comments are documentation: editing one must not fail the contract, and
    adding one must never satisfy it. Everything that survives is compared
    exactly, so a changed command is always a violation.
    """
    return [line for line in (raw.strip() for raw in str(run).splitlines()) if line and not line.startswith("#")]


def step_contract(step: dict) -> dict:
    """What a step actually does, in the form the expectations are written in."""
    contract = {key: step[key] for key in ("id", "if", "uses", "with", "env") if key in step}
    if "run" in step:
        contract["run"] = normalize_run(step["run"])
    return contract


def on_of(workflow: dict):
    """`on:` is YAML 1.1 for True, so it is read by value as well as by name."""
    return workflow.get("on", workflow.get(True))


def root_keys_of(workflow: dict) -> set[str]:
    return {"on" if key is True else key for key in workflow}


# ---------------------------------------------------------------- checking ---


def check_root(name: str, workflow: dict, contract: dict) -> list[str]:
    """The whole top level, closed.

    Every other contract here describes something inside `jobs:`, and a
    workflow can change what every step runs without touching one of them:
    `defaults.run.shell` is a documented, actionlint-clean way to wrap every
    `run:` in the file. So the root is an allowlist too -- and being an
    allowlist rather than a ban on `defaults`, it also refuses whatever key
    GitHub adds next that nobody here has thought about yet.
    """
    problems = []
    if root_keys_of(workflow) != contract["root_keys"]:
        problems.append(
            f"{name} must declare exactly the top-level keys {sorted(contract['root_keys'])}"
        )
    permissions = contract.get("permissions", WORKFLOW_PERMISSIONS)
    if workflow.get("permissions") != permissions:
        problems.append(f"{name} must declare exactly {permissions} at the top level")
    if workflow.get("concurrency") != contract["concurrency"]:
        problems.append(
            f"{name} must declare concurrency {contract['concurrency']!r} at the top level,"
            f" got {workflow.get('concurrency')!r}"
        )
    return problems + check_triggers(name, workflow, contract)


def check_triggers(name: str, workflow: dict, contract: dict) -> list[str]:
    """The declared triggers, exactly, and never pull_request_target."""
    on = on_of(workflow)
    declared = {on} if isinstance(on, str) else set(on or ())
    problems = []
    if "pull_request_target" in declared:
        problems.append(f"pull_request_target is prohibited in {name}")
    if declared != contract["triggers"]:
        problems.append(f"{name} must trigger on exactly {sorted(contract['triggers'])}, got {sorted(declared)}")
    return problems + check_inputs(name, on, contract["inputs"])


def check_inputs(name: str, on, expected) -> list[str]:
    """`workflow_dispatch.inputs`, closed, or required to be absent."""
    if expected is None:
        return []
    dispatch = on.get("workflow_dispatch") if isinstance(on, dict) else None
    declared = dispatch.get("inputs") if isinstance(dispatch, dict) else None
    if not expected:
        return [f"{name} must declare no workflow_dispatch inputs, got {sorted(declared)}"] if declared else []
    if not isinstance(declared, dict):
        return [f"{name} must declare a workflow_dispatch inputs mapping, got {declared!r}"]
    problems = []
    if set(declared) != set(expected):
        problems.append(f"{name} must declare exactly the inputs {sorted(expected)}, got {sorted(declared)}")
    for input_name, want in expected.items():
        problems += check_input(name, input_name, declared.get(input_name), want)
    return problems


def check_input(workflow: str, name: str, spec, want: dict) -> list[str]:
    """One input's declaration. `description` is prose and stays free."""
    if not isinstance(spec, dict):
        return [f"{workflow} input {name} must be a mapping, got {spec!r}"]
    problems = []
    unexpected = sorted(set(spec) - INPUT_KEYS)
    if unexpected:
        problems.append(f"{workflow} input {name} must not declare {unexpected}")
    problems += [
        f"{workflow} input {name} must declare {key}: {value!r}, got {spec.get(key)!r}"
        for key, value in want.items()
        if spec.get(key) != value
    ]
    return problems


def check_jobs(name: str, workflow: dict, contract: dict) -> list[str]:
    """Exactly the jobs the contract names.

    A job the contract does not know about is refused whatever it is called and
    whatever it does, so a second promotion cannot be added under a name nobody
    anticipated.
    """
    jobs = workflow.get("jobs") or {}
    expected = contract["jobs"]
    problems = []
    if set(jobs) != set(expected):
        problems.append(f"{name} must define exactly the jobs {sorted(expected)}, got {sorted(jobs)}")
    for job_name, want in expected.items():
        if job_name in jobs:
            problems += check_job(f"{name}:{job_name}", jobs[job_name], want)
    return problems


def check_job(label: str, job: dict, want: dict) -> list[str]:
    if want["kind"] == "called":
        return check_called_job(label, job, want)
    return check_step_job(label, job, want)


def check_called_job(label: str, job: dict, want: dict) -> list[str]:
    """A job that delegates to a reusable workflow, compared whole."""
    expected = {key: value for key, value in want.items() if key != "kind"}
    problems = []
    unexpected = sorted(set(job) - CALLED_JOB_KEYS)
    if unexpected:
        problems.append(f"{label} must not declare {unexpected}")
    if job != expected:
        problems.append(f"{label} is not the delegation the contract expects: {job!r}")
    return problems


# Compared through `.get`, so "must be exactly this" and "must not be declared
# at all" are one comparison: a contract that names no `environment` requires
# the job to declare none, and a job that grows one is refused by the same line
# that would refuse the wrong one.
WIRING_KEYS = ("needs", "permissions", "runs-on", "environment", "concurrency")


def check_step_job(label: str, job: dict, want: dict) -> list[str]:
    """A job with steps: its settings, its wiring, and its steps in order."""
    allowed = ENVIRONMENT_JOB_KEYS if "environment" in want else STEP_JOB_KEYS
    unexpected = sorted(set(job) - allowed)
    problems = [f"{label} must not declare {unexpected}"] if unexpected else []
    problems += [
        f"{label} {key} must be exactly {want.get(key)!r}, got {job.get(key)!r}"
        for key in WIRING_KEYS
        if job.get(key) != want.get(key)
    ]
    return problems + check_timeout(label, job, want) + check_steps(label, job, want)


def check_timeout(label: str, job: dict, want: dict) -> list[str]:
    """Exactly the minutes this job is allowed, and an integer.

    The type is checked before the value, and `bool` is excluded from it.
    YAML's `"45"` is a string Actions rejects at parse time, so a contract that
    compared it loosely would be green on a workflow nobody can run; and
    `True == 1` in Python, so a bare equality would read `timeout-minutes: true`
    as a one-minute limit.
    """
    expected = want["timeout-minutes"]
    timeout = job.get("timeout-minutes")
    if isinstance(timeout, int) and not isinstance(timeout, bool) and timeout == expected:
        return []
    return [f"{label} must declare timeout-minutes: {expected}, got {timeout!r}"]


def check_steps(label: str, job: dict, want: dict) -> list[str]:
    """Same steps, same order, nothing extra.

    This is where a wrapper around cutover.sh in the preparation workflow, a
    call to rollback.sh or drain-old.sh where none belongs, a hand-written
    selector patch, a migration and a stray `echo` are all refused, without any
    of them having to be recognised: they are not the step the contract has at
    that position, and there is no position spare.
    """
    steps = job.get("steps") or []
    expected = want["steps"]
    if len(steps) != len(expected):
        return [f"{label} must contain exactly {len(expected)} steps, got {len(steps)}"]
    problems = []
    for index, (step, target) in enumerate(zip(steps, expected)):
        problems += check_step(label, index, step, target)
    return problems


def check_step(label: str, index: int, step: dict, want: dict) -> list[str]:
    problems = []
    unexpected = sorted(set(step) - STEP_KEYS)
    if unexpected:
        problems.append(f"{label} step {index} must not declare {unexpected}")
    if "if" in step and "steps." in str(step["if"]):
        problems += check_after_condition(label, index, str(step["if"]))
    actual = step_contract(step)
    if actual != want:
        problems.append(f"{label} step {index} is not the step the contract expects: {actual!r}")
    return problems


def check_after_condition(label: str, index: int, condition: str) -> list[str]:
    """A step gated on a mutation's conclusion must name what it accepts.

    Compared as a policy rather than as text, because the exact-contract match
    alone would report only "not the step expected" for a condition that is
    wrong in a specific and repeatable way. The two spellings that quietly
    include `skipped` are named, so a mutation says why it failed.
    """
    problems = []
    for rejected in AFTER_CONDITION_REJECTED:
        if rejected in condition and "!= 'skipped'" not in condition:
            problems.append(
                f"{label} step {index} must not gate on {rejected} alone: a step skipped after an"
                " earlier failure satisfies it, so nothing was mutated and production is read anyway"
            )
    if "!cancelled()" not in condition:
        problems.append(f"{label} step {index} must not run on a cancelled job")
    return problems


def run(directory: pathlib.Path) -> int:
    problems = []
    for name, contract in CONTRACTS.items():
        path = directory / name
        try:
            workflow = load(path)
        except (OSError, yaml.YAMLError) as error:
            problems.append(f"{name} cannot be read from {path}: {error}")
            continue
        if not isinstance(workflow, dict):
            problems.append(f"{name} is not a workflow document")
            continue
        problems += check_root(name, workflow, contract) + check_jobs(name, workflow, contract)
    for problem in problems:
        print(problem, file=sys.stderr)
    if problems:
        return 1
    print(f"The {len(CONTRACTS)} continuous delivery workflows match their contracts.")
    return 0


if __name__ == "__main__":
    default = pathlib.Path(__file__).resolve().parents[2] / ".github" / "workflows"
    sys.exit(run(pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else default))
