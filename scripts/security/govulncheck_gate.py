#!/usr/bin/env python3
"""Decide whether govulncheck's findings are acceptable.

govulncheck's own exit code cannot express "this one advisory is accepted, and
only until it is fixed upstream", so the pass/fail decision lives here instead.
The gate is deliberately narrower than the tool's: it fails on every *called*
vulnerability whose id is not listed in .govulncheckignore.yaml, and it fails
on a listed id whose exception has expired, so an accepted advisory cannot be
inherited indefinitely by whoever comes next.

Only symbol-level findings count. That is the same set govulncheck reports as
"your code is affected by": a vulnerability in a module we require but never
call is not something this gate can act on.
"""

from __future__ import annotations

import datetime
import json
import pathlib
import sys

import yaml


def decode_stream(raw: str) -> list[dict]:
    """govulncheck -format json emits concatenated objects, not one document."""
    decoder = json.JSONDecoder()
    messages: list[dict] = []
    index = 0
    while index < len(raw):
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index >= len(raw):
            break
        message, index = decoder.raw_decode(raw, index)
        messages.append(message)
    return messages


def called_advisories(messages: list[dict]) -> set[str]:
    """Advisory ids with a trace that reaches one of our own call sites."""
    called = set()
    for message in messages:
        finding = message.get("finding")
        if not finding:
            continue
        if any("function" in frame for frame in finding.get("trace") or []):
            called.add(finding["osv"])
    return called


def entry_problem(entry: dict, today: datetime.date) -> str | None:
    """Why this exception cannot be used, or None when it stands."""
    identifier = entry.get("id")
    expiry = entry.get("expired_at")
    if not identifier or not entry.get("statement") or not entry.get("tracking"):
        return f"{identifier or '<no id>'}: needs id, statement and tracking"
    if not isinstance(expiry, datetime.date):
        return f"{identifier}: expired_at must be a date"
    if expiry < today:
        return f"{identifier}: exception expired on {expiry}"
    return None


def load_exceptions(path: pathlib.Path, today: datetime.date) -> tuple[set[str], list[str]]:
    """Accepted ids, plus the complaints about entries that can no longer be used."""
    if not path.exists():
        return set(), []
    document = yaml.safe_load(path.read_text()) or {}
    accepted, problems = set(), []
    for entry in document.get("vulnerabilities") or []:
        problem = entry_problem(entry, today)
        if problem:
            problems.append(problem)
        else:
            accepted.add(entry["id"])
    return accepted, problems


def report(unaccepted: set[str], problems: list[str], accepted_hits: set[str]) -> int:
    for advisory in sorted(accepted_hits):
        print(f"govulncheck: {advisory} accepted by .govulncheckignore.yaml")
    for problem in problems:
        print(f"govulncheck gate: {problem}", file=sys.stderr)
    for advisory in sorted(unaccepted):
        print(f"govulncheck gate: {advisory} is called and not accepted", file=sys.stderr)
    if unaccepted or problems:
        print("Go vulnerability gate failed.", file=sys.stderr)
        return 1
    print("Go vulnerability gate passed.")
    return 0


def main(argv: list[str]) -> int:
    if len(argv) < 2:
        print("usage: govulncheck_gate.py REPORT...", file=sys.stderr)
        return 2
    root = pathlib.Path(__file__).resolve().parents[2]
    accepted, problems = load_exceptions(root / ".govulncheckignore.yaml", datetime.date.today())

    called: set[str] = set()
    for report_path in argv[1:]:
        called |= called_advisories(decode_stream(pathlib.Path(report_path).read_text()))

    return report(called - accepted, problems, called & accepted)


if __name__ == "__main__":
    sys.exit(main(sys.argv))
