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
)
SHA_PATTERN = re.compile(r"[0-9a-f]{40}")
DEPENDABOT_ACTOR = "dependabot[bot]"


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


def pull_request_subjects(base_sha: str, head_sha: str) -> tuple[str, ...]:
    if not SHA_PATTERN.fullmatch(base_sha) or not SHA_PATTERN.fullmatch(head_sha):
        raise ValueError("Pull request base and head SHAs must be full lowercase SHAs")

    result = subprocess.run(
        ["git", "log", "--no-merges", "--format=%s", f"{base_sha}..{head_sha}"],
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
