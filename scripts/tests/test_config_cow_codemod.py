#!/usr/bin/env python3
# file: scripts/tests/test_config_cow_codemod.py
# version: 1.0.0
# guid: 259e31e7-064f-42cf-bc38-c9f7280d111a
# last-edited: 2026-11-21
"""Tests for scripts/config_cow_codemod.py (stdlib unittest).

The pure-Python parts (edit application, SetForTest folding, de-duplication,
counting) run anywhere. The end-to-end cases drive the real go/ast analyzer and
need a Go toolchain; CI's script-test job has one (it runs make types-check),
so there they run rather than skip.
"""

import importlib.util
import io
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location(
    "config_cow_codemod", ROOT / "scripts" / "config_cow_codemod.py"
)
cc = importlib.util.module_from_spec(_spec)
sys.modules["config_cow_codemod"] = cc
_spec.loader.exec_module(cc)

HAVE_GO = shutil.which("go") is not None


class ApplyEditsTest(unittest.TestCase):
    def test_applies_in_offset_order_regardless_of_input_order(self):
        src = b"aaa bbb ccc"
        out = cc.apply_edits(src, [cc.Edit(8, 11, "Z"), cc.Edit(0, 3, "X")])
        self.assertEqual(out, b"X bbb Z")

    def test_overlap_is_an_error_not_a_guess(self):
        with self.assertRaises(ValueError):
            cc.apply_edits(b"abcdef", [cc.Edit(0, 4, "x"), cc.Edit(2, 5, "y")])


class MergeAdjacentSetsTest(unittest.TestCase):
    P = "config.SetForTest(func(c *config.Config) { "

    def _set(self, start, end, body):
        return cc.Edit(start, end, f"{self.P}{body} }})")

    def test_three_adjacent_statements_fold_into_one_closure_in_order(self):
        src = b"\tA\n\tB\n\tC\n"
        edits = [
            self._set(1, 2, "c.Joker = 1"),
            self._set(4, 5, "c.Bane = 2"),
            self._set(7, 8, "c.Riddler = 3"),
        ]
        out = cc.merge_adjacent_sets(src, edits)
        self.assertEqual(len(out), 1)
        self.assertEqual(out[0].start, 1)
        self.assertEqual(out[0].end, 8)
        body = out[0].new
        self.assertLess(body.index("Joker"), body.index("Bane"))
        self.assertLess(body.index("Bane"), body.index("Riddler"))
        self.assertTrue(body.startswith("config.SetForTest(func(c *config.Config) {\n"))
        self.assertTrue(body.endswith("\n\t})"))

    def test_blank_line_or_comment_keeps_statements_apart(self):
        src = b"\tA\n\n\tB\n\t// Alfred\n\tC\n"
        edits = [
            self._set(1, 2, "c.Joker = 1"),
            self._set(5, 6, "c.Bane = 2"),
            self._set(18, 19, "c.Riddler = 3"),
        ]
        out = cc.merge_adjacent_sets(src, edits)
        self.assertEqual(len(out), 3)

    def test_read_edits_pass_through(self):
        src = b"x := config.AppConfig.RootDir\n"
        e = cc.Edit(5, 21, "config.Get()")
        self.assertEqual(cc.merge_adjacent_sets(src, [e]), [e])


class PlanAndSummaryTest(unittest.TestCase):
    def test_identical_edits_from_a_multi_assignment_are_kept_once(self):
        e = {"start": 0, "end": 10, "new": "config.SetForTest(...)"}
        a = cc.Site.from_json(
            {
                "file": "f_test.go",
                "line": 1,
                "col": 1,
                "kind": "test-set",
                "auto": True,
                "text": "",
                "edits": [e],
            }
        )
        b = cc.Site.from_json(
            {
                "file": "f_test.go",
                "line": 1,
                "col": 30,
                "kind": "test-set",
                "auto": True,
                "text": "",
                "edits": [e],
            }
        )
        plan = cc.plan_file_edits([a, b])
        self.assertEqual(len(plan["f_test.go"]), 1)

    def test_summary_splits_test_and_nontest(self):
        sites = [
            cc.Site("a.go", 1, 1, "read", True, "x"),
            cc.Site("a_test.go", 1, 1, "read", True, "x"),
            cc.Site("b.go", 1, 1, "pointer-handoff", False, "x"),
        ]
        s = cc.summarize(sites)
        self.assertEqual(s["auto"], 2)
        self.assertEqual(s["manual"], 1)
        self.assertEqual(s["by_kind"]["read"], {"nontest": 1, "test": 1})
        self.assertEqual(s["files_manual"], 1)


NONTEST = """\
package gotham

import (
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

type organizer struct{ cfg *config.Config }

func reads() (string, int) {
	root := config.AppConfig.RootDir
	n := config.AppConfig.Dedup.Signals.Confidence["isbn"]
	_ = config.AppConfig.EffectiveEmbeddingMode()
	return root, int(n)
}

func copies() config.Config { return config.AppConfig }

func handoff() *organizer { return &organizer{cfg: &config.AppConfig} }

func fieldAddr() *[]string { return &config.AppConfig.SupportedExtensions }

func mutators() {
	_ = config.AppConfig.Validate()
	_ = append(config.AppConfig.SupportedExtensions, ".m4b")
	sort.Strings(config.AppConfig.SupportedExtensions)
	_ = config.AppConfig.SupportedExtensions[1:]
	config.AppConfig.RootDir = "/tmp/batcave"
}

func shadowed() {
	config := struct{ AppConfig struct{ RootDir string } }{}
	_ = config.AppConfig.RootDir
}
"""

TESTFILE = """\
package gotham

import (
	"testing"

	cfgpkg "github.com/falkcorp/audiobook-organizer/internal/config"
)

func TestAlfred(t *testing.T) {
	orig := cfgpkg.AppConfig
	t.Cleanup(func() { cfgpkg.AppConfig = orig })
	cfgpkg.AppConfig.RootDir = "/tmp/wayne-manor"
	cfgpkg.AppConfig.EnableAIParsing = true
	cfgpkg.AppConfig.DatabaseType, cfgpkg.AppConfig.DatabasePath = "pebble", "/tmp/arkham"
	var other string
	other, cfgpkg.AppConfig.PlaylistDir = "x", "/tmp/gcpd"
	cfgpkg.AppConfig.ScanWorkers += 2
	cfgpkg.AppConfig.PlaylistDir = cfgpkg.AppConfig.RootDir
	cfgpkg.AppConfig.Hook = func() string { return cfgpkg.AppConfig.RootDir }
	_ = other
}
"""


IN_PACKAGE = """\
package config

type Config struct{ RootDir string; AppConfig int }

var AppConfig Config

func root() string { return AppConfig.RootDir }

func Snapshot() Config { return AppConfig }

func field(c Config) int { return c.AppConfig }

func local() string {
	AppConfig := Config{}
	return AppConfig.RootDir
}
"""


@unittest.skipUnless(HAVE_GO, "needs a Go toolchain to run the go/ast analyzer")
class EndToEndTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name) / "gotham"
        self.dir.mkdir()
        (self.dir / "gotham.go").write_text(NONTEST)
        (self.dir / "gotham_test.go").write_text(TESTFILE)

    def tearDown(self):
        self.tmp.cleanup()

    def sites(self, test_writes=True):
        files = cc.collect_go_files([str(self.dir)], [])
        sites, stderr, code = cc.run_analyzer(files, test_writes)
        self.assertEqual(code, 0, stderr)
        return sites

    def kinds(self, sites, fname):
        return [(s.line, s.kind, s.auto) for s in sites if s.file.endswith(fname)]

    def test_nontest_classification(self):
        got = self.kinds(self.sites(), "gotham.go")
        self.assertEqual(
            got,
            [
                (12, "read", True),
                (13, "read", True),
                (14, "read", True),  # EffectiveEmbeddingMode is in READONLY_METHODS
                (18, "value-copy", True),
                (20, "pointer-handoff", False),
                (22, "field-address", False),
                (25, "method-call", False),  # Validate normalises in place
                (26, "mutating-builtin", False),
                (27, "mutating-sort", False),
                (28, "slice-expr", False),
                (29, "write", False),
            ],
        )  # line 34 (the shadowed local named config) is not a site

    def test_test_writes_rewrite_only_the_exact_shapes(self):
        got = self.kinds(self.sites(), "gotham_test.go")
        self.assertEqual(
            got,
            [
                (10, "value-copy", True),
                (11, "test-replace", True),
                (12, "test-set", True),
                (13, "test-set", True),
                (14, "test-set", True),
                (14, "test-set", True),
                (16, "test-set", True),  # mixed targets: other is captured
                (17, "test-set", True),  # op-assignment
                (18, "test-set", True),  # right-hand side read becomes c
                (18, "read", True),
                (19, "test-set", True),  # the literal's read stays live: Get()
                (19, "read", True),
            ],
        )

    def test_without_test_writes_every_test_write_is_manual(self):
        got = [
            k
            for _, k, auto in self.kinds(self.sites(test_writes=False), "gotham_test.go")
            if not auto
        ]
        self.assertEqual(got.count("write"), 9)

    def test_every_test_write_in_the_fixture_is_rewritten(self):
        manual = [s for s in self.sites() if not s.auto and s.file.endswith("_test.go")]
        self.assertEqual(manual, [])

    def test_write_rewrites_and_a_second_run_is_a_no_op(self):
        buf = io.StringIO()
        with redirect_stdout(buf):
            rc = cc.main(["--write", "--test-writes", str(self.dir)])
        self.assertEqual(rc, 0)
        nontest = (self.dir / "gotham.go").read_text()
        self.assertIn("root := config.Get().RootDir", nontest)
        self.assertIn('config.Get().Dedup.Signals.Confidence["isbn"]', nontest)
        self.assertIn("return config.Snapshot()", nontest)
        self.assertIn("&config.AppConfig}", nontest)  # refused, untouched
        self.assertIn('config.AppConfig.RootDir = "/tmp/batcave"', nontest)
        test = (self.dir / "gotham_test.go").read_text()
        self.assertIn("orig := cfgpkg.Snapshot()", test)
        self.assertIn("cfgpkg.ReplaceForTest(orig)", test)
        # Two adjacent single writes and the all-config multi-assignment fold
        # into one closure, in source order.
        self.assertIn(
            "\tcfgpkg.SetForTest(func(c *cfgpkg.Config) {\n"
            '\t\tc.RootDir = "/tmp/wayne-manor"\n'
            "\t\tc.EnableAIParsing = true\n"
            '\t\tc.DatabaseType, c.DatabasePath = "pebble", "/tmp/arkham"\n'
            "\t})\n",
            test,
        )
        self.assertIn('other, c.PlaylistDir = "x", "/tmp/gcpd"', test)
        self.assertIn("c.ScanWorkers += 2", test)
        self.assertIn("c.PlaylistDir = c.RootDir", test)
        self.assertIn("c.Hook = func() string { return cfgpkg.Get().RootDir }", test)
        gofmt = subprocess.run(
            ["gofmt", "-l", str(self.dir)], capture_output=True, text=True, check=False
        )
        self.assertEqual(gofmt.stdout.strip(), "")

        again = [s for s in self.sites() if s.auto]
        self.assertEqual(again, [], "a second run must propose nothing")

    def test_in_package_bare_identifier(self):
        pkg = Path(self.tmp.name) / "internal" / "config"
        pkg.mkdir(parents=True)
        (pkg / "x.go").write_text(IN_PACKAGE)
        sites, stderr, code = cc.run_analyzer([str(pkg / "x.go")], False)
        self.assertEqual(code, 0, stderr)
        self.assertEqual([(s.line, s.kind) for s in sites], [(7, "read"), (9, "value-copy")])
        self.assertEqual(sites[0].edits[0].new, "Get()")
        # Not Snapshot(): inside package config that is the function being
        # rewritten, and it would call itself.
        self.assertEqual(sites[1].edits[0].new, "*Get()")


if __name__ == "__main__":
    unittest.main()
