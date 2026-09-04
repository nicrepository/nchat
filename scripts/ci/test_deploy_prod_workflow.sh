#!/usr/bin/env bash
# Behaviour tests for the candidate-only production deploy workflow (CICD-05).
#
# Two things are proved here, both offline and neither against a cluster.
#
# First, the workflow contract: the checker is driven with copies of the real
# workflow that each break one invariant -- a promotion added, a second job
# added, the stable-selector proof removed, a gate marked continue-on-error --
# and every one of them must be refused. A checker that only ever sees a correct
# workflow proves nothing about what it would catch, so a benign mutation is
# driven through it too.
#
# Second, the release binding: release-digests.sh is driven with a manifest
# whose seal is broken, one that seals a different commit, and one missing an
# image. Each must refuse and leave no artifacts directory, because a deploy
# that pins digests from an unproven manifest is exactly the failure the sealed
# manifest exists to prevent.
#
# What is deliberately NOT tested here is the cluster half: that the deploy
# leaves the stable Services alone is proved at run time by the workflow's own
# before/after comparison, and the scripts it calls are covered by
# test_prod_blue_green_scripts.sh. What this file proves is that the comparison
# cannot be removed, softened or reordered out of the way.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
WORKFLOW="$ROOT_DIR/.github/workflows/deploy-nchat-prod.yml"
CHECKER="$ROOT_DIR/scripts/ci/check_deploy_prod_workflow.py"
RELEASE_MANIFEST="$ROOT_DIR/scripts/deploy/nchat-prod/release-manifest.sh"
RELEASE_DIGESTS="$ROOT_DIR/scripts/deploy/nchat-prod/release-digests.sh"
JOB_MUTATOR=""

SOURCE_SHA=0123456789abcdef0123456789abcdef01234567
OTHER_SHA=89abcdef0123456789abcdef0123456789abcdef
RUN_ID=123456789
FAILURES=0

# Brings in the canonical image inventory the manifest is built from.
# shellcheck source=scripts/deploy/nchat-prod/release-manifest.sh
source "$RELEASE_MANIFEST"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-deploy-prod-test.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

fail() {
  echo "  [FAIL] $*" >&2
  FAILURES=$((FAILURES + 1))
}

pass() {
  echo "  [ok] $*"
}

assert_equals() {
  local name="$1" expected="$2" actual="$3"
  if [[ "$expected" != "$actual" ]]; then
    fail "$name: expected [$expected], got [$actual]"
    return
  fi
  pass "$name"
}

# --- workflow contract ------------------------------------------------------

expect_workflow_refused() {
  local name="$1" copy="$2"
  if python3 "$CHECKER" "$copy" >/dev/null 2>&1; then
    fail "$name: the workflow checker accepted it"
    return
  fi
  pass "$name"
}

# A step that is accepted proves as much as one that is refused: without this,
# a checker that simply refused every workflow would pass the whole suite.
expect_workflow_accepted() {
  local name="$1" copy="$2"
  if python3 "$CHECKER" "$copy" >/dev/null 2>&1; then
    pass "$name"
    return
  fi
  fail "$name: the workflow checker refused it"
}

test_real_workflow_satisfies_the_contract() {
  echo "the committed workflow"
  if python3 "$CHECKER" "$WORKFLOW"; then
    pass "the production deploy workflow satisfies the candidate-only contract"
    return
  fi
  fail "the committed production deploy workflow does not satisfy its own contract"
}

# A copy of the real workflow with exactly one thing changed, applied to the
# parsed YAML rather than to its text: the contract is about structure, and a
# sed expression that silently matched nothing would leave the workflow intact
# and be reported as a passing refusal.
mutate() {
  local name="$1" operation="$2" copy
  copy="$WORK_DIR/workflow-$name.yml"
  python3 "$JOB_MUTATOR" "$WORKFLOW" "$copy" "$operation"
  printf '%s' "$copy"
}

write_job_mutator() {
  cat >"$JOB_MUTATOR" <<'PYTHON'
"""Break one property of the production deploy workflow, on a copy.

Each mutation is one independent edit, so they are held in a table of small
functions rather than a chain of branches: adding a case must not make the
dispatcher harder to read, and a reviewer should be able to see the whole list
of things this suite claims to catch in one screen.
"""

import sys

import yaml

PINNED_ACTION = "some/action@1111111111111111111111111111111111111111"
CUTOVER = "scripts/deploy/nchat-prod/cutover.sh"
ROLLBACK = "scripts/deploy/nchat-prod/rollback.sh"
DRAIN = "scripts/deploy/nchat-prod/drain-old.sh"
PATCH_SELECTOR = (
    "kubectl patch svc chat-service --type=json "
    '-p \'[{"op":"replace","path":"/spec/selector/nchat.io~1release-slot"}]\''
)
# Step positions in the candidate job. Named rather than repeated, so a step
# added to the contract moves them in one place instead of silently retargeting
# half the mutations at their neighbours.
DEPLOY = 7
SMOKE = 8
VERIFY = 9
IDENTITY = 10


def steps(workflow, job="candidate"):
    return workflow["jobs"][job]["steps"]


def slot_step(workflow):
    """The step that snapshots the selectors and derives the candidate slot."""
    return steps(workflow)[6]


def dispatch_inputs(workflow):
    """`on:` is the boolean True once PyYAML has read it."""
    return workflow["on" if "on" in workflow else True]["workflow_dispatch"]["inputs"]


def add_run(workflow, command):
    steps(workflow).append({"name": "Added", "run": command})


def add_action(workflow):
    steps(workflow).append({"name": "Added", "uses": PINNED_ACTION})


def move(workflow, source, destination):
    steps(workflow).insert(destination, steps(workflow).pop(source))


def edit_run(step, old, new=""):
    """Replace one line of a step's `run:`, and prove the line was there.

    A mutation that matched nothing would leave the fixture identical to the
    real workflow, the checker would accept it, and the suite would report that
    acceptance as a passing refusal -- a negative test proving nothing. So the
    target must be present exactly once, and the text must actually change.
    """
    before = step["run"]
    if before.count(old) != 1:
        raise SystemExit(f"mutation target appears {before.count(old)} times: {old!r}")
    after = before.replace(old, new) if new else drop_line(before, old)
    if after == before:
        raise SystemExit(f"mutation changed nothing: {old!r}")
    step["run"] = after


def drop_line(run, target):
    """Remove the single line holding `target`, leaving every other line."""
    return "".join(
        line for line in run.splitlines(keepends=True) if target not in line
    )


def add_job(workflow, name, command):
    """A second operational job, however it is spelled."""
    workflow["jobs"][name] = {
        "needs": "candidate",
        "runs-on": workflow["jobs"]["candidate"]["runs-on"],
        "permissions": {"contents": "read"},
        "steps": [{"name": "Added", "run": command}],
    }


# Ways this workflow could reach a promotion, and ways its executable surface
# could grow. None of these needs to be recognised as dangerous by the checker;
# they are refused because the contract lists no such step and no such job.
PROMOTION_SURFACE = {
    "wrapped-cutover": lambda w: add_run(w, f'bash {CUTOVER} --target "$CANDIDATE_SLOT"'),
    "env-wrapped-cutover": lambda w: add_run(w, f'env bash {CUTOVER} --target "$CANDIDATE_SLOT"'),
    "rollback-script": lambda w: add_run(w, f'{ROLLBACK} --target "$CANDIDATE_SLOT"'),
    "drain-old-script": lambda w: add_run(w, f'{DRAIN} --target "$CANDIDATE_SLOT"'),
    "switch-helper": lambda w: add_run(
        w,
        "source scripts/deploy/nchat-prod/lib.sh; "
        'switch_services_to_slot "$CANDIDATE_SLOT"',
    ),
    "hand-written-patch": lambda w: add_run(w, PATCH_SELECTOR),
    "harmless-echo": lambda w: add_run(w, "echo hello"),
    "extra-action": lambda w: add_action(w),
}

# A second job is the shape this task exists to keep out: the cutover phase
# arriving early, under whatever name. The contract names one job, so none of
# these has to be recognised as a promotion to be refused.
SECOND_JOB = {
    "cutover-job": lambda w: add_job(w, "cutover", f'{CUTOVER} --target green'),
    "renamed-cutover-job": lambda w: add_job(w, "promote", f'{CUTOVER} --target green'),
    "harmless-second-job": lambda w: add_job(w, "notify", "echo done"),
    "candidate-environment": lambda w: w["jobs"]["candidate"].__setitem__(
        "environment", "production"),
    "candidate-environment-mapping": lambda w: w["jobs"]["candidate"].__setitem__(
        "environment", {"name": "production"}),
    "candidate-needs": lambda w: w["jobs"]["candidate"].__setitem__("needs", "build"),
    "conditional-job": lambda w: w["jobs"]["candidate"].__setitem__("if", "always()"),
    "job-write-permission": lambda w: w["jobs"]["candidate"]["permissions"].__setitem__(
        "packages", "write"),
    "outputs-restored": lambda w: w["jobs"]["candidate"].__setitem__(
        "outputs", {"slot": "${{ steps.slot.outputs.slot }}"}),
}

# The release this workflow is supposed to perform, and in what order.
CANDIDATE_RELEASE = {
    "deploy-replaced": lambda w: steps(w)[DEPLOY].__setitem__("run", "true"),
    "deploy-removed": lambda w: steps(w).pop(DEPLOY),
    "smoke-removed": lambda w: steps(w).pop(SMOKE),
    "smoke-before-deploy": lambda w: move(w, SMOKE, DEPLOY),
    "soft-smoke": lambda w: steps(w)[SMOKE].__setitem__("continue-on-error", True),
    "conditional-smoke": lambda w: steps(w)[SMOKE].__setitem__("if", "always()"),
    "soft-deploy": lambda w: steps(w)[DEPLOY].__setitem__("continue-on-error", True),
}

# The stable-selector invariant: the snapshot, the comparison, and the fact that
# neither can be softened into something that reports a difference as a pass.
SELECTOR_INVARIANT = {
    "verification-removed": lambda w: steps(w).pop(VERIFY),
    "verification-before-deploy": lambda w: move(w, VERIFY, DEPLOY),
    "soft-verification": lambda w: steps(w)[VERIFY].__setitem__("continue-on-error", True),
    "conditional-verification": lambda w: steps(w)[VERIFY].__setitem__("if", "always()"),
    "tolerated-difference": lambda w: edit_run(
        steps(w)[VERIFY],
        'diff -u "$SELECTORS_BEFORE" "$SELECTORS_AFTER"',
        'diff -u "$SELECTORS_BEFORE" "$SELECTORS_AFTER" || true'),
    # A comparison against a file nobody wrote is a comparison that always
    # passes -- and the two steps must name one file for it to mean anything.
    "verification-reads-elsewhere": lambda w: steps(w)[VERIFY]["env"].__setitem__(
        "SELECTORS_BEFORE", "${{ runner.temp }}/other.txt"),
    "snapshot-writes-elsewhere": lambda w: slot_step(w)["env"].__setitem__(
        "SELECTORS_BEFORE", "${{ runner.temp }}/other.txt"),
    # Exactly one line goes: the write that persists the snapshot. The source
    # of lib.sh, the collect_service_slots reading, the active slot, the
    # candidate slot and both outputs all stay, so what fails is the missing
    # snapshot and nothing else.
    "snapshot-removed": lambda w: edit_run(
        slot_step(w), 'printf \'%s\\n\' "$mapping" >"$SELECTORS_BEFORE"'),
    # Repairing a moved selector destroys the only record that it moved. The
    # step detects; it must never correct.
    "self-healing-verification": lambda w: edit_run(
        steps(w)[VERIFY],
        'diff -u "$SELECTORS_BEFORE" "$SELECTORS_AFTER"',
        'diff -u "$SELECTORS_BEFORE" "$SELECTORS_AFTER" ||'
        ' switch_services_to_slot "$ACTIVE_SLOT"'),
}

# The candidate must be running the release this run built, observed from the
# cluster. The smoke proves the slot agrees with itself, which a concurrent
# redeploy of that slot satisfies just as well -- only the identity separates
# the two releases.
RELEASE_IDENTITY = {
    "identity-removed": lambda w: steps(w).pop(IDENTITY),
    "identity-replaced": lambda w: steps(w)[IDENTITY].__setitem__("run", "true"),
    "identity-soft": lambda w: steps(w)[IDENTITY].__setitem__("continue-on-error", True),
    "identity-conditional": lambda w: steps(w)[IDENTITY].__setitem__("if", "always()"),
    "identity-tolerated": lambda w: edit_run(
        steps(w)[IDENTITY],
        'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE"',
        'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE" || true'),
    # The comparison itself, gone: the state is read and then not checked.
    "identity-not-compared": lambda w: edit_run(
        steps(w)[IDENTITY],
        'require_slot_release_identity "$CANDIDATE_SLOT" "$EXPECTED_RELEASE"',
        'slot_release_state "$CANDIDATE_SLOT"'),
    # Half an identity is not a weaker check, it is a different one: two builds
    # of one commit share the SHA, and a rebuild would pass as the release that
    # was deployed.
    "identity-sha-only": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "EXPECTED_RELEASE", "${{ inputs.sha }}"),
    "identity-id-only": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "EXPECTED_RELEASE", "${{ steps.release.outputs.release_id }}"),
    "identity-sha-rewired": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "EXPECTED_RELEASE",
        "${{ steps.slot.outputs.slot }}:${{ steps.release.outputs.release_id }}"),
    "identity-id-rewired": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "EXPECTED_RELEASE", "${{ inputs.sha }}:${{ steps.slot.outputs.slot }}"),
    "identity-expected-hardcoded": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "EXPECTED_RELEASE", "deadbeef:cafe"),
    "identity-slot-hardcoded": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "CANDIDATE_SLOT", "green"),
    "identity-slot-from-input": lambda w: steps(w)[IDENTITY]["env"].__setitem__(
        "CANDIDATE_SLOT", "${{ inputs.slot }}"),
    # Order is the contract: an identity read before the deploy describes the
    # slot's previous release, and would pass on a deploy that never happened.
    "identity-before-deploy": lambda w: move(w, IDENTITY, DEPLOY),
    # The report may not claim a release the gate has not proved.
    "report-before-identity": lambda w: move(w, IDENTITY + 1, IDENTITY),
}

# The candidate slot comes from the cluster, never from an input and never from
# a literal. A hardcoded slot deploys over whatever happens to be in it.
SLOT_DERIVATION = {
    "slot-hardcoded-green": lambda w: steps(w)[SMOKE]["env"].__setitem__(
        "CANDIDATE_SLOT", "green"),
    "slot-hardcoded-blue": lambda w: steps(w)[SMOKE]["env"].__setitem__(
        "CANDIDATE_SLOT", "blue"),
    "slot-from-input": lambda w: steps(w)[SMOKE]["env"].__setitem__(
        "CANDIDATE_SLOT", "${{ inputs.slot }}"),
    "slot-not-opposite": lambda w: edit_run(
        slot_step(w),
        'echo "slot=$(opposite_slot "$active")" >>"$GITHUB_OUTPUT"',
        'echo "slot=$active" >>"$GITHUB_OUTPUT"'),
}

# The deploy must build the slot the snapshot step resolved. Dropping the
# binding puts deploy.sh back to deriving its own candidate from a second
# reading of the cluster, which is the divergence the env exists to close.
CANDIDATE_BINDING = {
    "deploy-slot-unbound": lambda w: steps(w)[DEPLOY]["env"].pop("NCHAT_PROD_CANDIDATE_SLOT"),
    "deploy-slot-hardcoded": lambda w: steps(w)[DEPLOY]["env"].__setitem__(
        "NCHAT_PROD_CANDIDATE_SLOT", "green"),
    "deploy-slot-from-input": lambda w: steps(w)[DEPLOY]["env"].__setitem__(
        "NCHAT_PROD_CANDIDATE_SLOT", "${{ inputs.slot }}"),
    # The deploy and the smoke must name one slot. A binding wired to the
    # active slot instead would deploy over the slot serving production.
    "deploy-slot-is-active": lambda w: steps(w)[DEPLOY]["env"].__setitem__(
        "NCHAT_PROD_CANDIDATE_SLOT", "${{ steps.slot.outputs.active }}"),
}

# A job with no time limit holds the concurrency group for as long as it hangs;
# a job with a minute fails healthy releases. Both directions are the contract.
TIMEOUT = {
    "timeout-removed": lambda w: w["jobs"]["candidate"].pop("timeout-minutes"),
    "timeout-null": lambda w: w["jobs"]["candidate"].__setitem__("timeout-minutes", None),
    "timeout-one": lambda w: w["jobs"]["candidate"].__setitem__("timeout-minutes", 1),
    "timeout-44": lambda w: w["jobs"]["candidate"].__setitem__("timeout-minutes", 44),
    "timeout-46": lambda w: w["jobs"]["candidate"].__setitem__("timeout-minutes", 46),
    # Actions rejects a quoted timeout at parse time, so a contract that took
    # it would be green on a workflow nobody can run.
    "timeout-quoted": lambda w: w["jobs"]["candidate"].__setitem__("timeout-minutes", "45"),
}

# The input declarations, closed. `default` is the one that matters: it turns a
# required gate into a value the dispatcher can leave alone, so a release could
# start without anyone naming a commit or a build.
INPUT_KEYS = {
    "sha-default": lambda w: dispatch_inputs(w)["sha"].__setitem__("default", "deadbeef"),
    "run-id-default": lambda w: dispatch_inputs(w)["run_id"].__setitem__("default", "1"),
    "sha-deprecated": lambda w: dispatch_inputs(w)["sha"].__setitem__(
        "deprecationMessage", "use the other one"),
    "run-id-options": lambda w: dispatch_inputs(w)["run_id"].__setitem__("options", ["1"]),
    "sha-extra-key": lambda w: dispatch_inputs(w)["sha"].__setitem__("force", True),
    "run-id-extra-key": lambda w: dispatch_inputs(w)["run_id"].__setitem__("force", True),
}

# How the workflow can be started, and by what.
TRIGGERS = {
    "mutable-action": lambda w: steps(w)[1].__setitem__("uses", "actions/checkout@v4"),
    "pull-request-target": lambda w: dispatch_siblings(w).__setitem__("pull_request_target", None),
    "on-push": lambda w: dispatch_siblings(w).__setitem__("push", {"branches": ["main"]}),
}

# The dispatch schema. A workflow whose inputs are missing or retyped is not a
# workflow anyone can run correctly, and CI must not call it green.
INPUT_SCHEMA = {
    "sha-removed": lambda w: dispatch_inputs(w).pop("sha"),
    "run-id-removed": lambda w: dispatch_inputs(w).pop("run_id"),
    "sha-optional": lambda w: dispatch_inputs(w)["sha"].__setitem__("required", False),
    "run-id-optional": lambda w: dispatch_inputs(w)["run_id"].__setitem__("required", False),
    "sha-boolean": lambda w: dispatch_inputs(w)["sha"].__setitem__("type", "boolean"),
    "run-id-boolean": lambda w: dispatch_inputs(w)["run_id"].__setitem__("type", "boolean"),
    "sha-choice": lambda w: dispatch_inputs(w)["sha"].__setitem__("type", "choice"),
    "run-id-number": lambda w: dispatch_inputs(w)["run_id"].__setitem__("type", "number"),
    "inputs-removed": lambda w: dispatch_declaration(w).pop("inputs"),
    "extra-input": lambda w: dispatch_inputs(w).__setitem__(
        "force", {"description": "skip gates", "required": False, "type": "boolean"}),
}

# Serialisation of production deploys. A run applying migrations or waiting on a
# rollout is holding a half-built candidate, and must not be cancelled out from
# under itself nor overtaken by a second run into the same slot.
CONCURRENCY = {
    "concurrency-removed": lambda w: w.pop("concurrency"),
    "concurrency-null": lambda w: w.__setitem__("concurrency", None),
    "group-removed": lambda w: w["concurrency"].pop("group"),
    "group-null": lambda w: w["concurrency"].__setitem__("group", None),
    "group-changed": lambda w: w["concurrency"].__setitem__("group", "nchat-prod"),
    "cancel-removed": lambda w: w["concurrency"].pop("cancel-in-progress"),
    "cancel-true": lambda w: w["concurrency"].__setitem__("cancel-in-progress", True),
    # Quoted, so the expression evaluator reads it as truthy and cancels the run
    # that is holding the candidate. It has to be a real boolean.
    "cancel-quoted-false": lambda w: w["concurrency"].__setitem__("cancel-in-progress", "false"),
}

# An input that is not a mapping declares nothing. `sha:` with nothing after it
# parses as None, and a checker that shrugged that off would call a workflow
# nobody can dispatch correctly a passing one.
INPUT_SHAPE = {
    "sha-null": lambda w: dispatch_inputs(w).__setitem__("sha", None),
    "run-id-null": lambda w: dispatch_inputs(w).__setitem__("run_id", None),
    "sha-scalar": lambda w: dispatch_inputs(w).__setitem__("sha", "string"),
    "run-id-boolean-scalar": lambda w: dispatch_inputs(w).__setitem__("run_id", True),
    "sha-sequence": lambda w: dispatch_inputs(w).__setitem__("sha", []),
    "run-id-sequence": lambda w: dispatch_inputs(w).__setitem__("run_id", []),
}

# The top level of the file. `defaults.run.shell` is the one that matters: it is
# documented, actionlint accepts it, every listed step stays byte-for-byte what
# the contract expects, and every one of them is then run through a shell the
# contract never saw. Whatever the value, the key does not belong here.
ROOT_SURFACE = {
    "defaults-shell-injection": lambda w: w.__setitem__(
        "defaults", {"run": {"shell": "echo unexpected >&2; bash {0}"}}),
    "defaults-shell-benign": lambda w: w.__setitem__(
        "defaults", {"run": {"shell": "bash {0}"}}),
    "root-env": lambda w: w.__setitem__("env", {"NCHAT_PROD_ASSUME_YES": "1"}),
    "root-run-name": lambda w: w.__setitem__("run-name", "release ${{ inputs.sha }}"),
    "name-removed": lambda w: w.pop("name"),
}

# Not a violation: a comment is documentation and a step name is a label, so
# both must stay free to edit. A contract nobody can maintain gets bypassed.
BENIGN = {
    "edited-comment": lambda w: rename_and_recomment(w),
}


def dispatch_siblings(workflow):
    return workflow["on" if "on" in workflow else True]


def dispatch_declaration(workflow):
    return dispatch_siblings(workflow)["workflow_dispatch"]


def rename_and_recomment(workflow):
    candidate = steps(workflow)
    candidate[0]["run"] = candidate[0]["run"].replace("# A dispatch", "# EDITED dispatch")
    candidate[6]["name"] = "Renamed step"


MUTATIONS = {
    **PROMOTION_SURFACE,
    **SECOND_JOB,
    **CANDIDATE_RELEASE,
    **SELECTOR_INVARIANT,
    **SLOT_DERIVATION,
    **RELEASE_IDENTITY,
    **CANDIDATE_BINDING,
    **TIMEOUT,
    **INPUT_KEYS,
    **TRIGGERS,
    **INPUT_SCHEMA,
    **INPUT_SHAPE,
    **CONCURRENCY,
    **ROOT_SURFACE,
    **BENIGN,
}


def mutate(workflow, operation):
    if operation not in MUTATIONS:
        raise SystemExit(f"unknown mutation: {operation}")
    MUTATIONS[operation](workflow)


def main(source, destination, operation):
    with open(source, encoding="utf-8") as handle:
        workflow = yaml.safe_load(handle)
    mutate(workflow, operation)
    with open(destination, "w", encoding="utf-8") as handle:
        yaml.safe_dump(workflow, handle, sort_keys=False)
    return 0


sys.exit(main(sys.argv[1], sys.argv[2], sys.argv[3]))
PYTHON
}

# The property the whole design rests on, and the reason the contract is an
# allowlist rather than a search for dangerous commands: none of these has to be
# recognised as a promotion. There is no step in the contract that runs one, and
# no spare position for another, so every spelling fails for the same reason.
test_the_workflow_cannot_promote() {
  echo "the workflow's executable surface is closed"
  expect_workflow_refused "a promotion invoked through bash" \
    "$(mutate wrapped-cutover wrapped-cutover)"
  expect_workflow_refused "a promotion invoked through env bash" \
    "$(mutate env-wrapped env-wrapped-cutover)"
  expect_workflow_refused "a rollback script reachable from the deploy" \
    "$(mutate rollback rollback-script)"
  expect_workflow_refused "the old slot drained from the deploy" \
    "$(mutate drain-old drain-old-script)"
  expect_workflow_refused "the selector helper called directly" \
    "$(mutate switch-helper switch-helper)"
  expect_workflow_refused "a selector patched by hand with kubectl" \
    "$(mutate hand-patch hand-written-patch)"
  expect_workflow_refused "an added action, pinned by full SHA" \
    "$(mutate extra-action extra-action)"
  # Nothing about `echo hello` is dangerous. It is refused because the
  # executable surface is meant to stay small enough to audit, and that is what
  # makes the dangerous cases above impossible to smuggle in.
  expect_workflow_refused "an added step that does something harmless" \
    "$(mutate echo-hello harmless-echo)"
}

# The shape this task exists to keep out. Cutover is a later, separate step, so
# a second job here is refused whatever it is called -- and refused for being a
# second job, not for looking like a promotion, which is what makes the name
# irrelevant.
test_there_is_exactly_one_job() {
  echo "one job, and no promotion phase"
  expect_workflow_refused "a cutover job added back" \
    "$(mutate cutover-job cutover-job)"
  expect_workflow_refused "the same job under another name" \
    "$(mutate renamed-cutover renamed-cutover-job)"
  expect_workflow_refused "a second job that only reports" \
    "$(mutate second-job harmless-second-job)"
  # An environment here would attach an approval to the phase that has nothing
  # to approve yet, and pull that environment's secrets into an unprotected
  # deploy. Both spellings of the key are refused.
  expect_workflow_refused "the deploy put behind the production environment" \
    "$(mutate environment candidate-environment)"
  expect_workflow_refused "the same environment declared as a mapping" \
    "$(mutate environment-mapping candidate-environment-mapping)"
  expect_workflow_refused "the job made to depend on another" \
    "$(mutate job-needs candidate-needs)"
  expect_workflow_refused "the job made conditional, so a failed gate can be stepped over" \
    "$(mutate conditional-job conditional-job)"
  expect_workflow_refused "the job holding a write permission" \
    "$(mutate job-write job-write-permission)"
  # Outputs exist to feed a consumer. Re-declaring them is the first half of
  # adding one back, and it has to be reviewed as that.
  expect_workflow_refused "job outputs wired up for a consumer that does not exist" \
    "$(mutate outputs outputs-restored)"
}

# The order is a property of the release, not a detail of the file: digests are
# bound before anything is deployed, and the deploy finishes before the smoke
# that is supposed to be validating it.
test_the_candidate_performs_the_release() {
  echo "deploy -> smoke"
  expect_workflow_refused "a deploy replaced by a command that does nothing" \
    "$(mutate deploy-true deploy-replaced)"
  expect_workflow_refused "a run that never deploys" \
    "$(mutate no-deploy deploy-removed)"
  expect_workflow_refused "a run that never smokes" \
    "$(mutate no-smoke smoke-removed)"
  expect_workflow_refused "a smoke that runs before the deploy it validates" \
    "$(mutate early-smoke smoke-before-deploy)"
  expect_workflow_refused "an automated smoke marked continue-on-error" \
    "$(mutate soft-smoke soft-smoke)"
  expect_workflow_refused "an automated smoke made conditional" \
    "$(mutate conditional-smoke conditional-smoke)"
  expect_workflow_refused "a deploy marked continue-on-error" \
    "$(mutate soft-deploy soft-deploy)"
}

# The evidence the whole task turns on: the stable Services select after the run
# exactly what they selected before it. The proof is only worth something if it
# cannot be removed, reordered before the deploy it is watching, softened into a
# pass, pointed at a file nobody wrote, or turned into a repair.
test_the_stable_selector_invariant_is_proved() {
  echo "stable selectors before == after"
  expect_workflow_refused "a run that never re-reads the stable Services" \
    "$(mutate no-verify verification-removed)"
  expect_workflow_refused "the comparison moved in front of the deploy" \
    "$(mutate early-verify verification-before-deploy)"
  expect_workflow_refused "the comparison marked continue-on-error" \
    "$(mutate soft-verify soft-verification)"
  expect_workflow_refused "the comparison made conditional" \
    "$(mutate conditional-verify conditional-verification)"
  expect_workflow_refused "a difference swallowed by || true" \
    "$(mutate tolerated-diff tolerated-difference)"
  expect_workflow_refused "a comparison against a snapshot nobody wrote" \
    "$(mutate verify-elsewhere verification-reads-elsewhere)"
  expect_workflow_refused "a snapshot written where the comparison will not look" \
    "$(mutate snapshot-elsewhere snapshot-writes-elsewhere)"
  expect_workflow_refused "a run that takes no snapshot at all" \
    "$(mutate no-snapshot snapshot-removed)"
  # The step exists to detect that something moved production traffic. A step
  # that puts the selector back destroys the only record of it happening.
  expect_workflow_refused "a comparison that repairs the selector instead of failing" \
    "$(mutate self-healing self-healing-verification)"
}

# The candidate slot is opposite_slot(active), read from the cluster. A literal
# or an input would deploy over whatever happens to be serving.
# The smoke proves the candidate slot agrees with itself. It does not prove the
# slot is running the release this run built -- a concurrent redeploy of that
# same slot leaves the stable selectors untouched and produces a slot that is
# equally Ready and equally CONSISTENT. Only comparing the observed identity to
# the requested one separates the two, so that comparison cannot be removed,
# softened, half-declared or reordered out of the way.
test_the_candidate_carries_the_requested_release() {
  echo "the candidate is running the release this run built"
  expect_workflow_refused "a run that never checks what the candidate is running" \
    "$(mutate no-identity identity-removed)"
  expect_workflow_refused "an identity check replaced by a command that does nothing" \
    "$(mutate identity-true identity-replaced)"
  expect_workflow_refused "an identity check marked continue-on-error" \
    "$(mutate soft-identity identity-soft)"
  expect_workflow_refused "an identity check made conditional" \
    "$(mutate conditional-identity identity-conditional)"
  expect_workflow_refused "a mismatch swallowed by || true" \
    "$(mutate tolerated-identity identity-tolerated)"
  expect_workflow_refused "the cluster read kept but the comparison dropped" \
    "$(mutate uncompared-identity identity-not-compared)"
  # Two builds of one commit share the SHA and differ only in the seal, so a
  # commit-only comparison would pass a rebuild nobody deployed.
  expect_workflow_refused "an expected release naming only the commit" \
    "$(mutate sha-only-identity identity-sha-only)"
  expect_workflow_refused "an expected release naming only the build" \
    "$(mutate id-only-identity identity-id-only)"
  expect_workflow_refused "a commit half taken from another step" \
    "$(mutate rewired-sha identity-sha-rewired)"
  expect_workflow_refused "a build half taken from another step" \
    "$(mutate rewired-id identity-id-rewired)"
  expect_workflow_refused "an expected release written as a literal" \
    "$(mutate hardcoded-identity identity-expected-hardcoded)"
  expect_workflow_refused "an identity read from a hardcoded slot" \
    "$(mutate identity-green identity-slot-hardcoded)"
  expect_workflow_refused "an identity read from a slot named by an input" \
    "$(mutate identity-input identity-slot-from-input)"
  expect_workflow_refused "an identity read before the deploy it describes" \
    "$(mutate early-identity identity-before-deploy)"
  expect_workflow_refused "a report published before the identity is proved" \
    "$(mutate early-report report-before-identity)"
}

# One decision, not two. The workflow resolves the candidate from the same
# reading its selector proof is built on and hands that slot to deploy.sh; the
# smoke and the report name the same one. Without the binding, deploy.sh reads
# the cluster again and can build a different slot from the one being smoked.
test_the_deploy_builds_the_resolved_candidate() {
  echo "one candidate identity, from snapshot to evidence"
  expect_workflow_refused "a deploy left to resolve its own candidate" \
    "$(mutate unbound deploy-slot-unbound)"
  expect_workflow_refused "a deploy pinned to a hardcoded slot" \
    "$(mutate bound-green deploy-slot-hardcoded)"
  expect_workflow_refused "a deploy taking its slot from a workflow input" \
    "$(mutate bound-input deploy-slot-from-input)"
  expect_workflow_refused "a deploy pointed at the slot serving production" \
    "$(mutate bound-active deploy-slot-is-active)"
}

# A job with no limit holds the concurrency group for as long as it hangs, and
# the group is what serialises production releases.
test_the_job_is_time_limited() {
  echo "the deploy's time limit"
  expect_workflow_refused "a job with no time limit at all" \
    "$(mutate no-timeout timeout-removed)"
  expect_workflow_refused "a null time limit" \
    "$(mutate null-timeout timeout-null)"
  expect_workflow_refused "a limit too short to complete a healthy release" \
    "$(mutate one-minute timeout-one)"
  expect_workflow_refused "a limit one minute under the contract" \
    "$(mutate timeout-44 timeout-44)"
  expect_workflow_refused "a limit one minute over the contract" \
    "$(mutate timeout-46 timeout-46)"
  expect_workflow_refused "a quoted limit Actions would reject at parse time" \
    "$(mutate quoted-timeout timeout-quoted)"
}

# The inputs are gates. A `default` turns one into a value the dispatcher can
# leave alone, so a release could start without anyone naming a commit.
test_the_input_declarations_are_closed() {
  echo "input declarations carry nothing extra"
  expect_workflow_refused "a commit with a default nobody has to override" \
    "$(mutate default-sha sha-default)"
  expect_workflow_refused "a build run with a default nobody has to override" \
    "$(mutate default-run-id run-id-default)"
  expect_workflow_refused "a commit carrying a deprecation message" \
    "$(mutate deprecated-sha sha-deprecated)"
  expect_workflow_refused "a build run narrowed by an options list" \
    "$(mutate options-run-id run-id-options)"
  expect_workflow_refused "an unreviewed key on the commit input" \
    "$(mutate extra-key-sha sha-extra-key)"
  expect_workflow_refused "an unreviewed key on the build run input" \
    "$(mutate extra-key-run-id run-id-extra-key)"
}

test_the_candidate_slot_comes_from_the_cluster() {
  echo "the candidate slot"
  expect_workflow_refused "a smoke pointed at a hardcoded green" \
    "$(mutate hardcoded-green slot-hardcoded-green)"
  expect_workflow_refused "a smoke pointed at a hardcoded blue" \
    "$(mutate hardcoded-blue slot-hardcoded-blue)"
  expect_workflow_refused "a slot taken from a workflow input" \
    "$(mutate slot-input slot-from-input)"
  expect_workflow_refused "a candidate that is the active slot itself" \
    "$(mutate slot-active slot-not-opposite)"
}

test_untrusted_input_and_mutable_actions() {
  echo "untrusted triggers and mutable actions"
  expect_workflow_refused "a workflow that also runs on pull_request_target" \
    "$(mutate pr-target pull-request-target)"
  expect_workflow_refused "a workflow that also releases on push" \
    "$(mutate on-push on-push)"
  expect_workflow_refused "an action pinned to a mutable tag" \
    "$(mutate mutable-tag mutable-action)"
}

# The dispatch form is the only way this workflow starts, and both of its inputs
# are gates: the commit is proved against main, the run id binds the promotion to
# a sealed manifest. A workflow whose inputs went missing or changed type is one
# nobody can release with correctly, and CI must not report that as green.
test_the_dispatch_schema_is_a_contract() {
  echo "the workflow_dispatch schema"
  expect_workflow_refused "a dispatch that no longer asks for the commit" \
    "$(mutate no-sha sha-removed)"
  expect_workflow_refused "a dispatch that no longer asks for the build run" \
    "$(mutate no-run-id run-id-removed)"
  # An optional input is not a smaller version of a required one: it arrives as
  # the empty string, and the gate then refuses every run instead of the wrong
  # ones.
  expect_workflow_refused "a commit that may be omitted" \
    "$(mutate optional-sha sha-optional)"
  expect_workflow_refused "a build run that may be omitted" \
    "$(mutate optional-run-id run-id-optional)"
  expect_workflow_refused "a commit declared as a boolean" \
    "$(mutate boolean-sha sha-boolean)"
  expect_workflow_refused "a build run declared as a boolean" \
    "$(mutate boolean-run-id run-id-boolean)"
  expect_workflow_refused "a commit narrowed to a choice list" \
    "$(mutate choice-sha sha-choice)"
  expect_workflow_refused "a build run declared as a number" \
    "$(mutate number-run-id run-id-number)"
  expect_workflow_refused "a dispatch with no inputs at all" \
    "$(mutate no-inputs inputs-removed)"
  # A third input is a new lever on a production promotion. It has to be
  # reviewed as one rather than absorbed by a contract that only checks two.
  expect_workflow_refused "an extra input the contract does not know about" \
    "$(mutate extra-input extra-input)"
}

# The discriminating case. An allowlist compared too literally would fail on a
# reworded comment, and a contract nobody can edit safely gets bypassed instead
# of maintained. Comments and step names are not execution and must stay free.
# A production deploy is serialised on purpose. Without it, a second run can
# redeploy the slot the first one is still building: the later gates still fail
# closed, but the operator is watching a run that has quietly stopped describing
# the cluster.
test_releases_are_serialised() {
  echo "concurrency"
  expect_workflow_refused "a workflow with no concurrency group at all" \
    "$(mutate no-concurrency concurrency-removed)"
  expect_workflow_refused "a concurrency block set to null" \
    "$(mutate null-concurrency concurrency-null)"
  expect_workflow_refused "a concurrency block with no group" \
    "$(mutate no-group group-removed)"
  expect_workflow_refused "a null group" \
    "$(mutate null-group group-null)"
  expect_workflow_refused "a group renamed out from under the other workflows" \
    "$(mutate renamed-group group-changed)"
  expect_workflow_refused "a concurrency block that does not say whether to cancel" \
    "$(mutate no-cancel cancel-removed)"
  expect_workflow_refused "a run holding a candidate being cancellable" \
    "$(mutate cancel-true cancel-true)"
  expect_workflow_refused "cancel-in-progress quoted, and so truthy" \
    "$(mutate quoted-cancel cancel-quoted-false)"
}

# `sha:` with nothing after it is not a smaller declaration than a full one; it
# is no declaration. The same goes for a scalar or a list where a mapping
# belongs, and none of them may be read as an empty contract that passes.
test_inputs_must_be_mappings() {
  echo "input declarations that declare nothing"
  expect_workflow_refused "a commit input with an empty declaration" \
    "$(mutate null-sha sha-null)"
  expect_workflow_refused "a build run input with an empty declaration" \
    "$(mutate null-run-id run-id-null)"
  expect_workflow_refused "a commit input declared as a bare string" \
    "$(mutate scalar-sha sha-scalar)"
  expect_workflow_refused "a build run input declared as a bare boolean" \
    "$(mutate scalar-run-id run-id-boolean-scalar)"
  expect_workflow_refused "a commit input declared as a sequence" \
    "$(mutate sequence-sha sha-sequence)"
  expect_workflow_refused "a build run input declared as a sequence" \
    "$(mutate sequence-run-id run-id-sequence)"
}

# Everything else in this suite describes what happens inside `jobs:`. A
# workflow can change what every step runs without touching any of it, so the
# top level is closed too -- and closed as an allowlist, which is what makes the
# last two cases below refusals rather than gaps waiting to be reported.
test_the_root_surface_is_closed() {
  echo "the top level of the workflow"
  expect_workflow_refused "a default shell that wraps every step's command" \
    "$(mutate defaults-injection defaults-shell-injection)"
  # Refused for the same reason, and the reason is not that it looks dangerous:
  # a shell nobody reviewed is outside the contract whatever it happens to run.
  expect_workflow_refused "a default shell that looks entirely harmless" \
    "$(mutate defaults-benign defaults-shell-benign)"
  expect_workflow_refused "workflow-level environment variables" \
    "$(mutate root-env root-env)"
  expect_workflow_refused "a valid Actions key the contract does not include" \
    "$(mutate root-run-name root-run-name)"
  # The same comparison in the other direction: the contract is exact, so a
  # missing top-level key is as much a violation as an added one.
  expect_workflow_refused "a workflow missing a required top-level key" \
    "$(mutate no-name name-removed)"
}

test_documentation_is_not_execution() {
  echo "documentation is not execution"
  expect_workflow_accepted "a reworded comment and a renamed step" \
    "$(mutate edited-comment edited-comment)"
}

# --- release binding --------------------------------------------------------

fixture_digest() {
  printf 'sha256:%s' "$(printf '%s' "$1" | sha256sum | cut -d' ' -f1)"
}

# A sealed manifest for SOURCE_SHA, produced by the real generator so the
# fixture is a manifest and not an approximation of one.
make_manifest() {
  local name="$1" sha="${2-$SOURCE_SHA}" salt="${3-}" image
  local artifacts="$WORK_DIR/digests.$name" out="$WORK_DIR/manifest.$name"
  rm -rf "$artifacts" "$out"
  mkdir -p "$artifacts"
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    # The salt stands in for what a rebuild changes: same source commit, same
    # inventory, different image bytes and therefore different digests.
    printf '%s' "$(fixture_digest "$image$salt")" >"$artifacts/digest-$image.txt"
  done
  NCHAT_RELEASE_SOURCE_SHA="$sha" NCHAT_RELEASE_RUN_ID="$RUN_ID" \
    bash "$RELEASE_MANIFEST" "$artifacts" "$out" >/dev/null
  printf '%s' "$out"
}

run_digests() {
  local manifest_dir="$1" artifacts_dir="$2" sha="${3-$SOURCE_SHA}"
  NCHAT_PROD_RELEASE_SHA="$sha" \
    bash "$RELEASE_DIGESTS" "$manifest_dir" "$artifacts_dir" >/dev/null 2>&1
}

# The digest files a consumer would actually read, one line per file.
pinned_digests() {
  local artifacts_dir="$1"
  [[ -d "$artifacts_dir" ]] || return 0
  find "$artifacts_dir" -maxdepth 1 -name 'digest-*.txt' -printf '%f\n' | LC_ALL=C sort
}

# A refusal is only fail-closed if it also pinned nothing -- and "nothing" has
# to mean no usable digest file, not merely no directory. The directory itself
# is created before the gates run, because clearing a previous attempt's digests
# is what the gates must not be able to skip.
expect_digests_refused() {
  local name="$1" manifest_dir="$2" sha="${3-$SOURCE_SHA}"
  local artifacts="${4-$WORK_DIR/out.refused}" left
  if run_digests "$manifest_dir" "$artifacts" "$sha"; then
    fail "$name: the release binding accepted it"
    return
  fi
  left="$(pinned_digests "$artifacts")"
  if [[ -n "$left" ]]; then
    fail "$name: refused but left usable digests behind: $(tr '\n' ' ' <<<"$left")"
    return
  fi
  pass "$name"
}

# An output directory already holding a complete, readable set of digests from
# an earlier release. This is the state in which a stale file is dangerous: a
# consumer reading digest-web.txt cannot tell which attempt wrote it.
populate_stale_output() {
  local artifacts_dir="$1" image
  rm -rf "$artifacts_dir"
  mkdir -p "$artifacts_dir"
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    printf 'sha256:%064d' 1 >"$artifacts_dir/digest-$image.txt"
  done
  printf '%s' "$artifacts_dir"
}

test_a_sealed_manifest_pins_its_own_digests() {
  local manifest artifacts image expected actual count
  echo "a sealed manifest"
  manifest="$(make_manifest valid)"
  artifacts="$WORK_DIR/out.valid"
  if ! run_digests "$manifest" "$artifacts"; then
    fail "a valid sealed manifest was refused"
    return
  fi
  count="$(find "$artifacts" -name 'digest-*.txt' | wc -l)"
  if [[ "$count" -ne "${#NCHAT_DEV_IMAGES[@]}" ]]; then
    fail "expected ${#NCHAT_DEV_IMAGES[@]} pinned digests, got $count"
    return
  fi
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    expected="$(jq -r --arg i "$image" '.images[$i]' "$manifest/release-manifest.json")"
    actual="$(<"$artifacts/digest-$image.txt")"
    if [[ "$expected" != "$actual" ]]; then
      fail "$image was pinned to $actual, not the manifest's $expected"
      return
    fi
  done
  pass "every image is pinned to exactly the digest the manifest seals"
}

test_an_unproven_manifest_pins_nothing() {
  local tampered other short
  echo "an unproven manifest"
  tampered="$(make_manifest tampered)"
  # The bytes change, the seal does not: this is what a manifest edited after it
  # was sealed looks like.
  jq '.source_sha = "'"$OTHER_SHA"'"' "$tampered/release-manifest.json" >"$tampered/tmp"
  mv "$tampered/tmp" "$tampered/release-manifest.json"
  expect_digests_refused "a manifest edited after it was sealed" "$tampered"

  other="$(make_manifest other "$OTHER_SHA")"
  expect_digests_refused "a correctly sealed manifest of a different commit" "$other"

  short="$(make_manifest short)"
  expect_digests_refused "a release SHA that is not a full commit SHA" "$short" 0123456789abcdef
  expect_digests_refused "a manifest directory that holds no manifest" "$WORK_DIR"
}

# The failure this suite exists for: an output directory that already holds a
# previous attempt's digests, and a new attempt that must be refused. Clearing
# only after the gates pass would leave the old files in place, and the next
# step would read them as though this run had produced them.
test_a_refused_release_clears_the_previous_one() {
  local stale broken other missing
  echo "a refused release over a populated output directory"

  broken="$(make_manifest stale-seal)"
  printf 'tampered' >>"$broken/release-manifest.json"
  stale="$(populate_stale_output "$WORK_DIR/out.stale-seal")"
  expect_digests_refused "a broken seal does not leave the previous digests usable" \
    "$broken" "$SOURCE_SHA" "$stale"

  other="$(make_manifest stale-other "$OTHER_SHA")"
  stale="$(populate_stale_output "$WORK_DIR/out.stale-other")"
  expect_digests_refused "a manifest of a different commit does not leave them usable" \
    "$other" "$SOURCE_SHA" "$stale"

  stale="$(populate_stale_output "$WORK_DIR/out.stale-sha")"
  expect_digests_refused "an invalid release SHA does not leave them usable" \
    "$(make_manifest stale-sha)" 0123456789abcdef "$stale"

  missing="$WORK_DIR/manifest.absent"
  mkdir -p "$missing"
  stale="$(populate_stale_output "$WORK_DIR/out.stale-missing")"
  expect_digests_refused "an absent manifest does not leave them usable" \
    "$missing" "$SOURCE_SHA" "$stale"
}

# And the other half: after a refusal has cleared the directory, a valid release
# must publish its own digests there and nothing else -- no survivor of the
# earlier attempt mixed in with them.
test_a_valid_release_after_a_refused_one() {
  local artifacts manifest image expected actual
  echo "a valid release into a directory a refusal has cleared"
  artifacts="$(populate_stale_output "$WORK_DIR/out.recovered")"
  run_digests "$(make_manifest recover-bad "$OTHER_SHA")" "$artifacts" "$SOURCE_SHA" && {
    fail "the mismatched manifest was accepted"
    return
  }
  manifest="$(make_manifest recover-good)"
  if ! run_digests "$manifest" "$artifacts"; then
    fail "the valid release was refused after an earlier refusal"
    return
  fi
  for image in "${NCHAT_DEV_IMAGES[@]}"; do
    expected="$(jq -r --arg i "$image" '.images[$i]' "$manifest/release-manifest.json")"
    actual="$(<"$artifacts/digest-$image.txt")"
    if [[ "$expected" != "$actual" ]]; then
      fail "$image kept the stale digest $actual instead of $expected"
      return
    fi
  done
  assert_pinned_set_is_exactly_the_inventory "$artifacts"
}

assert_pinned_set_is_exactly_the_inventory() {
  local artifacts_dir="$1" expected actual
  expected="$(printf 'digest-%s.txt\n' "${NCHAT_DEV_IMAGES[@]}" | LC_ALL=C sort)"
  actual="$(pinned_digests "$artifacts_dir")"
  if [[ "$expected" != "$actual" ]]; then
    fail "the recovered directory holds $(tr '\n' ' ' <<<"$actual")"
    return
  fi
  pass "only the valid release's digests remain"
}

# The identity a release is promoted under. It has to come from the manifest,
# because the manifest is the only artifact that covers the image digests: the
# source SHA names the code and two builds of one commit do not produce the same
# bytes. This is the offline half of that property; the cluster half lives in
# test_prod_blue_green_scripts.sh.
test_the_release_identity_is_the_manifest_seal() {
  local manifest artifacts sealed recorded
  echo "the release identity"
  manifest="$(make_manifest identity)"
  artifacts="$WORK_DIR/out.identity"
  if ! run_digests "$manifest" "$artifacts"; then
    fail "a valid sealed manifest was refused"
    return
  fi
  sealed="$(cut -d ' ' -f1 <"$manifest/release-manifest.sha256")"
  recorded="$(<"$artifacts/release-id.txt")"
  assert_equals "the identity pinned for the deploy is the manifest's own seal" \
    "$sealed" "$recorded"
}

# The whole point of the identity, in one comparison: change nothing but the
# image bytes and the release becomes a different release.
test_a_rebuild_of_one_commit_is_a_different_release() {
  local first second id_first id_second
  echo "same commit, different build"
  first="$WORK_DIR/out.build-a"
  second="$WORK_DIR/out.build-b"
  run_digests "$(make_manifest build-a "$SOURCE_SHA")" "$first" || {
    fail "build A was refused"
    return
  }
  run_digests "$(make_manifest build-b "$SOURCE_SHA" rebuilt)" "$second" || {
    fail "build B was refused"
    return
  }
  id_first="$(<"$first/release-id.txt")"
  id_second="$(<"$second/release-id.txt")"
  if [[ "$id_first" == "$id_second" ]]; then
    fail "two builds of $SOURCE_SHA share the identity $id_first; a rebuild would pass as the release that was smoked"
    return
  fi
  pass "two builds of one commit carry different release identities"
}

main() {
  JOB_MUTATOR="$WORK_DIR/job-mutator.py"
  write_job_mutator
  test_real_workflow_satisfies_the_contract
  test_the_workflow_cannot_promote
  test_there_is_exactly_one_job
  test_the_candidate_performs_the_release
  test_the_stable_selector_invariant_is_proved
  test_the_candidate_slot_comes_from_the_cluster
  test_the_candidate_carries_the_requested_release
  test_the_deploy_builds_the_resolved_candidate
  test_the_job_is_time_limited
  test_the_input_declarations_are_closed
  test_untrusted_input_and_mutable_actions
  test_the_dispatch_schema_is_a_contract
  test_inputs_must_be_mappings
  test_releases_are_serialised
  test_the_root_surface_is_closed
  test_documentation_is_not_execution
  test_a_sealed_manifest_pins_its_own_digests
  test_the_release_identity_is_the_manifest_seal
  test_a_rebuild_of_one_commit_is_a_different_release
  test_an_unproven_manifest_pins_nothing
  test_a_refused_release_clears_the_previous_one
  test_a_valid_release_after_a_refused_one
  if [[ "$FAILURES" -ne 0 ]]; then
    echo "Production deploy workflow tests failed: $FAILURES" >&2
    return 1
  fi
  echo "Production deploy workflow tests passed."
}

main "$@"
