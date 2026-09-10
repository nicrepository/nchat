#!/usr/bin/env bash
# Behaviour tests for the production rollback workflow contract (CICD-08).
#
# The checker is driven with copies of the real workflow that each break one
# invariant -- a build added, a migration added, the schema gate removed, the
# smoke weakened to the candidate mode, the target derived from the cluster, a
# trigger that ties the file to the release pipeline -- and every one of them
# must be refused. A checker that only ever sees a correct workflow proves
# nothing about what it would catch, so the committed file is driven through it
# too, and so is a benign mutation.
#
# There is exactly one description of the structural contract, and it is
# check_rollback_prod_workflow.py. This file asserts the positive case by
# running that checker against the committed workflow -- not by restating what
# the workflow must contain. A second structural checker here would be a second
# definition of EXPECTED_STEPS, the step order, the root shape and the dispatch
# contract, and two definitions of one contract is one contract nobody
# maintains: the copy drifts, and the suite goes green on a workflow the real
# checker would refuse.
#
# What is deliberately NOT tested here is the cluster half: the scripts the
# workflow calls are covered by test_prod_blue_green_scripts.sh, and the schema
# gate by test_rollback_schema_gate.sh. What this file proves is that none of
# those gates can be removed, softened or reordered out of the way.
#
# Offline: it parses YAML and runs the checker. No cluster, no network.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
WORKFLOW="$ROOT_DIR/.github/workflows/rollback-nchat-prod.yml"
CHECKER="$ROOT_DIR/scripts/ci/check_rollback_prod_workflow.py"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-rollback-workflow-test.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT
MUTATOR="$WORK_DIR/mutator.py"
FAILURES=0

fail() { echo "  [FAIL] $*" >&2; FAILURES=$((FAILURES + 1)); }
pass() { echo "  [ok] $*"; }

# A copy of the real workflow with exactly one thing changed, applied to the
# parsed YAML rather than to its text: the contract is about structure, and a
# sed expression that silently matched nothing would leave the workflow intact
# and be reported as a passing refusal.
mutate() {
  local name="$1" operation="$2" copy
  copy="$WORK_DIR/workflow-$name.yml"
  python3 "$MUTATOR" "$WORKFLOW" "$copy" "$operation"
  printf '%s' "$copy"
}

expect_refused() {
  local name="$1" copy="$2"
  if python3 "$CHECKER" "$copy" >/dev/null 2>&1; then
    fail "$name: the workflow checker accepted it"
    return
  fi
  pass "$name"
}

expect_accepted() {
  local name="$1" copy="$2"
  if python3 "$CHECKER" "$copy" >/dev/null 2>&1; then
    pass "$name"
    return
  fi
  fail "$name: the workflow checker refused it"
}

write_mutator() {
  cat >"$MUTATOR" <<'PYTHON'
"""Break one property of the production rollback workflow, on a copy.

Each mutation is one independent edit, held in a table of small functions rather
than a chain of branches: a reviewer should be able to read the whole list of
things this suite claims to catch in one screen.
"""

import sys

import yaml

JOB = "rollback"
PINNED_ACTION = "some/action@1111111111111111111111111111111111111111"
ROLLBACK = "scripts/deploy/nchat-prod/rollback.sh"
CUTOVER = "scripts/deploy/nchat-prod/cutover.sh"
DEPLOY = "scripts/deploy/nchat-prod/deploy.sh"
DRAIN = "scripts/deploy/nchat-prod/drain-old.sh"
SMOKE = "scripts/deploy/nchat-prod/smoke.sh"
LEDGER = "scripts/deploy/nchat-prod/applied-migrations.sh"
CRITICAL_SECTION = "scripts/deploy/nchat-prod/rollback-critical-section.sh"
GATE = "scripts/deploy/nchat-prod/rollback-schema-gate.sh"
PATCH_SELECTOR = (
    "kubectl patch svc chat-service --type=json "
    '-p \'[{"op":"replace","path":"/spec/selector/nchat.io~1release-slot"}]\''
)

# Step positions, named rather than repeated so a step added to the contract
# moves them in one place instead of silently retargeting half the mutations at
# their neighbours.
VALIDATE = 0
CHECKOUT = 1
BEFORE = 2
TARGET = 3
# The schema proof and the switch are one step, under one lock.
CRITICAL = 4
MUTATION = CRITICAL
AFTER_RECORD = 5
CONVERGED = 6
IDENTITY = 7
MINIMUM_SMOKE = 8
EVIDENCE = 9

UNRESTRICTED_ALWAYS = "${{ always() }}"
CONCLUSION_NOT_EMPTY = "${{ !cancelled() && steps.rollback.conclusion != '' }}"
CONCLUSION_NOT_SKIPPED = "${{ !cancelled() && steps.rollback.conclusion != 'skipped' }}"
NOT_CANCELLED_ONLY = "${{ !cancelled() }}"
SUCCESS_ONLY = "${{ !cancelled() && steps.rollback.conclusion == 'success' }}"
NO_CANCEL_GUARD = (
    "${{ steps.rollback.conclusion == 'success'"
    " || steps.rollback.conclusion == 'failure' }}"
)


def steps(workflow):
    return workflow["jobs"][JOB]["steps"]


def job(workflow):
    return workflow["jobs"][JOB]


def add_run(workflow, command):
    steps(workflow).append({"name": "Added", "run": command})


def add_action(workflow):
    steps(workflow).append({"name": "Added", "uses": PINNED_ACTION})


def move(workflow, source, destination):
    steps(workflow).insert(destination, steps(workflow).pop(source))


def add_job(workflow, name, command):
    workflow["jobs"][name] = {
        "runs-on": job(workflow)["runs-on"],
        "permissions": {"contents": "read"},
        "steps": [{"name": "Added", "run": command}],
    }


def edit_run(step, old, new=""):
    """Replace one line of a step's `run:`, and prove the line was there.

    A mutation that matched nothing would leave the fixture identical to the
    real workflow and the suite would report the checker's acceptance as a
    passing refusal -- a negative test proving nothing.
    """
    before = step["run"]
    if before.count(old) != 1:
        raise SystemExit(f"mutation target appears {before.count(old)} times: {old!r}")
    after = before.replace(old, new) if new else drop_line(before, old)
    if after == before:
        raise SystemExit(f"mutation changed nothing: {old!r}")
    step["run"] = after


def drop_line(run, target):
    return "".join(
        line for line in run.splitlines(keepends=True) if target not in line
    )


def dispatch_siblings(workflow):
    return workflow["on" if "on" in workflow else True]


def dispatch_declaration(workflow):
    return dispatch_siblings(workflow)["workflow_dispatch"]


def dispatch_inputs(workflow):
    return dispatch_declaration(workflow)["inputs"]


def rename_and_recomment(workflow):
    validate = steps(workflow)[VALIDATE]
    validate["run"] = validate["run"].replace("# A dispatch", "# EDITED dispatch")
    steps(workflow)[BEFORE]["name"] = "Renamed step"


# Everything this workflow must never do. None of it has to be recognised as
# dangerous: there is no step in the contract that runs it and no spare
# position for one.
FORBIDDEN_SURFACE = {
    "docker-build": lambda w: add_run(w, "docker build -t nchat/chat-service ."),
    "docker-push": lambda w: add_run(w, "docker push ghcr.io/nicrepository/nchat/chat-service"),
    "buildx": lambda w: add_run(w, "docker buildx build --push ."),
    "migrate-up": lambda w: add_run(w, "scripts/db/migrate.sh up"),
    "migrate-down": lambda w: add_run(w, "scripts/db/migrate.sh down"),
    "migration-job": lambda w: add_run(
        w, "kubectl create job --from=cronjob/nchat-migrations migrate -n nchat-prod"),
    "candidate-deploy": lambda w: add_run(w, DEPLOY),
    "cutover": lambda w: add_run(w, f'{CUTOVER} --target "$TARGET_SLOT"'),
    "drain-old": lambda w: add_run(w, f'{DRAIN} --target "$TARGET_SLOT"'),
    "delete-pvc": lambda w: add_run(w, "kubectl delete pvc postgres-data-0 -n nchat-prod"),
    "delete-pv": lambda w: add_run(w, "kubectl delete pv nchat-postgres -n nchat-prod"),
    "secret-mutation": lambda w: add_run(
        w, "kubectl create secret generic nchat-secrets -n nchat-prod --from-literal=a=b"),
    "dns-mutation": lambda w: add_run(
        w, "curl -X PATCH https://api.cloudflare.com/client/v4/zones/z/dns_records/r"),
    "hand-written-patch": lambda w: add_run(w, PATCH_SELECTOR),
    "switch-helper": lambda w: add_run(
        w,
        "source scripts/deploy/nchat-prod/lib.sh; "
        'switch_services_to_slot "$TARGET_SLOT"'),
    "set-x": lambda w: edit_run(
        steps(w)[BEFORE], "set -euo pipefail", "set -euxo pipefail"),
    "harmless-echo": lambda w: add_run(w, "echo hello"),
    "extra-action": lambda w: add_action(w),
    "second-job": lambda w: add_job(w, "notify", "echo done"),
    "second-rollback-job": lambda w: add_job(w, "converge", f"{ROLLBACK} --target green 'auto'"),
}

# The job's own wiring. `needs` and `environment` are absent from the contract
# and both absences are rules: a dependency would tie the recovery path to the
# pipeline that may be what is broken, and an approval gate would queue the one
# procedure that restores service behind the one that promotes.
JOB_WIRING = {
    "job-renamed": lambda w: w["jobs"].__setitem__(
        "recover", w["jobs"].pop(JOB)),
    "job-needs": lambda w: job(w).__setitem__("needs", "candidate"),
    "job-environment": lambda w: job(w).__setitem__("environment", "production"),
    "job-conditional": lambda w: job(w).__setitem__("if", "always()"),
    "job-off-runner": lambda w: job(w).__setitem__("runs-on", "ubuntu-latest"),
    "job-generic-runner": lambda w: job(w).__setitem__(
        "runs-on", ["self-hosted", "linux", "x64"]),
    "job-outputs": lambda w: job(w).__setitem__("outputs", {"slot": "${{ inputs.target_slot }}"}),
    "job-write-permission": lambda w: job(w)["permissions"].__setitem__("contents", "write"),
    "job-packages-write": lambda w: job(w)["permissions"].__setitem__("packages", "write"),
    "job-id-token": lambda w: job(w)["permissions"].__setitem__("id-token", "write"),
    "job-actions-write": lambda w: job(w)["permissions"].__setitem__("actions", "write"),
    "job-issues-write": lambda w: job(w)["permissions"].__setitem__("issues", "write"),
    "workflow-write-permission": lambda w: w.__setitem__(
        "permissions", {"contents": "write"}),
    "no-timeout": lambda w: job(w).pop("timeout-minutes"),
    "timeout-quoted": lambda w: job(w).__setitem__("timeout-minutes", "15"),
    "timeout-huge": lambda w: job(w).__setitem__("timeout-minutes", 1440),
}

# How the workflow can be started. Every trigger but the dispatch either exposes
# production to an event nobody authorised or makes this file a downstream of
# the release pipeline it exists to recover from.
TRIGGERS = {
    "on-push": lambda w: dispatch_siblings(w).__setitem__("push", {"branches": ["main"]}),
    "on-pull-request": lambda w: dispatch_siblings(w).__setitem__("pull_request", None),
    "on-pull-request-target": lambda w: dispatch_siblings(w).__setitem__(
        "pull_request_target", None),
    "on-schedule": lambda w: dispatch_siblings(w).__setitem__(
        "schedule", [{"cron": "*/5 * * * *"}]),
    "on-workflow-run": lambda w: dispatch_siblings(w).__setitem__(
        "workflow_run", {"workflows": ["Deploy nchat-prod"], "types": ["completed"]}),
    "on-workflow-call": lambda w: dispatch_siblings(w).__setitem__("workflow_call", None),
    "dispatch-removed": lambda w: dispatch_siblings(w).pop("workflow_dispatch"),
    "mutable-action": lambda w: steps(w)[CHECKOUT].__setitem__("uses", "actions/checkout@v4"),
    "action-by-tag": lambda w: steps(w)[CHECKOUT].__setitem__("uses", "actions/checkout@main"),
}

# The dispatch schema. The slot is a closed choice and the reason is mandatory;
# every weakening of either is a production traffic switch nobody has to name.
INPUT_SCHEMA = {
    "target-removed": lambda w: dispatch_inputs(w).pop("target_slot"),
    "reason-removed": lambda w: dispatch_inputs(w).pop("reason"),
    "target-optional": lambda w: dispatch_inputs(w)["target_slot"].__setitem__(
        "required", False),
    "reason-optional": lambda w: dispatch_inputs(w)["reason"].__setitem__("required", False),
    "target-free-string": lambda w: dispatch_inputs(w)["target_slot"].__setitem__(
        "type", "string"),
    "target-boolean": lambda w: dispatch_inputs(w)["target_slot"].__setitem__(
        "type", "boolean"),
    "target-options-widened": lambda w: dispatch_inputs(w)["target_slot"].__setitem__(
        "options", ["blue", "green", "auto"]),
    "target-options-narrowed": lambda w: dispatch_inputs(w)["target_slot"].__setitem__(
        "options", ["blue"]),
    "target-options-removed": lambda w: dispatch_inputs(w)["target_slot"].pop("options"),
    "target-default": lambda w: dispatch_inputs(w)["target_slot"].__setitem__(
        "default", "blue"),
    "reason-default": lambda w: dispatch_inputs(w)["reason"].__setitem__("default", "n/a"),
    "reason-choice": lambda w: dispatch_inputs(w)["reason"].__setitem__("type", "choice"),
    "extra-input": lambda w: dispatch_inputs(w).__setitem__(
        "force", {"description": "skip gates", "required": False, "type": "boolean"}),
    "inputs-removed": lambda w: dispatch_declaration(w).pop("inputs"),
    "target-null": lambda w: dispatch_inputs(w).__setitem__("target_slot", None),
    "reason-null": lambda w: dispatch_inputs(w).__setitem__("reason", None),
    "target-scalar": lambda w: dispatch_inputs(w).__setitem__("target_slot", "blue"),
    "reason-sequence": lambda w: dispatch_inputs(w).__setitem__("reason", []),
}

# Validating the inputs before either reaches a command, and carrying them as
# environment rather than interpolating them into a script.
INPUT_HANDLING = {
    "validation-removed": lambda w: steps(w).pop(VALIDATE),
    "slot-check-removed": lambda w: edit_run(
        steps(w)[VALIDATE], '[[ "$TARGET_SLOT" == "blue" || "$TARGET_SLOT" == "green" ]]'),
    "reason-blank-check-removed": lambda w: edit_run(
        steps(w)[VALIDATE], '[[ "$ROLLBACK_REASON" =~ [^[:space:]] ]]'),
    "reason-charset-check-removed": lambda w: edit_run(
        steps(w)[VALIDATE],
        '[[ "$ROLLBACK_REASON" =~ ^[A-Za-z0-9[:space:]._,:;/()#@+-]{3,200}$ ]]'),
    "main-check-removed": lambda w: edit_run(
        steps(w)[VALIDATE], '[[ "$GITHUB_REF" == "refs/heads/main" ]]'),
    "validation-tolerated": lambda w: edit_run(
        steps(w)[VALIDATE],
        '[[ "$TARGET_SLOT" == "blue" || "$TARGET_SLOT" == "green" ]]',
        '[[ "$TARGET_SLOT" == "blue" || "$TARGET_SLOT" == "green" ]] || true'),
    "validation-soft": lambda w: steps(w)[VALIDATE].__setitem__("continue-on-error", True),
    "validation-after-mutation": lambda w: move(w, VALIDATE, MUTATION),
    # The interpolation this design exists to keep out: the reason as text in a
    # script the shell parses, instead of a value in the environment.
    "reason-interpolated": lambda w: steps(w)[MUTATION].__setitem__(
        "run",
        f'{ROLLBACK} --target "$TARGET_SLOT" "${{{{ inputs.reason }}}}"'),
    "target-interpolated": lambda w: steps(w)[MUTATION].__setitem__(
        "run", f'{ROLLBACK} --target ${{{{ inputs.target_slot }}}} "$ROLLBACK_REASON"'),
}

# The preflight, and the fact that none of it can be read after the traffic has
# already moved.
PREFLIGHT = {
    "before-removed": lambda w: steps(w).pop(BEFORE),
    "before-not-written": lambda w: edit_run(
        steps(w)[BEFORE], 'printf \'%s\\n\' "$mapping" >"$SELECTORS_BEFORE"'),
    "classification-removed": lambda w: edit_run(
        steps(w)[BEFORE], 'require_promotable_selectors "$mapping" "$TARGET_SLOT"'),
    "classification-tolerated": lambda w: edit_run(
        steps(w)[BEFORE],
        'require_promotable_selectors "$mapping" "$TARGET_SLOT"',
        'require_promotable_selectors "$mapping" "$TARGET_SLOT" || true'),
    # The defect the deploy contract already refuses once: a falsifiable check
    # whose exit status is swallowed by the `echo` it sits inside.
    "classification-inside-echo": lambda w: edit_run(
        steps(w)[BEFORE],
        'require_promotable_selectors "$mapping" "$TARGET_SLOT"',
        'echo "checked=$(resolve_active_slot "$mapping")"'),
    "before-after-mutation": lambda w: move(w, BEFORE, MUTATION),
    "target-gate-removed": lambda w: steps(w).pop(TARGET),
    "target-readiness-removed": lambda w: edit_run(
        steps(w)[TARGET], 'slot_ready "$TARGET_SLOT"'),
    "target-release-gate-removed": lambda w: edit_run(
        steps(w)[TARGET],
        'release="$(require_consistent_release "$TARGET_SLOT")"',
        'release="$(slot_release "$TARGET_SLOT" || echo unknown)"'),
    "target-gate-tolerated": lambda w: edit_run(
        steps(w)[TARGET], 'slot_ready "$TARGET_SLOT"', 'slot_ready "$TARGET_SLOT" || true'),
    "target-gate-soft": lambda w: steps(w)[TARGET].__setitem__("continue-on-error", True),
    "target-gate-after-mutation": lambda w: move(w, TARGET, MUTATION),
    # A target read from the cluster is the retry-becomes-reversal bug: a second
    # run would send production back to the release it was rescued from.
    "target-derived": lambda w: steps(w)[TARGET]["env"].__setitem__(
        "TARGET_SLOT",
        "${{ steps.before.outputs.active }}"),
    "target-hardcoded": lambda w: steps(w)[TARGET]["env"].__setitem__("TARGET_SLOT", "blue"),
}

# The lock that makes the schema proof still true when the traffic moves.
#
# This is the finding of the third review: the gate answered a question about a
# schema that could change before the switch, and neither a migration Job nor an
# operator's `make migrations-up` is serialised by GitHub's `concurrency:`. The
# proof and the switch are therefore one step holding one lock, and every way of
# pulling them back apart is refused here.
CRITICAL_SECTION_STEP = {
    "critical-section-removed": lambda w: steps(w).pop(CRITICAL),
    "critical-section-replaced": lambda w: steps(w)[CRITICAL].__setitem__("run", "true"),
    "critical-section-soft": lambda w: steps(w)[CRITICAL].__setitem__(
        "continue-on-error", True),
    "critical-section-conditional": lambda w: steps(w)[CRITICAL].__setitem__(
        "if", "always()"),
    "critical-section-tolerated": lambda w: steps(w)[CRITICAL].__setitem__(
        "run",
        f'{CRITICAL_SECTION} --target "$TARGET_SLOT" "$ROLLBACK_REASON" || true'),
    # The regression the lock exists to prevent: the gate and the switch split
    # back into separate steps, with a window between them.
    "gate-and-switch-split": lambda w: steps(w).__setitem__(
        CRITICAL, {"name": "Split", "id": "rollback",
                   "run": f'{ROLLBACK} --target "$TARGET_SLOT" "$ROLLBACK_REASON"'}),
    "unlocked-switch-added": lambda w: add_run(
        w, f'{ROLLBACK} --target "$TARGET_SLOT" "$ROLLBACK_REASON"'),
    "unlocked-ledger-read-added": lambda w: add_run(w, f'{LEDGER} >"$APPLIED_LEDGER"'),
    "unlocked-gate-added": lambda w: add_run(
        w, f'{GATE} migrations "$SHA" "$APPLIED_LEDGER"'),
    "critical-section-slot-hardcoded": lambda w: steps(w)[CRITICAL]["env"].__setitem__(
        "TARGET_SLOT", "blue"),
    "critical-section-reason-dropped": lambda w: steps(w)[CRITICAL]["env"].pop(
        "ROLLBACK_REASON"),
    "critical-section-ledger-unbound": lambda w: steps(w)[CRITICAL]["env"].pop(
        "NCHAT_PROD_APPLIED_LEDGER"),
    "critical-section-assume-yes-removed": lambda w: steps(w)[CRITICAL]["env"].pop(
        "NCHAT_PROD_ASSUME_YES"),
    "critical-section-id-removed": lambda w: steps(w)[CRITICAL].pop("id"),
    "critical-section-after-record": lambda w: move(w, CRITICAL, AFTER_RECORD),
}

# The switch itself, now reached only through the locked critical section.
# Everything that used to be spelled against `rollback.sh --target ...` in this
# step is spelled against the script that calls it, because that is what the
# workflow runs; the rules are the same rules.
MUTATION_STEP = {
    "mutation-tolerated": lambda w: steps(w)[MUTATION].__setitem__(
        "run",
        f'{CRITICAL_SECTION} --target "$TARGET_SLOT" "$ROLLBACK_REASON" || true'),
    # No reason means nothing is recorded, and rollback.sh refuses it anyway --
    # a workflow that spells it this way is broken as well as unaccountable.
    "reason-dropped": lambda w: steps(w)[MUTATION].__setitem__(
        "run", f'{CRITICAL_SECTION} --target "$TARGET_SLOT" "automated"'),
    "reason-env-dropped": lambda w: steps(w)[MUTATION]["env"].pop("ROLLBACK_REASON"),
    # The fallback that must never exist: a failure here choosing the other slot
    # turns one incident into two.
    "automatic-fallback": lambda w: steps(w)[MUTATION].__setitem__(
        "run",
        f'{CRITICAL_SECTION} --target "$TARGET_SLOT" "$ROLLBACK_REASON" || '
        f'{CRITICAL_SECTION} --target "$(opposite_slot "$TARGET_SLOT")" "$ROLLBACK_REASON"'),
    "recursive-retry": lambda w: steps(w)[MUTATION].__setitem__(
        "run",
        f'for _ in 1 2 3; do {CRITICAL_SECTION} --target "$TARGET_SLOT" '
        f'"$ROLLBACK_REASON" && break; done'),
    # A target derived from the cluster is the retry-becomes-reversal bug.
    "target-derived-at-switch": lambda w: steps(w)[MUTATION]["env"].__setitem__(
        "TARGET_SLOT", "${{ steps.before.outputs.active }}"),
}

# What must be proved after the traffic moved, and the record that survives a
# rollback that stopped part-way.
POST_SWITCH = {
    "after-record-removed": lambda w: steps(w).pop(AFTER_RECORD),
    "after-record-only-on-success": lambda w: steps(w)[AFTER_RECORD].pop("if"),
    "after-record-always": lambda w: steps(w)[AFTER_RECORD].__setitem__(
        "if", UNRESTRICTED_ALWAYS),
    "after-record-conclusion-nonempty": lambda w: steps(w)[AFTER_RECORD].__setitem__(
        "if", CONCLUSION_NOT_EMPTY),
    "after-record-not-skipped": lambda w: steps(w)[AFTER_RECORD].__setitem__(
        "if", CONCLUSION_NOT_SKIPPED),
    "after-record-not-cancelled-only": lambda w: steps(w)[AFTER_RECORD].__setitem__(
        "if", NOT_CANCELLED_ONLY),
    "after-record-success-only": lambda w: steps(w)[AFTER_RECORD].__setitem__(
        "if", SUCCESS_ONLY),
    "after-record-no-cancel-guard": lambda w: steps(w)[AFTER_RECORD].__setitem__(
        "if", NO_CANCEL_GUARD),
    "after-record-soft": lambda w: steps(w)[AFTER_RECORD].__setitem__(
        "continue-on-error", True),
    # A recording step that also judges would be skipped as a whole if it were
    # ever softened, losing the after-state of the run that most needs it.
    "after-record-asserts": lambda w: edit_run(
        steps(w)[AFTER_RECORD],
        'cat "$SELECTORS_AFTER"',
        'cat "$SELECTORS_AFTER"\nall_services_on_slot "$(cat "$SELECTORS_AFTER")" blue'),
    "converged-removed": lambda w: steps(w).pop(CONVERGED),
    "converged-soft": lambda w: steps(w)[CONVERGED].__setitem__("continue-on-error", True),
    "converged-unconditional": lambda w: steps(w)[CONVERGED].__setitem__(
        "if", UNRESTRICTED_ALWAYS),
    "converged-tolerated": lambda w: edit_run(
        steps(w)[CONVERGED],
        'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$TARGET_SLOT"',
        'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$TARGET_SLOT" || true'),
    "converged-not-compared": lambda w: edit_run(
        steps(w)[CONVERGED],
        'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$TARGET_SLOT"'),
    # Comparing against whatever the selectors ended up on passes on a rollback
    # that went nowhere.
    "converged-against-active": lambda w: edit_run(
        steps(w)[CONVERGED],
        'all_services_on_slot "$(cat "$SELECTORS_AFTER")" "$TARGET_SLOT"',
        'all_services_on_slot "$(cat "$SELECTORS_AFTER")" '
        '"$(resolve_active_slot "$(cat "$SELECTORS_AFTER")")"'),
    "converged-before-mutation": lambda w: move(w, CONVERGED, MUTATION),
    "converged-reads-elsewhere": lambda w: steps(w)[CONVERGED]["env"].__setitem__(
        "SELECTORS_AFTER", "${{ runner.temp }}/other.txt"),
    "identity-removed": lambda w: steps(w).pop(IDENTITY),
    "identity-tolerated": lambda w: edit_run(
        steps(w)[IDENTITY],
        'require_slot_release_identity "$TARGET_SLOT" "$TARGET_RELEASE"',
        'require_slot_release_identity "$TARGET_SLOT" "$TARGET_RELEASE" || true'),
    "identity-not-compared": lambda w: edit_run(
        steps(w)[IDENTITY],
        'require_slot_release_identity "$TARGET_SLOT" "$TARGET_RELEASE"',
        'slot_release_state "$TARGET_SLOT"'),
    "identity-before-mutation": lambda w: move(w, IDENTITY, MUTATION),
    "identity-release-unbound": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "TARGET_RELEASE", "${{ inputs.target_slot }}"),
}

# The minimum smoke, and the one mode that is semantically valid after a switch.
MINIMUM_SMOKE_STEP = {
    "smoke-removed": lambda w: steps(w).pop(MINIMUM_SMOKE),
    "smoke-soft": lambda w: steps(w)[MINIMUM_SMOKE].__setitem__("continue-on-error", True),
    "smoke-conditional": lambda w: steps(w)[MINIMUM_SMOKE].__setitem__("if", "always()"),
    "smoke-tolerated": lambda w: edit_run(
        steps(w)[MINIMUM_SMOKE],
        f'{SMOKE} --target "$TARGET_SLOT" --active',
        f'{SMOKE} --target "$TARGET_SLOT" --active || true'),
    # The candidate mode refuses a slot that holds traffic, which after a
    # rollback is every valid target: this spelling is a smoke that can only
    # fail, and a run reporting it as a gate would be reporting nonsense.
    "smoke-candidate-mode": lambda w: edit_run(
        steps(w)[MINIMUM_SMOKE],
        f'{SMOKE} --target "$TARGET_SLOT" --active',
        f'{SMOKE} --target "$TARGET_SLOT"'),
    "smoke-baseline-mode": lambda w: edit_run(
        steps(w)[MINIMUM_SMOKE],
        f'{SMOKE} --target "$TARGET_SLOT" --active',
        f'{SMOKE} --target "$TARGET_SLOT" --baseline'),
    "smoke-invented-bypass": lambda w: edit_run(
        steps(w)[MINIMUM_SMOKE],
        f'{SMOKE} --target "$TARGET_SLOT" --active',
        f'{SMOKE} --target "$TARGET_SLOT" --skip-isolation'),
    "smoke-before-mutation": lambda w: move(w, MINIMUM_SMOKE, MUTATION),
    "smoke-slot-hardcoded": lambda w: steps(w)[MINIMUM_SMOKE]["env"].__setitem__(
        "TARGET_SLOT", "green"),
}

# The evidence. It must record the reason, the target, the release and both
# selector readings, and it must survive a failed run.
EVIDENCE_STEP = {
    "evidence-removed": lambda w: steps(w).pop(EVIDENCE),
    "evidence-only-on-success": lambda w: steps(w)[EVIDENCE].pop("if"),
    "evidence-on-cancel": lambda w: steps(w)[EVIDENCE].__setitem__(
        "if", UNRESTRICTED_ALWAYS),
    "evidence-reason-dropped": lambda w: steps(w)[EVIDENCE]["env"].pop("ROLLBACK_REASON"),
    "evidence-target-dropped": lambda w: steps(w)[EVIDENCE]["env"].pop("TARGET_SLOT"),
    "evidence-release-dropped": lambda w: steps(w)[EVIDENCE]["env"].pop("TARGET_RELEASE"),
    "evidence-selectors-dropped": lambda w: steps(w)[EVIDENCE]["env"].pop("SELECTORS_AFTER"),
    "evidence-reason-not-printed": lambda w: edit_run(
        steps(w)[EVIDENCE], 'printf \'%s\\n\' "$ROLLBACK_REASON"'),
    "evidence-selectors-not-printed": lambda w: edit_run(
        steps(w)[EVIDENCE], 'recorded "$SELECTORS_AFTER"'),
    # The line that would put credentials into a public run summary.
    "evidence-env-dump": lambda w: edit_run(
        steps(w)[EVIDENCE], "echo \"### Reason\"", "env"),
    "evidence-kubeconfig": lambda w: edit_run(
        steps(w)[EVIDENCE], "echo \"### Reason\"", 'cat "$KUBECONFIG"'),
}

# The top level. `defaults.run.shell` is the one that matters: it is documented,
# actionlint accepts it, every listed step stays byte-for-byte what the contract
# expects, and every one of them is then run through a shell nobody reviewed.
ROOT_SURFACE = {
    "defaults-shell-injection": lambda w: w.__setitem__(
        "defaults", {"run": {"shell": "echo unexpected >&2; bash {0}"}}),
    "defaults-shell-benign": lambda w: w.__setitem__(
        "defaults", {"run": {"shell": "bash {0}"}}),
    "root-env": lambda w: w.__setitem__("env", {"NCHAT_PROD_ASSUME_YES": "1"}),
    "root-run-name": lambda w: w.__setitem__("run-name", "rollback ${{ inputs.reason }}"),
    "name-removed": lambda w: w.pop("name"),
}

# Serialisation against the cutover. A group of its own would let a rollback and
# a promotion patch the same ten selectors at once.
CONCURRENCY = {
    "concurrency-removed": lambda w: w.pop("concurrency"),
    "concurrency-null": lambda w: w.__setitem__("concurrency", None),
    "group-removed": lambda w: w["concurrency"].pop("group"),
    "group-of-its-own": lambda w: w["concurrency"].__setitem__("group", "nchat-prod-rollback"),
    "group-per-slot": lambda w: w["concurrency"].__setitem__(
        "group", "nchat-prod-rollback-${{ inputs.target_slot }}"),
    "cancel-removed": lambda w: w["concurrency"].pop("cancel-in-progress"),
    "cancel-true": lambda w: w["concurrency"].__setitem__("cancel-in-progress", True),
    "cancel-quoted-false": lambda w: w["concurrency"].__setitem__(
        "cancel-in-progress", "false"),
}

# Not a violation: a comment is documentation and a step name is a label.
BENIGN = {
    "edited-comment": rename_and_recomment,
}

MUTATIONS = {
    **FORBIDDEN_SURFACE,
    **JOB_WIRING,
    **TRIGGERS,
    **INPUT_SCHEMA,
    **INPUT_HANDLING,
    **PREFLIGHT,
    **CRITICAL_SECTION_STEP,
    **MUTATION_STEP,
    **POST_SWITCH,
    **MINIMUM_SMOKE_STEP,
    **EVIDENCE_STEP,
    **ROOT_SURFACE,
    **CONCURRENCY,
    **BENIGN,
}


def main(source, destination, operation):
    if operation not in MUTATIONS:
        raise SystemExit(f"unknown mutation: {operation}")
    with open(source, encoding="utf-8") as handle:
        workflow = yaml.safe_load(handle)
    MUTATIONS[operation](workflow)
    with open(destination, "w", encoding="utf-8") as handle:
        yaml.safe_dump(workflow, handle, sort_keys=False)
    return 0


sys.exit(main(sys.argv[1], sys.argv[2], sys.argv[3]))
PYTHON
}

# Every mutation in one named group must be refused. The group name is what a
# reader of the output sees, so it says what property is being held.
expect_group_refused() {
  local heading="$1"
  shift
  echo "$heading"
  local operation
  for operation in "$@"; do
    expect_refused "$operation" "$(mutate "$operation" "$operation")"
  done
}

test_the_committed_workflow_satisfies_its_contract() {
  echo "the committed workflow"
  if python3 "$CHECKER" "$WORKFLOW"; then
    pass "the production rollback workflow satisfies its contract"
    return
  fi
  fail "the committed production rollback workflow does not satisfy its own contract"
}

# Every `run:` block, parsed by the shell that will run it.
#
# The contract checker compares step text exactly, which proves the workflow is
# the one that was reviewed and proves nothing about whether bash can execute
# it. That gap shipped a workflow whose first step died on a syntax error --
# `;` inside a bracket expression ends the `[[` command -- while this suite and
# actionlint both reported PASS, because neither had ever asked bash.
#
# `bash -n` is the whole fix: no new framework, and it does not depend on
# ShellCheck being installed.
test_every_run_block_parses() {
  echo "every run: block is valid bash"
  local failures
  failures="$(python3 - "$WORKFLOW" <<'PYTHON'
import subprocess
import sys

import yaml

with open(sys.argv[1], encoding="utf-8") as handle:
    workflow = yaml.safe_load(handle)

problems = []
for job_name, job in workflow["jobs"].items():
    for index, step in enumerate(job.get("steps") or []):
        script = step.get("run")
        if script is None:
            continue
        done = subprocess.run(
            ["bash", "-n", "-c", script], capture_output=True, text=True
        )
        if done.returncode != 0:
            first = done.stderr.strip().splitlines()[:2]
            problems.append(f"{job_name} step {index}: " + " | ".join(first))

for problem in problems:
    print(problem)
PYTHON
  )"
  if [[ -z "$failures" ]]; then
    pass "every run: block parses under bash -n"
    return
  fi
  while IFS= read -r problem; do
    fail "$problem"
  done <<<"$failures"
}

test_documentation_is_not_execution() {
  echo "comments and step names stay free to edit"
  expect_accepted "an edited comment and a renamed step" \
    "$(mutate benign edited-comment)"
}

main() {
  write_mutator
  test_the_committed_workflow_satisfies_its_contract
  test_every_run_block_parses
  expect_group_refused "the executable surface is closed" \
    docker-build docker-push buildx migrate-up migrate-down migration-job \
    candidate-deploy cutover drain-old delete-pvc delete-pv secret-mutation \
    dns-mutation hand-written-patch switch-helper set-x harmless-echo \
    extra-action second-job second-rollback-job
  expect_group_refused "the job stays one independent, least-privileged job" \
    job-renamed job-needs job-environment job-conditional job-off-runner \
    job-generic-runner job-outputs job-write-permission job-packages-write \
    job-id-token job-actions-write job-issues-write workflow-write-permission \
    no-timeout timeout-quoted timeout-huge
  expect_group_refused "the workflow stays a manual dispatch, independent of the pipeline" \
    on-push on-pull-request on-pull-request-target on-schedule on-workflow-run \
    on-workflow-call dispatch-removed mutable-action action-by-tag
  expect_group_refused "the dispatch names a slot and a reason, both mandatory" \
    target-removed reason-removed target-optional reason-optional \
    target-free-string target-boolean target-options-widened \
    target-options-narrowed target-options-removed target-default \
    reason-default reason-choice extra-input inputs-removed target-null \
    reason-null target-scalar reason-sequence
  expect_group_refused "the inputs are validated as data before they are used" \
    validation-removed slot-check-removed reason-blank-check-removed \
    reason-charset-check-removed main-check-removed validation-tolerated \
    validation-soft validation-after-mutation reason-interpolated \
    target-interpolated
  expect_group_refused "nothing moves before the preflight has read the cluster" \
    before-removed before-not-written classification-removed \
    classification-tolerated classification-inside-echo before-after-mutation \
    target-gate-removed target-readiness-removed target-release-gate-removed \
    target-gate-tolerated target-gate-soft target-gate-after-mutation \
    target-derived target-hardcoded
  expect_group_refused "the schema proof and the switch stay one locked step" \
    critical-section-removed critical-section-replaced critical-section-soft \
    critical-section-conditional critical-section-tolerated \
    gate-and-switch-split unlocked-switch-added unlocked-ledger-read-added \
    unlocked-gate-added critical-section-slot-hardcoded \
    critical-section-reason-dropped critical-section-ledger-unbound \
    critical-section-assume-yes-removed critical-section-id-removed \
    critical-section-after-record
  expect_group_refused "the switch names its target once, with no fallback" \
    mutation-tolerated reason-dropped reason-env-dropped automatic-fallback \
    recursive-retry target-derived-at-switch
  expect_group_refused "the after-state is recorded and then judged" \
    after-record-removed after-record-only-on-success after-record-always \
    after-record-conclusion-nonempty after-record-not-skipped \
    after-record-not-cancelled-only after-record-success-only \
    after-record-no-cancel-guard after-record-soft after-record-asserts \
    converged-removed converged-soft converged-unconditional \
    converged-tolerated converged-not-compared converged-against-active \
    converged-before-mutation converged-reads-elsewhere identity-removed \
    identity-tolerated identity-not-compared identity-before-mutation \
    identity-release-unbound
  expect_group_refused "the minimum smoke is the post-rollback one and cannot be weakened" \
    smoke-removed smoke-soft smoke-conditional smoke-tolerated \
    smoke-candidate-mode smoke-baseline-mode smoke-invented-bypass \
    smoke-before-mutation smoke-slot-hardcoded
  expect_group_refused "the evidence records the operation and never a secret" \
    evidence-removed evidence-only-on-success evidence-on-cancel \
    evidence-reason-dropped evidence-target-dropped evidence-release-dropped \
    evidence-selectors-dropped evidence-reason-not-printed \
    evidence-selectors-not-printed evidence-env-dump evidence-kubeconfig
  expect_group_refused "the root surface is closed" \
    defaults-shell-injection defaults-shell-benign root-env root-run-name \
    name-removed
  expect_group_refused "a rollback cannot race a cutover" \
    concurrency-removed concurrency-null group-removed group-of-its-own \
    group-per-slot cancel-removed cancel-true cancel-quoted-false
  test_documentation_is_not_execution
  if [[ "$FAILURES" -ne 0 ]]; then
    echo "Production rollback workflow tests failed: $FAILURES" >&2
    return 1
  fi
  echo "Production rollback workflow tests passed."
}

main "$@"
