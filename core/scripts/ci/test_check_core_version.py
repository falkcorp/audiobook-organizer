#!/usr/bin/env python3
# file: core/scripts/ci/test_check_core_version.py
# version: 1.0.0
# guid: 89df8edc-a41b-4995-9d91-151668ad1ae2
# last-edited: 2026-11-08
"""Tests for check_core_version.py (12 §12.4.3). Run: python3 -m unittest discover -s core/scripts/ci -p 'test_*.py'."""

from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import check_core_version as ccv  # noqa: E402
import core_ci_lib as lib  # noqa: E402


def git(repo: pathlib.Path, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", str(repo), "-c", "user.name=Bruce Wayne", "-c", "user.email=bruce@wayne.example", *args],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()


def make_core(
    root: pathlib.Path,
    version: str = "0.2.0",
    hello_floor: str | None = '"0.1.0"',
    packages: dict[str, str] | None = None,
) -> pathlib.Path:
    """A minimal core/ tree: the constant, hello's manifest and the npm packages."""
    core = root / "core"
    (core / "version").mkdir(parents=True)
    (core / "version" / "version.go").write_text(f'package version\n\nconst Version = "{version}"\n')
    (core / "examples" / "hello").mkdir(parents=True)
    body = "package hello\n\nvar Manifest = module.Manifest{\n\tID: \"hello\",\n"
    if hello_floor is not None:
        body += f"\tMinCoreVersion: {hello_floor}, // compared with version.Version\n"
    (core / "examples" / "hello" / "hello.go").write_text(body + "}\n")
    packages = {"api-client": version} if packages is None else packages
    (core / "web").mkdir()
    (core / "web" / "package.json").write_text('{"name":"mediacore-web","version":"0.0.0","private":true}')
    for name, ver in packages.items():
        pkg = core / "web" / "packages" / name
        pkg.mkdir(parents=True)
        (pkg / "package.json").write_text(json.dumps({"name": f"@mediacore/{name}", "version": ver}))
    return core


class Base(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = pathlib.Path(self._tmp.name)

    def run_check(self, core: pathlib.Path, fetch: bool = False) -> tuple[list[str], list[str]]:
        return ccv.check(core, fetch=fetch)

    def init_repo(self, core: pathlib.Path) -> None:
        git(self.root, "init", "-q", "-b", "main")
        git(self.root, "add", "-A")
        git(self.root, "commit", "-q", "-m", "init")

    def record(self, name: str) -> None:
        git(self.root, "update-ref", f"refs/core-checkpoints/{name}", "HEAD")


class VersionChecks(Base):
    def test_consistent_tree_passes(self) -> None:
        errors, notes = self.run_check(make_core(self.root))
        self.assertEqual(errors, [])
        self.assertTrue(any("not applicable" in n for n in notes), notes)

    def test_no_checkpoint_passes(self) -> None:
        core = make_core(self.root)
        self.init_repo(core)
        errors, notes = self.run_check(core)
        self.assertEqual(errors, [])
        self.assertIn("checkpoints: none recorded yet (passes)", notes)

    def test_checkpoint_at_or_below_constant_passes(self) -> None:
        core = make_core(self.root, version="0.3.0", packages={"api-client": "0.3.0"})
        self.init_repo(core)
        self.record("v0.2.0")
        self.record("v0.3.0")  # equality passes: the PR that bumps comes before the record
        self.assertEqual(self.run_check(core)[0], [])

    def test_checkpoint_above_constant_fails(self) -> None:
        core = make_core(self.root, version="0.2.0")
        self.init_repo(core)
        self.record("v0.2.0")
        self.record("v0.10.0")  # newest by version, not by string order
        errors, _ = self.run_check(core)
        self.assertEqual(len(errors), 1)
        self.assertIn("below the newest checkpoint", errors[0])
        self.assertIn("v0.10.0", errors[0])

    def test_malformed_checkpoint_name_fails(self) -> None:
        core = make_core(self.root)
        self.init_repo(core)
        self.record("0.1.0-final")
        errors, _ = self.run_check(core)
        self.assertEqual(len(errors), 1)
        self.assertIn("vX.Y.Z", errors[0])

    def test_hello_floor_above_constant_fails(self) -> None:
        errors, _ = self.run_check(make_core(self.root, version="0.1.0", hello_floor='"0.2.0"', packages={"api-client": "0.1.0"}))
        self.assertEqual(len(errors), 1)
        self.assertIn("MinCoreVersion 0.2.0 is above version.Version 0.1.0", errors[0])

    def test_hello_floor_empty_or_absent_is_no_floor(self) -> None:
        self.assertEqual(self.run_check(make_core(self.root, hello_floor='""'))[0], [])
        other = self.root / "other"
        other.mkdir()
        self.assertEqual(self.run_check(make_core(other, hello_floor=None))[0], [])

    def test_hello_floor_not_a_literal_fails(self) -> None:
        errors, _ = self.run_check(make_core(self.root, hello_floor="minCore"))
        self.assertEqual(len(errors), 1)
        self.assertIn("not a string literal", errors[0])

    def test_npm_package_off_lockstep_fails(self) -> None:
        core = make_core(self.root, packages={"api-client": "0.2.0", "shell": "0.1.9"})
        errors, _ = self.run_check(core)
        self.assertEqual(len(errors), 1)
        self.assertIn("core/web/packages/shell/package.json", errors[0])
        self.assertIn("'0.1.9'", errors[0])

    def test_npm_workspace_without_packages_fails(self) -> None:
        errors, _ = self.run_check(make_core(self.root, packages={}))
        self.assertEqual(len(errors), 1)
        self.assertIn("vacuous", errors[0])

    def test_npm_not_applicable_without_core_web(self) -> None:
        core = make_core(self.root)
        (core / "web" / "package.json").unlink()
        errors, notes = self.run_check(core)
        self.assertEqual(errors, [])
        self.assertIn("npm lockstep: not applicable (no core/web/package.json)", notes)

    def test_unreadable_constant_fails(self) -> None:
        core = make_core(self.root)
        (core / "version" / "version.go").write_text("package version\n")
        errors, _ = self.run_check(core)
        self.assertEqual(len(errors), 1)
        self.assertIn("version.Version", errors[0])


class FetchBehavior(Base):
    def test_fetches_checkpoints_from_origin(self) -> None:
        """A checkpoint that exists only on the remote is seen (and fails) after the fetch."""
        origin = self.root / "origin.git"
        subprocess.run(["git", "init", "-q", "--bare", str(origin)], check=True)
        work = self.root / "work"
        work.mkdir()
        core = make_core(work, version="0.2.0")
        git(work, "init", "-q", "-b", "main")
        git(work, "add", "-A")
        git(work, "commit", "-q", "-m", "init")
        git(work, "remote", "add", "origin", str(origin))
        git(work, "push", "-q", "origin", "main")
        git(work, "update-ref", "refs/core-checkpoints/v0.9.0", "HEAD")
        git(work, "push", "-q", "origin", "refs/core-checkpoints/v0.9.0")
        git(work, "update-ref", "-d", "refs/core-checkpoints/v0.9.0")
        self.assertEqual(ccv.check(core, fetch=False)[0], [])
        errors, _ = ccv.check(core, fetch=True)
        self.assertEqual(len(errors), 1)
        self.assertIn("v0.9.0", errors[0])

    def test_unreachable_remote_fails_closed(self) -> None:
        core = make_core(self.root)
        self.init_repo(core)
        git(self.root, "remote", "add", "origin", str(self.root / "missing.git"))
        errors, _ = ccv.check(core, fetch=True)
        self.assertEqual(len(errors), 1)
        self.assertIn("could not be fetched", errors[0])

    def test_export_without_remote_is_not_applicable(self) -> None:
        core = make_core(self.root)
        self.init_repo(core)  # the SC4(a) export: a fresh repository holding only core/
        errors, notes = ccv.check(core, fetch=True)
        self.assertEqual(errors, [])
        self.assertIn("checkpoints: no 'origin' remote, nothing fetched", notes)


class Semver(unittest.TestCase):
    def test_precedence(self) -> None:
        p = lib.parse_semver
        self.assertLess(p("0.9.0"), p("0.10.0"))
        self.assertLess(p("1.0.0-rc.1"), p("1.0.0"))
        self.assertLess(p("1.0.0-rc.2"), p("1.0.0-rc.10"))
        self.assertLess(p("1.0.0-1"), p("1.0.0-alpha"))
        self.assertEqual(p("v1.2.3+build"), p("1.2.3"))

    def test_rejects_non_semver(self) -> None:
        for bad in ("1.2", "01.2.3", "a.b.c", ""):
            with self.assertRaises(ValueError, msg=bad):
                lib.parse_semver(bad)


if __name__ == "__main__":
    unittest.main()
