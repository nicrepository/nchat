#!/usr/bin/env bash
# Behaviour tests for the single image builder (CICD-05).
#
# Two things have to hold for a production build to mean anything: the workflow
# must be wired so that the commit it is given is the commit it builds, and the
# gate that decides whether that commit may be built must actually refuse the
# commits it claims to refuse. The first half is driven by feeding the contract
# checker copies of the real workflows with one invariant broken; the second by
# running the gate against a throwaway git repository whose history is known.
#
# No network, no Docker, no cluster.
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
BUILDER="$ROOT_DIR/.github/workflows/build-nchat-images.yml"
CALLER="$ROOT_DIR/.github/workflows/images.yml"
CHECKER="$ROOT_DIR/scripts/ci/check_build_images_workflow.py"
MAIN_GATE="$ROOT_DIR/scripts/deploy/nchat-prod/require-main-sha.sh"

FAILURES=0
MUTATOR=""

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/nchat-build-images-test.XXXXXX")"
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

# --- The workflow contract --------------------------------------------------

# Only builds fixtures. The contract itself lives in the checker and is never
# restated here, so a test cannot pass by agreeing with a broken checker.
write_mutator() {
  cat >"$MUTATOR" <<'PYTHON'
"""Break one invariant of the image workflows, on a copy."""

import sys

import yaml

CHECKOUT = "actions/checkout"
UPLOAD = "actions/upload-artifact"
BUILD = "docker/build-push-action"
GATE = "require-main-sha.sh"
GENERATOR = "release-manifest.sh"
MATRIX = "scripts/deploy/nchat-dev/image-matrix.sh"

INPUT_OPS = {
    "optional-sha",
    "numeric-sha",
    "drop-require-main",
    "require-main-by-default",
    "extra-input",
}
OUTPUT_OPS = {"drop-sha-output", "stale-sha-output"}
GATE_OPS = {"gate-other-sha", "shallow-gate-history", "echoed-inventory"}
JOB_OPS = {
    "checkout-head",
    "unpinned-checkout",
    "inventory-may-push",
    "second-matrix",
    "drop-main-gate",
    "always-main-gate",
    "gate-before-checkout",
}
BUILD_OPS = {
    "drop-build-checkout",
    "duplicate-build-checkout",
    "checkout-after-push",
    "drop-checkout-ref",
    "persist-credentials",
    "drop-sbom",
    "drop-provenance",
    "mutable-tag",
    "tag-is-identity",
    "synthesised-digest",
    "ungated-digest",
    "drop-digest-artifact",
    "duplicate-digest-artifact",
}
DAG_OPS = {
    "manifest-blocks-deploy",
    "deploy-blocks-manifest",
    "detached-manifest",
    "drop-manifest",
    "manifest-other-sha",
}
CALLER_OPS = DAG_OPS | {
    "dispatch-without-proof",
    "caller-builds-head",
    "detached-deploy",
    "unconditional-deploy",
    "deploy-elsewhere",
    "build-main-pushes",
}


def triggers(workflow):
    """`on:` is YAML 1.1 for True, which is how PyYAML keys it."""
    return workflow[True] if True in workflow else workflow["on"]


def steps_using(steps, action):
    return [s for s in steps if str(s.get("uses", "")).split("@")[0] == action]


def index_gating(steps):
    return next(i for i, s in enumerate(steps) if GATE in str(s.get("run", "")))


def push_step(steps):
    return steps_using(steps, BUILD)[0]


def digest_step(steps):
    return next(s for s in steps if "DIGEST" in (s.get("env") or {}))


def mutate_inputs(workflow, operation):
    if operation not in INPUT_OPS:
        return False
    inputs = triggers(workflow)["workflow_call"]["inputs"]
    if operation == "optional-sha":
        inputs["sha"]["required"] = False
    elif operation == "numeric-sha":
        inputs["sha"]["type"] = "number"
    elif operation == "drop-require-main":
        del inputs["require_main"]
    elif operation == "require-main-by-default":
        inputs["require_main"]["default"] = True
    else:
        inputs["branch"] = {"required": False, "type": "string"}
    return True


def retarget_checkouts(jobs):
    for job in jobs.values():
        for step in steps_using(job["steps"], CHECKOUT):
            step["with"]["ref"] = "${{ github.sha }}"


def mutate_outputs(workflow, operation):
    if operation not in OUTPUT_OPS:
        return False
    if operation == "drop-sha-output":
        del triggers(workflow)["workflow_call"]["outputs"]
    else:
        workflow["jobs"]["inventory"]["outputs"]["sha"] = "${{ github.sha }}"
    return True


def mutate_inventory(workflow, operation):
    if operation not in GATE_OPS:
        return False
    steps = workflow["jobs"]["inventory"]["steps"]
    if operation == "gate-other-sha":
        steps[index_gating(steps)]["env"]["RELEASE_SHA"] = "${{ github.sha }}"
    elif operation == "shallow-gate-history":
        steps_using(steps, CHECKOUT)[0]["with"]["fetch-depth"] = 1
    else:
        next(s for s in steps if MATRIX in str(s.get("run", "")))["run"] = f"echo {MATRIX}"
    return True


def mutate_jobs(workflow, operation):
    if operation not in JOB_OPS:
        return False
    jobs = workflow["jobs"]
    inventory = jobs["inventory"]["steps"]
    if operation == "checkout-head":
        retarget_checkouts(jobs)
    elif operation == "unpinned-checkout":
        steps_using(jobs["build"]["steps"], CHECKOUT)[0]["uses"] = "actions/checkout@v4"
    elif operation == "inventory-may-push":
        jobs["inventory"]["permissions"]["packages"] = "write"
    elif operation == "second-matrix":
        jobs["build"]["strategy"]["matrix"]["image"] = ["web", "admin-web", "migrations"]
    elif operation == "drop-main-gate":
        inventory.pop(index_gating(inventory))
    elif operation == "always-main-gate":
        inventory[index_gating(inventory)]["if"] = "true"
    else:
        inventory.insert(0, inventory.pop(index_gating(inventory)))
    return True


CHECKOUT_OPS = {
    "drop-build-checkout",
    "duplicate-build-checkout",
    "checkout-after-push",
    "drop-checkout-ref",
    "persist-credentials",
}


def mutate_checkout(steps, operation):
    checkout = steps_using(steps, CHECKOUT)[0]
    if operation == "drop-build-checkout":
        steps.remove(checkout)
    elif operation == "duplicate-build-checkout":
        steps.insert(0, dict(checkout))
    elif operation == "checkout-after-push":
        steps.remove(checkout)
        steps.insert(steps.index(push_step(steps)) + 1, checkout)
    elif operation == "drop-checkout-ref":
        del checkout["with"]["ref"]
    else:
        checkout["with"]["persist-credentials"] = True
    return True


def mutate_build(workflow, operation):
    if operation not in BUILD_OPS:
        return False
    steps = workflow["jobs"]["build"]["steps"]
    tag = "ghcr.io/nicrepository/nchat/${{ matrix.image }}"
    if operation in CHECKOUT_OPS:
        return mutate_checkout(steps, operation)
    if operation == "drop-sbom":
        push_step(steps)["with"]["sbom"] = False
    elif operation == "drop-provenance":
        del push_step(steps)["with"]["provenance"]
    elif operation == "mutable-tag":
        push_step(steps)["with"]["tags"] = f"{tag}:latest"
    elif operation == "tag-is-identity":
        push_step(steps)["with"]["tags"] = tag + ":${{ github.ref_name }}"
    elif operation == "synthesised-digest":
        digest_step(steps)["env"]["DIGEST"] = "sha256:${{ inputs.sha }}"
    elif operation == "ungated-digest":
        digest_step(steps)["run"] = 'printf "%s" "$DIGEST" >"digest-$IMAGE.txt"'
    elif operation == "drop-digest-artifact":
        steps.remove(steps_using(steps, UPLOAD)[0])
    else:
        steps.append(dict(steps_using(steps, UPLOAD)[0]))
    return True


def mutate_release_dag(jobs, operation):
    if operation == "manifest-blocks-deploy":
        jobs["deploy"]["needs"] = ["build", "release-manifest"]
    elif operation == "deploy-blocks-manifest":
        jobs["release-manifest"]["needs"] = ["build", "deploy"]
    elif operation == "detached-manifest":
        del jobs["release-manifest"]["needs"]
    elif operation == "drop-manifest":
        del jobs["release-manifest"]
    else:
        manifest_generation(jobs)["env"]["NCHAT_RELEASE_SOURCE_SHA"] = "${{ github.sha }}"
    return True


def manifest_generation(jobs):
    steps = jobs["release-manifest"]["steps"]
    return next(s for s in steps if GENERATOR in str(s.get("run", "")))


def mutate_caller(workflow, operation):
    if operation not in CALLER_OPS:
        return False
    jobs = workflow["jobs"]
    if operation in DAG_OPS:
        return mutate_release_dag(jobs, operation)
    if operation == "dispatch-without-proof":
        jobs["build"]["with"]["require_main"] = False
    elif operation == "caller-builds-head":
        jobs["build"]["with"]["sha"] = "${{ github.sha }}"
    elif operation == "detached-deploy":
        del jobs["deploy"]["needs"]
    elif operation == "unconditional-deploy":
        del jobs["deploy"]["if"]
    elif operation == "deploy-elsewhere":
        jobs["deploy"]["uses"] = "./.github/workflows/deploy-nchat-prod.yml"
    else:
        triggers(workflow)["push"]["branches"] = ["develop", "main"]
    return True


def main(source, destination, operation):
    workflow = yaml.safe_load(open(source, encoding="utf-8"))
    for mutation in (mutate_inputs, mutate_outputs, mutate_inventory, mutate_jobs, mutate_build, mutate_caller):
        if mutation(workflow, operation):
            break
    else:
        raise SystemExit(f"unknown mutation: {operation}")
    with open(destination, "w", encoding="utf-8") as handle:
        yaml.safe_dump(workflow, handle, sort_keys=False)
    return 0


sys.exit(main(sys.argv[1], sys.argv[2], sys.argv[3]))
PYTHON
}

# A copy of one real workflow with one invariant broken. A mutation that fails
# to apply is a failure of its own: it would otherwise leave no copy behind, and
# a checker refusing a missing file looks exactly like one refusing the mutant.
MUTANT=""

mutate() {
  local workflow="$1" operation="$2"
  MUTANT="$WORK_DIR/$operation.yml"
  rm -f "$MUTANT"
  python3 "$MUTATOR" "$workflow" "$MUTANT" "$operation" && [[ -s "$MUTANT" ]]
}

expect_refused() {
  local name="$1" builder="$2" caller="$3"
  if python3 "$CHECKER" "$builder" "$caller" >/dev/null 2>&1; then
    fail "$name: the contract checker accepted it"
    return
  fi
  pass "$name"
}

expect_builder_refused() {
  local name="$1" operation="$2"
  if ! mutate "$BUILDER" "$operation"; then
    fail "$name: the mutation could not be applied"
    return
  fi
  expect_refused "$name" "$MUTANT" "$CALLER"
}

expect_caller_refused() {
  local name="$1" operation="$2"
  if ! mutate "$CALLER" "$operation"; then
    fail "$name: the mutation could not be applied"
    return
  fi
  expect_refused "$name" "$BUILDER" "$MUTANT"
}

test_workflows_satisfy_the_contract() {
  echo "the image workflows as committed"
  if python3 "$CHECKER" "$BUILDER" "$CALLER" >/dev/null; then
    pass "the builder and its caller satisfy the contract"
  else
    fail "the builder and its caller do not satisfy the contract"
  fi
}

test_sha_contract() {
  echo "explicit SHA contract"
  expect_builder_refused "a SHA the caller may omit" optional-sha
  expect_builder_refused "a SHA that is not a string" numeric-sha
  expect_builder_refused "no way to ask for the main proof" drop-require-main
  expect_builder_refused "the main proof demanded of development builds" require-main-by-default
  expect_builder_refused "an extra branch input beside the SHA" extra-input
  expect_builder_refused "checkouts of the caller's HEAD instead of the SHA" checkout-head
}

test_inventory_and_gate() {
  echo "inventory and the main gate"
  expect_builder_refused "an inventory job that may push to the registry" inventory-may-push
  expect_builder_refused "a second, hand-written image matrix" second-matrix
  expect_builder_refused "no reachable-from-main proof at all" drop-main-gate
  expect_builder_refused "the proof demanded of every build" always-main-gate
  expect_builder_refused "the proof taken before the repository exists" gate-before-checkout
  expect_builder_refused "an action pinned by tag" unpinned-checkout
  expect_builder_refused "a proof taken on a commit other than the one built" \
    gate-other-sha
  expect_builder_refused "a proof taken on a history too shallow to walk main" \
    shallow-gate-history
  expect_builder_refused "an inventory step that only echoes the script path" \
    echoed-inventory
}

# The regression a code review found: the checker validated the ref of every
# checkout it found, which said nothing at all when the build job had none.
test_build_checkout_is_mandatory() {
  echo "the build context is a checkout of the requested SHA"
  expect_builder_refused "a build job with no checkout at all" drop-build-checkout
  expect_builder_refused "a second checkout in the build job" duplicate-build-checkout
  expect_builder_refused "a checkout taken after the image is built" checkout-after-push
  expect_builder_refused "a checkout with no ref of its own" drop-checkout-ref
  expect_builder_refused "a checkout that keeps the token in the build context" \
    persist-credentials
}

test_build_and_digest() {
  echo "build identity, attestations and digests"
  expect_builder_refused "SBOM turned off" drop-sbom
  expect_builder_refused "provenance removed" drop-provenance
  expect_builder_refused "a mutable tag as the pushed identity" mutable-tag
  expect_builder_refused "a branch name as the pushed identity" tag-is-identity
  expect_builder_refused "a digest synthesised from the tag" synthesised-digest
  expect_builder_refused "a digest written without being validated" ungated-digest
  expect_builder_refused "no digest artifact" drop-digest-artifact
  expect_builder_refused "a duplicated digest artifact" duplicate-digest-artifact
}

# The regression a code review found: with the manifest inside the reusable
# builder, a manifest that could not be written failed the whole call and took
# the development deploy with it. They are siblings of the build now, and the
# manifest still has to name the SHA the builder reported building.
test_release_dag() {
  echo "the release DAG: manifest and deploy are siblings of the build"
  expect_builder_refused "a builder that does not report the SHA it built" drop-sha-output
  expect_builder_refused "a reported SHA that is not the one requested" stale-sha-output
  expect_caller_refused "a deploy held back by the release manifest" manifest-blocks-deploy
  expect_caller_refused "a manifest held back by the deploy" deploy-blocks-manifest
  expect_caller_refused "a manifest that does not wait for the build" detached-manifest
  expect_caller_refused "a release with no manifest at all" drop-manifest
  expect_caller_refused "a manifest sealing a different SHA than the build" \
    manifest-other-sha
}

test_caller_contract() {
  echo "the caller, and the development flow it must preserve"
  expect_caller_refused "a dispatch that skips the main proof" dispatch-without-proof
  expect_caller_refused "a dispatch that builds HEAD instead of the requested SHA" caller-builds-head
  expect_caller_refused "a deploy that no longer waits for the build" detached-deploy
  expect_caller_refused "a deploy that is no longer restricted to develop" unconditional-deploy
  expect_caller_refused "a deploy routed away from nchat-dev" deploy-elsewhere
  expect_caller_refused "pushes to main building images directly" build-main-pushes
}

# --- The reachable-from-main gate -------------------------------------------

git_quiet() {
  git -C "$REPO_DIR" -c user.email=ci@example.invalid -c user.name=CI "$@" >/dev/null 2>&1
}

commit_on() {
  local branch="$1" message="$2"
  git_quiet checkout -B "$branch"
  echo "$message" >"$REPO_DIR/$message"
  git_quiet add -A
  git_quiet commit -m "$message"
  git -C "$REPO_DIR" rev-parse HEAD
}

# A history where main and a side branch genuinely diverge, so "not reachable"
# is a property of the graph and not of a name.
make_repository() {
  REPO_DIR="$WORK_DIR/repo"
  mkdir -p "$REPO_DIR"
  git_quiet init
  MAIN_FIRST="$(commit_on main first)"
  MAIN_TIP="$(commit_on main second)"
  git_quiet checkout -B side "$MAIN_FIRST"
  SIDE_SHA="$(commit_on side third)"
  git_quiet checkout main
}

run_gate() {
  (
    cd "$REPO_DIR"
    NCHAT_RELEASE_MAIN_REF=refs/heads/main bash "$MAIN_GATE" "$@"
  )
}

expect_gate_accepts() {
  local name="$1" sha="$2"
  if run_gate "$sha" >/dev/null 2>&1; then
    pass "$name"
    return
  fi
  fail "$name: the gate refused a commit main can reach"
}

expect_gate_refuses() {
  local name="$1"
  shift
  if run_gate "$@" >/dev/null 2>&1; then
    fail "$name: the gate accepted it"
    return
  fi
  pass "$name"
}

test_main_gate() {
  echo "reachable-from-main gate"
  make_repository
  expect_gate_accepts "the tip of main" "$MAIN_TIP"
  expect_gate_accepts "an older commit main still reaches" "$MAIN_FIRST"
  expect_gate_refuses "a commit on a branch main never merged" "$SIDE_SHA"
  expect_gate_refuses "a SHA with 39 characters" "${MAIN_TIP:0:39}"
  expect_gate_refuses "an abbreviated SHA" "${MAIN_TIP:0:12}"
  expect_gate_refuses "an uppercase SHA" "${MAIN_TIP^^}"
  expect_gate_refuses "an empty SHA" ""
  expect_gate_refuses "no SHA at all"
  expect_gate_refuses "two SHAs at once" "$MAIN_TIP" "$MAIN_FIRST"
  expect_gate_refuses "a well-formed SHA no object matches" \
    0123456789abcdef0123456789abcdef01234567
}

test_main_gate_reports_the_reference() {
  local output
  echo "the gate names the reference it considered"
  output="$(run_gate "$MAIN_FIRST")"
  assert_equals "the verdict names the tip of main it was taken against" \
    "Release SHA $MAIN_FIRST is reachable from refs/heads/main (tip $MAIN_TIP)." "$output"
  if (cd "$REPO_DIR" && NCHAT_RELEASE_MAIN_REF=refs/heads/absent bash "$MAIN_GATE" "$MAIN_TIP" \
    >/dev/null 2>&1); then
    fail "an unresolvable main reference was accepted"
    return
  fi
  pass "an unresolvable main reference is refused"
}

main() {
  MUTATOR="$WORK_DIR/mutate.py"
  write_mutator
  test_workflows_satisfy_the_contract
  test_sha_contract
  test_inventory_and_gate
  test_build_checkout_is_mandatory
  test_build_and_digest
  test_release_dag
  test_caller_contract
  test_main_gate
  test_main_gate_reports_the_reference
  if [[ "$FAILURES" -ne 0 ]]; then
    echo "Image builder workflow tests failed: $FAILURES" >&2
    return 1
  fi
  echo "Image builder workflow tests passed."
}

main "$@"
