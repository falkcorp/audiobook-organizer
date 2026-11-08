#!/usr/bin/env python3
# file: core/scripts/ci/test_readiness.py
# version: 1.0.0
# guid: 0ff54696-e19a-41e0-9b8c-0eacda720ab3
# last-edited: 2026-11-08
"""Tests for readiness_report.py, coredoc_check.py and core_vulncheck.py (12 §12.3.11).

Run: python3 -m unittest discover -s core/scripts/ci -p 'test_*.py'.
"""

from __future__ import annotations

import contextlib
import io
import json
import pathlib
import stat
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import core_ci_lib as lib  # noqa: E402
import core_vulncheck as vuln  # noqa: E402
import coredoc_check as coredoc  # noqa: E402
import readiness_report as report  # noqa: E402


def quiet():
    return contextlib.redirect_stdout(io.StringIO())


class ReportTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.out = pathlib.Path(self._tmp.name) / ".ci" / "readiness"

    def test_report_folds_the_results_and_marks_a_missing_one_as_not_run(self) -> None:
        lib.write_result(
            self.out,
            lib.Result("SC1", "t1", lib.FAIL, "0 checkpoint(s) recorded", ["recorded: none"]),
        )
        lib.write_result(self.out, lib.Result("SC5", "t5", lib.PASS, "total 80.0%"))
        lib.write_result(
            self.out,
            lib.Result("SC6", "t6", lib.NOT_APPLICABLE, "not applicable: generator not landed"),
        )
        with quiet():
            code = report.main(
                ["--out", str(self.out), "--date", "2026-11-08", "--commit", "abc123"]
            )
        self.assertEqual(code, 0)  # always: the report never fails a merge
        data = json.loads((self.out / "readiness.json").read_text())
        self.assertEqual((data["date"], data["commit"]), ("2026-11-08", "abc123"))
        by_id = {c["id"]: c for c in data["criteria"]}
        self.assertEqual(list(by_id), ["SC1", "SC5", "SC6", "SC9"])
        self.assertEqual(by_id["SC9"]["status"], lib.ERROR)
        self.assertIn("did not run", by_id["SC9"]["measured"])
        md = (self.out / "readiness.md").read_text()
        self.assertIn("# Core readiness report, 2026-11-08", md)
        self.assertIn("`abc123`", md)
        self.assertIn("| SC5 t5 | PASS | total 80.0% |", md)
        self.assertIn("| SC6 t6 | N/A |", md)
        self.assertIn("    recorded: none", md)

    def test_report_writes_only_inside_its_directory(self) -> None:
        with quiet():
            report.main(["--out", str(self.out), "--date", "2026-11-08", "--commit", "abc123"])
        written = sorted(p.name for p in self.out.parent.parent.rglob("*") if p.is_file())
        self.assertEqual(written, ["readiness.json", "readiness.md"])


class CoredocTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.core = pathlib.Path(self._tmp.name)

    def test_not_applicable_until_the_generator_lands(self) -> None:
        r = coredoc.measure(self.core)
        self.assertEqual(r.status, lib.NOT_APPLICABLE)
        self.assertIn("has not landed yet", r.measured)

    def generator(self, exit_code: int) -> None:
        (self.core / "go.mod").write_text("module example.com/fake\n\ngo 1.27.0\n")
        gen = self.core / "internal" / "cmd" / "coredoc"
        gen.mkdir(parents=True, exist_ok=True)
        (gen / "main.go").write_text(
            'package main\n\nimport (\n\t"flag"\n\t"fmt"\n\t"os"\n)\n\n'
            'func main() {\n\tcheck := flag.Bool("check", false, "")\n\tflag.Parse()\n'
            f'\tif !*check {{\n\t\tos.Exit(2)\n\t}}\n\tfmt.Println("checked")\n\tos.Exit({exit_code})\n}}\n'
        )

    def test_runs_the_generator_in_check_mode_once_it_exists(self) -> None:
        self.generator(0)
        r = coredoc.measure(self.core)
        self.assertEqual(r.status, lib.PASS, r.details)
        self.generator(1)
        r = coredoc.measure(self.core)
        self.assertEqual(r.status, lib.FAIL, r.details)
        self.assertIn("exited 1", r.measured)


class VulncheckTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = pathlib.Path(self._tmp.name)

    def fake(self, exit_code: int) -> pathlib.Path:
        path = self.dir / "govulncheck"
        path.write_text(f"#!/bin/sh\necho 'scanning'\necho 'detail' >&2\nexit {exit_code}\n")
        path.chmod(path.stat().st_mode | stat.S_IXUSR)
        return path

    def test_exit_status_maps_to_the_verdict(self) -> None:
        for code, status in ((0, lib.PASS), (3, lib.FAIL), (1, lib.ERROR)):
            with self.subTest(code=code):
                r = vuln.measure(self.dir, self.fake(code), self.dir / "out")
                self.assertEqual(r.status, status)
        self.assertIn("scanning", (self.dir / "out" / "sc9-govulncheck.txt").read_text())

    def test_a_missing_binary_is_an_error_not_a_pass(self) -> None:
        r = vuln.measure(self.dir, self.dir / "nope", None)
        self.assertEqual(r.status, lib.ERROR)
        self.assertIn("make -C core tools", r.measured)


if __name__ == "__main__":
    unittest.main()
