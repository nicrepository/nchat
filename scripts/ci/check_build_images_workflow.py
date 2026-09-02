#!/usr/bin/env python3
"""The image builder must be callable only with a commit it can prove.

CICD-05 moved the eleven builds into one reusable workflow so that production
and development cannot drift apart. That only holds if the builder keeps the
properties the release depends on: a full SHA arrives as an explicit input, the
checkouts and the pushed tag all name that same SHA, the matrix comes from the
canonical inventory rather than a second hand-written list, attestations stay
on, each image records the digest its own push returned, and the registry write
lives in the one job that pushes.

The caller is half of the contract too: develop must keep building the commit it
pushed and deploying it exactly as before, and a manual dispatch must ask for
the reachable-from-main proof.

Every value is compared for equality against the parsed YAML, never by
substring, so a lookalike expression is a violation rather than a match.

Usage: check_build_images_workflow.py <builder-workflow> <caller-workflow>
Exit code 0 when the contract holds; 1 with one short reason per violation on
stderr when it does not.
"""

from __future__ import annotations

import pathlib
import re
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]
INVENTORY = ROOT / "scripts" / "deploy" / "nchat-dev" / "images.txt"

SHA = "${{ inputs.sha }}"
PINNED = re.compile(r"[^@]+@[a-f0-9]{40}")
CHECKOUT_ACTION = "actions/checkout"
UPLOAD_ACTION = "actions/upload-artifact"
BUILD_ACTION = "docker/build-push-action"

EXPECTED_IMAGES = {
    "admin-service",
    "admin-web",
    "auth-service",
    "chat-service",
    "document-converter",
    "file-service",
    "media-service",
    "migrations",
    "notification-service",
    "search-service",
    "web",
}

# The builder only builds. Sealing the release belongs to the caller, where it
# cannot hold back a deploy whose images already exist.
JOBS = {"inventory", "build"}
JOB_PERMISSIONS = {
    "inventory": {"contents": "read"},
    "build": {"actions": "read", "contents": "read", "packages": "write"},
}
BUILDER_SHA_OUTPUT = "${{ jobs.inventory.outputs.sha }}"
INVENTORY_SHA_OUTPUT = "${{ inputs.sha }}"
MATRIX_IMAGE = "${{ fromJSON(needs.inventory.outputs.images) }}"
IMAGES_OUTPUT = "${{ steps.images.outputs.images }}"
MATRIX_SCRIPT = "scripts/deploy/nchat-dev/image-matrix.sh"
# A commit is 40 lowercase hex characters or it is not a commit. Anything
# looser -- an abbreviation, a ref name -- would let the checkout resolve
# something nobody can name again afterwards.
SHA_FORMAT = "^[a-f0-9]{40}$"
SHA_FORMAT_ENV = {"RELEASE_SHA": SHA}
# The gate as the repository writes it, plus the only two variations bash treats
# identically: the braces and the quotes around the variable are optional. This
# is a contract checker for one controlled workflow, not a shell parser, so the
# accepted set stays this small and this auditable. The regex on the right must
# stay unquoted -- quoting it would make bash compare literal text instead of
# matching, which is the same no-op the pattern exists to refuse.
SHA_GATE = re.compile(
    r'\[\[\s+"?\$\{?RELEASE_SHA\}?"?\s+=~\s+\^\[a-f0-9\]\{40\}\$\s+\]\]'
)
MAIN_GATE_SCRIPT = "scripts/deploy/nchat-prod/require-main-sha.sh"
MAIN_GATE_CONDITION = "${{ inputs.require_main }}"
# The commit the gate proves has to be the commit the build then checks out.
# Anything else -- github.sha, a matrix value, another job's output -- would
# prove one commit and build a different one.
MAIN_GATE_ENV = {"RELEASE_SHA": SHA}
# `git merge-base --is-ancestor` walks history, so a production build fetches
# all of it. Development keeps the shallow clone: full history is only needed
# where the proof is demanded.
GATE_FETCH_DEPTH = "${{ inputs.require_main && '0' || '1' }}"
BUILD_INPUTS = {
    "push": True,
    "provenance": "mode=max",
    "sbom": True,
    "tags": "ghcr.io/nicrepository/nchat/${{ matrix.image }}:" + SHA,
}
DIGEST_SOURCE = "${{ steps.push.outputs.digest }}"
DIGEST_CONTRACT = "sha256:[a-f0-9]{64}"
DIGEST_ARTIFACT = {
    "name": "digest-${{ matrix.image }}",
    "path": "digest-${{ matrix.image }}.txt",
    "retention-days": 7,
}
MUTABLE_TAGS = ("latest", "main", "master", "prod", "stable")

BUILDER_REFERENCE = "./.github/workflows/build-nchat-images.yml"
DEPLOY_REFERENCE = "./.github/workflows/deploy-nchat-dev.yml"
CALLER_BUILD_WITH = {
    "sha": "${{ github.event_name == 'workflow_dispatch' && inputs.sha || github.sha }}",
    "require_main": "${{ github.event_name == 'workflow_dispatch' }}",
}
CALLER_DEPLOY_WITH = {"sha": "${{ github.sha }}"}
MANIFEST_JOB = "release-manifest"
# Both consumers of a build hang off the build alone: neither may wait for the
# other, or a manifest that cannot be written would stop a development deploy.
CONSUMER_NEEDS = {"build"}
MANIFEST_SHA = "${{ needs.build.outputs.sha }}"
MANIFEST_GENERATOR = "scripts/deploy/nchat-prod/release-manifest.sh"
CALLER_DEPLOY_IF = "github.event_name == 'push' && github.ref == 'refs/heads/develop'"
CALLER_PUSH_BRANCHES = ["develop"]


def load(path):
    with open(path, encoding="utf-8") as handle:
        return yaml.safe_load(handle)


def steps_using(steps, action):
    """Steps running exactly `action`, at whatever SHA it is pinned to."""
    return [s for s in steps if str(s.get("uses", "")).split("@")[0] == action]


def invokes(step, script):
    """True when the step actually runs `script`, not merely names it.

    The path has to be the first word of a command line: `echo <script>`,
    `printf '%s' <script>` and a comment all mention the path without ever
    running it, and each one would leave the contract unproven.
    """
    for line in str(step.get("run", "")).splitlines():
        command = line.strip()
        if command.startswith("#"):
            continue
        if command.split()[:1] == [script]:
            return True
    return False


def steps_running(steps, script):
    """Steps that actually invoke `script`."""
    return [s for s in steps if invokes(s, script)]


def compare(label, actual, expected):
    problems = []
    for key, value in expected.items():
        if actual.get(key) != value:
            problems.append(f"{label} {key} must be {value!r}, got {actual.get(key)!r}")
    return problems


def check_call_contract(workflow):
    """The builder takes a full SHA from its caller and nothing implicit."""
    call = (workflow.get(True) or workflow.get("on") or {}).get("workflow_call")
    if not isinstance(call, dict):
        return ["the builder must be a workflow_call workflow"]
    return check_declared_inputs(call.get("inputs") or {})


def check_declared_inputs(inputs):
    problems = []
    sha = inputs.get("sha") or {}
    if sha.get("required") is not True or sha.get("type") != "string":
        problems.append("input sha must be a required string")
    require_main = inputs.get("require_main") or {}
    if require_main.get("type") != "boolean" or require_main.get("default") is not False:
        problems.append("input require_main must be a boolean defaulting to false")
    unexpected = sorted(set(inputs) - {"sha", "require_main"})
    if unexpected:
        problems.append(f"the builder must not accept the inputs {unexpected}")
    return problems


def check_sha_output(workflow):
    """The caller can only name the commit this run validated and built."""
    call = (workflow.get(True) or workflow.get("on") or {}).get("workflow_call") or {}
    value = ((call.get("outputs") or {}).get("sha") or {}).get("value")
    problems = []
    if value != BUILDER_SHA_OUTPUT:
        problems.append(f"the builder must output sha as {BUILDER_SHA_OUTPUT!r}")
    return problems + check_inventory_sha_output(workflow)


def check_inventory_sha_output(workflow):
    inventory = (workflow.get("jobs") or {}).get("inventory") or {}
    if (inventory.get("outputs") or {}).get("sha") != INVENTORY_SHA_OUTPUT:
        return [f"inventory must output sha as {INVENTORY_SHA_OUTPUT!r}"]
    return []


def check_jobs(workflow):
    jobs = workflow.get("jobs") or {}
    problems = []
    if set(jobs) != JOBS:
        problems.append(f"the builder must define exactly the jobs {sorted(JOBS)}")
    for name, permissions in JOB_PERMISSIONS.items():
        job = jobs.get(name) or {}
        if job and job.get("permissions") != permissions:
            problems.append(f"{name} must run with exactly {permissions}")
    return problems


def check_pinning(workflow):
    """Every action is pinned by commit, so a moved tag cannot change a build."""
    problems = []
    for name, job in (workflow.get("jobs") or {}).items():
        for step in job.get("steps", []):
            uses = str(step.get("uses", ""))
            if uses and not PINNED.fullmatch(uses):
                problems.append(f"{name} action is not pinned by a full SHA: {uses}")
    return problems


def check_checkout_refs(workflow):
    """Every checkout names the requested SHA, never the caller's default ref."""
    problems = []
    for name, job in (workflow.get("jobs") or {}).items():
        for step in steps_using(job.get("steps", []), CHECKOUT_ACTION):
            ref = step.get("with", {}).get("ref")
            if ref != SHA:
                problems.append(f"{name} checkout must use ref {SHA!r}, got {ref!r}")
    return problems


def check_inventory_job(workflow):
    job = (workflow.get("jobs") or {}).get("inventory") or {}
    steps = job.get("steps", [])
    problems = []
    if (job.get("outputs") or {}).get("images") != IMAGES_OUTPUT:
        problems.append(f"inventory must output images as {IMAGES_OUTPUT!r}")
    matrix = steps_running(steps, MATRIX_SCRIPT)
    if len(matrix) != 1 or matrix[0].get("id") != "images":
        problems.append(f"inventory must run {MATRIX_SCRIPT} in exactly one step with id images")
    return problems + check_sha_validation(steps) + check_main_gate(steps)


def executable_lines(run):
    """The lines of a `run:` block bash would actually execute.

    Blank lines and whole-line comments are dropped, so a gate that has been
    commented out cannot go on satisfying the contract it no longer enforces.
    """
    for line in str(run).splitlines():
        stripped = line.strip()
        if stripped and not stripped.startswith("#"):
            yield stripped


def gates_sha(step):
    """True when the whole step is the gate: one line, and that line tests it.

    Containing the pattern is not the same as applying it, and neither is
    running it somewhere whose exit code nobody reads. A step that holds only
    this line has nowhere left to put an `if false`, a `|| true`, or a later
    command whose status replaces the gate's, so the step fails exactly when
    the commit is not a full SHA. The canonical workflow already writes it as
    that single line; anything richer is outside the contract.
    """
    lines = list(executable_lines(step.get("run", "")))
    return len(lines) == 1 and SHA_GATE.fullmatch(lines[0]) is not None


def check_sha_validation(steps):
    """The requested commit is proved well-formed before anything uses it.

    First step of the first job, so neither the checkout nor the main gate
    ever sees a value this workflow has not shown to be a full SHA.
    """
    validate = [s for s in steps if gates_sha(s)]
    if len(validate) != 1:
        return [f"inventory must test RELEASE_SHA against {SHA_FORMAT!r} in exactly one step"]
    problems = compare("SHA validation env", validate[0].get("env") or {}, SHA_FORMAT_ENV)
    if steps.index(validate[0]) != 0:
        problems.append("the requested SHA must be validated before any other inventory step")
    return problems


def check_main_gate(steps):
    """The reachable-from-main proof runs before anything is built."""
    gate = steps_running(steps, MAIN_GATE_SCRIPT)
    if len(gate) != 1:
        return [f"inventory must run {MAIN_GATE_SCRIPT} in exactly one step"]
    problems = []
    if str(gate[0].get("if", "")) != MAIN_GATE_CONDITION:
        problems.append(f"the main gate must be guarded by {MAIN_GATE_CONDITION!r}")
    problems += compare("main gate env", gate[0].get("env") or {}, MAIN_GATE_ENV)
    return problems + check_gate_checkout(steps, gate[0])


def check_gate_checkout(steps, gate):
    """The proof needs the repository, and enough history to walk main."""
    checkout = steps_using(steps, CHECKOUT_ACTION)
    if not checkout:
        return ["the main gate must run on a checked-out repository"]
    if steps.index(checkout[0]) > steps.index(gate):
        return ["the main gate must run on an already checked-out repository"]
    depth = (checkout[0].get("with") or {}).get("fetch-depth")
    if depth != GATE_FETCH_DEPTH:
        return [f"the main gate checkout must use fetch-depth {GATE_FETCH_DEPTH!r}, got {depth!r}"]
    return []


def check_build_job(workflow):
    job = (workflow.get("jobs") or {}).get("build") or {}
    steps = job.get("steps", [])
    problems = []
    if job.get("needs") not in ("inventory", ["inventory"]):
        problems.append("build must depend on inventory")
    strategy = job.get("strategy") or {}
    if (strategy.get("matrix") or {}).get("image") != MATRIX_IMAGE:
        problems.append(f"build must iterate the inventory matrix {MATRIX_IMAGE!r}")
    return problems + check_build_checkout(steps) + check_push_step(steps) + check_digest(steps)


def check_build_checkout(steps):
    """The build context is a checkout of the requested SHA, or it is nothing.

    Existence first: a build job with no checkout at all would build whatever
    the runner's workspace happened to hold, and validating "every checkout
    found" says nothing when none was found.
    """
    checkout = steps_using(steps, CHECKOUT_ACTION)
    if len(checkout) != 1:
        return [f"build must contain exactly one {CHECKOUT_ACTION} step, found {len(checkout)}"]
    return check_checkout_inputs(checkout[0].get("with") or {}) + check_checkout_order(
        steps, checkout[0]
    )


def check_checkout_inputs(inputs):
    problems = []
    if inputs.get("ref") != SHA:
        problems.append(f"build checkout must use ref {SHA!r}, got {inputs.get('ref')!r}")
    if inputs.get("persist-credentials") is not False:
        problems.append("build checkout must set persist-credentials: false")
    return problems


def check_checkout_order(steps, checkout):
    """A checkout after the push would have built the wrong tree."""
    push = steps_using(steps, BUILD_ACTION)
    if len(push) != 1:
        return []
    if steps.index(checkout) > steps.index(push[0]):
        return [f"build must check out the requested SHA before {BUILD_ACTION} runs"]
    return []


def check_push_step(steps):
    push = steps_using(steps, BUILD_ACTION)
    if len(push) != 1:
        return [f"build must use {BUILD_ACTION} exactly once"]
    if push[0].get("id") != "push":
        return ["the build step must be identified as push so its digest can be read"]
    inputs = push[0].get("with", {})
    problems = compare(BUILD_ACTION, inputs, BUILD_INPUTS)
    for tag in str(inputs.get("tags", "")).splitlines():
        if tag.rsplit(":", 1)[-1] in MUTABLE_TAGS:
            problems.append(f"a mutable tag must never identify a build: {tag}")
    return problems


def check_digest(steps):
    """The digest is read from this push and refused unless it is one."""
    record = [s for s in steps if (s.get("env") or {}).get("DIGEST") == DIGEST_SOURCE]
    problems = []
    if len(record) != 1:
        problems.append(f"build must record exactly one digest taken from {DIGEST_SOURCE!r}")
    elif DIGEST_CONTRACT not in str(record[0].get("run", "")):
        problems.append(f"the recorded digest must be gated on {DIGEST_CONTRACT!r}")
    upload = steps_using(steps, UPLOAD_ACTION)
    if len(upload) != 1:
        return problems + ["build must upload exactly one digest artifact"]
    return problems + compare("digest artifact", upload[0].get("with", {}), DIGEST_ARTIFACT)


def check_inventory_file(path):
    """Exactly the eleven images production is contracted to ship."""
    try:
        lines = [line.split() for line in path.read_text(encoding="utf-8").splitlines() if line]
    except OSError as error:
        return [f"the canonical inventory cannot be read: {error}"]
    images = [fields[1] for fields in lines if len(fields) == 3]
    if len(images) != len(lines) or sorted(images) != sorted(EXPECTED_IMAGES):
        return [f"the canonical inventory must list exactly {sorted(EXPECTED_IMAGES)}"]
    return []


def check_caller(caller):
    """develop keeps its build and its deploy; a dispatch asks for the proof."""
    jobs = caller.get("jobs") or {}
    build = jobs.get("build") or {}
    deploy = jobs.get("deploy") or {}
    problems = []
    if build.get("uses") != BUILDER_REFERENCE:
        problems.append(f"the caller build job must use {BUILDER_REFERENCE}")
    problems += compare("caller build with", build.get("with", {}), CALLER_BUILD_WITH)
    if deploy.get("uses") != DEPLOY_REFERENCE:
        problems.append(f"the caller deploy job must use {DEPLOY_REFERENCE}")
    if deploy.get("needs") not in ("build", ["build"]):
        problems.append("the caller deploy job must depend on build")
    if str(deploy.get("if", "")) != CALLER_DEPLOY_IF:
        problems.append(f"the caller deploy job must be guarded by {CALLER_DEPLOY_IF!r}")
    problems += compare("caller deploy with", deploy.get("with", {}), CALLER_DEPLOY_WITH)
    return problems + check_release_dag(jobs) + check_caller_triggers(caller)


def needs_of(job):
    """`needs: build` and `needs: [build]` are the same dependency."""
    needs = job.get("needs", [])
    return {needs} if isinstance(needs, str) else set(needs)


def check_release_dag(jobs):
    """Manifest and deploy are siblings of the build, never of each other."""
    manifest = jobs.get(MANIFEST_JOB) or {}
    problems = []
    if not manifest:
        return [f"the caller must seal the release in a {MANIFEST_JOB} job"]
    for name in (MANIFEST_JOB, "deploy"):
        if needs_of(jobs.get(name) or {}) != CONSUMER_NEEDS:
            problems.append(f"{name} must depend on exactly {sorted(CONSUMER_NEEDS)}")
    return problems + check_manifest_identity(manifest.get("steps", []))


def check_manifest_identity(steps):
    """The manifest names the SHA the builder reported, not the caller's ref."""
    generate = steps_running(steps, MANIFEST_GENERATOR)
    if len(generate) != 1:
        return [f"the caller must run {MANIFEST_GENERATOR} in exactly one step"]
    source = (generate[0].get("env") or {}).get("NCHAT_RELEASE_SOURCE_SHA")
    if source != MANIFEST_SHA:
        return [f"the manifest source SHA must be {MANIFEST_SHA!r}, got {source!r}"]
    return []


def check_caller_triggers(caller):
    triggers = caller.get(True) or caller.get("on") or {}
    problems = []
    if (triggers.get("push") or {}).get("branches") != CALLER_PUSH_BRANCHES:
        problems.append(f"the caller must build pushes to exactly {CALLER_PUSH_BRANCHES}")
    return problems + check_dispatch_input(triggers)


def check_dispatch_input(triggers):
    """A production build is asked for by SHA, never by whatever ref dispatched it."""
    dispatch = triggers.get("workflow_dispatch") or {}
    sha = (dispatch.get("inputs") or {}).get("sha") or {}
    if sha.get("required") is not True or sha.get("type") != "string":
        return ["the dispatch trigger must take a required string sha"]
    return []


def run(builder_path, caller_path):
    try:
        builder = load(builder_path)
        caller = load(caller_path)
    except (OSError, yaml.YAMLError) as error:
        print(f"the image workflows cannot be read: {error}", file=sys.stderr)
        return 1
    problems = (
        check_call_contract(builder)
        + check_sha_output(builder)
        + check_jobs(builder)
        + check_pinning(builder)
        + check_checkout_refs(builder)
        + check_inventory_job(builder)
        + check_build_job(builder)
        + check_inventory_file(INVENTORY)
        + check_caller(caller)
    )
    for problem in problems:
        print(problem, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1], sys.argv[2]))
