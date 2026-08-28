#!/usr/bin/env python3
"""Every local `replace` target a Dockerfile's module needs must be copied into it.

A Go module that resolves a dependency through

    replace github.com/nicrepository/nchat/libs/go/platform => ../../libs/go/platform

is declaring a build input that lives outside its own directory. A developer
never notices: their checkout always has the directory. A container build only
has what COPY put there, so a Dockerfile that copies the service and not the
replace target fails with

    replacement directory ../../libs/go/platform does not exist

which is what broke the document-converter image (PR #692) and blocked the
nchat-dev deploy after it merged.

This derives the requirement from go.mod rather than from a list kept here: add
a replace and this demands the COPY, remove the COPY and this fails. A grep for
"libs/go" would pass the day someone adds a second shared module.

CI-safe: reads files, builds nothing, needs no Docker and no network.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# "COPY [--flags] src... dest" — the sources are every argument but the last.
COPY_PATTERN = re.compile(r"^\s*COPY\s+(?P<rest>.+)$", re.IGNORECASE)
FROM_PATTERN = re.compile(r"^\s*FROM\s", re.IGNORECASE)
# A replace whose right-hand side is a filesystem path, not a module version.
REPLACE_PATTERN = re.compile(
    r"^\s*replace\s+(?P<module>\S+)\s+=>\s+(?P<path>\.[^\s]*)\s*$"
)
# The same, inside a `replace (...)` block.
REPLACE_BLOCK_ENTRY = re.compile(r"^\s*(?P<module>\S+)\s+=>\s+(?P<path>\.[^\s]*)\s*$")


def local_replacements(go_mod: Path) -> list[tuple[str, Path]]:
    """Every (module, repo-relative target) this go.mod replaces with a local path."""
    found: list[tuple[str, Path]] = []
    in_block = False
    for line in go_mod.read_text().splitlines():
        stripped = line.strip()
        if stripped.startswith("replace ("):
            in_block = True
            continue
        if in_block:
            if stripped == ")":
                in_block = False
                continue
            match = REPLACE_BLOCK_ENTRY.match(line)
        else:
            match = REPLACE_PATTERN.match(line)
        if not match:
            continue
        # Relative to the module's own directory, as the go tool resolves it.
        target = (go_mod.parent / match.group("path")).resolve()
        found.append((match.group("module"), target.relative_to(ROOT)))
    return found


def build_stage_sources(dockerfile: Path) -> list[str]:
    """COPY sources in the FIRST stage, which is where the go build runs.

    Only the first stage matters: a runtime stage copying the compiled binary
    out of the build stage cannot satisfy a module input.
    """
    sources: list[str] = []
    seen_first_from = False
    for line in dockerfile.read_text().splitlines():
        if FROM_PATTERN.match(line):
            if seen_first_from:
                break
            seen_first_from = True
            continue
        match = COPY_PATTERN.match(line)
        if not match:
            continue
        arguments = [
            argument
            for argument in match.group("rest").split()
            if not argument.startswith("--")
        ]
        # The last argument is the destination.
        sources.extend(arguments[:-1])
    return sources


def copy_covers(sources: list[str], target: Path) -> bool:
    """Whether some COPY source is the target or an ancestor directory of it.

    `COPY libs/go ./libs/go` covers libs/go/platform, which is what makes the
    directory-level copy the robust form: the next shared module is covered the
    day it is added.
    """
    wanted = target.as_posix()
    for source in sources:
        source = source.lstrip("./").rstrip("/")
        if not source:
            continue
        if wanted == source or wanted.startswith(source + "/"):
            return True
    return False


def modules_built_by(dockerfile: Path, go_services: list[str]) -> list[Path]:
    """The service modules whose source this Dockerfile copies.

    A Dockerfile that templates the service name (Dockerfile.service's
    `services/${SERVICE}`) builds any of them, so it must satisfy all of them.
    """
    text = dockerfile.read_text()
    if "services/${SERVICE}" in text or "services/$SERVICE" in text:
        return [ROOT / "services" / name / "go.mod" for name in go_services]
    modules = []
    for name in go_services:
        if re.search(rf"COPY\s+[^\n]*services/{re.escape(name)}\b", text):
            modules.append(ROOT / "services" / name / "go.mod")
    return modules


def go_service_names() -> list[str]:
    """The Go services the image inventory declares, as the build matrix reads it."""
    inventory = ROOT / "scripts/deploy/nchat-dev/images.txt"
    names = []
    for line in inventory.read_text().splitlines():
        fields = line.split()
        if len(fields) == 3 and fields[0] == "go":
            names.append(fields[1])
    return names


def main() -> int:
    failures = 0
    checked = 0
    go_services = go_service_names()
    if not go_services:
        print("  [FAIL] no Go services found in scripts/deploy/nchat-dev/images.txt", file=sys.stderr)
        return 1

    for dockerfile in sorted(ROOT.glob("Dockerfile*")):
        modules = modules_built_by(dockerfile, go_services)
        if not modules:
            continue
        sources = build_stage_sources(dockerfile)
        for go_mod in modules:
            if not go_mod.exists():
                continue
            for module, target in local_replacements(go_mod):
                checked += 1
                if copy_covers(sources, target):
                    continue
                failures += 1
                print(
                    f"  [FAIL] {dockerfile.name}: builds {go_mod.parent.name}, which replaces\n"
                    f"         {module}\n"
                    f"         with the local path {target}, but the build stage never copies it.\n"
                    f"         Add: COPY {target.parent.as_posix()} ./{target.parent.as_posix()}",
                    file=sys.stderr,
                )

    if failures:
        print(
            f"\nDockerfile module input check failed with {failures} missing build input(s).",
            file=sys.stderr,
        )
        return 1
    print(f"  [OK]   every local module replacement is a copied build input ({checked} checked)")
    print("Dockerfile module input check passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
