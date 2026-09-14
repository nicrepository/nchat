#!/usr/bin/env python3
"""The production deploy workflow is exactly two jobs (CICD-05, CICD-07).

The guarantee this file exists to hold is one sentence: exactly one execution
path in the workflow changes a stable Service selector, it is `cutover.sh`
called from the `cutover` job, and that job runs only behind the `production`
environment after the `candidate` job has passed every gate before it.

An earlier version of this checker tried to prove that by reading the shell in
each `run:` and looking for dangerous commands. That is the wrong shape for the
problem. Shell has unboundedly many ways to spell one invocation -- `bash x.sh`,
`env bash x.sh`, a variable holding the path, `kubectl patch svc` instead of
`kubectl patch service` -- so a denylist is a list of the spellings someone
happened to think of, and anything unrecognised is read as harmless. That is
fail-open, and it is not fixable by adding more patterns.

So the direction is inverted. This workflow is small, it is security-critical,
and it should stay both: every job and step it is allowed to contain is written
out below, and the workflow must match that contract exactly -- same steps, same
order, same commands, same wiring. Nothing has to be recognised as dangerous,
because nothing unlisted is permitted at all. A promotion added to the
`candidate` job is refused for the same reason `echo hello` is: no such step is
in the contract. A third job -- called `promote`, `finish`, or anything else --
is refused for the same reason: the contract names exactly two.

That is also what confines the promotion to the one place it belongs. Only the
`cutover` job may run `cutover.sh`, it may run it only at the one
position written out below, and the job carrying it is the only one the contract
permits to declare `environment: production`. The `candidate` job may not
declare `needs` or `environment` at all: an approval attached to it would gate
the phase with nothing to approve and pull that environment's secrets into an
unprotected deploy, and the two jobs would stop being separable.

The promotion is not disabled anywhere and never was: there is no `if: false`
to re-enable and no input that selects a promoting path, because a gate that is
one edit away from being open is not a boundary. What holds it shut is the
approval on the environment, which lives on the environment and not in this
repository.

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
# main, and the run id is what binds the deploy to a sealed manifest. An input
# that went missing, became optional, or changed type would leave CI green while
# the only way to deploy was broken or quietly weakened -- an optional `sha`
# defaults to the empty string, which the first validation step would then be
# refusing instead of a mistake anyone meant to make.
#
# The set is exact, and that is also what keeps a bypass out: a third input is a
# new lever on a production deploy -- a slot to target, a gate to force -- and
# must be reviewed as one rather than absorbed silently.
DISPATCH_INPUTS = ("sha", "run_id")
# Serialising production deploys is structural, not a nicety. A run applying
# migrations or waiting on a rollout is holding a half-built candidate, and a
# second run in the meantime would redeploy that slot underneath it. The later
# gates still fail closed, but "fails closed" and "behaves predictably" are
# different properties, and an operator watching a run that is quietly no longer
# describing the cluster is owed the second one.
#
# cancel-in-progress must be false, and false as a boolean: the string "false"
# is truthy to the expression evaluator, so a quoted one would cancel exactly
# the run that is holding a half-built candidate.
CONCURRENCY = {"group": "nchat-prod-deploy", "cancel-in-progress": False}
# `description` is prose and free to reword; these two decide behaviour.
INPUT_CONTRACT = {"required": True, "type": "string"}
# And nothing else may be declared. `default` is the one that matters: it turns
# a required gate into a value the dispatcher can leave alone, so a release
# could be started without anyone naming a commit or a build. `options` and
# `deprecationMessage` are refused for the same reason `run-name` is -- they
# are behaviour nobody reviewed here. `description` stays free in content.
INPUT_KEYS = {"required", "type", "description"}
# Long enough for the work, short enough that a wedged run does not hold the
# concurrency group all day: the candidate deploys ten workloads and may run
# migrations, the cutover patches ten Services and reads each one back. Both
# directions are the contract -- a minute would fail healthy runs, and no limit
# at all would let either sit on the group indefinitely, which for the cutover
# includes the whole approval wait.
TIMEOUT_MINUTES = {CANDIDATE: 45, CUTOVER: 15}
WORKFLOW_PERMISSIONS = {"contents": "read"}
RUNNER = ["self-hosted", "linux", "x64", "nchat-prod-deploy"]

CHECKOUT = "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683"
DOWNLOAD = "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093"
SHA = "${{ inputs.sha }}"
RUN_ID = "${{ inputs.run_id }}"
# The slot the smoke and the report are wired to, and the release identity the
# report names. Comparing these strings exactly is what stops a hardcoded
# `green`, a second workflow input, or another step's output standing in.
SLOT_OUTPUT = "${{ steps.slot.outputs.slot }}"
ACTIVE_OUTPUT = "${{ steps.slot.outputs.active }}"
RELEASE_ID_OUTPUT = "${{ steps.release.outputs.release_id }}"
# What the cutover job promotes. Read from the candidate job's declared outputs
# and from nowhere else: a slot taken from an input, a literal or a second
# reading of the cluster would be a candidate nobody approved.
NEEDS_SLOT = "${{ needs.candidate.outputs.slot }}"
NEEDS_RELEASE_ID = "${{ needs.candidate.outputs.release_id }}"
ROLLBACK_OUTPUT = "${{ steps.before.outputs.rollback_target }}"
# The release this run built, as one string: the dispatched commit and the seal
# of the manifest its digests came from. Compared exactly, so dropping either
# half -- a commit with no build, a build with no commit -- is a violation, and
# so is standing a literal or another step's output in for one of them.
EXPECTED_RELEASE = f"{SHA}:{RELEASE_ID_OUTPUT}"
# The same identity as the cutover job can name it: the dispatched commit and
# the seal the candidate job resolved. Compared exactly, so half an identity or
# a half rewired to another expression is a violation here too.
PROMOTED_RELEASE = f"{SHA}:{NEEDS_RELEASE_ID}"
# The evidence cutover.sh checks: the slot AND the release on it. The slot name
# alone would still match a candidate that was redeployed after it was smoked.
SMOKE_EVIDENCE = f"{NEEDS_SLOT}:{PROMOTED_RELEASE}"
# The snapshot the stable-selector invariant is proved against. Both steps must
# name the same file, or the comparison is between a reading and nothing.
# The one `if:` the contract permits, and only on the step that records the
# after-state.
#
# It is an allowlist of the two conclusions that mean the promotion actually
# ran, and the allowlist form is the requirement rather than the wording.
# Naming any status function stops Actions inserting the implicit `success()`,
# so every looser spelling runs the step when nothing was promoted:
# `conclusion != ''` is satisfied by `skipped`, which is exactly what a step
# reports when an earlier one failed, and `!cancelled()` on its own is
# satisfied by everything. Both are refused below, by name.
AFTER_CONCLUSIONS = ("success", "failure")
AFTER_CONDITION = (
    "${{ !cancelled() && (steps.promote.conclusion == 'success'"
    " || steps.promote.conclusion == 'failure') }}"
)
# Spellings that read as "the promotion was reached" and are not. Refused
# explicitly, so the reason a mutation fails is the reason it is wrong.
AFTER_CONDITION_REJECTED = ("!= ''", '!= ""', "!= 'skipped'", '!= "skipped"')
SELECTORS_BEFORE = "${{ runner.temp }}/stable-selectors-before.txt"
SELECTORS_AFTER = "${{ runner.temp }}/stable-selectors-after.txt"

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

# The one channel between the two halves of a release: exactly what the cutover
# job consumes, and nothing else. A third output is a second channel and has to
# be reviewed as one; the cutover job declares none, because nothing follows it,
# and a declared output with no consumer is wiring for a job that would have to
# be added first -- in a diff this contract makes visible.
JOB_OUTPUTS = {
    CANDIDATE: {"slot": SLOT_OUTPUT, "release_id": RELEASE_ID_OUTPUT},
    CUTOVER: None,
}
READ_ONLY_PERMISSIONS = {"actions": "read", "contents": "read"}
JOB_PERMISSIONS = {CANDIDATE: READ_ONLY_PERMISSIONS, CUTOVER: READ_ONLY_PERMISSIONS}
# The wiring that makes the two jobs a sequence with a human in it. `None` is a
# requirement, not an absence: the candidate job must declare neither.
JOB_NEEDS = {CANDIDATE: None, CUTOVER: CANDIDATE}
JOB_ENVIRONMENT = {CANDIDATE: None, CUTOVER: "production"}

# The order is the contract too. Read down the job: prove the request, check out
# the proved commit, prove it is reachable from main, fetch the sealed manifest,
# pin the digests it seals, derive the release identity, snapshot the stable
# Services and learn which slot is idle, deploy into the other one, smoke it,
# prove the stable Services still select what the snapshot recorded, and prove
# the candidate is running the release this run built.
#
# Every dependency in that sentence is an ordering the contract holds by
# position: digests are bound before anything is deployed, the snapshot is taken
# before the deploy that must not disturb it, the deploy finishes before the
# smoke that validates it, and both invariants are read after all of it. The
# report is last, so no success is published before either has passed.
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
        # One read of the cluster feeding both the snapshot and the slot
        # decision. Written as one step because they must describe the same
        # instant: a separate reader could snapshot a state no deploy was ever
        # planned from, and the invariant would then prove nothing.
        {
            "id": "slot",
            "env": {"SELECTORS_BEFORE": SELECTORS_BEFORE},
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'mapping="$(collect_service_slots)"',
                'printf \'%s\\n\' "$mapping" >"$SELECTORS_BEFORE"',
                'active="$(resolve_active_slot "$mapping")"',
                'echo "active=$active" >>"$GITHUB_OUTPUT"',
                'echo "slot=$(opposite_slot "$active")" >>"$GITHUB_OUTPUT"',
                'echo "stable Service selectors before the deploy:"',
                'cat "$SELECTORS_BEFORE"',
            ],
        },
        {
            "env": {
                "ARTIFACTS_DIR": "${{ github.workspace }}/artifacts",
                # Compared exactly: the deploy must build the slot the snapshot
                # step resolved, not one it derives for itself.
                "NCHAT_PROD_CANDIDATE_SLOT": SLOT_OUTPUT,
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
        # `diff` is the assertion. Under `set -e` a difference ends the step,
        # which is the whole behaviour: this step detects that something moved a
        # stable Service, and it must never be the thing that puts one back.
        {
            "env": {
                "SELECTORS_BEFORE": SELECTORS_BEFORE,
                "SELECTORS_AFTER": SELECTORS_AFTER,
            },
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
        # The second invariant, independent of the first: the selectors prove
        # traffic did not move, this proves the candidate was not replaced.
        # `require_slot_release_identity` reads the cluster and refuses every state
        # that is not exactly the expected release, so there is no spelling of
        # this step that passes on a slot carrying something else.
        {
            "env": {
                "CANDIDATE_SLOT": SLOT_OUTPUT,
                "EXPECTED_RELEASE": EXPECTED_RELEASE,
            },
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE"',
                "echo",
                'echo "Slot $CANDIDATE_SLOT is running exactly $EXPECTED_RELEASE."',
            ],
        },
        {
            "env": {
                "ACTIVE_SLOT": ACTIVE_OUTPUT,
                "CANDIDATE_SLOT": SLOT_OUTPUT,
                "EXPECTED_RELEASE": EXPECTED_RELEASE,
            },
            "run": [
                'echo "Active slot   : $ACTIVE_SLOT, still serving every stable Service."',
                'echo "Candidate slot: $CANDIDATE_SLOT, verified against the cluster as running $EXPECTED_RELEASE, carrying no production traffic."',
                'echo "This job does not promote. The cutover job is next, and it starts only"',
                'echo "once a reviewer approves this run in the production environment."',
            ],
        },
    ],
    # The promotion, read down: check out the approved commit, prove main can
    # still reach it, fetch the sealed manifest again, read the cluster as it is
    # now, prove the approved candidate is still on it, promote, prove every
    # stable Service converged, prove the promoted slot still carries the
    # approved release, and only then report.
    #
    # Every dependency in that sentence is an ordering held by position. The
    # snapshot is taken before the identity it will be promoted under is
    # checked, both are checked before anything is patched, and both proofs
    # follow the patch. cutover.sh is the sixth step and there is no seventh
    # position for a second mutation.
    CUTOVER: [
        CHECKOUT_STEP,
        MAIN_GATE_STEP,
        DOWNLOAD_STEP,
        # The fail-closed read, and it is a classification rather than a
        # resolution: `require_promotable_selectors` refuses a Service selecting
        # anything that is neither slot -- an unexpected value, no release-slot
        # key, no Service -- and permits a blue/green split, which is what a
        # retry to the same target exists to converge. Its status is its own,
        # not swallowed inside another command's argument.
        #
        # The rollback target is derived from the authorised target, never read
        # back from the selectors.
        {
            "id": "before",
            "env": {
                "TARGET_SLOT": NEEDS_SLOT,
                "SELECTORS_BEFORE": SELECTORS_BEFORE,
            },
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'mapping="$(collect_service_slots)"',
                'printf \'%s\\n\' "$mapping" >"$SELECTORS_BEFORE"',
                'echo "stable Service selectors before the cutover:"',
                'cat "$SELECTORS_BEFORE"',
                'require_promotable_selectors "$mapping" "$TARGET_SLOT"',
                'rollback_target="$(opposite_slot "$TARGET_SLOT")"',
                'echo "rollback_target=$rollback_target" >>"$GITHUB_OUTPUT"',
                'echo "Every stable Service selects $TARGET_SLOT or $rollback_target."',
            ],
        },
        # The approval names a candidate:release; this is where the cluster is
        # required to still be carrying it. Removing it would leave the run
        # promoting whatever happens to be on the slot by now.
        {
            "env": {
                "CANDIDATE_SLOT": NEEDS_SLOT,
                "EXPECTED_RELEASE": PROMOTED_RELEASE,
            },
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE"',
                "echo",
                'echo "Slot $CANDIDATE_SLOT still carries exactly $EXPECTED_RELEASE."',
            ],
        },
        # The one mutation the whole contract exists to confine, compared
        # exactly: the script, the explicit `--target`, the slot it targets, the
        # evidence naming slot and release together, and the directory the
        # sealed manifest was downloaded into. No spelling of this step promotes
        # a slot the candidate job did not build.
        {
            "id": "promote",
            "env": {
                "CANDIDATE_SLOT": NEEDS_SLOT,
                "NCHAT_PROD_SMOKE_CONFIRMED": SMOKE_EVIDENCE,
                "NCHAT_PROD_RELEASE_MANIFEST_DIR": "release-manifest",
                "NCHAT_PROD_ASSUME_YES": "1",
            },
            "run": ['scripts/deploy/nchat-prod/cutover.sh --target "$CANDIDATE_SLOT"'],
        },
        # Recording the after-state, and asserting nothing about it. This is the
        # only step in either job the contract permits an `if:` on, and the
        # condition is compared exactly: a promotion that stopped part-way is
        # the run whose after-state matters most and the run an asserting step
        # would never reach, so this one runs whenever the promotion ran at all.
        # It carries no continue-on-error and cannot turn a failure into a pass.
        {
            "if": AFTER_CONDITION,
            "env": {"SELECTORS_AFTER": SELECTORS_AFTER},
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'collect_service_slots >"$SELECTORS_AFTER"',
                'echo "stable Service selectors after the cutover:"',
                'cat "$SELECTORS_AFTER"',
            ],
        },
        # The judgement, as an ordinary step: a failed promotion skips it, so no
        # success is ever claimed for one. `all_services_on_slot` passes only on
        # total convergence, so a missing Service, an unset selector, a leftover
        # slot and a blue/green mixture are all the same refusal.
        {
            "env": {
                "CANDIDATE_SLOT": NEEDS_SLOT,
                "SELECTORS_AFTER": SELECTORS_AFTER,
            },
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$CANDIDATE_SLOT"',
                'echo "Every stable Service selects slot $CANDIDATE_SLOT."',
            ],
        },
        # Selector convergence says where the traffic goes, not what is there.
        {
            "env": {
                "CANDIDATE_SLOT": NEEDS_SLOT,
                "EXPECTED_RELEASE": PROMOTED_RELEASE,
            },
            "run": [
                "set -euo pipefail",
                "source scripts/deploy/nchat-prod/lib.sh",
                'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE"',
                "echo",
                'echo "Production serves $EXPECTED_RELEASE on slot $CANDIDATE_SLOT."',
            ],
        },
        {
            "env": {
                "ROLLBACK_SLOT": ROLLBACK_OUTPUT,
                "CANDIDATE_SLOT": NEEDS_SLOT,
                "EXPECTED_RELEASE": PROMOTED_RELEASE,
            },
            "run": [
                'echo "Promoted slot : $CANDIDATE_SLOT, serving every stable Service as $EXPECTED_RELEASE."',
                'echo "Rollback slot : $ROLLBACK_SLOT, still running and untouched. It is the rollback target."',
                'echo "Roll back with: scripts/deploy/nchat-prod/rollback.sh --target $ROLLBACK_SLOT \'<reason>\'"',
                'echo "Observe before retiring the previous slot; see sections 13 to 16 of"',
                'echo "docs/runbooks/production-blue-green-deployment.md."',
            ],
        },
    ],
}

# `name` is a label, so it is free to change. Every other key a step may carry
# changes what runs or whether it runs. `continue-on-error` is absent because it
# would let a failed gate be stepped over; `shell` and `working-directory` change
# what a command means. `if` is permitted as a key only so the one step that
# needs it can declare it -- every step's contract is compared whole, so an `if`
# on any other step is a mismatch with an expectation that has none, and the one
# that does have it must carry exactly the condition written above.
STEP_KEYS = {"name", "id", "if", "uses", "with", "env", "run"}
BASE_JOB_KEYS = {
    "name",
    "runs-on",
    "permissions",
    "timeout-minutes",
    "outputs",
    "steps",
}
# `needs` and `environment` are permitted on the cutover job and refused on the
# candidate job, and both halves of that are rules rather than omissions. The
# candidate has nothing to depend on, and an `environment:` on it would attach
# an approval to the phase with nothing to approve and pull that environment's
# secrets into an unprotected deploy. `if:` and `strategy:` are in neither set,
# so they are refused by the same comparison that refuses a key GitHub has not
# shipped yet.
JOB_KEYS = {
    CANDIDATE: BASE_JOB_KEYS,
    CUTOVER: BASE_JOB_KEYS | {"needs", "environment"},
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
    contract = {
        key: step[key] for key in ("id", "if", "uses", "with", "env") if key in step
    }
    if "run" in step:
        contract["run"] = normalize_run(step["run"])
    return contract


def triggers_of(workflow):
    """`on:` is parsed as the boolean True by YAML 1.1, so it is read by value."""
    on = workflow.get("on", workflow.get(True))
    if isinstance(on, str):
        return {on}
    return set(on or ())


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
    """One input's declaration, closed. `description` is prose and stays free."""
    if not isinstance(spec, dict):
        return [f"workflow_dispatch input {name} must be a mapping, got {spec!r}"]
    problems = []
    unexpected = sorted(set(spec) - INPUT_KEYS)
    if unexpected:
        problems.append(f"workflow_dispatch input {name} must not declare {unexpected}")
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
    # Exactly these two jobs. A third is refused whatever it is called and
    # whatever it does, so a second promotion cannot be added under a name the
    # contract failed to anticipate.
    if set(workflow.get("jobs", {})) != set(EXPECTED_STEPS):
        problems.append(
            f"the workflow must define exactly the jobs {sorted(EXPECTED_STEPS)}, "
            f"got {sorted(workflow.get('jobs', {}))}"
        )
    if workflow.get("permissions") != WORKFLOW_PERMISSIONS:
        problems.append(f"the workflow must declare exactly {WORKFLOW_PERMISSIONS}")
    return problems


def check_job_shape(name, job):
    """The job's own settings, and no key that could re-open a closed gate.

    `if:` and `strategy:` are refused by the same comparison that refuses a key
    GitHub has not shipped yet: neither is in this job's JOB_KEYS, and nothing
    outside it is permitted.
    """
    problems = []
    unexpected = sorted(set(job) - JOB_KEYS[name])
    if unexpected:
        problems.append(f"{name} job must not declare {unexpected}")
    if job.get("permissions") != JOB_PERMISSIONS[name]:
        problems.append(f"{name} job must run with exactly {JOB_PERMISSIONS[name]}")
    if job.get("runs-on") != RUNNER:
        problems.append(f"{name} job must run on {RUNNER}")
    problems += check_timeout(name, job)
    return problems


def check_job_wiring(name, job):
    """What the job depends on, and what gates it.

    Both are compared to an expected value that may be `None`, so "must declare
    exactly this" and "must declare nothing" are one comparison. `environment`
    is compared to the plain string: the mapping form accepts `url:` and other
    keys nobody has reviewed here, and the approval itself is configured on the
    environment rather than in this file.
    """
    problems = []
    if job.get("needs") != JOB_NEEDS[name]:
        problems.append(
            f"{name} job needs must be exactly {JOB_NEEDS[name]!r}, got {job.get('needs')!r}"
        )
    if job.get("environment") != JOB_ENVIRONMENT[name]:
        problems.append(
            f"{name} job environment must be exactly {JOB_ENVIRONMENT[name]!r}, "
            f"got {job.get('environment')!r}"
        )
    return problems


def check_timeout(name, job):
    """Exactly the minutes this job is allowed, and an integer.

    The type is checked before the value, and `bool` is excluded from it. YAML's
    `"45"` is a string Actions rejects at parse time, so a contract that
    compared it loosely would be green on a workflow nobody can run; and
    `True == 1` in Python, so a bare equality would read `timeout-minutes: true`
    as a one-minute limit. Only then is the value compared.
    """
    expected = TIMEOUT_MINUTES[name]
    timeout = job.get("timeout-minutes")
    if isinstance(timeout, int) and not isinstance(timeout, bool):
        if timeout == expected:
            return []
    return [f"{name} job must declare timeout-minutes: {expected}, got {timeout!r}"]


def check_job_outputs(name, job):
    """Exactly the outputs the contract declares, or none where none belong."""
    if job.get("outputs") != JOB_OUTPUTS[name]:
        return [f"{name} job outputs must be exactly {JOB_OUTPUTS[name]!r}, got {job.get('outputs')!r}"]
    return []


def check_after_condition(name, index, condition):
    """A conditional step must name the conclusions it accepts, one by one.

    Compared as a policy rather than as text, because the exact-contract match
    alone would report only "not the step expected" for a condition that is
    wrong in a specific and repeatable way. Every conclusion the step may run on
    has to appear as its own equality, and the two spellings that quietly
    include `skipped` are named so a mutation says why it failed.
    """
    problems = []
    for conclusion in AFTER_CONCLUSIONS:
        if f"steps.promote.conclusion == '{conclusion}'" not in condition:
            problems.append(
                f"{name} step {index} must run on promote's {conclusion} conclusion explicitly"
            )
    for rejected in AFTER_CONDITION_REJECTED:
        if rejected in condition:
            problems.append(
                f"{name} step {index} must not gate on {rejected}: a step skipped after an "
                "earlier failure satisfies it, so nothing was promoted and production is read anyway"
            )
    if "!cancelled()" not in condition:
        problems.append(f"{name} step {index} must not run on a cancelled job")
    return problems


def check_step(name, index, step, expected):
    problems = []
    unexpected = sorted(set(step) - STEP_KEYS)
    if unexpected:
        problems.append(f"{name} step {index} must not declare {unexpected}")
    if "if" in step:
        problems += check_after_condition(name, index, str(step["if"]))
    actual = step_contract(step)
    if actual != expected:
        problems.append(f"{name} step {index} is not the step the contract expects: {actual!r}")
    return problems


def check_steps(name, job):
    """Same steps, same order, nothing extra.

    This is where a wrapper around cutover.sh in the candidate job, a call to
    rollback.sh or drain-old.sh in either, a hand-written selector patch, a
    migration and a stray `echo` are all refused, without any of them having to
    be recognised: they are not the step the contract has at that position, and
    there is no position spare.
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
        + check_job_wiring(name, job)
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
