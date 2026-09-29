# file: scripts/tests/test_go_test_shards.py
# version: 1.0.0
# guid: 9d4a1f63-2b7e-4c85-a0f1-6e3b8c2d7a59
# last-edited: 2026-09-29
"""Tests for scripts/ci/go_test_shards.py: shard balancing and coverage merge.

The merge feeds the CI coverage gate, so a block counted twice (once per shard)
or dropped would silently move the floor. These pin the exact merge semantics.
"""

from __future__ import annotations

import os
import sys
import tempfile
import unittest

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
