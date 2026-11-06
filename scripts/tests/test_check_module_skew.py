# file: scripts/tests/test_check_module_skew.py
# version: 1.0.0
# guid: e83babbf-1aa1-41f1-a19f-c43cf2c891fa
# last-edited: 2026-11-05
"""Tests for scripts/ci/check_module_skew.py (03 §3.2 item 6; 12 SC4(c)).

Each case builds a temp tree (core/go.sum present or not) and feeds the
check a fake ``go list -m all`` per module directory, so no test runs go.
"""

from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location("check_module_skew", ROOT / "scripts/ci/check_module_skew.py")
assert _spec and _spec.loader
cms = importlib.util.module_from_spec(_spec)
sys.modules["check_module_skew"] = cms
_spec.loader.exec_module(cms)

CORE_LIST = """github.com/falkcorp/mediacore
github.com/cockroachdb/pebble/v2 v2.1.7
golang.org/x/sync v0.18.0
"""
ROOT_LIST = """github.com/falkcorp/audiobook-organizer
github.com/cockroachdb/pebble/v2 v2.1.7
github.com/falkcorp/mediacore v0.0.0-00010101000000-000000000000 => ./core
golang.org/x/sync v0.18.0
github.com/gin-gonic/gin v1.12.0
"""


class ModuleSkewTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self._tmp.name)
        (self.root / "core").mkdir()
        (self.root / "core" / "go.sum").write_text("")
        self.lists = {self.root / "core": CORE_LIST, self.root: ROOT_LIST}

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def lister(self, d: pathlib.Path) -> dict[str, str]:
        return cms.parse_list_m(self.lists[d])

    def test_no_skew_passes(self) -> None:
        self.assertEqual(cms.check(self.root, [], self.lister), [])

    def test_skewed_module_fails_naming_it(self) -> None:
        self.lists[self.root] = ROOT_LIST.replace("golang.org/x/sync v0.18.0", "golang.org/x/sync v0.19.0")
        errors = cms.check(self.root, [], self.lister)
        self.assertEqual(len(errors), 1, errors)
        self.assertIn("golang.org/x/sync", errors[0])
        self.assertIn("v0.18.0", errors[0])
        self.assertIn("v0.19.0", errors[0])

    def test_module_missing_from_root_fails(self) -> None:
        self.lists[self.root] = ROOT_LIST.replace("golang.org/x/sync v0.18.0\n", "")
        errors = cms.check(self.root, [], self.lister)
        self.assertTrue(any("golang.org/x/sync" in e and "not the root's" in e for e in errors), errors)

    def test_missing_core_go_sum_fails(self) -> None:
        (self.root / "core" / "go.sum").unlink()
        errors = cms.check(self.root, [], self.lister)
        self.assertTrue(any("core/go.sum is missing" in e for e in errors), errors)

    def test_violated_pin_fails(self) -> None:
        pin = cms.parse_pin("github.com/cockroachdb/pebble/v2@v2.1.8")
        errors = cms.check(self.root, [pin], self.lister)
        self.assertTrue(any("--pin github.com/cockroachdb/pebble/v2@v2.1.8" in e for e in errors), errors)
        held = cms.parse_pin("github.com/cockroachdb/pebble/v2@v2.1.7")
        self.assertEqual(cms.check(self.root, [held], self.lister), [])

    def test_staticcheck_tools_modules_compared_once_core_has_one(self) -> None:
        sc_core = self.root / "core" / "tools" / "staticcheck"
        sc_root = self.root / "tools" / "staticcheck"
        sc_core.mkdir(parents=True)
        sc_root.mkdir(parents=True)
        (sc_core / "go.mod").write_text("module x\n")
        self.lists[sc_core] = "x\nhonnef.co/go/tools v0.7.0\n"
        self.lists[sc_root] = "y\nhonnef.co/go/tools v0.7.1\n"
        errors = cms.check(self.root, [], self.lister)
        self.assertTrue(any("staticcheck tools skew" in e and "honnef.co/go/tools" in e for e in errors), errors)

    def test_parse_replaced_module(self) -> None:
        mods = cms.parse_list_m("m\na v1 => b v2\nc v1 => ./local\n")
        self.assertEqual(mods, {"a": "v2", "c": "v1"})

    def test_bad_pin_is_a_usage_error(self) -> None:
        res = subprocess.run(
            [sys.executable, str(ROOT / "scripts/ci/check_module_skew.py"), "--pin", "nover"],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(res.returncode, 2, res.stderr)


if __name__ == "__main__":
    unittest.main()
