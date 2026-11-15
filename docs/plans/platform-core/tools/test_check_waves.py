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
        for i in ["R0-01", "R1-37", "R2-01", "R3-05", "F2-17", "F8-04", "F9-05", "OWNER-06", "F8-03b", "F7-10a"]:
            self.assertTrue(r.match(i), i)

    def test_rejects(self):
        r = id_re()
        for i in ["F8-3b", "F8-03B", "F8-03bb", "F8-03-b", "F8-b", "F8-0b3", "F1-01", "R4-01", "X1-01",
                  "OWNER-06b", "OWNER-6", "F8-03b ", " F8-03b", "F8-03\n"]:
            self.assertFalse(r.match(i), repr(i))


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
        self.assertIn("F8-03b", rows["F8-04"]["depends_on"])
        self.assertLess(row["wave"], rows["F8-04"]["wave"])


if __name__ == "__main__":
    unittest.main()
