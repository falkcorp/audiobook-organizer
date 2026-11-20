#!/usr/bin/env python3
# file: docs/plans/platform-core/tools/test_check_waves.py
# version: 1.1.0
# guid: 2f7b9d41-8c36-4e05-a1d8-6b3e0c5f9a72
# last-edited: 2026-11-15
"""Tests for check_waves.py's row-id pattern and for the checker over the real inventory.

check_waves.py runs its checks at import, so the pattern is read from the source and the
whole checker is run as a subprocess.

Usage: python3 -I test_check_waves.py   (from the repo root or any directory)
"""
import json
import os
import re
import subprocess
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.normpath(os.path.join(HERE, "..", "..", "..", ".."))
SCRIPT = os.path.join(HERE, "check_waves.py")


def id_re():
    with open(SCRIPT) as f:
        src = f.read()
    m = re.search(r"^ID_RE = (re\.compile\(.*\))$", src, re.M)
    assert m, "check_waves.py has no ID_RE line"
    return eval(m.group(1), {"re": re})


class IdPattern(unittest.TestCase):
    def test_accepts(self):
        r = id_re()
        for i in ["R0-01", "R1-37", "R2-01", "R3-05", "F2-17", "F8-04a", "F8-04b", "F9-05", "OWNER-06", "F8-03b", "F7-10a"]:
            self.assertTrue(r.match(i), i)

    def test_rejects(self):
        r = id_re()
        for i in ["F8-3b", "F8-03B", "F8-03bb", "F8-03-b", "F8-b", "F8-0b3", "F1-01", "R4-01", "X1-01",
                  "OWNER-06b", "OWNER-6", "F8-03b ", " F8-03b", "F8-03\n"]:
            self.assertFalse(r.match(i), repr(i))


def wave_funcs():
    """Load WAVE_RE and wave_key from check_waves.py without running the checker (it executes at import)."""
    with open(SCRIPT) as f:
        src = f.read()
    m = re.search(r"^WAVE_RE = re\.compile\(.*\)$", src, re.M)
    assert m, "check_waves.py has no WAVE_RE line"
    k = re.search(r"^def wave_key\(w\):\n(?:    .*\n|\n)+?(?=\n\S)", src, re.M)
    assert k, "check_waves.py has no wave_key function"
    failures = []
    ns = {"re": re, "fail": failures.append}
    exec(m.group(0) + "\n\n" + k.group(0), ns)
    return ns["WAVE_RE"], ns["wave_key"], failures


class WavePattern(unittest.TestCase):
    def test_accepts(self):
        r, _, _ = wave_funcs()
        for w in ["W01", "W38", "W38b", "W39", "W47"]:
            self.assertTrue(r.match(w), w)

    def test_rejects(self):
        r, _, _ = wave_funcs()
        for w in ["W38B", "W3", "W38bb", "W38\n", "W38b\n", "w38", "W038", "W38-b", " W38", "W38 ", "W"]:
            self.assertFalse(r.match(w), repr(w))

    def test_order_is_numeric_with_the_letter_after_the_bare_wave(self):
        _, key, failures = wave_funcs()
        self.assertLess(key("W38"), key("W38b"))
        self.assertLess(key("W38b"), key("W39"))
        self.assertLess(key("W09"), key("W10"))
        self.assertLess(key("W38b"), key("W38c"))
        self.assertEqual(failures, [])
        key("W38B")
        self.assertEqual(len(failures), 1, "a malformed wave must be reported, not ordered")


class RealInventory(unittest.TestCase):
    def test_all_checks_pass_and_the_f8_03b_row_is_in_it(self):
        inv = os.path.join(HERE, "inventory.json")
        res = subprocess.run([sys.executable, "-I", SCRIPT, inv, os.path.join(REPO, "docs", "specs", "platform-core")],
                             capture_output=True, text=True)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)
        self.assertIn("RESULT: PASS", res.stdout)
        self.assertIn("1 schema and ID scheme: PASS", res.stdout)

    def test_the_f8_03b_row_exists_and_f8_04_waits_for_it(self):
        # The checker passes over an inventory that lacks the row as readily as
        # over one that has it (it did, until round 10), so the row is asserted.
        with open(os.path.join(HERE, "inventory.json")) as f:
            rows = {r["id"]: r for r in json.load(f)}
        self.assertIn("F8-03b", rows)
        row = rows["F8-03b"]
        self.assertEqual(row["depends_on"], ["F8-03"])
        self.assertEqual(row["release"], "F8")
        self.assertTrue(row["files"], "the F8-03b row names no files")
        self.assertIn("F8-03b", rows["F8-04a"]["depends_on"])
        self.assertLess(row["wave"], rows["F8-04a"]["wave"])

    def test_f8_04_is_split_and_b_follows_a(self):
        # F8-04 was split into F8-04a (backup, health, metrics) and F8-04b (the updater) on 2026-11-20. The old row is
        # gone; F8-04b waits for F8-04a and sits in W38b, which the checker orders between W38 and W39.
        with open(os.path.join(HERE, "inventory.json")) as f:
            rows = {r["id"]: r for r in json.load(f)}
        self.assertNotIn("F8-04", rows)
        self.assertIn("F8-04a", rows)
        self.assertIn("F8-04b", rows)
        self.assertEqual(rows["F8-04a"]["wave"], "W38")
        self.assertEqual(rows["F8-04b"]["wave"], "W38b")
        self.assertIn("F8-04a", rows["F8-04b"]["depends_on"])
        # F8-04a depends on neither F6-03 nor F7-09 (ruling R-A: it merges independently of F6-03).
        self.assertNotIn("F6-03", rows["F8-04a"]["depends_on"])
        self.assertNotIn("F7-09", rows["F8-04a"]["depends_on"])
        self.assertEqual(sorted(rows["R2-01"]["depends_on"]), ["F8-04a", "F8-04b"])


if __name__ == "__main__":
    unittest.main()
