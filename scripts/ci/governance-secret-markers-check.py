#!/usr/bin/env python3

from __future__ import annotations

import os
import re
import subprocess
import sys
from pathlib import Path

SENSITIVE_ASSIGNMENT_NAMES = (
    "pass" "word",
    "sec" "ret",
    "tok" "en",
    "private" "_key",
)

PRIVATE_KEY_HEADER = "BEGIN PRIVATE" " KEY"

assignment_names_pattern = "|".join(
    re.escape(name) for name in SENSITIVE_ASSIGNMENT_NAMES
)

ASSIGNMENT_PATTERN = re.compile(
    rf"(?:{assignment_names_pattern})="
    r"(?P<value>"
    r"\$\{\{\s*(?:secrets|vars|env)\.[A-Za-z_][A-Za-z0-9_]*\s*\}\}"
    r'|"[^"\r\n]*"'
    r"|'[^'\r\n]*'"
    r"|[^\s\r\n]*"
    r")"
)

SHELL_VARIABLE_PATTERN = re.compile(
    r"^\$\{?[A-Za-z_][A-Za-z0-9_]*\}?$"
)

GITHUB_EXPRESSION_PATTERN = re.compile(
    r"^\$\{\{\s*(?:secrets|vars|env)\.[A-Za-z_][A-Za-z0-9_]*\s*\}\}$"
)

PLACEHOLDER_PATTERN = re.compile(
    r"^(?:"
    r"REPLACE_ME(?:_[A-Z0-9_]+)?"
    r"|CHANGE_ME(?:_[A-Z0-9_]+)?"
    r"|EXAMPLE(?:_[A-Z0-9_]+)?"
    r"|<[^>\r\n]+>"
    r"|\*+"
    r")$"
)


def normalize_value(value: str) -> str:
    normalized = value.strip()

    # Shell line continuation.
    if normalized.endswith("\\"):
        normalized = normalized[:-1].rstrip()

    if (
        len(normalized) >= 2
        and normalized[0] == normalized[-1]
        and normalized[0] in {"'", '"'}
    ):
        normalized = normalized[1:-1].strip()

    return normalized


def is_safe_reference(value: str) -> bool:
    normalized = normalize_value(value)

    return bool(
        SHELL_VARIABLE_PATTERN.fullmatch(normalized)
        or GITHUB_EXPRESSION_PATTERN.fullmatch(normalized)
        or PLACEHOLDER_PATTERN.fullmatch(normalized)
    )


def inspect_line(line: str) -> list[str]:
    findings: list[str] = []

    if PRIVATE_KEY_HEADER in line:
        findings.append("private key PEM header")

    for match in ASSIGNMENT_PATTERN.finditer(line):
        if not is_safe_reference(match.group("value")):
            findings.append("literal secret-like assignment")

    return findings


def make_assignment(name: str, value: str) -> str:
    return f"{name}={value}"


def run_self_test() -> None:
    password_name, secret_name, token_name, private_key_name = (
        SENSITIVE_ASSIGNMENT_NAMES
    )

    safe_lines = (
        f'--set=migrator_{password_name}="$POSTGRES_MIGRATOR_PASSWORD" \\',
        f'--set=app_{password_name}="$POSTGRES_APP_PASSWORD" <<\'SQL\'',
        make_assignment(token_name, "${TOKEN}"),
        make_assignment(secret_name, '"${SECRET_VALUE}"'),
        make_assignment(private_key_name, "REPLACE_ME_PRIVATE_KEY"),
        make_assignment(password_name, "<generated-locally>"),
        make_assignment(token_name, "***"),
        make_assignment(token_name, "${{ secrets.GITHUB_TOKEN }}"),
    )

    unsafe_lines = (
        make_assignment(password_name, "hunter2"),
        make_assignment(secret_name, '"literal-value"'),
        make_assignment(token_name, "'abc123'"),
        make_assignment(private_key_name, "/tmp/private.pem"),
        f"-----{PRIVATE_KEY_HEADER}-----",
    )

    for line in safe_lines:
        if inspect_line(line):
            raise AssertionError(f"Safe fixture was rejected: {line!r}")

    for line in unsafe_lines:
        if not inspect_line(line):
            raise AssertionError(f"Unsafe fixture was accepted: {line!r}")


def tracked_files() -> list[Path]:
    output = subprocess.check_output(["git", "ls-files", "-z"])
    return [
        Path(os.fsdecode(raw_path))
        for raw_path in output.split(b"\0")
        if raw_path
    ]


def main() -> int:
    run_self_test()

    findings: list[tuple[Path, int, str]] = []

    for path in tracked_files():
        if not path.is_file():
            continue

        try:
            raw_content = path.read_bytes()
        except OSError as error:
            print(f"Unable to read tracked file {path}: {error}", file=sys.stderr)
            return 2

        # Ignore binary files.
        if b"\0" in raw_content:
            continue

        content = raw_content.decode("utf-8", errors="ignore")

        for line_number, line in enumerate(content.splitlines(), start=1):
            for finding in inspect_line(line):
                findings.append((path, line_number, finding))

    if findings:
        for path, line_number, finding in findings:
            # Do not print the line or possible credential value.
            print(f"{path}:{line_number}: {finding}", file=sys.stderr)

        print(
            "Obvious literal secret marker found in versioned files.",
            file=sys.stderr,
        )
        return 1

    print("Repository secret marker governance check passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
