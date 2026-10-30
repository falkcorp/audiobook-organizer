# file: scripts/tests/test_fixture_test_packages.py
# version: 1.0.1
# guid: 5b7e2c94-1d3a-4f68-9a0e-c4d8b6f21a73
# last-edited: 2026-10-30
"""Tests for scripts/ci/fixture_test_packages.py: the shard partition must not
depend on the host the shard job lands on, and a package assigned to this shard
that this host cannot test must fail the job rather than vanish from it."""

from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "ci"))

import fixture_test_packages as ftp  # noqa: E402

M = ftp.MODULE


def pkg(name: str, tests: bool = True) -> dict:
    return {
        "ImportPath": f"{M}/{name}",
        "Dir": f"/nonexistent/{name}",
        "GoFiles": ["a.go"],
        "TestGoFiles": ["a_test.go"] if tests else [],
    }


def listing(pkgs: list[dict]) -> mock.Mock:
    return mock.Mock(returncode=0, stdout="\n".join(json.dumps(p) for p in pkgs), stderr="")


class PartitionHostIndependentTest(unittest.TestCase):
    def test_discovery_pins_goos_goarch_cgo(self):
        with mock.patch.object(ftp.subprocess, "run", return_value=listing([pkg("x")])) as run:
            ftp._load_packages(Path("."))
        env = run.call_args.kwargs["env"]
        self.assertEqual((env["GOOS"], env["GOARCH"], env["CGO_ENABLED"]), ("linux", "amd64", "1"))

    def test_host_set_differences_do_not_change_partition(self):
        # Discovery is pinned, so a darwin host whose native set differs (extra
        # package, one missing) still produces the same discovery input and so
        # the same shards. The native listing is only consulted for the check.
        linux = [pkg(n) for n in ("a", "b", "c", "d", "e", "f", "g")]
        darwin_native = [pkg(n) for n in ("a", "b", "c", "d", "e", "g", "darwin_only")]

        def fake_run(cmd, **kw):
            env = kw.get("env", {})
            native = env.get("GOOS") != "linux"
            return listing(darwin_native if native else linux)

        def calls_short(directory, files):
            return False

        with mock.patch.object(ftp.subprocess, "run", fake_run), mock.patch.object(
            ftp, "_calls_short", calls_short
        ), mock.patch.object(ftp, "_REQUIRED_HELPERS", ()):
            pkgs = ftp._load_packages(Path("."))
            selected = ftp.select(pkgs, set(), calls_short=lambda d, f: True)
        self.assertEqual(selected, sorted(f"{M}/{n}" for n in "abcdefg"))
        shards = [ftp.shard(selected, i, 3) for i in (1, 2, 3)]
        # shard() returns Units; none of these test packages is heavy enough to split.
        self.assertEqual(sorted(u.pkg for s in shards for u in s), selected)
        self.assertEqual(len({u for s in shards for u in s}), len(selected))

    def test_partition_hash_is_order_independent_and_sensitive(self):
        self.assertEqual(ftp.partition_hash(["b", "a"]), ftp.partition_hash(["a", "b"]))
        self.assertNotEqual(ftp.partition_hash(["a"]), ftp.partition_hash(["a", "b"]))
        self.assertEqual(len(ftp.partition_hash(["a"])), 12)


class AssignedButUnbuildableTest(unittest.TestCase):
    def verify(self, assigned: list[str], native: list[dict]) -> None:
        with mock.patch.object(ftp.subprocess, "run", return_value=listing(native)):
            ftp.verify_testable_here([f"{M}/{n}" for n in assigned], Path("."))

    def test_all_present_passes(self):
        self.verify(["a", "b"], [pkg("a"), pkg("b")])

    def test_missing_from_native_list_exits_nonzero(self):
        with self.assertRaises(SystemExit) as cm:
            self.verify(["a", "b"], [pkg("a")])
        self.assertIn(f"{M}/b: not in this host's go list", str(cm.exception))

    def test_no_test_files_on_this_host_exits_nonzero(self):
        with self.assertRaises(SystemExit) as cm:
            self.verify(["a"], [pkg("a", tests=False)])
        self.assertIn("no test files build on this host", str(cm.exception))

    def test_package_error_exits_nonzero(self):
        broken = {**pkg("a"), "Error": {"Err": "build constraints exclude all Go files"}}
        with self.assertRaises(SystemExit) as cm:
            self.verify(["a"], [broken])
        self.assertIn("build constraints exclude all Go files", str(cm.exception))

    def test_main_exits_nonzero_for_unbuildable_assigned_package(self):
        disc = ftp.Discovery([f"{M}/a", f"{M}/b"], set())
        with mock.patch.object(ftp, "discover", return_value=disc), mock.patch.object(
            ftp.subprocess, "run", return_value=listing([pkg("a")])
        ), mock.patch.object(sys, "argv", ["x", "--shard", "1/1"]), self.assertRaises(
            SystemExit
        ) as cm:
            ftp.main()
        self.assertIn("refusing to skip", str(cm.exception))

class TimingAndWeightsTest(unittest.TestCase):
    def _res(self, label, out, ok=True, is_slice=False, secs=9.0):
        return ftp.Result(label, 0 if ok else 1, secs, out, ok, is_slice)

    def test_sidecar_is_what_the_planner_weighs_by(self):
        doc = json.loads((Path(ftp.__file__).with_name("fixture_weights.json")).read_text())
        self.assertEqual(ftp._WEIGHTS, doc["weights"])

    def test_a_slice_is_timed_by_its_ok_line_and_a_whole_run_per_package(self):
        slice_ = self._res(
            f"{M}/internal/server#1/2", f"ok  \t{M}/internal/server\t605.4s\n", is_slice=True
        )
        whole = self._res(
            "2 whole packages",
            f"ok  \t{M}/internal/undo\t68.2s\nok  \t{M}/internal/merge\t3.0s\n",
        )
        self.assertEqual(
            [(t.unit, t.seconds) for t in ftp.result_timings(slice_)],
            [("internal/server#1/2", 605.4)],
        )
        self.assertEqual(
            [(t.unit, t.seconds) for t in ftp.result_timings(whole)],
            [("internal/undo", 68.2), ("internal/merge", 3.0)],
        )

    def test_a_slice_without_an_ok_line_falls_back_to_wall_time(self):
        res = self._res(f"{M}/internal/server#2/2", "", is_slice=True, secs=41.0)
        self.assertEqual([(t.unit, t.seconds) for t in ftp.result_timings(res)], [("internal/server#2/2", 41.0)])

    def test_a_failed_package_reports_no_timing_but_its_passing_mates_do(self):
        res = self._res(
            "2 whole packages",
            f"ok  \t{M}/internal/merge\t3.0s\nFAIL\t{M}/internal/undo\t5.0s\n",
            ok=False,
        )
        self.assertEqual([t.unit for t in ftp.result_timings(res)], ["internal/merge"])

    def test_a_failed_slice_gets_no_wall_time_fallback(self):
        res = self._res(f"{M}/internal/server#1/2", "", ok=False, is_slice=True, secs=77.0)
        self.assertEqual(ftp.result_timings(res), [])

    def test_emit_prints_lines_and_writes_the_json_file(self):
        res = self._res("1 whole packages", f"ok  \t{M}/internal/undo\t68.2s\n")
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "fixture-times.json"
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                ftp.emit_timings([res], str(path))
            self.assertIn("::timing::internal/undo=68.2", buf.getvalue())
            rows = json.loads(path.read_text())["timings"]
            self.assertEqual([(r["unit"], r["seconds"]) for r in rows], [("internal/undo", 68.2)])

    def test_emit_writes_no_file_when_none_is_asked_for(self):
        res = self._res("1 whole packages", f"ok  \t{M}/internal/undo\t68.2s\n")
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("FIXTURE_TIMES_FILE", None)
            with contextlib.redirect_stdout(io.StringIO()):
                ftp.emit_timings([res])  # must not raise


if __name__ == "__main__":
    unittest.main()
