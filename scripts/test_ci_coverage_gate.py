#!/usr/bin/env python3
# file: scripts/test_ci_coverage_gate.py
# version: 1.0.0
# guid: 5e0d8c2a-7b3f-4f61-8c9e-1a2b4d6f8e03
# last-edited: 2026-09-26

"""Unit tests for scripts/ci_coverage_gate.py (no network).

Run with:  python3 -m unittest discover -s scripts -p 'test_ci_coverage_gate.py' -v
"""

import base64
import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

_HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location("ci_coverage_gate", _HERE / "ci_coverage_gate.py")
gate = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
sys.modules["ci_coverage_gate"] = gate
_spec.loader.exec_module(gate)

# The awk program the .woodpecker test workflows run, with Woodpecker's `$$`
# escapes undone. Kept here so the test proves the statement counting.
AWK = 'NR>1{t+=$2; if($3>0)c+=$2} END{printf "CI-COVERAGE workflow=%s covered=%d total=%d\\n", wf, c, t}'


class ParseCombineTest(unittest.TestCase):
    def test_parse_ignores_echoed_program(self):
        text = (
            "+ awk '... CI-COVERAGE workflow=%s covered=%d total=%d ...'\n"
            "CI-COVERAGE workflow=test-database covered=900 total=1000\n"
        )
        self.assertEqual(gate.parse_lines(text), [("test-database", 900, 1000)])

    def test_combine_is_statement_weighted(self):
        found = [("test-database", 900, 1000), ("test-server-scanner", 100, 1000), ("test-rest", 0, 2000)]
        pct, problems = gate.combine(found)
        self.assertEqual(problems, [])
        self.assertEqual(pct, 25.0)  # 1000 / 4000, not the mean of 90/10/0

    def test_missing_duplicate_and_unexpected(self):
        found = [("test-database", 1, 2), ("test-database", 1, 2), ("test-other", 1, 1)]
        _, problems = gate.combine(found)
        text = "\n".join(problems)
        self.assertIn("test-database: 2 CI-COVERAGE lines", text)
        self.assertIn("test-server-scanner: no CI-COVERAGE line", text)
        self.assertIn("test-other: unexpected", text)

    def test_decode_log_entries(self):
        entries = [{"data": base64.b64encode(b"CI-COVERAGE workflow=test-rest covered=1 total=2").decode()}]
        self.assertEqual(gate.parse_lines(gate.decode_log_entries(entries)), [("test-rest", 1, 2)])


class AwkMatchesGoToolCoverTest(unittest.TestCase):
    def test_awk_counts_statements(self):
        profile = (
            "mode: atomic\n"
            "m/a/x.go:1.1,2.2 3 5\n"
            "m/a/x.go:3.1,4.2 2 0\n"
            "m/a/y.go:1.1,2.2 5 1\n"
        )
        with tempfile.TemporaryDirectory() as td:
            p = Path(td) / "cover.out"
            p.write_text(profile)
            out = subprocess.run(["awk", "-v", "wf=test-rest", AWK, str(p)], text=True, capture_output=True).stdout
        self.assertEqual(gate.parse_lines(out), [("test-rest", 8, 10)])


class MainTest(unittest.TestCase):
    def run_main(self, lines, floor="30"):
        with tempfile.TemporaryDirectory() as td:
            log = Path(td) / "a.log"
            log.write_text("\n".join(lines))
            ff = Path(td) / "floor.txt"
            ff.write_text(floor + "\n")
            return gate.main(["--logs", str(log), "--floor-file", str(ff)])

    def test_pass_and_fail(self):
        ok = [f"CI-COVERAGE workflow={w} covered=50 total=100" for w in gate.EXPECTED_WORKFLOWS]
        self.assertEqual(self.run_main(ok), 0)
        self.assertEqual(self.run_main(ok, floor="60"), 1)
        self.assertEqual(self.run_main(ok[:2]), 1)


if __name__ == "__main__":
    unittest.main()
