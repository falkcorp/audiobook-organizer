#!/usr/bin/env python3
# file: scripts/test_deadcode_ratchet.py
# version: 1.0.0
# guid: 2b8e41c7-6d09-4f3a-a5e2-90c1d7f3b648
# last-edited: 2026-10-11
"""Unit tests for scripts/deadcode-ratchet.py (stdlib unittest, no Go needed)."""
import importlib.util
import tempfile
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "deadcode_ratchet", Path(__file__).with_name("deadcode-ratchet.py")
)
dr = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(dr)

SAMPLE = """\
internal/foo/a.go:10:6: unreachable func: Used
internal/foo/a.go:20:14: unreachable func: T.Method
internal/foo/a_test.go:5:6: unreachable func: HelperInTest
internal/foo/b.go:5:6: unreachable func: ResetForTest
internal/foo/c.go:5:6: unreachable func: SetXForTesting
pkg/sdk/s.go:5:6: unreachable func: Exported
internal/writeback/w.go:5:6: unreachable func: Write
internal/testutil/t.go:5:6: unreachable func: Make
internal/foo/footest/t.go:5:6: unreachable func: Make
internal/conformance/c.go:5:6: unreachable func: Run
internal/rapidgen/g.go:5:6: unreachable func: Gen
internal/database/mock_store.go:5:6: unreachable func: Mock
"""


class ParseTest(unittest.TestCase):
    def test_filters_and_identifiers(self):
        self.assertEqual(dr.parse(SAMPLE), {"internal/foo.Used", "internal/foo.T.Method"})

    def test_line_numbers_do_not_matter(self):
        moved = SAMPLE.replace(":10:6:", ":99:6:")
        self.assertEqual(dr.parse(moved), dr.parse(SAMPLE))


class CompareTest(unittest.TestCase):
    def test_new_identifier_fails(self):
        base = dr.parse(SAMPLE)
        cur = dr.parse(SAMPLE + "internal/foo/d.go:1:6: unreachable func: Fresh\n")
        new, gone = dr.compare(cur, base)
        self.assertEqual(new, ["internal/foo.Fresh"])
        self.assertEqual(gone, [])

    def test_removed_identifier_passes(self):
        base = dr.parse(SAMPLE)
        cur = dr.parse(SAMPLE.replace("internal/foo/a.go:10:6: unreachable func: Used\n", ""))
        new, gone = dr.compare(cur, base)
        self.assertEqual(new, [])
        self.assertEqual(gone, ["internal/foo.Used"])


class MainTest(unittest.TestCase):
    def _run(self, baseline_text, output_text):
        with tempfile.TemporaryDirectory() as d:
            base, out = Path(d) / "base.txt", Path(d) / "out.txt"
            base.write_text(baseline_text)
            out.write_text(output_text)
            return dr.main(["--baseline", str(base), "--input", str(out)])

    def test_exit_codes(self):
        self.assertEqual(self._run("internal/foo.Used\ninternal/foo.T.Method\n", SAMPLE), 0)
        self.assertEqual(self._run("internal/foo.Used\n", SAMPLE), 1)
        self.assertEqual(self._run("internal/foo.Used\ninternal/foo.T.Method\ninternal/foo.Gone\n", SAMPLE), 0)


if __name__ == "__main__":
    unittest.main()
