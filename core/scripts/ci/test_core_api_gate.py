#!/usr/bin/env python3
# file: core/scripts/ci/test_core_api_gate.py
# version: 1.0.0
# guid: d7867b5b-3d6a-4921-ae7f-fdd406b9c93f
# last-edited: 2026-11-08
"""Tests for core_api_gate.py (12 §12.3 SC1).

The filter fixtures are real `apidiff -m -incompatible` output (x/exp
v0.0.0-20261014143641-f0251eb4aa3e) over a throwaway module laid out like core,
so a change in apidiff's output format fails here instead of silently passing
or failing every readiness run. Run: python3 -m unittest discover -s core/scripts/ci -p 'test_*.py'.
"""

from __future__ import annotations

import contextlib
import io
import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import core_api_gate as gate  # noqa: E402
import core_ci_lib as lib  # noqa: E402

# Object lines, module mode: a changed object, a removed object (the kernel/
# and examples/ lines among them).
OBJECT_LINES = """\
- ./examples/hello.H: removed
- package github.com/falkcorp/mediacore/gone: removed
- ./kernel.Boot: changed from func(int) to func(int, int)
- ./sdk.Do: changed from func(int) to func(string)
- ./sdk.T.A: removed
"""

# Package lines, module mode: whole packages removed, including nested ones and
# a package whose name only starts with "kernel".
PACKAGE_LINES = """\
- package github.com/falkcorp/mediacore/examples/hello: removed
- package github.com/falkcorp/mediacore/examples/hello/deep: removed
- package github.com/falkcorp/mediacore/gone: removed
- package github.com/falkcorp/mediacore/kernel: removed
- package github.com/falkcorp/mediacore/kernel/sub: removed
- package github.com/falkcorp/mediacore/kernelx: removed
- ./sdk.Do: changed from func(int) to func(string)
- ./sdk.T.A: removed
"""


class FilterTest(unittest.TestCase):
    def test_a_kernel_break_is_filtered(self) -> None:
        self.assertNotIn("- ./kernel.Boot: changed from func(int) to func(int, int)", gate.filter_incompatible(OBJECT_LINES))

    def test_an_examples_break_is_filtered(self) -> None:
        self.assertNotIn("- ./examples/hello.H: removed", gate.filter_incompatible(OBJECT_LINES))

    def test_an_sdk_break_is_reported(self) -> None:
        self.assertEqual(
            gate.filter_incompatible(OBJECT_LINES),
            [
                "- package github.com/falkcorp/mediacore/gone: removed",
                "- ./sdk.Do: changed from func(int) to func(string)",
                "- ./sdk.T.A: removed",
            ],
        )

    def test_removed_kernel_and_examples_packages_are_filtered_nested_ones_too(self) -> None:
        self.assertEqual(
            gate.filter_incompatible(PACKAGE_LINES),
            [
                "- package github.com/falkcorp/mediacore/gone: removed",
                "- package github.com/falkcorp/mediacore/kernelx: removed",
                "- ./sdk.Do: changed from func(int) to func(string)",
                "- ./sdk.T.A: removed",
            ],
        )

    def test_nothing_left_is_the_passing_case(self) -> None:
        only_kernel = "- ./kernel.Boot: changed from func(int) to func(int, int)\n- package github.com/falkcorp/mediacore/kernel/sub: removed\n"
        self.assertEqual(gate.filter_incompatible(only_kernel), [])
        self.assertEqual(gate.filter_incompatible(""), [])


def refs(*names: str) -> list[tuple[str, lib.Semver]]:
    out = [(f"refs/core-checkpoints/{n}", lib.parse_semver(n)) for n in names]
    return sorted(out, key=lambda r: r[1], reverse=True)


def dates(**by_name: str) -> dict[str, str]:
    return {f"refs/core-checkpoints/{n.replace('_', '.')}": d for n, d in by_name.items()}


def no_breaks(old: str, new: str) -> list[str]:
    return []


class EvaluateTest(unittest.TestCase):
    FOUR = refs("v0.5.0", "v0.6.0", "v0.7.0", "v0.8.0")
    SPREAD = dates(v0_5_0="2026-01-01", v0_6_0="2026-01-20", v0_7_0="2026-02-10", v0_8_0="2026-02-20")
    FIRST = lib.parse_semver("v0.5.0")

    def test_three_stable_pairs_over_six_weeks_pass(self) -> None:
        r = gate.evaluate(self.FOUR, self.FIRST, self.SPREAD, no_breaks)
        self.assertEqual(r.status, lib.PASS, r.details)
        self.assertIn("3/3 stable pairs", r.measured)

    def test_no_checkpoint_is_not_met(self) -> None:
        r = gate.evaluate([], self.FIRST, {}, no_breaks)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("0 checkpoint(s) recorded", r.measured)

    def test_three_refs_are_two_pairs_and_not_enough(self) -> None:
        r = gate.evaluate(refs("v0.6.0", "v0.7.0", "v0.8.0"), self.FIRST, {}, no_breaks)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("4 needed", r.measured)

    def test_an_incompatible_pair_fails_and_lists_the_break(self) -> None:
        def breaks(old: str, new: str) -> list[str]:
            return ["- ./sdk.Do: removed"] if new.endswith("v0.7.0") else []

        r = gate.evaluate(self.FOUR, self.FIRST, self.SPREAD, breaks)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("    - ./sdk.Do: removed", r.details)
        self.assertTrue(any("v0.6.0 -> v0.7.0 has incompatible changes" in d for d in r.details))

    def test_a_span_under_six_weeks_fails(self) -> None:
        close = dates(v0_5_0="2026-01-01", v0_6_0="2026-01-05", v0_7_0="2026-01-10", v0_8_0="2026-02-01")
        r = gate.evaluate(self.FOUR, self.FIRST, close, no_breaks)
        self.assertEqual(r.status, lib.FAIL)
        self.assertTrue(any("span 31 day(s) < 42" in d for d in r.details), r.details)

    def test_the_span_boundary_is_inclusive(self) -> None:
        exact = dates(v0_5_0="2026-01-01", v0_6_0="2026-01-05", v0_7_0="2026-01-10", v0_8_0="2026-02-12")
        self.assertEqual(gate.evaluate(self.FOUR, self.FIRST, exact, no_breaks).status, lib.PASS)

    def test_without_a_counting_start_nothing_counts(self) -> None:
        r = gate.evaluate(self.FOUR, None, self.SPREAD, no_breaks)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("0/3 stable pairs", r.measured)
        self.assertTrue(any("nothing counts yet" in d for d in r.details))

    def test_pairs_before_the_counting_start_are_reported_not_counted(self) -> None:
        r = gate.evaluate(self.FOUR, lib.parse_semver("v0.6.0"), self.SPREAD, no_breaks)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("2/3 stable pairs", r.measured)
        self.assertTrue(any("v0.5.0 -> v0.6.0" in d and "reported only" in d for d in r.details))

    def test_only_the_newest_four_are_looked_at(self) -> None:
        many = refs("v0.1.0", "v0.2.0", "v0.5.0", "v0.6.0", "v0.7.0", "v0.8.0")
        seen: list[tuple[str, str]] = []

        def record(old: str, new: str) -> list[str]:
            seen.append((old[-5:], new[-5:]))
            return []

        gate.evaluate(many, self.FIRST, self.SPREAD, record)
        self.assertEqual(seen, [("0.5.0", "0.6.0"), ("0.6.0", "0.7.0"), ("0.7.0", "0.8.0")])


class MeasureTest(unittest.TestCase):
    def test_missing_apidiff_with_enough_checkpoints_is_an_error_not_a_pass(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            subprocess.run(["git", "init", "-q", "-b", "main", str(root)], check=True)
            core = root / "core"
            core.mkdir()
            (core / "go.mod").write_text("module github.com/falkcorp/mediacore\n\ngo 1.27.0\n")
            subprocess.run(["git", "-C", str(root), "add", "-A"], check=True)
            subprocess.run(
                ["git", "-C", str(root), "-c", "user.name=Bruce Wayne", "-c", "user.email=bruce@wayne.example", "commit", "-q", "-m", "init"],
                check=True,
            )
            for v in ("v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"):
                subprocess.run(["git", "-C", str(root), "update-ref", f"refs/core-checkpoints/{v}", "HEAD"], check=True)
            r = gate.measure(core, root / "no-such-apidiff", None, fetch=False, remote="origin")
            self.assertEqual(r.status, lib.ERROR)
            self.assertIn("is missing", r.measured)


class ExitCodeTest(unittest.TestCase):
    """finish(): report mode never fails a merge; otherwise FAIL is 1 and ERROR is 2."""

    def test_codes(self) -> None:
        def result(status: str) -> lib.Result:
            return lib.Result("SC1", "t", status, "m")

        for status, strict, report in (
            (lib.PASS, 0, 0),
            (lib.NOT_APPLICABLE, 0, 0),
            (lib.FAIL, 1, 0),
            (lib.ERROR, 2, 0),
        ):
            with self.subTest(status=status):
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(lib.finish(result(status), None, report_only=False), strict)
                    self.assertEqual(lib.finish(result(status), None, report_only=True), report)


if __name__ == "__main__":
    unittest.main()
