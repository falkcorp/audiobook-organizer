#!/usr/bin/env python3
# file: scripts/test_fixture_test_packages.py
# version: 1.0.0
# guid: 3c6e9a41-7f2d-4b85-9e10-d4a8b25c7f93
# last-edited: 2026-10-05

"""Tests for scripts/ci/fixture_test_packages.py, the package selector behind
`make test-fixtures` and ci.yml's fixture-tests job.

The selector fails closed on an empty list, but a detector that stopped
recognizing vptest would still return the direct testing.Short() callers, so
the list would stay non-empty while every vptest-based package (the merge,
undo and redirect state-machine tests) silently left the PR gate. These tests
run discovery against the real module and pin both ends of that chain.

Run with:  python3 -m unittest discover -s scripts -p 'test_*.py'
Needs `go` on PATH (ci.yml runs this step after setup-go).
"""

import importlib.util
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "fixture_test_packages", Path(__file__).parent / "ci" / "fixture_test_packages.py"
)
assert _spec is not None and _spec.loader is not None
ftp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ftp)

M = ftp.MODULE


class RealModuleDiscovery(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.result = ftp.discover()

    def test_vptest_is_a_helper(self):
        self.assertIn(f"{M}/internal/versionprimary/vptest", self.result.helpers)

    def test_merge_is_selected(self):
        # internal/merge's tests reach testing.Short() only through vptest.New,
        # so this proves the helper path, not just the direct-call path.
        self.assertIn(f"{M}/internal/merge", self.result.selected)

    def test_shards_cover_every_package_once(self):
        got = sorted(p for i in (1, 2, 3) for p in ftp.shard(self.result.selected, i, 3))
        self.assertEqual(got, self.result.selected)


def _pkg(path, imports=(), test_imports=(), short_in_go=False, short_in_test=False, has_tests=True):
    return {
        "ImportPath": path,
        "Dir": path,
        "GoFiles": ["short.go"] if short_in_go else ["plain.go"],
        "Imports": list(imports),
        "TestGoFiles": (["short_test.go"] if short_in_test else ["plain_test.go"]) if has_tests else [],
        "TestImports": list(test_imports),
    }


def _fake_calls_short(_directory, files):
    return any(f.startswith("short") for f in files)


class HelperClosure(unittest.TestCase):
    def test_wrapper_of_a_helper_is_a_helper_at_any_depth(self):
        pkgs = [
            _pkg("vptest", short_in_go=True, has_tests=False),
            _pkg("wrap1", imports=["vptest"], has_tests=False),
            _pkg("wrap2", imports=["wrap1"], has_tests=False),
            _pkg("unrelated", imports=["fmt"], has_tests=False),
            _pkg("uses_wrap2", test_imports=["wrap2"]),
            _pkg("uses_unrelated", test_imports=["unrelated"]),
            _pkg("direct", short_in_test=True),
        ]
        helpers = ftp.find_helpers(pkgs, _fake_calls_short)
        self.assertEqual(helpers, {"vptest", "wrap1", "wrap2"})
        self.assertEqual(ftp.select(pkgs, helpers, _fake_calls_short), ["direct", "uses_wrap2"])


if __name__ == "__main__":
    unittest.main()
