# file: scripts/tests/test_short_test_shards.py
# version: 1.4.0
# guid: e4a7c1b9-58d2-4f36-a0c3-2d9b6f8e1a74
# last-edited: 2026-11-08
"""Tests for scripts/ci/short_test_shards.py: plan completeness, determinism, merge.

The shard plan is what decides whether a test gates a PR at all: a name that
falls out of every split group would simply never run, and the PR would stay
green. These pin the guards against that (the completeness check and the
empty-shard failure) with a stubbed `go test -list`, so no Go toolchain or
network is needed.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from dataclasses import replace
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "ci"))

import short_test_shards as sts  # noqa: E402

MOD = "example.com/m"


def _pkg(key: str, has_tests: bool = True) -> sts.Package:
    return sts.Package(f"{MOD}/{key}", key, has_tests)


# Two heavy packages that get split, a few light ones, one without tests.
PKGS = [
    _pkg("big"),
    _pkg("huge"),
    _pkg("small/a"),
    _pkg("small/b"),
    _pkg("small/c"),
    _pkg("notests", has_tests=False),
]
NAMES = {
    f"{MOD}/big": [f"TestBig{i:02d}" for i in range(20)],
    f"{MOD}/huge": [f"TestHuge{i:02d}" for i in range(30)] + ["FuzzHuge", "ExampleHuge"],
    f"{MOD}/small/a": ["TestA1", "TestA2"],
    f"{MOD}/small/b": ["TestB1"],
    f"{MOD}/small/c": ["TestC1", "TestC2", "TestC3"],
}
WEIGHTS = {"big": 320, "huge": 470, "small/a": 40}
TIMINGS = {"huge": {"TestHuge00": 120.0, "TestHuge01": 80.0}}


def stub_lister(paths: list[str], go_args: list[str]) -> dict[str, list[str]]:
    return {p: sorted(NAMES.get(p, [])) for p in paths}


def plan(count: int = 4) -> tuple[list[list[sts.Unit]], dict[str, list[str]]]:
    units, listed = sts.build_units(PKGS, WEIGHTS, TIMINGS, ["-short", "-race"], stub_lister)
    return sts.assign(units, count), listed


class PlanTest(unittest.TestCase):
    def test_heavy_packages_are_split_light_ones_are_whole(self) -> None:
        bins, listed = plan()
        units = [u for b in bins for u in b]
        groups = {}
        for u in units:
            groups.setdefault(u.pkg.key, []).append(u.group)
        self.assertEqual(sorted(groups["big"]), [1, 2, 3])  # ceil(320 / 150)
        self.assertEqual(sorted(groups["huge"]), [1, 2, 3, 4])  # ceil(470 / 150)
        for key in ("small/a", "small/b", "small/c", "notests"):
            self.assertEqual(groups[key], [0])
        # Only split packages are listed; the rest run whole.
        self.assertEqual(sorted(listed), [f"{MOD}/big", f"{MOD}/huge"])

    def test_split_weights_add_up_to_the_package_weight(self) -> None:
        bins, _ = plan()
        units = [u for b in bins for u in b]
        self.assertAlmostEqual(sum(u.weight for u in units if u.pkg.key == "huge"), 470.0)
        self.assertAlmostEqual(sum(u.weight for u in units if u.pkg.key == "big"), 320.0)

    def test_missing_timings_key_falls_back_to_package_weight(self) -> None:
        # "big" has a weight but no timings key: it is still split, by count.
        bins, _ = plan()
        sizes = sorted(len(u.names) for b in bins for u in b if u.pkg.key == "big")
        self.assertEqual(sizes, [6, 7, 7])

    def test_unknown_package_weights(self) -> None:
        self.assertEqual(sts.package_weight(_pkg("small/b"), WEIGHTS, TIMINGS), sts._DEFAULT_WEIGHT)
        self.assertEqual(
            sts.package_weight(_pkg("notests", False), WEIGHTS, TIMINGS), sts._NO_TESTS_WEIGHT
        )
        # A timings key without a weight weighs the sum of its tests.
        self.assertEqual(
            sts.package_weight(_pkg("t"), {}, {"t": {"TestX": 200.0, "TestY": 1.5}}), 201.5
        )

    def test_plan_is_deterministic(self) -> None:
        first, _ = plan()
        for _ in range(3):
            again, _ = plan()
            self.assertEqual(first, again)
        # Input order does not matter either.
        units, _ = sts.build_units(list(reversed(PKGS)), WEIGHTS, TIMINGS, [], stub_lister)
        self.assertEqual(
            [sorted(u.label for u in b) for b in sts.assign(units, 4)],
            [sorted(u.label for u in b) for b in first],
        )


class CompletenessTest(unittest.TestCase):
    def test_full_plan_passes(self) -> None:
        bins, listed = plan()
        self.assertEqual(sts.verify(bins, PKGS, listed), [])
        total = sum(len(v) for v in NAMES.values())
        self.assertEqual(sts.full_check(bins, PKGS, listed, [], stub_lister), (total, 0, 0))

    def _drop_one_name(self, bins: list[list[sts.Unit]]) -> list[list[sts.Unit]]:
        out = [list(b) for b in bins]
        for b in out:
            for i, u in enumerate(b):
                if u.pkg.key == "huge" and len(u.names) > 1:
                    b[i] = replace(u, names=u.names[1:])
                    return out
        raise AssertionError("no split group to shrink")

    def test_removing_one_name_fails(self) -> None:
        bins, listed = plan()
        broken = self._drop_one_name(bins)
        errors = sts.verify(broken, PKGS, listed)
        self.assertEqual(len(errors), 1)
        self.assertIn("huge: 1 tests in no group", errors[0])
        total, dup, missing = sts.full_check(broken, PKGS, listed, [], stub_lister)
        self.assertEqual((dup, missing), (0, 1))

    def test_duplicated_name_fails(self) -> None:
        bins, listed = plan()
        broken = [list(b) for b in bins]
        u = next(u for b in broken for u in b if u.pkg.key == "big")
        other = next(
            (i, j)
            for i, b in enumerate(broken)
            for j, v in enumerate(b)
            if v.pkg.key == "big" and v != u
        )
        v = broken[other[0]][other[1]]
        broken[other[0]][other[1]] = replace(v, names=v.names + (u.names[0],))
        self.assertTrue(
            any("in more than one group" in e for e in sts.verify(broken, PKGS, listed))
        )
        self.assertEqual(sts.full_check(broken, PKGS, listed, [], stub_lister)[1], 1)

    def test_dropped_whole_package_fails(self) -> None:
        bins, listed = plan()
        broken = [[u for u in b if u.pkg.key != "small/c"] for b in bins]
        self.assertIn("small/c: not assigned to any shard", sts.verify(broken, PKGS, listed))
        self.assertEqual(sts.full_check(broken, PKGS, listed, [], stub_lister)[2], 3)

    def test_package_without_tests_must_still_be_assigned(self) -> None:
        # It contributes uncovered statements to the coverage total.
        bins, listed = plan()
        broken = [[u for u in b if u.pkg.key != "notests"] for b in bins]
        self.assertIn("notests: not assigned to any shard", sts.verify(broken, PKGS, listed))

    def test_empty_shard_fails(self) -> None:
        # More shards than units: LPT leaves the extra bins empty.
        pkgs = [_pkg("small/a"), _pkg("small/b")]
        units, listed = sts.build_units(pkgs, {}, {}, [], stub_lister)
        bins = sts.assign(units, 4)
        errors = sts.verify(bins, pkgs, listed)
        self.assertEqual(errors, ["shard 3/4 has no units", "shard 4/4 has no units"])

    def test_main_refuses_a_plan_with_an_empty_shard(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            w = os.path.join(d, "w.json")
            with open(w, "w", encoding="utf-8") as fh:
                json.dump({}, fh)
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = sts.main(
                    [
                        "--shard",
                        "1/4",
                        "--plan",
                        "--weights",
                        w,
                        "--timings",
                        os.path.join(d, "none.json"),
                        "--",
                        "-short",
                    ],
                    lister=stub_lister,
                    loader=lambda: [_pkg("small/a")],
                )
            self.assertEqual(rc, 1)
            self.assertIn("shard 2/4 has no units", buf.getvalue())

    def test_lister_failure_propagates(self) -> None:
        # A compile error makes go_test_shards.list_tests raise SystemExit;
        # it must surface, not be swallowed into an unsplit package.
        def failing(paths: list[str], go_args: list[str]) -> dict[str, list[str]]:
            raise SystemExit("::error::go test -list failed")

        with self.assertRaises(SystemExit):
            sts.build_units(PKGS, WEIGHTS, TIMINGS, [], failing)

    def test_check_prints_ok_line(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            w = os.path.join(d, "w.json")
            with open(w, "w", encoding="utf-8") as fh:
                json.dump(WEIGHTS, fh)
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = sts.main(
                    ["--check", "--weights", w, "--", "-short"],
                    lister=stub_lister,
                    loader=lambda: PKGS,
                )
            self.assertEqual(rc, 0)
            total = sum(len(v) for v in NAMES.values())
            self.assertEqual(
                buf.getvalue().strip(),
                f"OK: {total} tests across 4 shards, 0 duplicates, 0 missing",
            )


CORE_MOD = "example.com/core"


def _core_pkg(key: str, has_tests: bool = True) -> sts.Package:
    # Core packages carry the module and a `core/`-prefixed key, so a weights
    # entry for core can never collide with a root package of the same path.
    return sts.Package(f"{CORE_MOD}/{key}", f"core/{key}", has_tests, module="core")


CORE_PKGS = [_core_pkg("auth"), _core_pkg("storage/keys"), _core_pkg("buildid", has_tests=False)]
BOTH = PKGS + CORE_PKGS


def both_plan(count: int = 4) -> tuple[list[list[sts.Unit]], dict[str, list[str]]]:
    units, listed = sts.build_units(BOTH, WEIGHTS, TIMINGS, ["-short", "-race"], stub_lister)
    return sts.assign(units, count), listed


class CoreShardTest(unittest.TestCase):
    """R1-03: root packages fill N shards, every core package lands in the one core shard."""

    def test_core_packages_are_assigned_to_exactly_one_core_shard(self) -> None:
        bins, listed = both_plan()
        self.assertEqual(len(bins), 5)  # four root shards plus the core shard
        core_bin = bins[4]
        self.assertEqual(sorted(u.pkg.key for u in core_bin), sorted(p.key for p in CORE_PKGS))
        self.assertTrue(all(u.pkg.module == "core" for u in core_bin))
        # Never in a root shard, and each exactly once (verify checks the count).
        self.assertFalse(any(u.pkg.module == "core" for b in bins[:4] for u in b))
        self.assertEqual(sts.verify(bins, BOTH, listed), [])

    def test_a_core_package_in_a_root_shard_fails(self) -> None:
        bins, listed = both_plan()
        moved = bins[4][0]
        broken = [list(b) for b in bins]
        broken[4] = broken[4][1:]
        broken[0].append(moved)
        errors = sts.verify(broken, BOTH, listed)
        self.assertTrue(
            any(f"{moved.pkg.key}: core package in root shard 1/4" in e for e in errors)
        )

    def test_a_root_package_in_the_core_shard_fails(self) -> None:
        bins, listed = both_plan()
        moved = bins[0][0]
        broken = [list(b) for b in bins]
        broken[0] = broken[0][1:]
        broken[4].append(moved)
        errors = sts.verify(broken, BOTH, listed)
        self.assertTrue(
            any(f"{moved.pkg.key}: root package in the core shard" in e for e in errors)
        )

    def test_a_dropped_core_package_fails(self) -> None:
        bins, listed = both_plan()
        broken = [[u for u in b if u.pkg.key != "core/auth"] for b in bins]
        self.assertIn("core/auth: not assigned to any shard", sts.verify(broken, BOTH, listed))

    def test_an_empty_core_shard_fails(self) -> None:
        # The module is present (a core package is listed) yet nothing was assigned to it.
        bins, listed = both_plan()
        errors = sts.verify(bins[:4] + [[]], BOTH, listed)
        self.assertIn("core shard has no units", errors)

    def test_a_root_only_plan_has_no_core_shard(self) -> None:
        bins, _ = plan()
        self.assertEqual(len(bins), 4)

    def test_core_shard_spec(self) -> None:
        self.assertEqual(sts._parse_shard("core/4"), (5, 4))
        for spec in ("core", "core/0", "core/x", "1/core"):
            with self.assertRaises(SystemExit):
                sts._parse_shard(spec)

    def test_core_calls_run_in_the_core_module_without_a_coverage_profile(self) -> None:
        bins, _ = both_plan()
        calls = sts.plan_calls(bins[4], ["-short", "-race", "-covermode=atomic", "-timeout", "25m"])
        self.assertTrue(calls)
        for c in calls:
            self.assertEqual(c.module, "core")
            self.assertFalse(any(a.startswith(("-cover", "-coverprofile")) for a in c.go_args))
            self.assertIn("-race", c.go_args)
            self.assertIn("25m", c.go_args)
        # Root units keep their coverage flags.
        root_calls = sts.plan_calls(bins[0], ["-short", "-covermode=atomic"])
        self.assertTrue(
            all(c.module == "." and "-covermode=atomic" in c.go_args for c in root_calls)
        )

    def test_core_shard_run_invokes_go_in_core_and_writes_no_profile(self) -> None:
        bins, _ = both_plan()
        seen: list[list[str]] = []

        def fake_run(cmd: list[str], **kwargs: object) -> mock.Mock:
            seen.append(cmd)
            return mock.Mock(returncode=0, stdout="ok\n", stderr="")

        with contextlib.ExitStack() as stack:
            d = stack.enter_context(tempfile.TemporaryDirectory())
            stack.enter_context(mock.patch.object(sts.subprocess, "run", fake_run))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            out = os.path.join(d, "c.out")
            rc = sts.run_shard(bins[4], ["-short", "-race", "-covermode=atomic"], out)
            self.assertFalse(os.path.exists(out))  # core has no coverage floor yet
        self.assertEqual(rc, 0)
        self.assertEqual(len(seen), 1)  # every whole core package in one go test call
        self.assertEqual(seen[0][:4], ["go", "-C", "core", "test"])
        self.assertFalse(any(a.startswith("-cover") for a in seen[0]))
        self.assertIn(f"{CORE_MOD}/auth", seen[0])

    def test_go_test_output_with_raw_bytes_is_decoded_not_raised(self) -> None:
        child = [sys.executable, "-c", "import sys; sys.stdout.buffer.write(b'ok\\x1f\\x8b\\xff end')"]
        res = subprocess.run(child, **sts.GO_TEST_IO)
        self.assertIn("ok", res.stdout)
        self.assertIn("end", res.stdout)
        self.assertIn("\ufffd", res.stdout)

    def test_shard_run_captures_go_test_with_the_replacing_decoder(self) -> None:
        bins, _ = both_plan()
        seen: list[dict[str, object]] = []

        def fake_run(cmd: list[str], **kwargs: object) -> mock.Mock:
            seen.append(kwargs)
            return mock.Mock(returncode=0, stdout="ok\n", stderr="")

        with contextlib.ExitStack() as stack:
            d = stack.enter_context(tempfile.TemporaryDirectory())
            stack.enter_context(mock.patch.object(sts.subprocess, "run", fake_run))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            sts.run_shard(bins[4], ["-short"], os.path.join(d, "c.out"))
        self.assertTrue(seen)
        self.assertTrue(all(k.get("errors") == "replace" for k in seen), seen)

    def test_core_packages_are_listed_from_the_core_directory(self) -> None:
        # `go test -list` of a core package from the root fails on the root's
        # go.sum, so the default lister runs once per module, in its directory.
        cwds: list[tuple[str, list[str]]] = []

        def fake_list(paths: list[str], go_args: list[str]) -> dict[str, list[str]]:
            cwds.append((os.path.basename(os.getcwd()), sorted(paths)))
            return {p: [] for p in paths}

        here = os.getcwd()
        with tempfile.TemporaryDirectory() as d:
            core = os.path.join(d, "corehome")
            os.mkdir(core)
            os.chdir(d)
            try:
                with contextlib.ExitStack() as stack:
                    stack.enter_context(mock.patch.object(sts.gts, "list_tests_many", fake_list))
                    stack.enter_context(mock.patch.object(sts, "CORE_DIR", "corehome"))
                    wrapped = sts.module_lister(sts.gts.list_tests_many, BOTH)
                    wrapped([p.path for p in BOTH], [])
                    self.assertEqual(os.getcwd(), os.path.realpath(d))
            finally:
                os.chdir(here)
        self.assertEqual(len(cwds), 2)
        self.assertEqual(cwds[0][0], os.path.basename(d))
        self.assertEqual(cwds[1], ("corehome", sorted(p.path for p in CORE_PKGS)))
        # A stub lister is called once, untouched.
        self.assertIs(sts.module_lister(stub_lister, BOTH), stub_lister)

    def test_core_shard_check_covers_core_tests(self) -> None:
        names = dict(NAMES)
        names[f"{CORE_MOD}/auth"] = ["TestAuth1", "TestAuth2"]
        names[f"{CORE_MOD}/storage/keys"] = ["TestKeys1"]

        def lister(paths: list[str], go_args: list[str]) -> dict[str, list[str]]:
            return {p: sorted(names.get(p, [])) for p in paths}

        units, listed = sts.build_units(BOTH, WEIGHTS, TIMINGS, [], lister)
        bins = sts.assign(units, 4)
        total = sum(len(v) for v in names.values())
        self.assertEqual(sts.full_check(bins, BOTH, listed, [], lister), (total, 0, 0))


class StaleWeightsTest(unittest.TestCase):
    """A weight for a package in neither module's `go list ./...` is an error (R1-03)."""

    def test_unknown_key_is_reported(self) -> None:
        self.assertEqual(sts.stale_weights({"gone/pkg": 200}, BOTH), ["gone/pkg"])

    def test_known_root_and_core_keys_pass(self) -> None:
        self.assertEqual(sts.stale_weights({"big": 1, "core/auth": 1}, BOTH), [])

    def test_a_core_package_needs_the_core_prefix(self) -> None:
        # "auth" alone names no package: core keys are namespaced.
        self.assertEqual(sts.stale_weights({"auth": 1}, BOTH), ["auth"])

    def test_main_fails_on_a_stale_weight_entry(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            w = os.path.join(d, "w.json")
            with open(w, "w", encoding="utf-8") as fh:
                json.dump({**WEIGHTS, "internal/moved/away": 300}, fh)
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = sts.main(
                    ["--check", "--weights", w, "--", "-short"],
                    lister=stub_lister,
                    loader=lambda: PKGS,
                )
            self.assertEqual(rc, 1)
            self.assertIn("internal/moved/away", buf.getvalue())
            self.assertIn("not in go list ./...", buf.getvalue())

    def test_main_passes_with_core_packages_and_no_stale_weight(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            w = os.path.join(d, "w.json")
            with open(w, "w", encoding="utf-8") as fh:
                json.dump({**WEIGHTS, "core/auth": 5}, fh)
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = sts.main(
                    ["--check", "--weights", w, "--", "-short"],
                    lister=stub_lister,
                    loader=lambda: BOTH,
                )
            self.assertEqual(rc, 0)
            self.assertIn("across 4 shards and the core shard", buf.getvalue())


class RunShardTest(unittest.TestCase):
    def test_default_runs_every_call_at_once(self) -> None:
        # ci.yml's timeout invariant (job cap 35 > go test -timeout 25m) only
        # holds if no call waits for a slot, so by default they all start
        # together. All eight calls meet at a barrier; a pool smaller than eight
        # would never fill it and the barrier would break.
        # One shard holding everything: 1 whole-package call + 7 split groups,
        # more calls than a 4-core runner has cores.
        (mine,), _ = plan(1)
        calls = 1 + sum(1 for u in mine if u.group)
        self.assertEqual(calls, 8)
        barrier = threading.Barrier(calls, timeout=5)
        seen = []

        def fake_run(cmd: list[str], **kwargs: object) -> mock.Mock:
            barrier.wait()
            prof = next(a for a in cmd if a.startswith("-coverprofile="))
            with open(prof.split("=", 1)[1], "w", encoding="utf-8") as fh:
                fh.write("mode: atomic\n")
            seen.append(cmd)
            return mock.Mock(returncode=0, stdout="ok\n", stderr="")

        with contextlib.ExitStack() as stack:
            d = stack.enter_context(tempfile.TemporaryDirectory())
            stack.enter_context(mock.patch.object(sts.subprocess, "run", fake_run))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            rc = sts.run_shard(mine, ["-short"], os.path.join(d, "c.out"))
        self.assertEqual(rc, 0)
        self.assertEqual(len(seen), calls)

    def test_split_group_that_runs_nothing_fails(self) -> None:
        bins, _ = plan()
        mine = next(b for b in bins if any(u.group for u in b))

        def fake_run(cmd: list[str], **kwargs: object) -> mock.Mock:
            out = "ok  pkg 0.1s [no tests to run]\n" if "-run" in cmd else "ok\n"
            return mock.Mock(returncode=0, stdout=out, stderr="")

        with contextlib.ExitStack() as stack:
            d = stack.enter_context(tempfile.TemporaryDirectory())
            stack.enter_context(mock.patch.object(sts.subprocess, "run", fake_run))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            self.assertEqual(sts.run_shard(mine, [], os.path.join(d, "c.out")), 1)

    def test_oversized_run_pattern_fails_the_plan(self) -> None:
        bins, listed = plan()
        with mock.patch.object(sts, "_MAX_PATTERN", 50):
            errors = sts.verify(bins, PKGS, listed)
        self.assertTrue(errors)
        self.assertTrue(all("-run pattern is" in e for e in errors))


class MergeTest(unittest.TestCase):
    def _write(self, d: str, name: str, body: str) -> str:
        p = os.path.join(d, name)
        with open(p, "w", encoding="utf-8") as fh:
            fh.write(body)
        return p

    def test_merged_shards_equal_the_single_run(self) -> None:
        # One run over two packages vs. the same blocks split across shards
        # (pkg y split in two groups): the merged profile has the same blocks
        # and the same covered set, so the same total.
        single = (
            "mode: atomic\nx.go:1.1,2.1 3 2\nx.go:3.1,4.1 2 0\ny.go:1.1,2.1 4 1\ny.go:3.1,4.1 1 1\n"
        )
        with tempfile.TemporaryDirectory() as d:
            s1 = self._write(
                d, "coverage-1.out", "mode: atomic\nx.go:1.1,2.1 3 2\nx.go:3.1,4.1 2 0\n"
            )
            s2 = self._write(
                d, "coverage-2.out", "mode: atomic\ny.go:1.1,2.1 4 1\ny.go:3.1,4.1 1 0\n"
            )
            s3 = self._write(
                d, "coverage-3.out", "mode: atomic\ny.go:1.1,2.1 4 0\ny.go:3.1,4.1 1 1\n"
            )
            out = os.path.join(d, "coverage.out")
            self.assertEqual(sts.main(["--merge", out, s1, s2, s3]), 0)
            with open(out, encoding="utf-8") as fh:
                self.assertEqual(fh.read(), single)

    def test_missing_shard_artifact_fails_the_merge(self) -> None:
        with tempfile.TemporaryDirectory() as d:
            s1 = self._write(d, "coverage-1.out", "mode: atomic\nx.go:1.1,2.1 3 2\n")
            out = os.path.join(d, "coverage.out")
            with contextlib.redirect_stdout(io.StringIO()):
                rc = sts.main(["--merge", out, s1, os.path.join(d, "coverage-2.out")])
            self.assertEqual(rc, 1)
            self.assertFalse(os.path.exists(out))


class ShardSpecTest(unittest.TestCase):
    def test_bad_shard_specs_are_refused(self) -> None:
        for spec in ("0/4", "5/4", "1-4", "x"):
            with self.assertRaises(SystemExit):
                sts._parse_shard(spec)
        self.assertEqual(sts._parse_shard("2/4"), (2, 4))


if __name__ == "__main__":
    unittest.main()


class EstimateFloorTest(unittest.TestCase):
    def test_zero_second_tests_are_not_free(self) -> None:
        # Recorded 0.00 s tests cost the floor, so a split spreads them by
        # count instead of piling every "free" test into one group.
        names = [f"TestZ{i:03d}" for i in range(300)] + ["TestBig"]
        timings = dict.fromkeys(names, 0.0)
        timings["TestBig"] = 60.0
        est = sts._estimate(names, timings)
        self.assertEqual(est["TestZ000"], sts._MIN_TEST_SECONDS)
        self.assertEqual(est["TestBig"], 60.0)
        groups = sts.gts.balance(names, 3, est)
        sizes = sorted(len(g) for g in groups)
        # TestBig alone outweighs 240 floor tests, so its group is small and
        # the other two share the rest nearly evenly.
        self.assertLess(sizes[0], 100)
        self.assertLess(sizes[2] - sizes[1], 5)
