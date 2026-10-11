#!/usr/bin/env python3
# file: scripts/test_deadcode_ratchet.py
# version: 1.1.0
# guid: 2b8e41c7-6d09-4f3a-a5e2-90c1d7f3b648
# last-edited: 2026-10-11
"""Unit tests for scripts/deadcode-ratchet.py (stdlib unittest, no Go needed)."""
import importlib.util
import io
import os
import re
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

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
internal/database/dbtest/t.go:5:6: unreachable func: Make
internal/versionprimary/vptest/t.go:5:6: unreachable func: Make
internal/database/aggtest/t.go:5:6: unreachable func: Make
internal/logger/logtest/t.go:5:6: unreachable func: Make
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


class TestDirAllowListTest(unittest.TestCase):
    def test_listed_helper_dirs_are_excluded(self):
        for d in ("aggtest", "dbtest", "vptest", "logtest"):
            self.assertEqual(dr.parse(f"internal/x/{d}/t.go:1:6: unreachable func: F\n"), set(), d)

    def test_unlisted_test_suffix_dir_is_kept(self):
        # A production package that merely ends in "test" must not be dropped.
        self.assertEqual(
            dr.parse("internal/latest/l.go:1:6: unreachable func: F\n"), {"internal/latest.F"}
        )

    def test_every_existing_test_helper_dir_is_listed(self):
        root = Path(__file__).resolve().parent.parent
        found = set()
        for base in ("internal", "pkg", "cmd", "tools"):
            for dirpath, dirnames, _ in os.walk(root / base):
                for d in dirnames:
                    if d.endswith("test") and any(
                        f.endswith(".go") and not f.endswith("_test.go")
                        for f in os.listdir(os.path.join(dirpath, d))
                    ):
                        found.add(d)
        # Existing *test dirs holding non-test Go files are helper packages
        # unless they are named production packages; each must be a decision.
        self.assertLessEqual(found - dr.EXCLUDED_PKG_NAMES, set(), found)


class PinTest(unittest.TestCase):
    def test_version_matches_tools_module(self):
        gomod = (Path(__file__).resolve().parent.parent / "tools/deadcode/go.mod").read_text()
        self.assertRegex(gomod, r"golang\.org/x/tools " + re.escape(dr.DEADCODE_TOOLS_VERSION))


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


class ParseMismatchTest(unittest.TestCase):
    def test_parse_raises_on_unrecognised_line(self):
        with self.assertRaises(dr.ToolError):
            dr.parse(SAMPLE + "something unexpected\n")

    def test_main_exits_2_on_unrecognised_line(self):
        with tempfile.TemporaryDirectory() as d:
            base, out = Path(d) / "b.txt", Path(d) / "o.txt"
            base.write_text("internal/foo.Used\n")
            out.write_text("garbage that is not a finding\n")
            with redirect_stderr(io.StringIO()):
                self.assertEqual(dr.main(["--baseline", str(base), "--input", str(out)]), 2)

    def test_blank_lines_are_fine(self):
        self.assertEqual(dr.parse("\n" + SAMPLE + "\n"), dr.parse(SAMPLE))


class PruneTest(unittest.TestCase):
    def _prune(self, flag, baseline_text, output_text):
        with tempfile.TemporaryDirectory() as d:
            base, out = Path(d) / "base.txt", Path(d) / "out.txt"
            base.write_text(baseline_text)
            out.write_text(output_text)
            with redirect_stdout(io.StringIO()):
                rc = dr.main([flag, "--baseline", str(base), "--input", str(out)])
            return rc, base.read_text()

    def test_lower_only_never_adds(self):
        for flag in ("--prune", "--write-baseline"):
            rc, text = self._prune(
                flag,
                "internal/foo.Used\ninternal/foo.Gone\n",
                SAMPLE,  # current = Used + T.Method; T.Method is NEW, Gone is fixed
            )
            self.assertEqual(rc, 0)
            self.assertEqual(text, "internal/foo.Used\n", flag)

    def test_prune_with_nothing_fixed_is_a_noop(self):
        rc, text = self._prune("--prune", "internal/foo.T.Method\ninternal/foo.Used\n", SAMPLE)
        self.assertEqual((rc, text), (0, "internal/foo.T.Method\ninternal/foo.Used\n"))


class InstallFailureTest(unittest.TestCase):
    def _run_main(self, side_effect):
        with tempfile.TemporaryDirectory() as d:
            base = Path(d) / "b.txt"
            base.write_text("internal/foo.Used\n")
            err = io.StringIO()
            with mock.patch.dict(os.environ, {"DEADCODE_BIN": ""}), \
                 mock.patch.object(dr, "find_binary", return_value=None), \
                 mock.patch.object(dr.subprocess, "run", side_effect=side_effect), \
                 redirect_stderr(err):
                rc = dr.main(["--baseline", str(base)])
            return rc, err.getvalue()

    def test_install_failure_exits_2_with_message(self):
        def boom(cmd, **kw):
            if cmd[:1] == ["git"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="/tmp\n", stderr="")
            raise subprocess.CalledProcessError(1, cmd, stderr="dial tcp: proxy unreachable")

        rc, err = self._run_main(boom)
        self.assertEqual(rc, 2)
        self.assertIn("could not build deadcode", err)
        self.assertIn("make deadcode-install", err)

    def test_install_timeout_exits_2(self):
        def hang(cmd, **kw):
            if cmd[:1] == ["git"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="/tmp\n", stderr="")
            raise subprocess.TimeoutExpired(cmd, 1)

        rc, err = self._run_main(hang)
        self.assertEqual(rc, 2)
        self.assertIn("timed out", err)

    def test_wrong_tool_version_exits_2(self):
        def run(cmd, **kw):
            if cmd[:1] == ["git"]:
                return subprocess.CompletedProcess(cmd, 0, stdout="/tmp\n", stderr="")
            return subprocess.CompletedProcess(
                cmd, 0, stdout="\tmod\tgolang.org/x/tools\tv0.1.0\t\n", stderr=""
            )

        with mock.patch.object(dr, "find_binary", return_value="/x/deadcode"):
            with tempfile.TemporaryDirectory() as d:
                base = Path(d) / "b.txt"
                base.write_text("")
                err = io.StringIO()
                with mock.patch.object(dr.subprocess, "run", side_effect=run), redirect_stderr(err):
                    rc = dr.main(["--baseline", str(base)])
        self.assertEqual(rc, 2)
        self.assertIn("was not built from golang.org/x/tools", err.getvalue())


if __name__ == "__main__":
    unittest.main()
