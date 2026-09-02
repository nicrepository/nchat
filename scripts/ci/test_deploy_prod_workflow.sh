#!/usr/bin/env bash
# Behaviour tests for the candidate/cutover separation (CICD-06).
#
# Two things are proved here, both offline and neither against a cluster.
#
# First, the workflow contract: the checker is driven with copies of the real
# workflow that each break one invariant -- the environment removed, the
# dependency broken, a promotion moved into the candidate, a gate marked
# continue-on-error -- and every one of them must be refused. A checker that
# only ever sees a correct workflow proves nothing about what it would catch.
#
# Second, the release binding: release-digests.sh is driven with a manifest
# whose seal is broken, one that seals a different commit, and one missing an
# image. Each must refuse and leave no artifacts directory, because a deploy
# that pins digests from an unproven manifest is exactly the failure the sealed
# manifest exists to prevent.
#
# What is deliberately NOT tested here is a rejected environment approval, which
# GitHub decides remotely. The runbook carries the evidence procedure for it;
# what stands in for it offline is the confinement check, which proves there is
# nothing outside the protected job that a rejection would have to stop.
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
    pass "the production deploy workflow satisfies the candidate/cutover contract"
    return
  fi
  fail "the committed production deploy workflow does not satisfy its own contract"
}

# The approval is only worth something if the job it gates is the only way to
# promote, and if nothing can start that job early.
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
PATCH_SELECTOR = (
    "kubectl patch svc chat-service --type=json "
    '-p \'[{"op":"replace","path":"/spec/selector/nchat.io~1release-slot"}]\''
)


def steps(workflow, job):
    return workflow["jobs"][job]["steps"]


def outputs(workflow):
    return workflow["jobs"]["candidate"]["outputs"]


def promotion_env(workflow):
    """The env of the one step that promotes, where the wiring is asserted."""
    return steps(workflow, "cutover")[4]["env"]


def dispatch_inputs(workflow):
    """`on:` is the boolean True once PyYAML has read it."""
    return workflow["on" if "on" in workflow else True]["workflow_dispatch"]["inputs"]


def add_run(workflow, job, command):
    steps(workflow, job).append({"name": "Added", "run": command})


def add_action(workflow, job):
    steps(workflow, job).append({"name": "Added", "uses": PINNED_ACTION})


def move(workflow, job, source, destination):
    steps(workflow, job).insert(destination, steps(workflow, job).pop(source))


# Ways the candidate could reach a promotion, and ways its executable surface
# could grow. None of these needs to be recognised as dangerous by the checker;
# they are refused because the contract lists no such step.
CANDIDATE_SURFACE = {
    "wrapped-cutover": lambda w: add_run(w, "candidate", f'bash {CUTOVER} --target "$CANDIDATE_SLOT"'),
    "env-wrapped-cutover": lambda w: add_run(w, "candidate", f'env bash {CUTOVER} --target "$CANDIDATE_SLOT"'),
    "hand-written-patch": lambda w: add_run(w, "candidate", PATCH_SELECTOR),
    "harmless-echo": lambda w: add_run(w, "candidate", "echo hello"),
    "extra-action-candidate": lambda w: add_action(w, "candidate"),
}

# The release the candidate is supposed to perform, and in what order.
CANDIDATE_RELEASE = {
    "deploy-replaced": lambda w: steps(w, "candidate")[7].__setitem__("run", "true"),
    "deploy-removed": lambda w: steps(w, "candidate").pop(7),
    "smoke-removed": lambda w: steps(w, "candidate").pop(8),
    "smoke-before-deploy": lambda w: move(w, "candidate", 8, 7),
    "soft-smoke": lambda w: steps(w, "candidate")[8].__setitem__("continue-on-error", True),
    "conditional-smoke": lambda w: steps(w, "candidate")[8].__setitem__("if", "always()"),
}

# The slot and release identity the promotion must act on.
WIRING = {
    "slot-output-removed": lambda w: outputs(w).pop("slot"),
    "slot-output-rewired": lambda w: outputs(w).__setitem__("slot", "${{ steps.release.outputs.slot }}"),
    "release-id-output-rewired": lambda w: outputs(w).__setitem__(
        "release_id", "${{ steps.slot.outputs.release_id }}"),
    "slot-hardcoded-green": lambda w: promotion_env(w).__setitem__("CANDIDATE_SLOT", "green"),
    "slot-hardcoded-blue": lambda w: promotion_env(w).__setitem__("CANDIDATE_SLOT", "blue"),
    "slot-from-input": lambda w: promotion_env(w).__setitem__("CANDIDATE_SLOT", "${{ inputs.slot }}"),
    "evidence-hardcoded": lambda w: promotion_env(w).__setitem__(
        "NCHAT_PROD_SMOKE_CONFIRMED", "green:abc:deadbeef"),
    "evidence-drops-release-id": lambda w: promotion_env(w).__setitem__(
        "NCHAT_PROD_SMOKE_CONFIRMED", "${{ needs.candidate.outputs.slot }}:${{ inputs.sha }}"),
    "manifest-dir-removed": lambda w: promotion_env(w).pop("NCHAT_PROD_RELEASE_MANIFEST_DIR"),
}

# What makes the protected job protected, and what keeps it small.
PROTECTION = {
    "harmless-true": lambda w: add_run(w, "cutover", "true"),
    "extra-action-cutover": lambda w: add_action(w, "cutover"),
    "cutover-write-permission": lambda w: w["jobs"]["cutover"]["permissions"].__setitem__(
        "packages", "write"),
    "no-environment": lambda w: w["jobs"]["cutover"].pop("environment"),
    "other-environment": lambda w: w["jobs"]["cutover"].__setitem__("environment", "staging"),
    "no-needs": lambda w: w["jobs"]["cutover"].pop("needs"),
    "conditional-cutover": lambda w: w["jobs"]["cutover"].__setitem__("if", "always()"),
    "candidate-environment": lambda w: w["jobs"]["candidate"].__setitem__("environment", "production"),
}

# How the workflow can be started, and by what.
TRIGGERS = {
    "mutable-action": lambda w: steps(w, "candidate")[1].__setitem__("uses", "actions/checkout@v4"),
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

# Serialisation of production releases. A run holding a deployed candidate while
# it waits for approval must not be cancelled, nor overtaken by a second one.
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
    candidate = steps(workflow, "candidate")
    candidate[0]["run"] = candidate[0]["run"].replace("# A dispatch", "# EDITED dispatch")
    candidate[6]["name"] = "Renamed step"


MUTATIONS = {
    **CANDIDATE_SURFACE,
    **CANDIDATE_RELEASE,
    **WIRING,
    **PROTECTION,
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
test_the_candidate_cannot_promote() {
  echo "the candidate's executable surface is closed"
  expect_workflow_refused "a promotion invoked through bash" \
    "$(mutate wrapped-cutover wrapped-cutover)"
  expect_workflow_refused "a promotion invoked through env bash" \
    "$(mutate env-wrapped env-wrapped-cutover)"
  expect_workflow_refused "a selector patched by hand with kubectl" \
    "$(mutate hand-patch hand-written-patch)"
  expect_workflow_refused "an added action, pinned by full SHA" \
    "$(mutate extra-action extra-action-candidate)"
  # Nothing about `echo hello` is dangerous. It is refused because the
  # executable surface is meant to stay small enough to audit, and that is what
  # makes the dangerous cases above impossible to smuggle in.
  expect_workflow_refused "an added step that does something harmless" \
    "$(mutate echo-hello harmless-echo)"
}

test_the_protected_job_stays_closed() {
  echo "the protected job's executable surface is closed"
  expect_workflow_refused "an added command in the cutover" \
    "$(mutate cutover-true harmless-true)"
  expect_workflow_refused "an added action in the cutover, pinned by full SHA" \
    "$(mutate cutover-action extra-action-cutover)"
  expect_workflow_refused "a cutover job holding write permission" \
    "$(mutate cutover-write cutover-write-permission)"
}

# The order is a property of the release, not a detail of the file: digests are
# bound before anything is deployed, and the deploy finishes before the smoke
# that is supposed to be validating it.
test_the_candidate_performs_the_release() {
  echo "candidate -> deploy -> smoke"
  expect_workflow_refused "a deploy replaced by a command that does nothing" \
    "$(mutate deploy-true deploy-replaced)"
  expect_workflow_refused "a candidate that never deploys" \
    "$(mutate no-deploy deploy-removed)"
  expect_workflow_refused "a candidate that never smokes" \
    "$(mutate no-smoke smoke-removed)"
  expect_workflow_refused "a smoke that runs before the deploy it validates" \
    "$(mutate early-smoke smoke-before-deploy)"
  expect_workflow_refused "an automated smoke marked continue-on-error" \
    "$(mutate soft-smoke soft-smoke)"
  expect_workflow_refused "an automated smoke made conditional" \
    "$(mutate conditional-smoke conditional-smoke)"
}

# The promotion must act on the slot and the release the candidate actually
# produced. A literal slot would promote whatever happens to be in it.
test_the_promotion_is_wired_to_the_candidate() {
  echo "slot and release identity wiring"
  expect_workflow_refused "a candidate that publishes no slot" \
    "$(mutate no-slot-output slot-output-removed)"
  expect_workflow_refused "a slot output taken from another step" \
    "$(mutate rewired-slot slot-output-rewired)"
  expect_workflow_refused "a release identity taken from another step" \
    "$(mutate rewired-release-id release-id-output-rewired)"
  expect_workflow_refused "a cutover promoting a hardcoded green" \
    "$(mutate hardcoded-green slot-hardcoded-green)"
  expect_workflow_refused "a cutover promoting a hardcoded blue" \
    "$(mutate hardcoded-blue slot-hardcoded-blue)"
  expect_workflow_refused "a cutover taking its slot from a workflow input" \
    "$(mutate slot-input slot-from-input)"
  expect_workflow_refused "evidence with a hardcoded release identity" \
    "$(mutate hardcoded-evidence evidence-hardcoded)"
  expect_workflow_refused "evidence that names only the commit" \
    "$(mutate sha-only-evidence evidence-drops-release-id)"
  expect_workflow_refused "a cutover with no manifest to re-derive the identity from" \
    "$(mutate no-manifest-dir manifest-dir-removed)"
}

test_the_protected_job_is_protected() {
  echo "the protected cutover job"
  expect_workflow_refused "a cutover job with no environment" \
    "$(mutate no-environment no-environment)"
  expect_workflow_refused "a cutover job gated on some other environment" \
    "$(mutate other-environment other-environment)"
  expect_workflow_refused "a cutover that does not wait for the candidate" \
    "$(mutate no-needs no-needs)"
  expect_workflow_refused "a cutover made conditional, so a failed gate can be stepped over" \
    "$(mutate conditional-cutover conditional-cutover)"
  expect_workflow_refused "a candidate put behind the production environment" \
    "$(mutate candidate-environment candidate-environment)"
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
# A production release is serialised on purpose. Without it, a second run can
# redeploy the slot a waiting candidate already owns: the later gates still fail
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
  test_the_candidate_cannot_promote
  test_the_protected_job_stays_closed
  test_the_candidate_performs_the_release
  test_the_promotion_is_wired_to_the_candidate
  test_the_protected_job_is_protected
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
