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


def effective_base_sha(base_sha: str, head_sha: str) -> str:
    """The commit to validate from: the pull request's base, or the adoption commit.

    The boundary moves only when both things are true at once:

      * the adoption commit is on this head's history, so the policy genuinely
        applies to the work being proposed; and
      * the base predates adoption, which is what makes the range retroactive.

    Everything else keeps PR_BASE_SHA, so an ordinary feature -> develop pull
    request is validated exactly as before: develop is long past adoption, so
    its base is already after the boundary and nothing changes.

    A head that does not contain the adoption commit keeps its base too. That is
    the conservative direction — more commits validated, not fewer — and it is
    the right answer for a branch cut before the policy that has not merged it.
    """
    if not is_ancestor(GOVERNANCE_ENFORCEMENT_SHA, head_sha):
        return base_sha
    if is_ancestor(GOVERNANCE_ENFORCEMENT_SHA, base_sha):
        return base_sha
    return GOVERNANCE_ENFORCEMENT_SHA


def pull_request_subjects(base_sha: str, head_sha: str) -> tuple[str, ...]:
    if not SHA_PATTERN.fullmatch(base_sha) or not SHA_PATTERN.fullmatch(head_sha):
        raise ValueError("Pull request base and head SHAs must be full lowercase SHAs")

    # The adoption commit itself is excluded by the exclusive range: it is the
    # boundary, not a commit this policy has to re-approve.
    range_base = effective_base_sha(base_sha, head_sha)

    result = subprocess.run(
        ["git", "log", "--no-merges", "--format=%s", f"{range_base}..{head_sha}"],
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
