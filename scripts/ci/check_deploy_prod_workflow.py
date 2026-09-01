#!/usr/bin/env python3
"""The production deploy workflow must keep candidate and cutover apart (CICD-06).

The guarantee this file exists to hold is one sentence: while the `production`
environment has not released the protected job, no execution path in the
workflow changes a stable Service selector.

An earlier version of this checker tried to prove that by reading the shell in
each `run:` and looking for dangerous commands. That is the wrong shape for the
problem. Shell has unboundedly many ways to spell one invocation -- `bash x.sh`,
`env bash x.sh`, a variable holding the path, `kubectl patch svc` instead of
`kubectl patch service` -- so a denylist is a list of the spellings someone
happened to think of, and anything unrecognised is read as harmless. That is
fail-open, and it is not fixable by adding more patterns.

So the direction is inverted. This workflow is small, it is security-critical,
and it should stay both: every step it is allowed to contain is written out
below, and the workflow must match that contract exactly -- same steps, same
order, same commands, same wiring. Nothing has to be recognised as dangerous,
because nothing unlisted is permitted at all. `bash cutover.sh` in the candidate
is refused for the same reason `echo hello` is: no such step is in the contract.

Two consequences worth stating. The contract is the executable surface, so a
comment inside a `run:` is normalised away and may be edited freely, while a
changed command is a violation. And the contract is a ratchet: adding a step to
the workflow means adding it here, in a diff a reviewer will see.

Usage: check_deploy_prod_workflow.py <workflow-file>
Exit code 0 when the contract holds; 1 with one short reason per violation on
stderr when it does not.
"""

from __future__ import annotations

import sys

import yaml

# The whole top level, closed.
#
# Every other contract here describes something inside `jobs:`, and a workflow
# can change what every step runs without touching one of them: `defaults.run.
# shell` is a documented, actionlint-clean way to wrap every `run:` in the file.
# A checker that enumerated the jobs' steps exactly and left the root open was
# proving the wrong thing, so the root is an allowlist too -- and being an
# allowlist rather than a ban on `defaults`, it also refuses whatever key
# GitHub adds next that nobody here has thought about yet.
ROOT_KEYS = {"name", "on", "permissions", "concurrency", "jobs"}

CANDIDATE = "candidate"
CUTOVER = "cutover"
TRIGGERS = {"workflow_dispatch"}
# The dispatch form is part of the contract, not decoration. An operator drives
# this workflow by hand, and both values are gates: the commit is proved against
# main, and the run id is what binds the promotion to a sealed manifest. An
# input that went missing, became optional, or changed type would leave CI green
# while the only way to release was broken or quietly weakened -- an optional
# `sha` defaults to the empty string, which the first validation step would then
# be refusing instead of a mistake anyone meant to make.
#
# The set is exact. A third input is a new lever on a production promotion and
# must be reviewed as one, not absorbed silently.
DISPATCH_INPUTS = ("sha", "run_id")
# Serialising production releases is structural, not a nicety. A candidate can
# sit waiting for its approval for a long time, and a second run in the meantime
# would redeploy the same slot underneath it. The later gates would refuse the
# promotion -- the release identity would no longer match -- but "fails closed"
# and "behaves predictably" are different properties, and an operator watching a
# run that is quietly no longer describing the cluster is owed the second one.
#
# cancel-in-progress must be false, and false as a boolean: the string "false"
# is truthy to the expression evaluator, so a quoted one would cancel exactly
# the run that is holding a deployed candidate.
CONCURRENCY = {"group": "nchat-prod-deploy", "cancel-in-progress": False}
# `description` is prose and free to reword; these two decide behaviour.
INPUT_CONTRACT = {"required": True, "type": "string"}
WORKFLOW_PERMISSIONS = {"contents": "read"}
ENVIRONMENT = "production"
RUNNER = ["self-hosted", "linux", "x64", "nchat-prod-deploy"]

CHECKOUT = "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683"
DOWNLOAD = "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093"
SHA = "${{ inputs.sha }}"
RUN_ID = "${{ inputs.run_id }}"
# The slot and the release identity travel from the candidate to the promotion
# as job outputs. Comparing these strings exactly is what stops a hardcoded
# `green`, a second workflow input, or another step's output standing in.
SLOT_OUTPUT = "${{ steps.slot.outputs.slot }}"
RELEASE_ID_OUTPUT = "${{ steps.release.outputs.release_id }}"
NEEDS_SLOT = "${{ needs.candidate.outputs.slot }}"
NEEDS_RELEASE_ID = "${{ needs.candidate.outputs.release_id }}"

CHECKOUT_STEP = {
    "uses": CHECKOUT,
    "with": {"ref": SHA, "fetch-depth": 0, "persist-credentials": False},
}
MAIN_GATE_STEP = {
    "env": {"RELEASE_SHA": SHA},
    "run": ['scripts/deploy/nchat-prod/require-main-sha.sh "$RELEASE_SHA"'],
}
# Confined to this run's named build by run-id, and to a read-only token.
DOWNLOAD_STEP = {
    "uses": DOWNLOAD,
    "with": {
        "name": "release-manifest",
        "path": "release-manifest",
        "run-id": RUN_ID,
        "github-token": "${{ secrets.GITHUB_TOKEN }}",
    },
}

JOB_OUTPUTS = {
    CANDIDATE: {"slot": SLOT_OUTPUT, "release_id": RELEASE_ID_OUTPUT},
    CUTOVER: None,
}
JOB_PERMISSIONS = {
    CANDIDATE: {"actions": "read", "contents": "read"},
    CUTOVER: {"actions": "read", "contents": "read"},
}
JOB_NEEDS = {CANDIDATE: set(), CUTOVER: {CANDIDATE}}
# The candidate must not declare one: an environment there would put the
# approval in front of the phase that has nothing to approve yet.
JOB_ENVIRONMENT = {CANDIDATE: None, CUTOVER: ENVIRONMENT}

# The order is the contract too. Read down the candidate: prove the request,
# check out the proved commit, prove it is reachable from main, fetch the sealed
# manifest, pin the digests it seals, derive the release identity, learn which
# slot is idle, deploy into it, smoke it. Digest binding precedes the deploy,
# the deploy precedes the smoke, and the slot exists before either needs it.
EXPECTED_STEPS = {
    CANDIDATE: [
        {
            "env": {"RELEASE_SHA": SHA, "BUILD_RUN_ID": RUN_ID},
            "run": [
                "set -euo pipefail",
                '[[ "$RELEASE_SHA" =~ ^[a-f0-9]{40}$ ]]',
                '[[ "$BUILD_RUN_ID" =~ ^[1-9][0-9]{0,18}$ ]]',
                '[[ "$GITHUB_REF" == "refs/heads/main" ]]',
            ],
        },
        CHECKOUT_STEP,
        MAIN_GATE_STEP,
        DOWNLOAD_STEP,
        {
            "env": {"NCHAT_PROD_RELEASE_SHA": SHA},
            "run": ["scripts/deploy/nchat-prod/release-digests.sh release-manifest artifacts"],
        },
        {
            "id": "release",
            "run": ['echo "release_id=$(cat artifacts/release-id.txt)" >>"$GITHUB_OUTPUT"'],
        },
        {
            "id": "slot",
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'slot="$(opposite_slot "$(resolve_active_slot "$(collect_service_slots)")")"',
                'echo "slot=$slot" >>"$GITHUB_OUTPUT"',
            ],
        },
        {
            "env": {
                "ARTIFACTS_DIR": "${{ github.workspace }}/artifacts",
                "NCHAT_PROD_RELEASE_SHA": SHA,
                "NCHAT_PROD_TOPOLOGY_FILE": "${{ vars.NCHAT_PROD_TOPOLOGY_FILE }}",
                "NCHAT_PROD_CAPACITY_EVIDENCE_DIR": "${{ vars.NCHAT_PROD_CAPACITY_EVIDENCE_DIR }}",
                "NCHAT_PROD_ASSUME_YES": "1",
            },
            "run": ["scripts/deploy/nchat-prod/deploy.sh"],
        },
        {
            "env": {"CANDIDATE_SLOT": SLOT_OUTPUT},
            "run": ['scripts/deploy/nchat-prod/smoke.sh --target "$CANDIDATE_SLOT"'],
        },
        {
            "env": {
                "CANDIDATE_SLOT": SLOT_OUTPUT,
                "RELEASE_SHA": SHA,
                "RELEASE_ID": RELEASE_ID_OUTPUT,
            },
            "run": [
                'echo "Candidate slot $CANDIDATE_SLOT carries release $RELEASE_SHA ($RELEASE_ID) and no production traffic."',
                'echo "Complete the authenticated release smoke printed above against the slot\'s"',
                'echo "preview hosts, record it in the release ticket, then approve the"',
                "echo \"'production' environment to release the cutover job.\"",
            ],
        },
    ],
    CUTOVER: [
        {
            "env": {"RELEASE_SHA": SHA},
            "run": [
                "set -euo pipefail",
                '[[ "$RELEASE_SHA" =~ ^[a-f0-9]{40}$ ]]',
                '[[ "$GITHUB_REF" == "refs/heads/main" ]]',
            ],
        },
        CHECKOUT_STEP,
        MAIN_GATE_STEP,
        DOWNLOAD_STEP,
        {
            "env": {
                "CANDIDATE_SLOT": NEEDS_SLOT,
                # Slot, commit and sealed release together. cutover.sh compares
                # all three against the cluster and re-derives the identity from
                # the manifest, so a rebuild of the same commit stops matching.
                "NCHAT_PROD_SMOKE_CONFIRMED": f"{NEEDS_SLOT}:{SHA}:{NEEDS_RELEASE_ID}",
                "NCHAT_PROD_RELEASE_MANIFEST_DIR": "${{ github.workspace }}/release-manifest",
                "NCHAT_PROD_ASSUME_YES": "1",
            },
            "run": ['scripts/deploy/nchat-prod/cutover.sh --target "$CANDIDATE_SLOT"'],
        },
    ],
}

# `name` is a label, so it is free to change. Every other key a step may carry
# changes what runs or whether it runs, and none of them is in this contract:
# `if` and `continue-on-error` would let a failed gate be stepped over, `shell`
# and `working-directory` change what a command means.
STEP_KEYS = {"name", "id", "uses", "with", "env", "run"}
JOB_KEYS = {
    "name",
    "needs",
    "environment",
    "runs-on",
    "permissions",
    "timeout-minutes",
    "outputs",
    "steps",
}


def load(path):
    with open(path, encoding="utf-8") as handle:
        return yaml.safe_load(handle)


def normalize_run(run):
    """The executable lines of a `run:`, comments and blank lines removed.

    Comments are documentation: editing one must not fail the contract, and
    adding one must never satisfy it. Everything that survives is compared
    exactly, so a changed command is always a violation.
    """
    lines = []
    for raw in str(run).splitlines():
        code = raw.strip()
        if code and not code.startswith("#"):
            lines.append(code)
    return lines


def step_contract(step):
    """What a step actually does, in the form the expectations are written in."""
    contract = {key: step[key] for key in ("id", "uses", "with", "env") if key in step}
    if "run" in step:
        contract["run"] = normalize_run(step["run"])
    return contract


def triggers_of(workflow):
    """`on:` is parsed as the boolean True by YAML 1.1, so it is read by value."""
    on = workflow.get("on", workflow.get(True))
    if isinstance(on, str):
        return {on}
    return set(on or ())


def needs_of(job):
    needs = job.get("needs", [])
    if isinstance(needs, str):
        return {needs}
    return set(needs)


def environment_of(job):
    """`environment: production` and `environment: {name: production}` are one."""
    environment = job.get("environment")
    if isinstance(environment, dict):
        return environment.get("name")
    return environment


def dispatch_inputs_of(workflow):
    """Whatever is declared under `workflow_dispatch.inputs`, unexamined.

    Returned as-is rather than coerced to {}: `sha:` with nothing after it
    parses as None, and turning that into an empty mapping would report a
    missing contract as a satisfied one.
    """
    on = workflow.get("on", workflow.get(True))
    if not isinstance(on, dict):
        return None
    dispatch = on.get("workflow_dispatch")
    if not isinstance(dispatch, dict):
        return None
    return dispatch.get("inputs")


def check_input(name, spec):
    """One input's declaration. `description` is prose and stays free."""
    if not isinstance(spec, dict):
        return [f"workflow_dispatch input {name} must be a mapping, got {spec!r}"]
    problems = []
    for key, value in INPUT_CONTRACT.items():
        if spec.get(key) != value:
            problems.append(
                f"workflow_dispatch input {name} must declare {key}: {value!r}, got {spec.get(key)!r}"
            )
    return problems


def check_dispatch_inputs(workflow):
    inputs = dispatch_inputs_of(workflow)
    if not isinstance(inputs, dict):
        return [f"workflow_dispatch must declare an inputs mapping, got {inputs!r}"]
    problems = []
    if set(inputs) != set(DISPATCH_INPUTS):
        problems.append(
            f"the workflow must declare exactly the inputs {sorted(DISPATCH_INPUTS)}, "
            f"got {sorted(inputs)}"
        )
    for name in DISPATCH_INPUTS:
        if name in inputs:
            problems += check_input(name, inputs[name])
    return problems


def check_concurrency(workflow):
    """Compared whole, so a missing key and a retyped value fail alike."""
    if workflow.get("concurrency") != CONCURRENCY:
        return [
            f"the workflow must declare concurrency {CONCURRENCY}, "
            f"got {workflow.get('concurrency')!r}"
        ]
    return []


def root_keys_of(workflow):
    """The top-level keys, with `on:` named back.

    YAML 1.1 reads a bare `on` as the boolean true, so the key arrives as True
    and would otherwise look like something nobody declared.
    """
    return {"on" if key is True else key for key in workflow}


def check_root_shape(workflow):
    """Exactly the expected top-level keys: no extras, and none missing."""
    keys = root_keys_of(workflow)
    if keys != ROOT_KEYS:
        return [
            f"the workflow must declare exactly the top-level keys {sorted(ROOT_KEYS)}, "
            f"got {sorted(keys)}"
        ]
    return []


def check_triggers(workflow):
    """A dispatch and nothing else: no push, no schedule, and no PR at all."""
    triggers = triggers_of(workflow)
    problems = []
    if "pull_request_target" in triggers:
        problems.append("pull_request_target is prohibited in the production deploy workflow")
    if triggers != TRIGGERS:
        problems.append(f"the workflow must trigger on exactly {sorted(TRIGGERS)}, got {sorted(triggers)}")
    return problems


def check_workflow(workflow):
    problems = []
    if set(workflow.get("jobs", {})) != set(EXPECTED_STEPS):
        problems.append(
            f"the workflow must define exactly the jobs {sorted(EXPECTED_STEPS)}, "
            f"got {sorted(workflow.get('jobs', {}))}"
        )
    if workflow.get("permissions") != WORKFLOW_PERMISSIONS:
        problems.append(f"the workflow must declare exactly {WORKFLOW_PERMISSIONS}")
    return problems


def check_job_shape(name, job):
    """The job's own settings, and no key that could re-open a closed gate."""
    problems = []
    unexpected = sorted(set(job) - JOB_KEYS)
    if unexpected:
        problems.append(f"{name} job must not declare {unexpected}")
    if needs_of(job) != JOB_NEEDS[name]:
        problems.append(f"{name} job must depend on exactly {sorted(JOB_NEEDS[name])}")
    if environment_of(job) != JOB_ENVIRONMENT[name]:
        problems.append(f"{name} job environment must be {JOB_ENVIRONMENT[name]!r}")
    if job.get("permissions") != JOB_PERMISSIONS[name]:
        problems.append(f"{name} job must run with exactly {JOB_PERMISSIONS[name]}")
    if job.get("runs-on") != RUNNER:
        problems.append(f"{name} job must run on {RUNNER}")
    return problems


def check_job_outputs(name, job):
    """The wiring the promotion depends on, compared as exact expressions."""
    if job.get("outputs") != JOB_OUTPUTS[name]:
        return [f"{name} job outputs must be exactly {JOB_OUTPUTS[name]!r}, got {job.get('outputs')!r}"]
    return []


def check_step(name, index, step, expected):
    problems = []
    unexpected = sorted(set(step) - STEP_KEYS)
    if unexpected:
        problems.append(f"{name} step {index} must not declare {unexpected}")
    actual = step_contract(step)
    if actual != expected:
        problems.append(f"{name} step {index} is not the step the contract expects: {actual!r}")
    return problems


def check_steps(name, job):
    """Same steps, same order, nothing extra.

    This is where a wrapper, a hand-written selector patch and a stray `echo`
    are all refused, without any of them having to be recognised: they are not
    the step the contract has at that position, and there is no position spare.
    """
    steps = job.get("steps") or []
    expected = EXPECTED_STEPS[name]
    if len(steps) != len(expected):
        return [f"{name} job must contain exactly {len(expected)} steps, got {len(steps)}"]
    problems = []
    for index, (step, want) in enumerate(zip(steps, expected)):
        problems += check_step(name, index, step, want)
    return problems


def check_job(name, job):
    return (
        check_job_shape(name, job)
        + check_job_outputs(name, job)
        + check_steps(name, job)
    )


def run(path):
    try:
        workflow = load(path)
        jobs = workflow["jobs"]
    except (OSError, KeyError, TypeError, yaml.YAMLError) as error:
        print(f"the production deploy workflow cannot be read from {path}: {error}", file=sys.stderr)
        return 1
    problems = (
        check_root_shape(workflow)
        + check_triggers(workflow)
        + check_dispatch_inputs(workflow)
        + check_concurrency(workflow)
        + check_workflow(workflow)
    )
    for name in EXPECTED_STEPS:
        if name in jobs:
            problems += check_job(name, jobs[name])
    for problem in problems:
        print(problem, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1]))
