#!/usr/bin/env python3

import importlib.util
import os
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch


SCRIPT_PATH = Path(__file__).with_name("git-conventions-check.py")
SPEC = importlib.util.spec_from_file_location("git_conventions_check", SCRIPT_PATH)
assert SPEC and SPEC.loader
git_conventions_check = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(git_conventions_check)


class GitConventionsCheckTest(unittest.TestCase):
    def test_accepts_documented_human_branch_names(self) -> None:
        for branch in (
            "feature/files-481-block-unapproved-download",
            "fix/chat-437-authenticated-user-sidebar",
            "chore/repo-520-git-governance",
            "security/auth-123-session-hardening",
            "hotfix/514-auth-session-bypass",
            "release/1.2.0",
            "release/v1.2.0",
            "release/1.2.0-rc.1",
            "release/rc1",
            "release/release_candidate-1",
        ):
            with self.subTest(branch=branch):
                self.assertTrue(git_conventions_check.is_valid_branch(branch))

    def test_rejects_invalid_human_branch_names(self) -> None:
        for branch in (
            "teste",
            "unknown/repo-520-git-governance",
            "Fix/chat-123-bug",
            "feature/chat-sem-id",
            "feature/chat-123-",
            "alvaro/teste",
            "release/",
            "release/...",
            "release/-1.2.0",
            "release/.1.2.0",
            "release/@@@",
            "release/1.2.0/test",
            "release/1 2 3",
        ):
            with self.subTest(branch=branch):
                self.assertFalse(git_conventions_check.is_valid_branch(branch))

    def test_accepts_documented_conventional_commit_subjects(self) -> None:
        for subject in (
            "feat: add presence heartbeat",
            "fix(files): strip API prefix",
            "docs(repo): document branch strategy",
            "test(chat): cover idle transition",
            "refactor(chat): simplify activity tracker",
            "chore(deps): bump dependency",
            "ci(repo): enforce git conventions",
            "build(repo): update build metadata",
            "security(auth): harden session validation",
            "perf(chat): reduce payload size",
            "feat(api)!: change attachment contract",
            "feat!: change public API",
        ):
            with self.subTest(subject=subject):
                self.assertTrue(git_conventions_check.is_valid_conventional_subject(subject))

    def test_rejects_invalid_conventional_commit_subjects(self) -> None:
        for subject in (
            "unknown(repo): add gate",
            "fix:",
            "fix add gate",
            "fix:\tadd gate",
            "Implementação da tarefa",
            "Fix(repo): uppercase type",
            "fix(repo): add $(id)",
            "fix(repo): add `id`",
            'fix(repo): add "; id; #',
            "fix(repo): add\nnewline",
        ):
            with self.subTest(subject=subject):
                self.assertFalse(git_conventions_check.is_valid_conventional_subject(subject))

    def test_reports_all_invalid_human_commit_subjects(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="alvaro-neto",
            branch="chore/repo-520-git-governance",
            title="ci(repo): enforce git conventions",
            subjects=("feat: add gate", "adjustments", "final"),
        )

        self.assertEqual(errors, ["Invalid commit subjects: adjustments; final"])

    def test_rejects_invalid_dependabot_title_but_exempts_its_branch(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="dependabot[bot]",
            branch="dependabot/go_modules/services/chat-service/example-1.2.3",
            title="Update dependency",
            subjects=("chore(deps): bump example",),
        )

        self.assertEqual(errors, ["Invalid pull request title: Update dependency"])

    def test_does_not_grant_dependabot_exception_to_humans(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="alvaro-neto",
            branch="dependabot/go_modules/services/chat-service/example-1.2.3",
            title="chore(deps): bump example",
            subjects=("chore(deps): bump example",),
        )

        self.assertEqual(
            errors,
            [
                "Invalid branch name: "
                "dependabot/go_modules/services/chat-service/example-1.2.3"
            ],
        )

    def test_validates_commit_subjects_for_dependabot(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="dependabot[bot]",
            branch="dependabot/go_modules/services/chat-service/example-1.2.3",
            title="chore(deps): bump example",
            subjects=("bump example",),
        )

        self.assertEqual(errors, ["Invalid commit subjects: bump example"])

    def test_rejects_shell_metacharacters_in_commit_subjects(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="alvaro-neto",
            branch="chore/repo-520-git-governance",
            title="ci(repo): enforce git conventions",
            subjects=('fix(repo): add "; id; #',),
        )

        self.assertEqual(errors, ['Invalid commit subjects: fix(repo): add "; id; #'])

    def test_rejects_non_dependabot_actor_on_dependabot_branch(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="dependabot-bot",
            branch="dependabot/go_modules/example-1.2.3",
            title="chore(deps): bump example",
            subjects=("chore(deps): bump example",),
        )

        self.assertEqual(
            errors,
            ["Invalid branch name: dependabot/go_modules/example-1.2.3"],
        )

    def test_skips_pull_request_validation_for_push_and_dispatch(self) -> None:
        for event_name in ("push", "workflow_dispatch"):
            with self.subTest(event_name=event_name):
                self.assertEqual(
                    git_conventions_check.validate_event(
                        event_name=event_name,
                        actor="alvaro-neto",
                        branch="invalid",
                        title="invalid",
                        subjects=("invalid",),
                    ),
                    [],
                )

    def test_validates_pull_request_context(self) -> None:
        self.assertEqual(
            git_conventions_check.validate_event(
                event_name="pull_request",
                actor="alvaro-neto",
                branch="invalid",
                title="invalid",
                subjects=("invalid",),
            ),
            [
                "Invalid branch name: invalid",
                "Invalid pull request title: invalid",
                "Invalid commit subjects: invalid",
            ],
        )

    def test_pull_request_subjects_reads_git_output(self) -> None:
        base_sha = "a" * 40
        head_sha = "b" * 40
        completed_process = subprocess.CompletedProcess(
            args=[], returncode=0, stdout="feat: add gate\nfix: fix gate\n\n"
        )

        with patch.object(
            git_conventions_check.subprocess, "run", return_value=completed_process
        ) as run:
            self.assertEqual(
                git_conventions_check.pull_request_subjects(base_sha, head_sha),
                ("feat: add gate", "fix: fix gate"),
            )

        run.assert_called_once_with(
            ["git", "log", "--format=%s", f"{base_sha}..{head_sha}"],
            check=True,
            capture_output=True,
            text=True,
        )

    def test_pull_request_subjects_rejects_invalid_base_sha(self) -> None:
        with self.assertRaisesRegex(ValueError, "full lowercase SHAs"):
            git_conventions_check.pull_request_subjects("invalid", "a" * 40)

    def test_pull_request_subjects_rejects_invalid_head_sha(self) -> None:
        with self.assertRaisesRegex(ValueError, "full lowercase SHAs"):
            git_conventions_check.pull_request_subjects("a" * 40, "B" * 40)

    def test_pull_request_subjects_propagates_git_failures(self) -> None:
        error = subprocess.CalledProcessError(128, ["git", "log"])

        with patch.object(git_conventions_check.subprocess, "run", side_effect=error):
            with self.assertRaises(subprocess.CalledProcessError):
                git_conventions_check.pull_request_subjects("a" * 40, "b" * 40)

    def test_pull_request_subjects_propagates_operational_errors(self) -> None:
        with patch.object(
            git_conventions_check.subprocess,
            "run",
            side_effect=OSError("git unavailable"),
        ):
            with self.assertRaisesRegex(OSError, "git unavailable"):
                git_conventions_check.pull_request_subjects("a" * 40, "b" * 40)

    def test_required_environment_rejects_missing_and_empty_values(self) -> None:
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, "MISSING"):
                git_conventions_check.required_environment("MISSING")

        with patch.dict(os.environ, {"EMPTY": ""}, clear=True):
            with self.assertRaisesRegex(ValueError, "EMPTY"):
                git_conventions_check.required_environment("EMPTY")

    def test_main_accepts_valid_pull_request(self) -> None:
        environment = {
            "GITHUB_EVENT_NAME": "pull_request",
            "PR_AUTHOR": "alvaro-neto",
            "PR_BASE_SHA": "a" * 40,
            "PR_HEAD_SHA": "b" * 40,
            "PR_HEAD_REF": "chore/repo-520-git-governance",
            "PR_TITLE": "ci(repo): enforce git conventions",
        }

        with patch.dict(os.environ, environment, clear=True), patch.object(
            git_conventions_check,
            "pull_request_subjects",
            return_value=("ci(repo): enforce git conventions",),
        ):
            self.assertEqual(git_conventions_check.main(), 0)

    def test_main_rejects_convention_violation(self) -> None:
        environment = {
            "GITHUB_EVENT_NAME": "pull_request",
            "PR_AUTHOR": "alvaro-neto",
            "PR_BASE_SHA": "a" * 40,
            "PR_HEAD_SHA": "b" * 40,
            "PR_HEAD_REF": "invalid",
            "PR_TITLE": "invalid",
        }

        with patch.dict(os.environ, environment, clear=True), patch.object(
            git_conventions_check, "pull_request_subjects", return_value=("invalid",)
        ):
            self.assertEqual(git_conventions_check.main(), 1)

    def test_main_returns_infrastructure_error(self) -> None:
        environment = {
            "GITHUB_EVENT_NAME": "pull_request",
            "PR_AUTHOR": "alvaro-neto",
            "PR_BASE_SHA": "a" * 40,
            "PR_HEAD_SHA": "b" * 40,
            "PR_HEAD_REF": "chore/repo-520-git-governance",
            "PR_TITLE": "ci(repo): enforce git conventions",
        }

        with patch.dict(os.environ, environment, clear=True), patch.object(
            git_conventions_check,
            "pull_request_subjects",
            side_effect=OSError("git unavailable"),
        ):
            self.assertEqual(git_conventions_check.main(), 2)


if __name__ == "__main__":
    unittest.main()
