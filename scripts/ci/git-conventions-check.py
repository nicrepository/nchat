#!/usr/bin/env python3

from __future__ import annotations

import os
import re
import subprocess
import sys


HUMAN_BRANCH_PATTERN = re.compile(
    r"(?:feature|fix|chore|security)/[a-z0-9]+-[0-9]+-[a-z0-9]+(?:-[a-z0-9]+)*"
    r"|hotfix/[0-9]+-[a-z0-9]+(?:-[a-z0-9]+)*"
    r"|release/[A-Za-z0-9][A-Za-z0-9._-]*"
)
CONVENTIONAL_SUBJECT_PATTERN = re.compile(
    r"(?:feat|fix|docs|test|refactor|chore|ci|build|security|perf)"
    r"(?:\([a-z0-9][a-z0-9._/-]*\))?!?: "
    r"[^\s$`\"';#&|<>\\](?:[^\r\n$`\"';#&|<>\\]*[^\s$`\"';#&|<>\\])?"
    r"(?: \(#[1-9][0-9]*\))?"
)
SHA_PATTERN = re.compile(r"[0-9a-f]{40}")
DEPENDABOT_ACTOR = "dependabot[bot]"

# The commit that introduced this policy: ci(repo): enforce git contribution
# conventions (#522). It is the point of adoption, so it is also the boundary
# the policy is enforced from.
#
# Without a boundary the check is retroactive by accident. A release pull
# request promotes an old main to a much newer head, so PR_BASE_SHA..PR_HEAD_SHA
# contains every commit written before the policy existed — commits nobody can
# fix without rewriting history, and which no contributor is responsible for.
# Validating them turns a release into an unpassable gate while proving nothing
# about the work being released.
#
# The boundary is ancestry, never a date: timestamps can be forged, rewritten by
# a rebase, or simply out of order, whereas "is this commit reachable from that
# one" is a fact about the graph.
GOVERNANCE_ENFORCEMENT_SHA = "3bc74ea6c72373602024a5d9a99971a7a06b5b40"


def is_valid_branch(branch: str) -> bool:
    return bool(HUMAN_BRANCH_PATTERN.fullmatch(branch))


def is_valid_conventional_subject(subject: str) -> bool:
    return bool(CONVENTIONAL_SUBJECT_PATTERN.fullmatch(subject))


def validate_pull_request(
    *, actor: str, branch: str, title: str, subjects: tuple[str, ...]
) -> list[str]:
    errors: list[str] = []

    if actor != DEPENDABOT_ACTOR and not is_valid_branch(branch):
        errors.append(f"Invalid branch name: {branch}")
    if not is_valid_conventional_subject(title):
        errors.append(f"Invalid pull request title: {title}")

    invalid_subjects = [
        subject for subject in subjects if not is_valid_conventional_subject(subject)
    ]
    if invalid_subjects:
        errors.append(f"Invalid commit subjects: {'; '.join(invalid_subjects)}")

    return errors


def validate_event(
    *, event_name: str, actor: str, branch: str, title: str, subjects: tuple[str, ...]
) -> list[str]:
    if event_name != "pull_request":
        return []
    return validate_pull_request(
        actor=actor, branch=branch, title=title, subjects=subjects
    )


def required_environment(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        raise ValueError(f"Missing required environment variable: {name}")
    return value


def is_ancestor(ancestor_sha: str, descendant_sha: str) -> bool:
    """Whether ancestor_sha is reachable from descendant_sha.

    git merge-base --is-ancestor answers with its exit status: 0 yes, 1 no, and
    anything above that is a real failure — an unknown revision, a corrupt or
    shallow object store, git missing entirely. Only 0 and 1 are answers; the
    rest is raised so an infrastructure problem can never be mistaken for
    "this commit is not covered by the policy" and quietly widen what passes.
    """
    result = subprocess.run(
        ["git", "merge-base", "--is-ancestor", ancestor_sha, descendant_sha],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode == 0:
        return True
    if result.returncode == 1:
        return False
    raise subprocess.CalledProcessError(
        result.returncode, result.args, result.stdout, result.stderr
    )


def subject_revision_arguments(base_sha: str, head_sha: str) -> list[str]:
    """The revision arguments selecting exactly the commits this pull request adds.

    Three sets, never two:

        validated = ancestors(head) - ancestors(base) - ancestors(adoption)

    The base exclusion is not optional and never substitutable. A commit already
    reachable from the base is, by definition, not something this pull request
    proposes -- it is already on the branch being merged into, and the author has
    no way to change it. Dropping that exclusion is what let a legacy subject
    reappear when `main` was merged into a release branch: the merge pulled
    main's lineage into the head, and a commit sitting on that lineage is not an
    ancestor of the adoption commit, so an adoption-only range let it back in.

    The adoption exclusion is a *second* filter layered on top, for the case the
    base itself predates the policy -- a release pull request promoting an old
    `main`. It removes the pre-policy history the base does not already cover.

    Expressed as `head ^base ^adoption` rather than a single `X..head`, because
    a single two-dot range can only ever subtract one set. `head ^base` is
    exactly `base..head`, so a head without the adoption commit is validated
    precisely as it was before.
    """
    revisions = [head_sha, f"^{base_sha}"]
    if is_ancestor(GOVERNANCE_ENFORCEMENT_SHA, head_sha):
        revisions.append(f"^{GOVERNANCE_ENFORCEMENT_SHA}")
    return revisions


def pull_request_subjects(base_sha: str, head_sha: str) -> tuple[str, ...]:
    if not SHA_PATTERN.fullmatch(base_sha) or not SHA_PATTERN.fullmatch(head_sha):
        raise ValueError("Pull request base and head SHAs must be full lowercase SHAs")

    # Both the base and the adoption commit are excluded by `^`: each is a
    # boundary, not a commit this policy has to re-approve.
    result = subprocess.run(
        [
            "git",
            "log",
            "--no-merges",
            "--format=%s",
            *subject_revision_arguments(base_sha, head_sha),
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    return tuple(subject for subject in result.stdout.splitlines() if subject)


def main() -> int:
    event_name = os.environ.get("GITHUB_EVENT_NAME", "")
    if event_name != "pull_request":
        print("Git conventions check skipped outside pull_request events.")
        return 0

    try:
        base_sha = required_environment("PR_BASE_SHA")
        head_sha = required_environment("PR_HEAD_SHA")
        errors = validate_event(
            event_name=event_name,
            actor=required_environment("PR_AUTHOR"),
            branch=required_environment("PR_HEAD_REF"),
            title=required_environment("PR_TITLE"),
            subjects=pull_request_subjects(base_sha, head_sha),
        )
    except (OSError, subprocess.CalledProcessError, ValueError) as error:
        print(f"Unable to validate Git conventions: {error}", file=sys.stderr)
        return 2

    if errors:
        print("Git conventions check failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print("Git conventions check passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
