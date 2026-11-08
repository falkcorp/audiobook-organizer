# file: scripts/tests/test_core_consumer_smoke.py
# version: 1.0.0
# guid: 5c031a2f-3a7e-4dd7-86e3-aa6563038031
# last-edited: 2026-11-08
"""Tests for scripts/ci/core_consumer_smoke.py (12 §12.3 SC3).

The generated module and the step logic are tested with a fake runner, so no
test runs go; the real run is the nightly job (and `python3 scripts/ci/core_consumer_smoke.py`).
"""

from __future__ import annotations

import importlib.util
import pathlib
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location("core_consumer_smoke", ROOT / "scripts/ci/core_consumer_smoke.py")
assert _spec and _spec.loader
smoke = importlib.util.module_from_spec(_spec)
sys.modules["core_consumer_smoke"] = smoke
_spec.loader.exec_module(smoke)
lib = smoke.lib

INTERNAL_ERROR = (
    "negative/negative.go:3:8: use of internal package github.com/falkcorp/mediacore/internal/bootseam not allowed"
)


def make_core(root: pathlib.Path) -> pathlib.Path:
    core = root / "core"
    (core / "examples/hello/cmd/hello").mkdir(parents=True)
    (core / "examples/hello/cmd/hello/main.go").write_text("package main\n\nfunc main() {}\n")
    (core / "internal/zeta").mkdir(parents=True)
    (core / "internal/zeta/z.go").write_text("package zeta\n")
    (core / "internal/alpha").mkdir()
    (core / "internal/alpha/a_test.go").write_text("package alpha\n")  # tests only: not importable
    (core / "internal/beta").mkdir()
    (core / "internal/beta/b.go").write_text("package beta\n")
    (core / "go.mod").write_text("module github.com/falkcorp/mediacore\n\ngo 1.27.0\n")
    return core


class SmokeTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = pathlib.Path(self._tmp.name)
        self.core = make_core(self.root)
        self.work = self.root / "work"
        self.work.mkdir()

    def runner(self, fail: str = "", negative_code: int = 1, negative_output: str = INTERNAL_ERROR):
        calls: list[list[str]] = []

        def run(argv: list[str], cwd: pathlib.Path) -> tuple[int, str]:
            calls.append(argv)
            if argv[:2] == ["go", "build"] and argv[2] == "./negative":
                return negative_code, negative_output
            if " ".join(argv[:3]).startswith(fail) and fail:
                return 1, "boom"
            return 0, ""

        return run, calls

    def test_go_mod_has_one_require_one_replace_and_an_unrelated_module_path(self) -> None:
        text = smoke.render_go_mod(self.core)
        self.assertIn("module example.com/mediacore-smoke\n", text)
        self.assertIn("go 1.27.0\n", text)
        self.assertEqual(text.count("require "), 1)
        self.assertIn("require github.com/falkcorp/mediacore v0.0.0-00010101000000-000000000000\n", text)
        self.assertEqual(text.count("replace "), 1)
        self.assertIn(f"replace github.com/falkcorp/mediacore => {self.core}\n", text)

    def test_the_negative_file_imports_the_first_non_test_internal_package(self) -> None:
        self.assertEqual(
            smoke.negative_source(self.core),
            'package negative\n\nimport _ "github.com/falkcorp/mediacore/internal/beta"\n',
        )

    def test_a_pass_runs_tidy_build_vet_test_then_the_negative_check(self) -> None:
        run, calls = self.runner()
        r = smoke.smoke(self.core, self.work, run)
        self.assertEqual(r.status, lib.PASS, r.details)
        self.assertEqual(
            calls,
            [["go", "mod", "tidy"], ["go", "build", "./..."], ["go", "vet", "./..."], ["go", "test", "-count=1", "./..."], ["go", "build", "./negative"]],
        )
        self.assertEqual((self.work / "main.go").read_text(), "package main\n\nfunc main() {}\n")
        self.assertIn("TestSmoke_OpenAPI", (self.work / "smoke_test.go").read_text())

    def test_a_failing_step_fails_and_stops(self) -> None:
        for step in ("go build", "go vet", "go test"):
            with self.subTest(step=step):
                run, calls = self.runner(fail=step)
                r = smoke.smoke(self.core, self.work, run)
                self.assertEqual(r.status, lib.FAIL)
                self.assertNotIn(["go", "build", "./negative"], calls)

    def test_an_internal_import_that_compiles_fails(self) -> None:
        run, _ = self.runner(negative_code=0, negative_output="")
        r = smoke.smoke(self.core, self.work, run)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("compiled", r.measured)

    def test_an_internal_import_that_fails_for_another_reason_fails(self) -> None:
        run, _ = self.runner(negative_output="negative/negative.go:3:8: cannot find package")
        r = smoke.smoke(self.core, self.work, run)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("not with", r.measured)

    def test_a_core_without_hello_is_an_error(self) -> None:
        (self.core / "examples/hello/cmd/hello/main.go").unlink()
        run, _ = self.runner()
        self.assertEqual(smoke.smoke(self.core, self.work, run).status, lib.ERROR)


if __name__ == "__main__":
    unittest.main()
