# file: scripts/tests/test_fixture_shard_weights.py
# version: 1.0.0
# guid: 6c1e8a52-3f47-4b09-8d2a-5e7b9c0f1a34
# last-edited: 2026-10-30
"""Tests for scripts/ci/fixture_shard_weights.py: reading timings from the
shapes CI leaves them in, and turning samples into the weights table."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "ci"))

import fixture_shard_weights as fsw  # noqa: E402

M = fsw.MODULE

RAW_LOG = f"""\
=== {M}/internal/server#1/2: ok in 624s
ok  \t{M}/internal/server\t605.414s
=== 2 whole packages: ok in 694s
ok  \t{M}/internal/undo\t682.062s
ok  \t{M}/internal/versionprimary\t3.225s
=== {M}/internal/server#2/2: FAIL in 5s
FAIL\t{M}/internal/server\t4.0s
"""


def T(pkg, part=0, parts=1, secs=1.0):
    return fsw.Timing(pkg, part, parts, secs)


class Parsing(unittest.TestCase):
    def test_unit_forms(self):
        self.assertEqual(fsw.parse_unit("internal/undo", 3.0), T("internal/undo", secs=3.0))
        self.assertEqual(
            fsw.parse_unit(f"{M}/internal/server#2/3", 5.0), T("internal/server", 1, 3, 5.0)
        )
        with self.assertRaises(ValueError):
            fsw.parse_unit("internal/server#4/3", 1.0)

    def test_raw_log_attributes_slice_and_whole_package_lines_by_header(self):
        got = fsw.parse_text(RAW_LOG)
        self.assertEqual(
            sorted(got),
            sorted(
                [
                    T("internal/server", 0, 2, 605.414),
                    T("internal/undo", secs=682.062),
                    T("internal/versionprimary", secs=3.225),
                ]
            ),
        )

    def test_timing_lines_win_over_ok_lines_so_nothing_counts_twice(self):
        text = RAW_LOG + "::timing::internal/undo=700.0\n"
        self.assertEqual(fsw.parse_text(text), [T("internal/undo", secs=700.0)])

    def test_timing_line_round_trips(self):
        t = T("internal/server", 1, 2, 12.34)
        self.assertEqual(fsw.parse_text(fsw.timing_line(t) + "\n"), [T("internal/server", 1, 2, 12.3)])

    def test_json_round_trips(self):
        ts = [T("internal/server", 0, 2, 600.0), T("internal/server", 1, 2, 500.0), T("a", secs=2.0)]
        self.assertEqual(sorted(fsw.parse_source(fsw.to_json(ts))), sorted(ts))

    def test_json_of_another_version_is_refused(self):
        with self.assertRaises(ValueError):
            fsw.parse_source(json.dumps({"version": 99, "timings": []}))


class Calibrate(unittest.TestCase):
    PREV = {"weights": {"a": 100, "b": 50, "gone_quiet": 7}}

    def test_slices_are_summed_within_a_sample_and_the_median_taken_across(self):
        s = lambda x, y: [T("a", 0, 2, x), T("a", 1, 2, y)]
        doc, probs = fsw.calibrate([s(100, 100), s(300, 200), s(10, 20)], self.PREV)
        self.assertEqual(probs, [])
        self.assertEqual(doc["weights"]["a"], 200)  # sums 200, 500, 30 -> median 200
        self.assertEqual(doc["measured_samples_per_package"]["a"], 3)

    def test_max_sizes_for_the_slowest_sample(self):
        doc, _ = fsw.calibrate([[T("a", secs=10.2)], [T("a", secs=90.0)]], self.PREV, "max")
        self.assertEqual(doc["weights"]["a"], 90)

    def test_rounds_up_to_whole_seconds(self):
        doc, _ = fsw.calibrate([[T("a", secs=10.2)]], self.PREV)
        self.assertEqual(doc["weights"]["a"], 11)

    def test_unmeasured_packages_keep_their_weight_and_are_flagged(self):
        doc, _ = fsw.calibrate([[T("a", secs=10.0)]], self.PREV)
        self.assertEqual(doc["weights"]["b"], 50)
        self.assertEqual(doc["estimated"], ["b", "gone_quiet"])

    def test_a_new_package_is_added_as_measured(self):
        doc, _ = fsw.calibrate([[T("new", secs=4.0)]], self.PREV)
        self.assertEqual(doc["weights"]["new"], 4)
        self.assertNotIn("new", doc["estimated"])

    def test_a_sample_missing_a_slice_is_dropped_for_that_package_not_reported_low(self):
        doc, probs = fsw.calibrate([[T("a", 0, 2, 100.0)], [T("a", secs=300.0)]], self.PREV)
        self.assertEqual(doc["weights"]["a"], 300)
        self.assertEqual(len(probs), 1)
        self.assertIn("a", probs[0])

    def test_a_slice_seen_twice_in_one_sample_is_a_problem(self):
        _, probs = fsw.sample_weights([T("a", 0, 2, 1.0), T("a", 0, 2, 1.0)])
        self.assertEqual(len(probs), 1)


class Files(unittest.TestCase):
    def test_load_weights_rejects_a_missing_or_empty_table(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "w.json"
            with self.assertRaises(SystemExit):
                fsw.load_weights(p)
            p.write_text(json.dumps({"weights": {}}))
            with self.assertRaises(SystemExit):
                fsw.load_weights(p)
            p.write_text(json.dumps({"weights": {"a": 0}}))
            with self.assertRaises(SystemExit):
                fsw.load_weights(p)

    def test_the_committed_table_is_valid_and_fully_measured_or_flagged(self):
        doc = fsw.load_weights()
        self.assertTrue(set(doc["estimated"]) <= set(doc["weights"]))
        for pkg in doc["weights"]:
            self.assertFalse(pkg.startswith(M), f"{pkg}: keys are repo-relative")


if __name__ == "__main__":
    unittest.main()
