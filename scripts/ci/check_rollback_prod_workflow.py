#!/usr/bin/env python3
"""The production rollback workflow is exactly one job (CICD-08).

Written in the same shape, and for the same reason, as
check_deploy_prod_workflow.py: the executable surface of a workflow that can
move production traffic is an allowlist, not a search for dangerous commands.
Shell has unboundedly many ways to spell one invocation, so a denylist is a list
of the spellings someone happened to think of and everything unrecognised reads
as harmless. That is fail-open. So every job and step this workflow is allowed
to contain is written out below and the file must match it exactly -- same
steps, same order, same commands, same wiring. A build, a migration, a candidate
deploy, a Secret, a volume, a DNS call and `echo hello` are all refused for the
same reason: no such step is in the contract, and there is no spare position.

What this contract adds over the deploy's, and why:

  * The trigger is a dispatch and nothing else, and the workflow depends on no
    other run. A `workflow_run`, a `workflow_call` or a `needs` on a release job
    would make the recovery path unavailable in exactly the situation it exists
    for -- a broken release pipeline.

  * The target is a `choice` over the two slots. The slot is never derived: a
    rollback that read its destination from the cluster would, on a second run,
    send production back to the release it had just been rescued from.

  * The reason is required, and it is validated as data before it is used. Both
    inputs travel as `env:` and never as expression interpolation into a script.

  * The schema proof and the switch are ONE step, holding the migration
    advisory lock across both. Separate steps leave a window in which a
    migration completes between the proof and the switch, and neither a
    migration Job nor an operator's `make migrations-up` is serialised by
    GitHub's `concurrency:`. The ledger is read from public.schema_migrations
    inside that lock and never from the cluster's workloads: deploy.sh migrates
    before it applies the candidate, so a release whose migration completed and
    whose rollout failed leaves the schema advanced with no workload carrying
    it.

  * The concurrency group is the deploy's. A rollback and a cutover patch the
    same selectors, and a group of its own would race them.

Usage: check_rollback_prod_workflow.py <workflow-file>
Exit code 0 when the contract holds; 1 with one short reason per violation on
stderr when it does not.
"""

from __future__ import annotations

import sys

import yaml

ROOT_KEYS = {"name", "on", "permissions", "concurrency", "jobs"}
JOB = "rollback"
TRIGGERS = {"workflow_dispatch"}
# Exactly two inputs. A third is a new lever on a production traffic switch -- a
# gate to force, a slot to skip a check for -- and must be reviewed as one.
DISPATCH_INPUTS = ("target_slot", "reason")
# The slot is a closed choice rather than a free string: an operator picks one
# of two, and no other value can be dispatched at all.
INPUT_CONTRACT = {
    "target_slot": {"required": True, "type": "choice", "options": ["blue", "green"]},
    "reason": {"required": True, "type": "string"},
}
INPUT_KEYS = {
    "target_slot": {"required", "type", "options", "description"},
    "reason": {"required", "type", "description"},
}
# The deploy's group, deliberately. Two runs patching the same ten selectors at
# once leave a namespace neither can describe, and `cancel-in-progress` must be
# the boolean false: the string "false" is truthy to the expression evaluator,
# and cancelling a run part-way through a traffic switch is the state this
# workflow exists to avoid.
CONCURRENCY = {"group": "nchat-prod-deploy", "cancel-in-progress": False}
# Long enough for ten patches with a read-back each and a smoke after; short
# enough that a wedged run does not hold the production traffic group all day.
TIMEOUT_MINUTES = 15
WORKFLOW_PERMISSIONS = {"contents": "read"}
# No `actions: read`: this workflow downloads no artifact, which is the same
# fact as its independence from the release pipeline.
JOB_PERMISSIONS = {"contents": "read"}
RUNNER = ["self-hosted", "linux", "x64", "nchat-prod-deploy"]
CHECKOUT = "actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683"

TARGET = "${{ inputs.target_slot }}"
REASON = "${{ inputs.reason }}"
TARGET_RELEASE = "${{ steps.target.outputs.target_release }}"
APPLIED_LEDGER = "${{ runner.temp }}/applied-migrations.txt"
SELECTORS_BEFORE = "${{ runner.temp }}/stable-selectors-before.txt"
SELECTORS_AFTER = "${{ runner.temp }}/stable-selectors-after.txt"

# The one conditional spelling that means "the mutation actually ran". Naming
# any status function stops Actions inserting the implicit success(), so every
# looser test runs the step on a run that rolled nothing back.
AFTER_CONCLUSIONS = ("success", "failure")
AFTER_CONDITION = (
    "${{ !cancelled() && (steps.rollback.conclusion == 'success'"
    " || steps.rollback.conclusion == 'failure') }}"
)
# Spellings that read as "the mutation was reached" and are not.
AFTER_CONDITION_REJECTED = ("!= ''", '!= ""', "!= 'skipped'", '!= "skipped"')
# The evidence step runs on every conclusion but a cancellation, because a
# refused or part-way rollback is the run whose record matters most. It asserts
# nothing, so running it after a failure claims nothing.
EVIDENCE_CONDITION = "${{ !cancelled() }}"

EXPECTED_STEPS = [
    # Both inputs are proved before either reaches a command, and the ref is
    # required to be main so a feature branch cannot run its own copy of these
    # gates against production.
    {
        "env": {"TARGET_SLOT": TARGET, "ROLLBACK_REASON": REASON},
        "run": [
            "set -euo pipefail",
            '[[ "$TARGET_SLOT" == "blue" || "$TARGET_SLOT" == "green" ]]',
            '[[ "$ROLLBACK_REASON" =~ [^[:space:]] ]]',
            '[[ "${#ROLLBACK_REASON}" -ge 3 && "${#ROLLBACK_REASON}" -le 200 ]]',
            '[[ "$GITHUB_REF" == "refs/heads/main" ]]',
        ],
    },
    # No `ref:`: the operational branch this was dispatched from, already
    # required to be main. Full history, because the schema gate walks the
    # migrations between two releases.
    {
        "uses": CHECKOUT,
        "with": {"fetch-depth": 0, "persist-credentials": False},
    },
    {
        "id": "before",
        "env": {"TARGET_SLOT": TARGET, "SELECTORS_BEFORE": SELECTORS_BEFORE},
        "run": [
            "set -euo pipefail",
            "source scripts/deploy/nchat-prod/lib.sh",
            'mapping="$(collect_service_slots)"',
            'printf \'%s\\n\' "$mapping" >"$SELECTORS_BEFORE"',
            'echo "stable Service selectors before the rollback:"',
            'cat "$SELECTORS_BEFORE"',
            'require_promotable_selectors "$mapping" "$TARGET_SLOT"',
            'echo "Every stable Service selects $TARGET_SLOT or $(opposite_slot "$TARGET_SLOT")."',
        ],
    },
    # Where the release the later proofs compare against comes from. Its own
    # status, not swallowed inside another command's argument.
    {
        "id": "target",
        "env": {"TARGET_SLOT": TARGET},
        "run": [
            "set -euo pipefail",
            "source scripts/deploy/nchat-prod/lib.sh",
            'release="$(require_consistent_release "$TARGET_SLOT")"',
            'slot_ready "$TARGET_SLOT"',
            'echo "target_release=$release" >>"$GITHUB_OUTPUT"',
            'echo "Slot $TARGET_SLOT is Ready and carries release $release."',
        ],
    },
    # The schema proof and the switch, in one step, under one lock.
    #
    # They cannot be separate steps and be sound. The gate's answer stops being
    # true the moment a migration completes, and a migration can complete from a
    # migration Job or from an operator's shell, neither of which GitHub's
    # `concurrency:` serialises. Reading the ledger in one step and switching in
    # a later one is exactly the window; checking afterwards does not close it.
    #
    # So the contract pins one step running rollback-critical-section.sh, which
    # holds scripts/db/migrate.sh's advisory lock across the ledger read, the
    # gate, the switch and the liveness proofs either side of it. A workflow that
    # went back to calling applied-migrations.sh, rollback-schema-gate.sh or
    # rollback.sh from separate steps is refused here -- not for what those
    # scripts do, but because the contract has one step at this position and
    # there is no spare one.
    {
        "id": "rollback",
        "env": {
            "TARGET_SLOT": TARGET,
            "ROLLBACK_REASON": REASON,
            "NCHAT_PROD_APPLIED_LEDGER": APPLIED_LEDGER,
            "NCHAT_PROD_ASSUME_YES": "1",
        },
        "run": [
            'scripts/deploy/nchat-prod/rollback-critical-section.sh --target "$TARGET_SLOT" "$ROLLBACK_REASON"'
        ],
    },
    {
        "if": AFTER_CONDITION,
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
        "env": {"TARGET_SLOT": TARGET, "SELECTORS_AFTER": SELECTORS_AFTER},
        "run": [
            "set -euo pipefail",
            "source scripts/deploy/nchat-prod/lib.sh",
            'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$TARGET_SLOT"',
            'echo "Every stable Service selects slot $TARGET_SLOT."',
        ],
    },
    {
        "env": {"TARGET_SLOT": TARGET, "TARGET_RELEASE": TARGET_RELEASE},
        "run": [
            "set -euo pipefail",
            "source scripts/deploy/nchat-prod/lib.sh",
            'require_slot_release_identity "$TARGET_SLOT" "$TARGET_RELEASE"',
            "echo",
            'echo "Production serves $TARGET_RELEASE on slot $TARGET_SLOT."',
        ],
    },
    # `--active` and nothing else. The candidate smoke refuses a slot that holds
    # traffic, which after a rollback is every valid target; `--active` requires
    # the opposite and stronger fact -- every stable Service selects this slot --
    # so this is not the candidate rule relaxed, it is a different question.
    {
        "id": "smoke",
        "env": {"TARGET_SLOT": TARGET},
        "run": ['scripts/deploy/nchat-prod/smoke.sh --target "$TARGET_SLOT" --active'],
    },
    # Read-only and asserting nothing, so running it after a failure claims
    # nothing. Every step that judges keeps its own status.
    {
        "if": EVIDENCE_CONDITION,
        "env": {
            "TARGET_SLOT": TARGET,
            "ROLLBACK_REASON": REASON,
            "TARGET_RELEASE": TARGET_RELEASE,
            "APPLIED_LEDGER": APPLIED_LEDGER,
            "ROLLBACK_RESULT": "${{ steps.rollback.conclusion }}",
            "SMOKE_RESULT": "${{ steps.smoke.conclusion }}",
            "SELECTORS_BEFORE": SELECTORS_BEFORE,
            "SELECTORS_AFTER": SELECTORS_AFTER,
        },
        "run": [
            "set -euo pipefail",
            'recorded() { if [[ -f "$1" ]]; then cat "$1"; else echo "not recorded"; fi; }',
            "recorded_count() {",
            'if [[ ! -f "$1" ]]; then echo "NOT READ"; return; fi',
            'if [[ ! -s "$1" ]]; then echo "READ FAILED (empty)"; return; fi',
            'if [[ "$(head -n 1 "$1")" != "# nchat-applied-migrations v1" ]]; then',
            'echo "INVALID (no ledger header)"',
            "return",
            "fi",
            "printf '%s row(s) read under the lock' \"$(($(wc -l <\"$1\") - 1))\"",
            "}",
            "{",
            'echo "## Production rollback"',
            "echo",
            'echo "| field | value |"',
            'echo "| --- | --- |"',
            'echo "| operation | production rollback (stable Service selectors only) |"',
            'echo "| target slot | $TARGET_SLOT |"',
            'echo "| target release | ${TARGET_RELEASE:-not established} |"',
            'echo "| schema proof + switch (under lock) | $ROLLBACK_RESULT |"',
            'echo "| applied-migration ledger | $(recorded_count "$APPLIED_LEDGER") |"',
            'echo "| rollback result | $ROLLBACK_RESULT |"',
            'echo "| minimum smoke | $SMOKE_RESULT |"',
            'echo "| run | $GITHUB_RUN_ID attempt $GITHUB_RUN_ATTEMPT |"',
            "echo",
            'echo "### Reason"',
            "echo '```text'",
            'printf \'%s\\n\' "$ROLLBACK_REASON"',
            "echo '```'",
            'echo "### Stable Service selectors before"',
            "echo '```text'",
            'recorded "$SELECTORS_BEFORE"',
            "echo '```'",
            'echo "### Stable Service selectors after"',
            "echo '```text'",
            'recorded "$SELECTORS_AFTER"',
            "echo '```'",
            "echo",
            'echo "No build, no image, no migration and no database change: a rollback moves"',
            'echo "the stable Service selectors and nothing else. The slot that left traffic is"',
            'echo "still running and must not be drained during the observation window."',
            '} >>"$GITHUB_STEP_SUMMARY"',
        ],
    },
]

# The positions the two conditional steps occupy. Every other step is compared
# with no `if` at all, so a condition anywhere else is a mismatch.
AFTER_RECORD_INDEX = 5
EVIDENCE_INDEX = len(EXPECTED_STEPS) - 1
CONDITIONAL_STEPS = {
    AFTER_RECORD_INDEX: AFTER_CONDITION,
    EVIDENCE_INDEX: EVIDENCE_CONDITION,
}

STEP_KEYS = {"name", "id", "if", "uses", "with", "env", "run"}
# `needs` and `environment` are absent from this set, and both absences are
# rules. A `needs` would tie the recovery path to another job in a pipeline that
# may be exactly what is broken. An `environment` would queue the one procedure
# that restores service behind the same approval that gates promotion; what
# authorises this run is the dispatch permission, the main-only ref and the
# host-side runner guard, none of which is editable from a feature branch.
# `if:`, `strategy:` and `outputs:` are refused by the same comparison.
JOB_KEYS = {"name", "runs-on", "permissions", "timeout-minutes", "steps"}


def load(path):
    with open(path, encoding="utf-8") as handle:
        return yaml.safe_load(handle)


def normalize_run(run):
    """The executable lines of a `run:`, comments and blank lines removed."""
    lines = []
    for raw in str(run).splitlines():
        code = raw.strip()
        if code and not code.startswith("#"):
            lines.append(code)
    return lines


def step_contract(step):
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
    """Whatever is declared under `workflow_dispatch.inputs`, unexamined."""
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
    unexpected = sorted(set(spec) - INPUT_KEYS[name])
    if unexpected:
        problems.append(f"workflow_dispatch input {name} must not declare {unexpected}")
    for key, value in INPUT_CONTRACT[name].items():
        if spec.get(key) != value:
            problems.append(
                f"workflow_dispatch input {name} must declare {key}: {value!r}, "
                f"got {spec.get(key)!r}"
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
    if workflow.get("concurrency") != CONCURRENCY:
        return [
            f"the workflow must declare concurrency {CONCURRENCY}, "
            f"got {workflow.get('concurrency')!r}"
        ]
    return []


def root_keys_of(workflow):
    return {"on" if key is True else key for key in workflow}


def check_root_shape(workflow):
    keys = root_keys_of(workflow)
    if keys != ROOT_KEYS:
        return [
            f"the workflow must declare exactly the top-level keys {sorted(ROOT_KEYS)}, "
            f"got {sorted(keys)}"
        ]
    return []


def check_triggers(workflow):
    """A dispatch and nothing else.

    `workflow_run` and `workflow_call` are named because they are the two that
    would look reasonable: both make this file a downstream of the release
    pipeline, and the run it exists to recover from is a run of that pipeline.
    """
    triggers = triggers_of(workflow)
    problems = []
    for forbidden in ("pull_request_target", "workflow_run", "workflow_call"):
        if forbidden in triggers:
            problems.append(
                f"{forbidden} is prohibited in the production rollback workflow: "
                "it must stay dispatchable when the release pipeline is broken"
            )
    if triggers != TRIGGERS:
        problems.append(
            f"the workflow must trigger on exactly {sorted(TRIGGERS)}, got {sorted(triggers)}"
        )
    return problems


def check_workflow(workflow):
    problems = []
    if set(workflow.get("jobs", {})) != {JOB}:
        problems.append(
            f"the workflow must define exactly the job ['{JOB}'], "
            f"got {sorted(workflow.get('jobs', {}))}"
        )
    if workflow.get("permissions") != WORKFLOW_PERMISSIONS:
        problems.append(f"the workflow must declare exactly {WORKFLOW_PERMISSIONS}")
    return problems


def check_timeout(job):
    """Exactly the minutes this job is allowed, and an integer.

    The type is checked before the value, and `bool` is excluded from it:
    Actions rejects the string "15" at parse time, and `True == 1` in Python
    would read `timeout-minutes: true` as a one-minute limit.
    """
    timeout = job.get("timeout-minutes")
    if isinstance(timeout, int) and not isinstance(timeout, bool):
        if timeout == TIMEOUT_MINUTES:
            return []
    return [f"{JOB} job must declare timeout-minutes: {TIMEOUT_MINUTES}, got {timeout!r}"]


def check_job_shape(job):
    problems = []
    unexpected = sorted(set(job) - JOB_KEYS)
    if unexpected:
        problems.append(f"{JOB} job must not declare {unexpected}")
    if job.get("permissions") != JOB_PERMISSIONS:
        problems.append(f"{JOB} job must run with exactly {JOB_PERMISSIONS}")
    if job.get("runs-on") != RUNNER:
        problems.append(f"{JOB} job must run on {RUNNER}")
    problems += check_timeout(job)
    return problems


def check_after_condition(index, condition):
    """The mutation-reached allowlist, compared as a policy rather than as text.

    The exact-contract match alone would report only "not the step expected" for
    a condition that is wrong in a specific and repeatable way, so every
    conclusion the step may run on has to appear as its own equality and the two
    spellings that quietly include `skipped` are named.
    """
    problems = []
    for conclusion in AFTER_CONCLUSIONS:
        if f"steps.rollback.conclusion == '{conclusion}'" not in condition:
            problems.append(
                f"step {index} must run on the rollback's {conclusion} conclusion explicitly"
            )
    for rejected in AFTER_CONDITION_REJECTED:
        if rejected in condition:
            problems.append(
                f"step {index} must not gate on {rejected}: a step skipped after an earlier "
                "failure satisfies it, so nothing was rolled back and production is read anyway"
            )
    return problems


def check_condition(index, step):
    """Where an `if:` may appear at all, and what it must say there."""
    if "if" not in step:
        return []
    condition = str(step["if"])
    if index not in CONDITIONAL_STEPS:
        return [f"step {index} must not be conditional"]
    problems = []
    if "!cancelled()" not in condition:
        problems.append(f"step {index} must not run on a cancelled job")
    if index == AFTER_RECORD_INDEX:
        problems += check_after_condition(index, condition)
    return problems


def check_step(index, step, expected):
    problems = []
    unexpected = sorted(set(step) - STEP_KEYS)
    if unexpected:
        problems.append(f"step {index} must not declare {unexpected}")
    problems += check_condition(index, step)
    actual = step_contract(step)
    if actual != expected:
        problems.append(f"step {index} is not the step the contract expects: {actual!r}")
    return problems


def check_steps(job):
    """Same steps, same order, nothing extra.

    A build, a migration, a candidate deploy, a hand-written selector patch, a
    Secret or volume mutation, a DNS call and a stray `echo` are all refused
    here without any of them having to be recognised: they are not the step the
    contract has at that position, and there is no position spare.
    """
    steps = job.get("steps") or []
    if len(steps) != len(EXPECTED_STEPS):
        return [f"{JOB} job must contain exactly {len(EXPECTED_STEPS)} steps, got {len(steps)}"]
    problems = []
    for index, (step, want) in enumerate(zip(steps, EXPECTED_STEPS)):
        problems += check_step(index, step, want)
    return problems


def run(path):
    try:
        workflow = load(path)
        jobs = workflow["jobs"]
    except (OSError, KeyError, TypeError, yaml.YAMLError) as error:
        print(
            f"the production rollback workflow cannot be read from {path}: {error}",
            file=sys.stderr,
        )
        return 1
    problems = (
        check_root_shape(workflow)
        + check_triggers(workflow)
        + check_dispatch_inputs(workflow)
        + check_concurrency(workflow)
        + check_workflow(workflow)
    )
    if JOB in jobs:
        problems += check_job_shape(jobs[JOB]) + check_steps(jobs[JOB])
    for problem in problems:
        print(problem, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1]))
