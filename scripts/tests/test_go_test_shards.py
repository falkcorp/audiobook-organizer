# file: scripts/tests/test_go_test_shards.py
# version: 1.1.0
# guid: 9d4a1f63-2b7e-4c85-a0f1-6e3b8c2d7a59
# last-edited: 2026-10-09
"""Tests for scripts/ci/go_test_shards.py: shard balancing and coverage merge.

The merge feeds the CI coverage gate, so a block counted twice (once per shard)
or dropped would silently move the floor. These pin the exact merge semantics.
"""

from __future__ import annotations

import os
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "ci"))

import go_test_shards as gts  # noqa: E402


class BalanceTest(unittest.TestCase):
    def test_every_test_lands_in_exactly_one_shard(self) -> None:
        names = [f"Test{i:03d}" for i in range(97)]
        shards = gts.balance(names, 6, {})
        flat = sorted(t for s in shards for t in s)
        self.assertEqual(flat, sorted(names))
        self.assertEqual(len(shards), 6)

    def test_slow_tests_are_spread_not_stacked(self) -> None:
        timings = {"TestA": 50.0, "TestB": 40.0, "TestC": 30.0, "TestD": 1.0, "TestE": 1.0}
        shards = gts.balance(list(timings), 3, timings)
        loads = sorted(sum(timings[t] for t in s) for s in shards)
        # LPT: A, B, C open a shard each; D and E both go to the lightest (C).
        self.assertEqual(loads, [32.0, 40.0, 50.0])

    def test_unknown_tests_get_the_median_estimate(self) -> None:
        timings = {"TestA": 10.0, "TestB": 10.0, "TestC": 10.0}
        shards = gts.balance(["TestA", "TestB", "TestC", "TestNew1", "TestNew2", "TestNew3"], 3, timings)
        self.assertEqual(sorted(len(s) for s in shards), [2, 2, 2])

    def test_more_shards_than_tests_drops_empty_shards(self) -> None:
        self.assertEqual(gts.balance(["TestOnly"], 4, {}), [["TestOnly"]])


class MergeProfilesTest(unittest.TestCase):
    def _write(self, d: str, name: str, body: str) -> str:
        p = os.path.join(d, name)
        with open(p, "w", encoding="utf-8") as fh:
            fh.write(body)
        return p

    def test_blocks_are_unioned_and_counts_summed(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            a = self._write(d, "a.out", "mode: atomic\nx.go:1.1,2.1 3 0\nx.go:3.1,4.1 2 5\n")
            b = self._write(d, "b.out", "mode: atomic\nx.go:1.1,2.1 3 4\nx.go:3.1,4.1 2 0\n")
            out = os.path.join(d, "m.out")
            gts.merge_profiles([a, b], out)
            with open(out, encoding="utf-8") as fh:
                self.assertEqual(fh.read(), "mode: atomic\nx.go:1.1,2.1 3 4\nx.go:3.1,4.1 2 5\n")

    def test_a_missing_shard_profile_is_skipped(self) -> None:
        # A shard that failed to build writes no profile; its failure is
        # reported through the exit code, and the merge must still succeed.
        with tempfile.TemporaryDirectory() as d:
            a = self._write(d, "a.out", "mode: atomic\nx.go:1.1,2.1 3 1\n")
            out = os.path.join(d, "m.out")
            gts.merge_profiles([a, os.path.join(d, "absent.out")], out)
            with open(out, encoding="utf-8") as fh:
                self.assertEqual(fh.read(), "mode: atomic\nx.go:1.1,2.1 3 1\n")

    def test_mixed_modes_are_refused(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            a = self._write(d, "a.out", "mode: atomic\n")
            b = self._write(d, "b.out", "mode: set\n")
            with self.assertRaises(SystemExit):
                gts.merge_profiles([a, b], os.path.join(d, "m.out"))


if __name__ == "__main__":
    unittest.main()


class ListTestsManyTest(unittest.TestCase):
    """One `go test -list` call lists several packages; names are attributed by
    the summary line that ends each package's block."""

    OUT = (
        "TestA1\nTestA2\nExampleA\nBenchmarkA\nok  \tm/a\t0.01s\n"
        "?   \tm/none\t[no test files]\n"
        "FuzzB\nTestB1\nok  \tm/b\t0.02s\n"
    )

    def test_names_are_attributed_per_package_in_one_call(self) -> None:
        calls: list[list[str]] = []

        def fake_run(cmd: list[str], **kwargs: object) -> mock.Mock:
            calls.append(cmd)
            return mock.Mock(returncode=0, stdout=self.OUT, stderr="")

        with mock.patch.object(gts.subprocess, "run", fake_run):
            got = gts.list_tests_many(["m/a", "m/none", "m/b"], ["-short", "-race", "-timeout", "25m"])
        self.assertEqual(
            got,
            {"m/a": ["ExampleA", "TestA1", "TestA2"], "m/none": [], "m/b": ["FuzzB", "TestB1"]},
        )
        self.assertEqual(calls, [["go", "test", "-race", "-list", ".", "m/a", "m/none", "m/b"]])

    def test_single_package_wrapper_keeps_its_shape(self) -> None:
        def fake_run(cmd: list[str], **kwargs: object) -> mock.Mock:
            return mock.Mock(returncode=0, stdout="TestB1\nFuzzB\nok  \tm/b\t0.02s\n", stderr="")

        with mock.patch.object(gts.subprocess, "run", fake_run):
            self.assertEqual(gts.list_tests("m/b", []), ["FuzzB", "TestB1"])

    def test_build_failure_and_unattributed_output_fail_loudly(self) -> None:
        def failing(cmd: list[str], **kwargs: object) -> mock.Mock:
            return mock.Mock(returncode=1, stdout="FAIL\tm/a [build failed]\n", stderr="x.go:1: boom")

        with mock.patch.object(gts.subprocess, "run", failing), self.assertRaises(SystemExit):
            gts.list_tests_many(["m/a"], [])

        def truncated(cmd: list[str], **kwargs: object) -> mock.Mock:
            return mock.Mock(returncode=0, stdout="TestA1\n", stderr="")

        with mock.patch.object(gts.subprocess, "run", truncated), self.assertRaises(SystemExit):
            gts.list_tests_many(["m/a"], [])
