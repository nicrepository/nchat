#!/usr/bin/env python3
"""Negative tests for the Dockerfile module input check.

A gate is only worth its runtime if it fails on the thing it claims to catch.
These reconstruct the exact regression — a Dockerfile that builds a module with
a local `replace` but never copies the replace target — and require the checker
to reject it, so a refactor that quietly stops asserting this is caught here
rather than by a failed image build after merge.
"""

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).resolve().parent / "dockerfile-module-inputs-check.py"
spec = importlib.util.spec_from_file_location("dockerfile_module_inputs_check", MODULE_PATH)
check = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(check)


class LocalReplacementTests(unittest.TestCase):
    def write_go_mod(self, body: str) -> Path:
        import tempfile

        directory = Path(tempfile.mkdtemp(dir=check.ROOT / "services")) / "go.mod"
        directory.parent.mkdir(parents=True, exist_ok=True)
        directory.write_text(body)
        self.addCleanup(lambda: __import__("shutil").rmtree(directory.parent))
        return directory

    def test_single_line_replace_resolves_relative_to_the_module(self):
        go_mod = self.write_go_mod(
            "module example\n\n"
            "replace github.com/nicrepository/nchat/libs/go/platform => ../../libs/go/platform\n"
        )
        found = check.local_replacements(go_mod)
        self.assertEqual(len(found), 1, found)
        self.assertEqual(found[0][1].as_posix(), "libs/go/platform")

    def test_block_replace_is_read_too(self):
        go_mod = self.write_go_mod(
            "module example\n\nreplace (\n"
            "\tgithub.com/nicrepository/nchat/libs/go/platform => ../../libs/go/platform\n"
            ")\n"
        )
        self.assertEqual(
            [target.as_posix() for _, target in check.local_replacements(go_mod)],
            ["libs/go/platform"],
        )

    def test_a_versioned_replace_is_not_a_local_build_input(self):
        go_mod = self.write_go_mod(
            "module example\n\nreplace example.com/a => example.com/b v1.2.3\n"
        )
        self.assertEqual(check.local_replacements(go_mod), [])


class CopyCoverageTests(unittest.TestCase):
    def test_a_directory_copy_covers_a_module_inside_it(self):
        self.assertTrue(check.copy_covers(["libs/go"], Path("libs/go/platform")))

    def test_an_exact_copy_covers_the_target(self):
        self.assertTrue(check.copy_covers(["libs/go/platform"], Path("libs/go/platform")))

    def test_a_sibling_copy_does_not_cover_it(self):
        self.assertFalse(check.copy_covers(["libs/go/other"], Path("libs/go/platform")))

    def test_the_regression_shape_is_not_covered(self):
        # Exactly what Dockerfile.document-converter copied before the fix.
        sources = [
            "go.work",
            "go.work.sum",
            "services/document-converter/go.mod",
            "services/document-converter",
        ]
        self.assertFalse(check.copy_covers(sources, Path("libs/go/platform")))

    def test_a_prefix_that_is_not_a_path_boundary_does_not_count(self):
        # "libs/go-extra" must not be read as covering "libs/go/platform".
        self.assertFalse(check.copy_covers(["libs/go-extra"], Path("libs/go/platform")))


class BuildStageSourceTests(unittest.TestCase):
    def parse(self, text: str) -> list[str]:
        import tempfile

        path = Path(tempfile.mkstemp(suffix=".Dockerfile")[1])
        path.write_text(text)
        self.addCleanup(path.unlink)
        return check.build_stage_sources(path)

    def test_only_the_build_stage_counts(self):
        sources = self.parse(
            "FROM golang AS build\n"
            "COPY libs/go ./libs/go\n"
            "FROM debian\n"
            "COPY --from=build /out/x /x\n"
        )
        self.assertEqual(sources, ["libs/go"])

    def test_flags_are_not_mistaken_for_sources(self):
        sources = self.parse("FROM golang AS build\nCOPY --chown=1:1 libs/go ./libs/go\n")
        self.assertEqual(sources, ["libs/go"])

    def test_the_destination_is_not_a_source(self):
        sources = self.parse("FROM golang AS build\nCOPY a b ./dest/\n")
        self.assertEqual(sources, ["a", "b"])


class RealRepositoryTests(unittest.TestCase):
    def test_the_committed_dockerfiles_pass(self):
        self.assertEqual(check.main(), 0)

    def test_document_converter_declares_the_replace_this_gate_exists_for(self):
        go_mod = check.ROOT / "services/document-converter/go.mod"
        targets = [target.as_posix() for _, target in check.local_replacements(go_mod)]
        self.assertIn("libs/go/platform", targets)

    def test_document_converter_dockerfile_copies_it(self):
        dockerfile = check.ROOT / "Dockerfile.document-converter"
        sources = check.build_stage_sources(dockerfile)
        self.assertTrue(
            check.copy_covers(sources, Path("libs/go/platform")),
            f"build stage copies {sources}, which does not include libs/go/platform",
        )


if __name__ == "__main__":
    unittest.main(verbosity=1)
