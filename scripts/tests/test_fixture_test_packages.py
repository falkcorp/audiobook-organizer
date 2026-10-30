# file: scripts/tests/test_fixture_test_packages.py
# version: 1.0.1
# guid: 5b7e2c94-1d3a-4f68-9a0e-c4d8b6f21a73
# last-edited: 2026-10-30
"""Tests for scripts/ci/fixture_test_packages.py: the shard partition must not
depend on the host the shard job lands on, and a package assigned to this shard
that this host cannot test must fail the job rather than vanish from it."""

from __future__ import annotations

import json
import os
import sys
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


if __name__ == "__main__":
    unittest.main()
