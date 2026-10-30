#!/usr/bin/env python3
# file: scripts/test_fixture_test_packages.py
# version: 1.2.0
# guid: 3c6e9a41-7f2d-4b85-9e10-d4a8b25c7f93
# last-edited: 2026-10-29

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

    def test_shards_cover_every_unit_once(self):
        got = sorted(u for i in (1, 2, 3) for u in ftp.shard(self.result.selected, i, 3))
        self.assertEqual(got, sorted(ftp.units(self.result.selected)))

    def test_every_package_runs_in_some_shard(self):
        ran = {u.pkg for i in (1, 2, 3) for u in ftp.shard(self.result.selected, i, 3)}
        self.assertEqual(sorted(ran), self.result.selected)

    def test_the_heavy_packages_are_split(self):
        # internal/server ran as one ~20 minute process against a 25 minute
        # timeout (pipeline 1823, 2026-10-29): it must stay split.
        self.assertGreater(ftp.split_count(f"{M}/internal/server"), 1)
        self.assertGreater(ftp.split_count(f"{M}/internal/plugins/maintenance"), 1)

    def test_no_shard_carries_much_more_than_its_share(self):
        loads = [ftp.shard_load(ftp.shard(self.result.selected, i, 3)) for i in (1, 2, 3)]
        mean = sum(loads) / 3
        self.assertLessEqual(max(loads), mean * 1.15, loads)

    def test_no_unit_is_heavier_than_the_split_target(self):
        for u in ftp.units(self.result.selected):
            self.assertLessEqual(ftp.weight(u.pkg) / u.parts, ftp._SPLIT_TARGET, str(u))


def _pkg(
    path,
    imports=(),
    test_imports=(),
    xtest_imports=(),
    short_in_go=False,
    short_in_test=False,
    has_tests=True,
    has_xtests=False,
):
    """A fake `go list -json` entry. has_tests gives it in-package _test.go
    files; has_xtests gives it external (package foo_test) ones, whose imports
    go list reports separately as XTestImports."""
    return {
        "ImportPath": path,
        "Dir": path,
        "GoFiles": ["short.go"] if short_in_go else ["plain.go"],
        "Imports": list(imports),
        "TestGoFiles": (["short_test.go"] if short_in_test else ["plain_test.go"])
        if has_tests
        else [],
        "TestImports": list(test_imports),
        "XTestGoFiles": ["plain_x_test.go"] if has_xtests else [],
        "XTestImports": list(xtest_imports),
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


class Selection(unittest.TestCase):
    def test_helper_with_its_own_tests_is_selected(self):
        # vptest's own tests import nothing that is a helper (TestImports never
        # lists the package itself), so only the helper rule can select them.
        pkgs = [
            _pkg("vptest", short_in_go=True),
            _pkg("vptest_x_only", imports=["vptest"], has_tests=False, has_xtests=True),
            _pkg("helper_no_tests", imports=["vptest"], has_tests=False),
        ]
        helpers = ftp.find_helpers(pkgs, _fake_calls_short)
        self.assertEqual(helpers, {"vptest", "vptest_x_only", "helper_no_tests"})
        self.assertEqual(ftp.select(pkgs, helpers, _fake_calls_short), ["vptest", "vptest_x_only"])

    def test_package_importing_helper_only_from_external_tests_is_selected(self):
        pkgs = [
            _pkg("vptest", short_in_go=True, has_tests=False),
            _pkg("xonly", has_tests=False, has_xtests=True, xtest_imports=["vptest"]),
            _pkg("xunrelated", has_tests=False, has_xtests=True, xtest_imports=["fmt"]),
        ]
        helpers = ftp.find_helpers(pkgs, _fake_calls_short)
        self.assertEqual(ftp.select(pkgs, helpers, _fake_calls_short), ["xonly"])


class PartOf(unittest.TestCase):
    def test_is_stable_across_processes(self):
        # sha256, not hash(): the three shard workflows are three processes (on
        # two OSes) and must agree. These values pin that.
        self.assertEqual(
            [ftp.part_of(n, 3) for n in ("TestA", "TestB", "TestC", "TestD", "TestE", "TestF")],
            [1, 0, 1, 1, 0, 0],
        )

    def test_spreads_names_evenly(self):
        counts = [0, 0, 0]
        for i in range(3000):
            counts[ftp.part_of(f"TestSomething{i}", 3)] += 1
        for c in counts:
            self.assertTrue(900 < c < 1100, counts)


class Units(unittest.TestCase):
    def test_unit_labels(self):
        self.assertEqual(str(ftp.Unit("p", 0, 1)), "p")
        self.assertEqual(str(ftp.Unit("p", 1, 3)), "p#2/3")

    def test_split_count_rounds_up_and_never_drops_below_one(self):
        orig = dict(ftp._WEIGHTS)
        try:
            ftp._WEIGHTS.update({"light": 1, "exact": ftp._SPLIT_TARGET, "over": ftp._SPLIT_TARGET + 1})
            self.assertEqual(ftp.split_count(f"{M}/light"), 1)
            self.assertEqual(ftp.split_count(f"{M}/exact"), 1)
            self.assertEqual(ftp.split_count(f"{M}/over"), 2)
        finally:
            ftp._WEIGHTS.clear()
            ftp._WEIGHTS.update(orig)

    def test_shard_is_deterministic_and_complete_for_a_split_package(self):
        orig = dict(ftp._WEIGHTS)
        try:
            ftp._WEIGHTS.update({"big": 3 * ftp._SPLIT_TARGET, "small": 5})
            pkgs = [f"{M}/big", f"{M}/small"]
            first = [ftp.shard(pkgs, i, 3) for i in (1, 2, 3)]
            self.assertEqual(first, [ftp.shard(pkgs, i, 3) for i in (1, 2, 3)])
            got = sorted(u for s in first for u in s)
            self.assertEqual(got, sorted(ftp.units(pkgs)))
            # The three parts of "big" are what spreads it over all three shards.
            self.assertEqual(sum(1 for s in first for u in s if u.pkg.endswith("/big")), 3)
        finally:
            ftp._WEIGHTS.clear()
            ftp._WEIGHTS.update(orig)


class Invocations(unittest.TestCase):
    def setUp(self):
        self._orig = dict(ftp._WEIGHTS)
        ftp._WEIGHTS.update({"big": 2 * ftp._SPLIT_TARGET, "small": 5, "small2": 5})
        self.names = {f"{M}/big": [f"TestBig{i}" for i in range(40)]}

    def tearDown(self):
        ftp._WEIGHTS.clear()
        ftp._WEIGHTS.update(self._orig)

    def _fake_list(self, pkgs, go_args):
        self.listed = (list(pkgs), list(go_args))
        return {p: self.names[p] for p in pkgs}

    def test_whole_packages_share_one_go_test_and_parts_get_run_patterns(self):
        us = [ftp.Unit(f"{M}/small", 0, 1), ftp.Unit(f"{M}/small2", 0, 1),
              ftp.Unit(f"{M}/big", 0, 2), ftp.Unit(f"{M}/big", 1, 2)]
        cmds = ftp.invocations(us, ["-race", "-count=1"], self._fake_list)
        self.assertEqual(len(cmds), 3)
        whole = cmds[0][1]
        self.assertEqual(whole, ["go", "test", "-race", "-count=1", f"{M}/small", f"{M}/small2"])
        self.assertEqual(self.listed, ([f"{M}/big"], ["-race", "-count=1"]))
        ran = []
        for _label, argv in cmds[1:]:
            self.assertEqual(argv[:4], ["go", "test", "-race", "-count=1"])
            self.assertEqual(argv[4], "-run")
            self.assertEqual(argv[-1], f"{M}/big")
            pat = argv[5]
            self.assertTrue(pat.startswith("^(") and pat.endswith(")$"))
            ran += pat[2:-2].split("|")
        self.assertEqual(sorted(ran), sorted(self.names[f"{M}/big"]))

    def test_a_shard_with_only_whole_packages_never_lists_tests(self):
        def boom(pkgs, go_args):
            raise AssertionError("listed")

        cmds = ftp.invocations([ftp.Unit(f"{M}/small", 0, 1)], ["-race"], boom)
        self.assertEqual(len(cmds), 1)

    def test_a_part_with_no_tests_is_an_error_not_a_silent_ok(self):
        self.names = {f"{M}/big": ["TestOnlyOne"]}
        k = ftp.part_of("TestOnlyOne", 2)
        empty = ftp.Unit(f"{M}/big", 1 - k, 2)
        with self.assertRaises(SystemExit):
            ftp.invocations([empty], [], self._fake_list)


class RunInvocations(unittest.TestCase):
    def test_exit_code_is_failure_if_any_command_fails(self):
        self.assertEqual(ftp.run_invocations([("a", ["true"]), ("b", ["true"])]), 0)
        self.assertEqual(ftp.run_invocations([("a", ["true"]), ("b", ["false"])]), 1)

    def test_a_run_that_matched_no_tests_fails_even_though_go_exits_zero(self):
        cmd = ["sh", "-c", "echo 'ok  \tpkg\t0.01s [no tests to run]'"]
        self.assertEqual(ftp.run_invocations([("a", cmd)]), 1)


if __name__ == "__main__":
    unittest.main()
