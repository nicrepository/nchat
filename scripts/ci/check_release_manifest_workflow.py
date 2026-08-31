#!/usr/bin/env python3
"""The release-manifest job must be wired the way the manifest's guarantees assume.

release-manifest.sh gates its own inputs, but it cannot see how it is invoked,
and the wiring is half the guarantee: a manifest built from another run's
artifacts, produced before the digests were downloaded, or kept for seven days
satisfies every check inside the script and is still worthless. So does one
generated on a runner without the coreutils it calls (`find -printf`, `date -d`,
`sha256sum`), or in a workspace no checkout ever populated.

Every value is compared for equality against the parsed YAML, never by
substring: `needs: prebuild` contains "build", `run: echo release-manifest.sh`
contains the script's name, and neither is the contract.

Usage: check_release_manifest_workflow.py <workflow-file>
Exit code 0 when the contract holds; 1 with one short reason per violation on
stderr when it does not.
"""

from __future__ import annotations

import re
import sys

import yaml

JOB = "release-manifest"
NEEDS = {"build"}
PERMISSIONS = {"actions": "read", "contents": "read"}
# The generator is a bash script calling GNU coreutils; this is the runner the
# contract was written against, not a preference.
RUNNER = "ubuntu-latest"
PINNED = re.compile(r"[^@]+@[a-f0-9]{40}")
CHECKOUT_ACTION = "actions/checkout"
# The builder reports the commit it built; the manifest job has to name that
# one, not the caller's own ref, which a dispatch does not build.
CHECKOUT_REF = "${{ needs.build.outputs.sha }}"
DOWNLOAD_ACTION = "actions/download-artifact"
UPLOAD_ACTION = "actions/upload-artifact"
# Confining the download to pattern/path/merge-multiple is what keeps the job on
# this run's own artifacts: run-id or github-token would arrive as an unexpected
# input and be refused with them.
DOWNLOAD_INPUTS = {
    "pattern": "digest-*",
    "path": "release-digests",
    "merge-multiple": True,
}
UPLOAD_INPUTS = {
    "name": "release-manifest",
    "path": "release-manifest/release-manifest.json\n"
    "release-manifest/release-manifest.sha256\n",
    "retention-days": 90,
    "if-no-files-found": "error",
}
GENERATOR = "scripts/deploy/nchat-prod/release-manifest.sh"
GENERATOR_RUN = f"{GENERATOR} release-digests release-manifest"
GENERATOR_ENV = {
    "NCHAT_RELEASE_SOURCE_SHA": "${{ needs.build.outputs.sha }}",
    "NCHAT_RELEASE_RUN_ID": "${{ github.run_id }}",
}


def load_job(path):
    with open(path, encoding="utf-8") as handle:
        return yaml.safe_load(handle)["jobs"][JOB]


def steps_using(steps, action):
    """Steps running exactly `action`, at whatever SHA it is pinned to.

    Splitting on "@" is what stops a lookalike such as
    other/actions-download-artifact-wrapper from being taken for the real one.
    """
    return [s for s in steps if str(s.get("uses", "")).split("@")[0] == action]


def generation_steps(steps):
    """Every step naming the generator, so one that only echoes it is found."""
    return [s for s in steps if GENERATOR in str(s.get("run", ""))]


def sole_index(steps, selected):
    """Position of `selected`'s only member, or None when it is not exactly one."""
    if len(selected) != 1:
        return None
    return steps.index(selected[0])


def compare(label, actual, expected):
    problems = []
    for key, value in expected.items():
        if actual.get(key) != value:
            problems.append(f"{JOB} {label} {key} must be {value!r}, got {actual.get(key)!r}")
    unexpected = sorted(set(actual) - set(expected))
    if unexpected:
        problems.append(f"{JOB} {label} must not accept {unexpected}")
    return problems


def needs_of(job):
    """`needs: build` and `needs: [build]` are the same dependency."""
    needs = job.get("needs", [])
    if isinstance(needs, str):
        return {needs}
    return set(needs)


def check_job(job):
    problems = []
    if needs_of(job) != NEEDS:
        problems.append(f"{JOB} job must depend on exactly {sorted(NEEDS)}")
    if job.get("permissions") != PERMISSIONS:
        problems.append(f"{JOB} job must run with exactly {PERMISSIONS}")
    for step in job.get("steps", []):
        uses = str(step.get("uses", ""))
        if uses and not PINNED.fullmatch(uses):
            problems.append(f"{JOB} action is not pinned by a full SHA: {uses}")
    return problems


def check_runner(job):
    if job.get("runs-on") != RUNNER:
        return [f"{JOB} job must run on {RUNNER}, got {job.get('runs-on')!r}"]
    return []


def check_checkout(steps):
    """One checkout, of the commit the manifest claims the images were built from.

    A manifest generated from a differently-checked-out tree would name a source
    SHA whose scripts never ran, so the ref is part of the contract.
    """
    checkout = steps_using(steps, CHECKOUT_ACTION)
    if len(checkout) != 1:
        return [f"{JOB} job must contain exactly one {CHECKOUT_ACTION} step"]
    ref = checkout[0].get("with", {}).get("ref")
    if ref != CHECKOUT_REF:
        return [f"{JOB} checkout must use ref {CHECKOUT_REF!r}, got {ref!r}"]
    return []


def check_download(steps):
    download = steps_using(steps, DOWNLOAD_ACTION)
    if len(download) != 1:
        return [f"{JOB} job must use {DOWNLOAD_ACTION} exactly once"]
    return compare(DOWNLOAD_ACTION, download[0].get("with", {}), DOWNLOAD_INPUTS)


def check_upload(steps):
    upload = steps_using(steps, UPLOAD_ACTION)
    if len(upload) != 1:
        return [f"{JOB} job must use {UPLOAD_ACTION} exactly once"]
    return compare(UPLOAD_ACTION, upload[0].get("with", {}), UPLOAD_INPUTS)


def check_generation(steps):
    """The step must run the generator, not merely mention it."""
    generate = generation_steps(steps)
    if len(generate) != 1:
        return [f"{JOB} job must run {GENERATOR} in exactly one step"]
    problems = []
    run = str(generate[0].get("run", "")).strip()
    if run != GENERATOR_RUN:
        problems.append(f"{JOB} generation step must run {GENERATOR_RUN!r}, got {run!r}")
    return problems + compare("generation env", generate[0].get("env", {}), GENERATOR_ENV)


def check_order(steps):
    """Checkout, then download, then generate, then upload.

    A missing or repeated step is left to the check that owns it, so a job
    without a checkout reports that and not a confusing ordering failure.
    """
    order = [
        sole_index(steps, steps_using(steps, CHECKOUT_ACTION)),
        sole_index(steps, steps_using(steps, DOWNLOAD_ACTION)),
        sole_index(steps, generation_steps(steps)),
        sole_index(steps, steps_using(steps, UPLOAD_ACTION)),
    ]
    if None in order:
        return []
    if order != sorted(order):
        return [
            f"{JOB} steps are in an invalid order: checkout, download digests, "
            "generate manifest, upload manifest"
        ]
    return []


def run(path):
    try:
        job = load_job(path)
    except (OSError, KeyError, TypeError, yaml.YAMLError) as error:
        print(f"{JOB} job cannot be read from {path}: {error}", file=sys.stderr)
        return 1
    steps = job.get("steps", [])
    problems = (
        check_job(job)
        + check_runner(job)
        + check_checkout(steps)
        + check_download(steps)
        + check_generation(steps)
        + check_upload(steps)
        + check_order(steps)
    )
    for problem in problems:
        print(problem, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1]))
