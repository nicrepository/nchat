#!/usr/bin/env python3

import importlib.util
import contextlib
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


ENFORCEMENT = git_conventions_check.GOVERNANCE_ENFORCEMENT_SHA


def log_call(run):
    """The argv of the single `git log` call, from a fake_git recorder."""
    for call in run.call_args_list:
        argv = call.args[0]
        if argv[:2] == ["git", "log"]:
            return argv
    raise AssertionError(f"git log was never called: {run.call_args_list}")


@contextlib.contextmanager
def fake_git(*, ancestry: dict, log_stdout: str = "", log_returncode: int = 0):
    """A git that answers merge-base from `ancestry` and log from `log_stdout`.

    ancestry maps (ancestor, descendant) -> bool, and is consulted by exit
    status the way the real command reports it: 0 for yes, 1 for no. A pair the
    test did not describe raises, so a test cannot pass by accident on a call it
    never thought about.
    """

    def run(argv, **kwargs):
        if argv[:3] == ["git", "merge-base", "--is-ancestor"]:
            key = (argv[3], argv[4])
            if key not in ancestry:
                raise AssertionError(f"unexpected ancestry query: {key}")
            return subprocess.CompletedProcess(
                args=argv, returncode=0 if ancestry[key] else 1, stdout="", stderr=""
            )
        if argv[:2] == ["git", "log"]:
            return subprocess.CompletedProcess(
                args=argv, returncode=log_returncode, stdout=log_stdout
            )
        raise AssertionError(f"unexpected git invocation: {argv}")

    with patch.object(git_conventions_check.subprocess, "run", side_effect=run) as mock:
        yield mock


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

    def test_pull_request_subjects_uses_git_to_exclude_merge_commits(self) -> None:
        """A modern pull request: base is already past adoption, so it is used."""
        base_sha = "a" * 40
        head_sha = "b" * 40

        with fake_git(
            ancestry={(ENFORCEMENT, head_sha): True, (ENFORCEMENT, base_sha): True},
            log_stdout="feat: add gate\nfix: fix gate\n\n",
        ) as run:
            self.assertEqual(
                git_conventions_check.pull_request_subjects(base_sha, head_sha),
                ("feat: add gate", "fix: fix gate"),
            )

        self.assertEqual(
            log_call(run),
            ["git", "log", "--no-merges", "--format=%s", f"{base_sha}..{head_sha}"],
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


class EnforcementBoundaryTests(unittest.TestCase):
    """The policy applies from its adoption commit forward, by ancestry.

    Requirement 4: a release pull request promotes an old base to a new head, so
    the raw range contains pre-policy history nobody can fix without rewriting
    it. Requirement 5: an ordinary feature -> develop pull request must keep
    validating every commit it introduces.
    """

    BASE = "a" * 40
    HEAD = "b" * 40

    def test_release_shaped_range_starts_at_the_adoption_commit(self) -> None:
        # Base predates adoption, head contains it: the retroactive case.
        with fake_git(
            ancestry={(ENFORCEMENT, self.HEAD): True, (ENFORCEMENT, self.BASE): False},
            log_stdout="feat(x): after adoption\n",
        ) as run:
            subjects = git_conventions_check.pull_request_subjects(self.BASE, self.HEAD)

        self.assertEqual(subjects, ("feat(x): after adoption",))
        # The range is asked of git as adoption..head, so pre-adoption commits
        # are never returned in the first place.
        self.assertEqual(
            log_call(run),
            ["git", "log", "--no-merges", "--format=%s", f"{ENFORCEMENT}..{self.HEAD}"],
        )

    def test_a_base_already_past_adoption_is_used_unchanged(self) -> None:
        with fake_git(
            ancestry={(ENFORCEMENT, self.HEAD): True, (ENFORCEMENT, self.BASE): True},
            log_stdout="feat(x): normal pull request\n",
        ) as run:
            git_conventions_check.pull_request_subjects(self.BASE, self.HEAD)

        self.assertEqual(
            log_call(run),
            ["git", "log", "--no-merges", "--format=%s", f"{self.BASE}..{self.HEAD}"],
        )

    def test_a_head_without_the_adoption_commit_keeps_its_base(self) -> None:
        """Conservative: validate more, never fewer, when the boundary cannot apply."""
        with fake_git(
            ancestry={(ENFORCEMENT, self.HEAD): False},
            log_stdout="feat(x): branch cut before adoption\n",
        ) as run:
            git_conventions_check.pull_request_subjects(self.BASE, self.HEAD)

        self.assertEqual(
            log_call(run),
            ["git", "log", "--no-merges", "--format=%s", f"{self.BASE}..{self.HEAD}"],
        )

    def test_a_commit_after_adoption_still_fails(self) -> None:
        """The boundary moves the range; it never softens the rule inside it."""
        errors = git_conventions_check.validate_pull_request(
            actor="alvaro-neto",
            branch="release/0.1.0",
            title="chore(release): NChat 0.1.0",
            subjects=("Feature/chat 17 agrupa categorias sidebar",),
        )
        self.assertEqual(
            errors,
            ["Invalid commit subjects: Feature/chat 17 agrupa categorias sidebar"],
        )

    def test_a_commit_before_adoption_never_reaches_validation(self) -> None:
        """A historical subject fails the pattern, so it must be excluded upstream."""
        legacy = "[TASK-79] Implementar deleção com placeholder"
        self.assertFalse(git_conventions_check.is_valid_conventional_subject(legacy))

        with fake_git(
            ancestry={(ENFORCEMENT, self.HEAD): True, (ENFORCEMENT, self.BASE): False},
            # git is asked for adoption..head, so it never reports the legacy commit.
            log_stdout="feat(x): after adoption\n",
        ):
            subjects = git_conventions_check.pull_request_subjects(self.BASE, self.HEAD)

        self.assertNotIn(legacy, subjects)
        self.assertEqual(
            git_conventions_check.validate_pull_request(
                actor="alvaro-neto",
                branch="release/0.1.0",
                title="chore(release): NChat 0.1.0",
                subjects=subjects,
            ),
            [],
        )

    def test_an_invalid_branch_still_fails_on_a_release_shaped_range(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="alvaro-neto",
            branch="Release/0.1.0",
            title="chore(release): NChat 0.1.0",
            subjects=(),
        )
        self.assertEqual(errors, ["Invalid branch name: Release/0.1.0"])

    def test_an_invalid_title_still_fails_on_a_release_shaped_range(self) -> None:
        errors = git_conventions_check.validate_pull_request(
            actor="alvaro-neto",
            branch="release/0.1.0",
            title="Release 0.1.0",
            subjects=(),
        )
        self.assertEqual(errors, ["Invalid pull request title: Release 0.1.0"])

    def test_merge_commits_stay_excluded_after_the_boundary_moves(self) -> None:
        with fake_git(
            ancestry={(ENFORCEMENT, self.HEAD): True, (ENFORCEMENT, self.BASE): False},
            log_stdout="feat(x): after adoption\n",
        ) as run:
            git_conventions_check.pull_request_subjects(self.BASE, self.HEAD)

        self.assertIn("--no-merges", log_call(run))

    def test_dependabot_rules_are_unchanged_by_the_boundary(self) -> None:
        self.assertEqual(
            git_conventions_check.validate_pull_request(
                actor="dependabot[bot]",
                branch="dependabot/go_modules/example-1.2.3",
                title="chore(deps): bump example",
                subjects=("chore(deps): bump example",),
            ),
            [],
        )


class AncestryProbeTests(unittest.TestCase):
    """git merge-base --is-ancestor answers by exit status; only 0 and 1 answer."""

    def probe(self, returncode: int):
        completed = subprocess.CompletedProcess(
            args=["git", "merge-base"], returncode=returncode, stdout="", stderr="boom"
        )
        with patch.object(
            git_conventions_check.subprocess, "run", return_value=completed
        ):
            return git_conventions_check.is_ancestor("a" * 40, "b" * 40)

    def test_zero_means_ancestor(self) -> None:
        self.assertTrue(self.probe(0))

    def test_one_means_not_an_ancestor(self) -> None:
        self.assertFalse(self.probe(1))

    def test_any_other_status_is_an_operational_error(self) -> None:
        """A broken graph must never be read as 'not covered by the policy'."""
        for returncode in (2, 128, 129):
            with self.subTest(returncode=returncode):
                with self.assertRaises(subprocess.CalledProcessError):
                    self.probe(returncode)

    def test_a_merge_base_failure_stops_the_whole_check(self) -> None:
        completed = subprocess.CompletedProcess(
            args=["git", "merge-base"], returncode=128, stdout="", stderr="bad object"
        )
        with patch.object(
            git_conventions_check.subprocess, "run", return_value=completed
        ):
            with self.assertRaises(subprocess.CalledProcessError):
                git_conventions_check.pull_request_subjects("a" * 40, "b" * 40)

    def test_main_reports_a_merge_base_failure_as_infrastructure(self) -> None:
        """Exit 2, not 0: an unusable graph is never a pass."""
        environment = {
            "GITHUB_EVENT_NAME": "pull_request",
            "PR_AUTHOR": "alvaro-neto",
            "PR_HEAD_REF": "release/0.1.0",
            "PR_TITLE": "chore(release): NChat 0.1.0",
            "PR_BASE_SHA": "a" * 40,
            "PR_HEAD_SHA": "b" * 40,
        }
        completed = subprocess.CompletedProcess(
            args=["git", "merge-base"], returncode=128, stdout="", stderr="bad object"
        )
        with patch.dict(os.environ, environment, clear=True), patch.object(
            git_conventions_check.subprocess, "run", return_value=completed
        ):
            self.assertEqual(git_conventions_check.main(), 2)

    def test_invalid_shas_are_rejected_before_any_git_runs(self) -> None:
        with patch.object(git_conventions_check.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "full lowercase SHAs"):
                git_conventions_check.pull_request_subjects("invalid", "b" * 40)
            with self.assertRaisesRegex(ValueError, "full lowercase SHAs"):
                git_conventions_check.pull_request_subjects("a" * 40, "B" * 40)
        run.assert_not_called()
