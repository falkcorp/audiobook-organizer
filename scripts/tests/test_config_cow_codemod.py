#!/usr/bin/env python3
# file: scripts/tests/test_config_cow_codemod.py
# version: 0.1.0
# guid: 7f3a2c58-91d4-4e6b-b2a0-6d9e1c4f8b37
# last-edited: 2026-11-22
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

    def test_skip_field_leaves_nontest_class_i_reads_alone(self):
        def site(f, text):
            return cc.Site(f, 1, 1, "read", True, text, edits=[cc.Edit(0, 1, "x")])

        sites = [
            site("a.go", "config.AppConfig.RootDir"),
            site("a.go", "config.AppConfig.RootDirs"),  # a different field
            site("a.go", "cfg.AppConfig.ITunes.PathMappings[0].From"),
            site("a.go", "config.AppConfig.ITunes.LibraryReadPath"),
            site("a.go", "config.AppConfig.DownloadClient.Torrent.Deluge.Host"),
            site("a_test.go", "config.AppConfig.RootDir"),  # tests are never skipped
        ]
        cc.skip_fields(sites, ["RootDir", "ITunes.PathMappings", "DownloadClient"])
        kinds = [s.kind for s in sites]
        self.assertEqual(
            kinds, ["skipped-field", "read", "skipped-field", "read", "skipped-field", "read"]
        )
        for s in sites:
            if s.kind == "skipped-field":
                self.assertFalse(s.auto)
                self.assertEqual(s.edits, [])
        self.assertEqual(cc.plan_file_edits(sites).get("a.go", []), [cc.Edit(0, 1, "x")] * 1)
        summary = cc.summarize(sites)
        self.assertEqual(summary["skipped"], 3)
        self.assertEqual(summary["manual"], 0)
        self.assertEqual(cc.format_manual(sites), [])

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


LOOP_TEST = """\
package gotham

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func TestRobin(t *testing.T) {
	for _, c := range []struct{ on bool }{{true}} {
		config.AppConfig.EnableAIParsing = c.on
	}
}
"""

HOIST_TEST = """\
package gotham

import (
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func mint(s string) string { return s + "/gotham" }

func TestBatgirl(t *testing.T) {
	cowV1 := "already spelled"
	config.AppConfig.RootDir = t.TempDir()
	config.AppConfig.PlaylistDir = mint(config.AppConfig.RootDir)
	config.AppConfig.ITunes.PathMappings[len(cowV1)].To = "/tmp/arkham"
	config.AppConfig.DatabaseType += strconv.Itoa(2)
	if config.AppConfig.DatabasePath = mint("x"); true {
	}
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

    def test_closure_parameter_never_shadows_a_name_the_statement_uses(self):
        (self.dir / "loop_test.go").write_text(LOOP_TEST)
        buf = io.StringIO()
        with redirect_stdout(buf):
            self.assertEqual(
                cc.main(["--write", "--test-writes", str(self.dir / "loop_test.go")]), 0
            )
        out = (self.dir / "loop_test.go").read_text()
        self.assertIn(
            "config.SetForTest(func(cfg *config.Config) { cfg.EnableAIParsing = c.on })", out
        )

    def test_a_right_hand_side_that_calls_is_hoisted_out_of_the_lock(self):
        (self.dir / "hoist_test.go").write_text(HOIST_TEST)
        sites = [s for s in self.sites() if s.file.endswith("hoist_test.go")]
        manual = sorted((s.line, s.detail) for s in sites if not s.auto)
        self.assertEqual([line for line, _ in manual], [16, 18])
        self.assertIn("call on the left-hand side", manual[0][1])
        self.assertIn("cannot be split", manual[1][1])
        buf = io.StringIO()
        with redirect_stdout(buf):
            self.assertEqual(
                cc.main(["--write", "--test-writes", str(self.dir / "hoist_test.go")]), 0
            )
        out = (self.dir / "hoist_test.go").read_text()
        # Two hoists in one block get distinct temporaries, and cowV1 is
        # skipped because the file already spells it.
        self.assertIn(
            "\tcowV2 := t.TempDir()\n\tconfig.SetForTest(func(c *config.Config) { c.RootDir = cowV2 })",
            out,
        )
        self.assertIn(
            "\tcowV3 := mint(config.Get().RootDir)\n\tconfig.SetForTest(func(c *config.Config) { c.PlaylistDir = cowV3 })",
            out,
        )
        self.assertIn("cowV4 := strconv.Itoa(2)", out)
        self.assertIn("config.SetForTest(func(c *config.Config) { c.DatabaseType += cowV4 })", out)
        gofmt = subprocess.run(
            ["gofmt", "-l", str(self.dir)], capture_output=True, text=True, check=False
        )
        self.assertEqual(gofmt.stdout.strip(), "")
        self.assertEqual(
            [s for s in self.sites() if s.auto and s.file.endswith("hoist_test.go")], []
        )

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
