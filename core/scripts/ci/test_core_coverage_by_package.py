#!/usr/bin/env python3
# file: core/scripts/ci/test_core_coverage_by_package.py
# version: 1.0.0
# guid: 9238f605-80f0-4a0a-858c-626c6a7360c6
# last-edited: 2026-11-08
"""Tests for core_coverage_by_package.py (12 §12.3 SC5). Run: python3 -m unittest discover -s core/scripts/ci -p 'test_*.py'."""

from __future__ import annotations

import contextlib
import io
import json
import pathlib
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import core_ci_lib as lib  # noqa: E402
import core_coverage_by_package as cov  # noqa: E402

MOD = "github.com/falkcorp/mediacore"
EXCLUDE = "kernel/|internal/|examples/"

# 10 statements in library (all covered), 10 in storage (4 covered), 10 in kernel
# (none covered), 10 in internal/pebble (none), one block listed twice.
PROFILE = f"""\
mode: set
{MOD}/library/a.go:3.10,5.2 6 1
{MOD}/library/a.go:7.10,9.2 4 1
{MOD}/storage/s.go:3.10,5.2 4 1
{MOD}/storage/s.go:7.10,9.2 6 0
{MOD}/kernel/k.go:3.10,5.2 10 0
{MOD}/internal/pebble/p.go:3.10,5.2 10 0
{MOD}/library/a.go:3.10,5.2 6 1
"""


class ParseTest(unittest.TestCase):
    def test_files_sum_statements_and_covered(self) -> None:
        files = cov.parse_profile(PROFILE)
        self.assertEqual(files[f"{MOD}/library/a.go"], (10, 10))
        self.assertEqual(files[f"{MOD}/storage/s.go"], (10, 4))

    def test_a_block_listed_twice_counts_once_and_covered_if_any_listing_covered_it(self) -> None:
        text = f"mode: count\n{MOD}/x/x.go:1.1,2.2 5 0\n{MOD}/x/x.go:1.1,2.2 5 3\n"
        self.assertEqual(cov.parse_profile(text)[f"{MOD}/x/x.go"], (5, 5))

    def test_not_a_profile_is_rejected(self) -> None:
        for bad in ("", "garbage\n", "mode: set\nnot a block\n"):
            with self.assertRaises(ValueError, msg=bad):
                cov.parse_profile(bad)


class EvaluateTest(unittest.TestCase):
    def test_excluded_packages_count_in_the_total_but_not_the_floor(self) -> None:
        r = cov.evaluate(cov.parse_profile(PROFILE), MOD, 60.0, 70.0, EXCLUDE)
        # total: 14 covered of 40 = 35%; floor: library 100%, storage 40% (below).
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("total 35.0%", r.measured)
        self.assertIn("1 of 2 SDK package(s) below 60%", r.measured)
        self.assertTrue(any("storage" in d and "40.0%" in d for d in r.details), r.details)
        self.assertFalse(any("kernel" in d or "pebble" in d for d in r.details))

    def test_passes_when_total_and_every_sdk_package_clear_the_floors(self) -> None:
        good = f"mode: set\n{MOD}/library/a.go:1.1,2.2 10 1\n{MOD}/storage/s.go:1.1,2.2 10 1\n{MOD}/kernel/k.go:1.1,2.2 10 0\n"
        r = cov.evaluate(cov.parse_profile(good), MOD, 60.0, 60.0, EXCLUDE)
        self.assertEqual(r.status, lib.PASS, r.details)

    def test_total_below_the_floor_fails_even_when_every_package_clears_it(self) -> None:
        text = f"mode: set\n{MOD}/library/a.go:1.1,2.2 10 1\n{MOD}/kernel/k.go:1.1,2.2 100 0\n"
        r = cov.evaluate(cov.parse_profile(text), MOD, 60.0, 70.0, EXCLUDE)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("total 9.1%", r.measured)

    def test_an_untested_package_at_zero_fails_the_floor(self) -> None:
        text = f"mode: set\n{MOD}/library/a.go:1.1,2.2 10 1\n{MOD}/untested/u.go:1.1,2.2 5 0\n"
        r = cov.evaluate(cov.parse_profile(text), MOD, 60.0, 50.0, EXCLUDE)
        self.assertEqual(r.status, lib.FAIL)
        self.assertTrue(any("untested" in d for d in r.details))

    def test_the_package_is_the_directory_of_the_file(self) -> None:
        text = f"mode: set\n{MOD}/a/b/c.go:1.1,2.2 4 1\n{MOD}/a/b/d.go:1.1,2.2 4 0\n{MOD}/a/e.go:1.1,2.2 4 0\n"
        r = cov.evaluate(cov.parse_profile(text), MOD, 40.0, 0.0, "")
        self.assertIn("2 SDK package(s) measured, 1 below 40%", r.details[1])


class MainTest(unittest.TestCase):
    def run_main(self, text: str, *extra: str) -> tuple[int, dict]:
        with tempfile.TemporaryDirectory() as tmp:
            out = pathlib.Path(tmp)
            profile = out / "core.out"
            profile.write_text(text)
            with contextlib.redirect_stdout(io.StringIO()):
                code = cov.main(
                    [str(profile), "--module", MOD, "--exclude", EXCLUDE, "--out", str(out), *extra]
                )
            return code, json.loads((out / "sc5.json").read_text())

    def test_fail_exits_1_and_report_exits_0(self) -> None:
        code, stored = self.run_main(PROFILE)
        self.assertEqual((code, stored["status"]), (1, lib.FAIL))
        code, _ = self.run_main(PROFILE, "--report")
        self.assertEqual(code, 0)

    def test_an_unusable_profile_is_an_error_result(self) -> None:
        code, stored = self.run_main("garbage\n")
        self.assertEqual((code, stored["status"]), (2, lib.ERROR))

    def test_a_failing_go_test_is_noted(self) -> None:
        _, stored = self.run_main(PROFILE, "--test-status", "1", "--report")
        self.assertTrue(any("go test exited 1" in d for d in stored["details"]))


if __name__ == "__main__":
    unittest.main()
