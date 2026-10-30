#!/usr/bin/env python3
# file: scripts/test_fixture_test_packages.py
# version: 1.3.0
# guid: 3c6e9a41-7f2d-4b85-9e10-d4a8b25c7f93
# last-edited: 2026-10-30

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

import contextlib
import importlib.util
import io
import os
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from pathlib import Path
from unittest import mock

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

    def test_no_two_parts_of_one_package_share_a_shard(self):
        # Splitting is there to spread a package over agents; two parts in one
        # shard would be back on one agent, under one budget. Every split
        # package has at most 3 parts, and there are 3 shards.
        for i in (1, 2, 3):
            pkgs = [u.pkg for u in ftp.shard(self.result.selected, i, 3)]
            self.assertEqual(sorted(pkgs), sorted(set(pkgs)), f"shard {i}")

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
    def setUp(self):
        self._orig = dict(ftp._WEIGHTS)

    def tearDown(self):
        ftp._WEIGHTS.clear()
        ftp._WEIGHTS.update(self._orig)

    def test_unit_labels(self):
        self.assertEqual(str(ftp.Unit("p", 0, 1)), "p")
        self.assertEqual(str(ftp.Unit("p", 1, 3)), "p#2/3")

    def test_split_count_rounds_up_and_never_drops_below_one(self):
        ftp._WEIGHTS.update({"light": 1, "exact": ftp._SPLIT_TARGET, "over": ftp._SPLIT_TARGET + 1})
        self.assertEqual(ftp.split_count(f"{M}/light"), 1)
        self.assertEqual(ftp.split_count(f"{M}/exact"), 1)
        self.assertEqual(ftp.split_count(f"{M}/over"), 2)

    def test_shard_is_deterministic_and_complete_for_a_split_package(self):
        ftp._WEIGHTS.update({"big": 3 * ftp._SPLIT_TARGET, "small": 5})
        pkgs = [f"{M}/big", f"{M}/small"]
        first = [ftp.shard(pkgs, i, 3) for i in (1, 2, 3)]
        self.assertEqual(first, [ftp.shard(pkgs, i, 3) for i in (1, 2, 3)])
        got = sorted(u for s in first for u in s)
        self.assertEqual(got, sorted(ftp.units(pkgs)))
        # One part of "big" in each shard, none doubled up.
        self.assertEqual(
            [sum(1 for u in s if u.pkg.endswith("/big")) for s in first],
            [1, 1, 1],
        )

    def test_parts_of_one_package_avoid_each_others_shard(self):
        # Plain longest-first dealing puts the second part of "a" back on the
        # shard that took the first: b and c fill shards 0 and 1 at 600, "a"
        # (two parts of 500) takes shard 2 at 500, and 500 on shard 2 is then
        # the lightest again. The sibling rule sends it to a shard with none.
        ftp._WEIGHTS.update({"a": 1000, "b": 600, "c": 600})
        pkgs = [f"{M}/a", f"{M}/b", f"{M}/c"]
        homes = [i for i in (1, 2, 3) if any(u.pkg.endswith("/a") for u in ftp.shard(pkgs, i, 3))]
        self.assertEqual(len(homes), 2, homes)

    def test_more_parts_than_shards_still_places_every_part(self):
        ftp._WEIGHTS.update({"a": 4 * ftp._SPLIT_TARGET})
        pkgs = [f"{M}/a"]
        got = sorted(u for i in (1, 2) for u in ftp.shard(pkgs, i, 2))
        self.assertEqual(got, sorted(ftp.units(pkgs)))
        self.assertEqual([len(ftp.shard(pkgs, i, 2)) for i in (1, 2)], [2, 2])


class Concurrency(unittest.TestCase):
    def test_slices_and_the_whole_run_stay_inside_the_budget(self):
        for budget in range(2, 65):
            for n in range(0, 12):
                pool, p = ftp.plan_concurrency(n, budget)
                self.assertGreaterEqual(p, 1)
                self.assertLessEqual(pool, n)
                if n:
                    self.assertGreaterEqual(pool, 1)
                    self.assertLessEqual(pool + p, budget, (n, budget))
                else:
                    self.assertEqual(p, budget)

    def test_known_plans(self):
        self.assertEqual(ftp.plan_concurrency(5, 8), (4, 4))
        self.assertEqual(ftp.plan_concurrency(2, 8), (2, 6))
        self.assertEqual(ftp.plan_concurrency(5, 2), (1, 1))
        # One CPU still runs one slice and one package at a time: the floor.
        self.assertEqual(ftp.plan_concurrency(3, 1), (1, 1))

    def test_job_budget_reads_fixture_jobs_then_cpu_count(self):
        with mock.patch.dict(os.environ, {"FIXTURE_JOBS": "6"}):
            self.assertEqual(ftp.job_budget(), 6)
        env = {k: v for k, v in os.environ.items() if k != "FIXTURE_JOBS"}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(ftp.os, "cpu_count", return_value=12):
                self.assertEqual(ftp.job_budget(), 12)
            with mock.patch.object(ftp.os, "cpu_count", return_value=None):
                self.assertEqual(ftp.job_budget(), 1)
        for bad in ("0", "-2", "many"):
            with mock.patch.dict(os.environ, {"FIXTURE_JOBS": bad}), self.assertRaises(SystemExit):
                ftp.job_budget()


class Invocations(unittest.TestCase):
    def setUp(self):
        self._orig = dict(ftp._WEIGHTS)
        ftp._WEIGHTS.update(
            {"big": 2 * ftp._SPLIT_TARGET, "bigger": 3 * ftp._SPLIT_TARGET, "small": 5, "small2": 5}
        )
        self.names = {
            f"{M}/big": [f"TestBig{i}" for i in range(40)],
            f"{M}/bigger": [f"TestBigger{i}" for i in range(60)],
        }

    def tearDown(self):
        ftp._WEIGHTS.clear()
        ftp._WEIGHTS.update(self._orig)

    def _fake_list(self, pkgs, go_args):
        self.listed = (list(pkgs), list(go_args))
        return {p: self.names[p] for p in pkgs}

    def test_whole_packages_share_one_go_test_and_parts_get_run_patterns(self):
        us = [
            ftp.Unit(f"{M}/small", 0, 1),
            ftp.Unit(f"{M}/small2", 0, 1),
            ftp.Unit(f"{M}/big", 0, 2),
            ftp.Unit(f"{M}/big", 1, 2),
        ]
        names = ftp.list_split(us, ["-race", "-count=1"], self._fake_list)
        invs = ftp.invocations(us, ["-race", "-count=1"], names, budget=8)
        self.assertEqual(len(invs), 3)
        whole = invs[0]
        self.assertFalse(whole.is_slice)
        # Two slices: pool 2, so the whole run gets -p 6 of the budget of 8.
        self.assertEqual(
            whole.argv,
            ["go", "test", "-race", "-count=1", "-p", "6", f"{M}/small", f"{M}/small2"],
        )
        self.assertEqual(self.listed, ([f"{M}/big"], ["-race", "-count=1"]))
        ran = []
        for inv in invs[1:]:
            self.assertTrue(inv.is_slice)
            self.assertEqual(inv.argv[:4], ["go", "test", "-race", "-count=1"])
            self.assertEqual(inv.argv[4], "-run")
            self.assertEqual(inv.argv[-1], f"{M}/big")
            pat = inv.argv[5]
            self.assertTrue(pat.startswith("^(") and pat.endswith(")$"))
            picked = pat[2:-2].split("|")
            self.assertEqual(inv.tests, len(picked))
            self.assertEqual(inv.names_hash, ftp.names_hash(picked))
            ran += picked
        self.assertEqual(sorted(ran), sorted(self.names[f"{M}/big"]))

    def test_the_whole_run_p_leaves_room_for_the_slice_pool(self):
        us = [ftp.Unit(f"{M}/small", 0, 1)] + [ftp.Unit(f"{M}/bigger", i, 3) for i in range(3)]
        names = ftp.list_split(us, [], self._fake_list)
        for budget in (2, 3, 8, 16):
            invs = ftp.invocations(us, [], names, budget)
            pool, p = ftp.plan_concurrency(3, budget)
            argv = invs[0].argv
            self.assertEqual(argv[argv.index("-p") + 1], str(p))
            self.assertLessEqual(pool + p, max(budget, 2))

    def test_slices_are_ordered_heaviest_first(self):
        ftp._WEIGHTS.update({"mid": 1000})
        self.names[f"{M}/mid"] = [f"TestMid{i}" for i in range(30)]
        us = [ftp.Unit(f"{M}/mid", i, 2) for i in range(2)] + [
            ftp.Unit(f"{M}/bigger", i, 3) for i in range(3)
        ]
        names = ftp.list_split(us, [], self._fake_list)
        invs = ftp.invocations(us, [], names, 8)
        weights = [i.weight for i in invs if i.is_slice]
        self.assertEqual(weights, sorted(weights, reverse=True))
        self.assertEqual(weights, [600.0, 600.0, 600.0, 500.0, 500.0])

    def test_a_shard_with_only_whole_packages_never_lists_tests(self):
        def boom(pkgs, go_args):
            raise AssertionError("listed")

        us = [ftp.Unit(f"{M}/small", 0, 1)]
        self.assertEqual(ftp.list_split(us, ["-race"], boom), {})
        invs = ftp.invocations(us, ["-race"], {}, 4)
        self.assertEqual(len(invs), 1)
        # No slices: the whole run owns the budget.
        self.assertEqual(invs[0].argv[:5], ["go", "test", "-race", "-p", "4"])

    def test_a_part_with_no_tests_is_an_error_not_a_silent_ok(self):
        self.names = {f"{M}/big": ["TestOnlyOne"]}
        k = ftp.part_of("TestOnlyOne", 2)
        empty = ftp.Unit(f"{M}/big", 1 - k, 2)
        names = ftp.list_split([empty], [], self._fake_list)
        with self.assertRaises(SystemExit):
            ftp.invocations([empty], [], names, 4)

    def test_names_hash_ignores_order_and_changes_with_the_set(self):
        self.assertEqual(ftp.names_hash(["TestA", "TestB"]), ftp.names_hash(["TestB", "TestA"]))
        self.assertNotEqual(ftp.names_hash(["TestA", "TestB"]), ftp.names_hash(["TestA", "TestC"]))


class RunInvocations(unittest.TestCase):
    def _run(self, invs, budget=4):
        out = io.StringIO()
        with tempfile.TemporaryDirectory() as d, contextlib.redirect_stdout(out):
            rc = ftp.run_invocations(invs, budget, Path(d) / "logs")
        return rc, out.getvalue()

    @staticmethod
    def _inv(label, argv, is_slice=True, weight=1.0):
        return ftp.Invocation(label, argv, is_slice, weight)

    def test_exit_code_is_failure_if_any_command_fails(self):
        a, b = self._inv("a", ["true"]), self._inv("b", ["true"])
        self.assertEqual(self._run([a, b])[0], 0)
        self.assertEqual(self._run([a, self._inv("b", ["false"])])[0], 1)

    def test_a_slice_that_matched_no_tests_fails_even_though_go_exits_zero(self):
        cmd = ["sh", "-c", "echo 'ok  \tpkg\t0.01s [no tests to run]'"]
        rc, out = self._run([self._inv("a", cmd)])
        self.assertEqual(rc, 1)
        self.assertIn("ran no tests", out)

    def test_the_whole_package_run_is_not_failed_for_that_marker(self):
        cmd = ["sh", "-c", "echo 'ok  \tpkg\t0.01s [no tests to run]'"]
        self.assertEqual(self._run([self._inv("a", cmd, is_slice=False)])[0], 0)

    def test_output_is_logged_to_a_file_and_printed_when_the_child_ends(self):
        with tempfile.TemporaryDirectory() as d:
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                ftp.run_invocations(
                    [self._inv("pkg#1/2", ["sh", "-c", "echo hello-log; exit 3"])], 4, Path(d)
                )
            logs = list(Path(d).glob("*.log"))
            self.assertEqual(len(logs), 1)
            self.assertIn("hello-log", logs[0].read_text())
        self.assertIn("=== pkg#1/2: FAIL", out.getvalue())
        self.assertIn("hello-log", out.getvalue())

    def _peak_concurrency(self, n, budget):
        with tempfile.TemporaryDirectory() as d:
            child = textwrap.dedent(
                """
                import os, sys, time
                d = sys.argv[1]
                mark = os.path.join(d, "live-%d" % os.getpid())
                open(mark, "w").close()
                seen = len([f for f in os.listdir(d) if f.startswith("live-")])
                with open(os.path.join(d, "seen-%d" % os.getpid()), "w") as f:
                    f.write(str(seen))
                time.sleep(0.4)
                os.remove(mark)
                """
            )
            invs = [self._inv(f"s{i}", [sys.executable, "-c", child, d]) for i in range(n)]
            rc, _ = self._run(invs, budget)
            self.assertEqual(rc, 0)
            return max(int(p.read_text()) for p in Path(d).glob("seen-*"))

    def test_slices_never_run_more_at_once_than_the_pool(self):
        # budget 4 -> pool 2.
        peak = self._peak_concurrency(6, 4)
        self.assertLessEqual(peak, 2)
        self.assertEqual(peak, 2, "the pool never ran two slices side by side")

    def test_a_one_cpu_budget_runs_slices_one_at_a_time(self):
        self.assertEqual(self._peak_concurrency(3, 1), 1)

    def test_slices_start_in_the_order_given(self):
        with tempfile.TemporaryDirectory() as d:
            order = Path(d) / "order"
            invs = [
                self._inv(f"s{i}", ["sh", "-c", f"echo {i} >> {order}"], weight=10 - i)
                for i in range(5)
            ]
            self._run(invs, budget=2)  # pool 1
            self.assertEqual(order.read_text().split(), ["0", "1", "2", "3", "4"])

    def test_sigterm_kills_the_children_and_prints_what_they_wrote(self):
        harness = textwrap.dedent(
            f"""
            import importlib.util, sys
            from pathlib import Path
            spec = importlib.util.spec_from_file_location("ftp", {str(Path(ftp.__file__))!r})
            m = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(m)
            inv = m.Invocation("slow#1/2", ["sh", "-c", "echo partial-output; exec sleep 60"], True, 1.0)
            sys.exit(m.run_invocations([inv], 4, Path(sys.argv[1])))
            """
        )
        with tempfile.TemporaryDirectory() as d:
            proc = subprocess.Popen(
                [sys.executable, "-c", harness, d],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
            )
            try:
                proc.stdout.readline()  # "child logs in ..."
                deadline = 100
                while deadline and "partial-output" not in "".join(
                    p.read_text() for p in Path(d).glob("*.log")
                ):
                    deadline -= 1
                    time.sleep(0.1)
                proc.send_signal(signal.SIGTERM)
                out, _ = proc.communicate(timeout=30)
            finally:
                if proc.poll() is None:
                    proc.kill()
            self.assertEqual(proc.returncode, 128 + signal.SIGTERM)
            self.assertIn("INTERRUPTED", out)
            self.assertIn("partial-output", out)


class StaticTestNames(unittest.TestCase):
    def test_finds_top_level_tests_and_fuzzers_only(self):
        src = textwrap.dedent(
            """
            package x
            func TestPlain(t *testing.T) {}
            func TestÄrger(t *testing.T) {}
            func TestMain(m *testing.M) {}
            func Testlower(t *testing.T) {}
            func testHelper(t *testing.T) {}
            func FuzzThing(f *testing.F) {}
            func (s *S) TestMethod(t *testing.T) {}
            func BenchmarkB(b *testing.B) {}
            func TestWithGap( tt *testing.T ) {}
            """
        )
        with tempfile.TemporaryDirectory() as d:
            (Path(d) / "a_test.go").write_text(src, encoding="utf-8")
            fake = [{"ImportPath": "m/x", "Dir": d, "TestGoFiles": ["a_test.go"]}]
            with mock.patch.object(ftp, "_go_list_json", return_value=fake) as lj:
                got = ftp.static_test_names(Path(d), ["m/x"])
            self.assertEqual(lj.call_args[0][2], ftp._DISCOVERY_ENV)
        self.assertEqual(got, {"m/x": {"TestPlain", "TestÄrger", "FuzzThing", "TestWithGap"}})

    def test_missing_here_reports_linux_only_tests_and_ignores_extras_and_examples(self):
        native = {"m/x": ["TestA", "TestNativeOnly", "ExampleX"], "m/y": ["TestB"]}
        discovery = {"m/x": {"TestA", "TestLinuxOnly", "FuzzLinux"}, "m/y": {"TestB"}}
        self.assertEqual(
            ftp.missing_here(native, discovery), {"m/x": ["FuzzLinux", "TestLinuxOnly"]}
        )


if __name__ == "__main__":
    unittest.main()
