# file: scripts/tests/test_web_build_against_tarballs.py
# version: 1.0.0
# guid: 46d0781f-8d44-4b83-8ae6-5c94c67e5bec
# last-edited: 2026-11-08
"""Tests for scripts/ci/web_build_against_tarballs.py (12 §12.3 SC8).

Tarballs are real (built with tarfile); npm is a fake runner. The real run is
the nightly job (and the script by hand after `npm pack`).
"""

from __future__ import annotations

import importlib.util
import io
import json
import pathlib
import sys
import tarfile
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location("web_build_against_tarballs", ROOT / "scripts/ci/web_build_against_tarballs.py")
assert _spec and _spec.loader
wb = importlib.util.module_from_spec(_spec)
sys.modules["web_build_against_tarballs"] = wb
_spec.loader.exec_module(wb)
lib = wb.lib


def pack(directory: pathlib.Path, name: str, filename: str) -> pathlib.Path:
    path = directory / filename
    data = json.dumps({"name": name, "version": "0.1.0"}).encode()
    with tarfile.open(path, "w:gz") as archive:
        info = tarfile.TarInfo("package/package.json")
        info.size = len(data)
        archive.addfile(info, io.BytesIO(data))
    return path


class WebBuildTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = pathlib.Path(self._tmp.name)
        self.pkgs = self.root / "pkgs"
        self.pkgs.mkdir()
        self.web = self.root / "web"
        (self.web / "node_modules/leftpad").mkdir(parents=True)
        (self.web / "node_modules/leftpad/index.js").write_text("x")
        (self.web / "dist").mkdir()
        (self.web / "src").mkdir()
        (self.web / "src/app.ts").write_text("export {}\n")
        self.write_manifest({"dependencies": {"@mediacore/api-client": "file:../core/web/packages/api-client", "react": "^19.0.0"}})
        self.work = self.root / "work"
        self.work.mkdir()

    def write_manifest(self, extra: dict) -> None:
        (self.web / "package.json").write_text(json.dumps({"name": "web", "version": "0.0.0", **extra}))

    def runner(self, fail: str = ""):
        calls: list[tuple[list[str], pathlib.Path]] = []

        def run(argv: list[str], cwd: pathlib.Path) -> tuple[int, str]:
            calls.append((argv, cwd))
            return (1, "npm ERR! boom") if fail and " ".join(argv[:2]) == fail else (0, "")

        return run, calls

    def test_index_reads_the_package_name_not_the_file_name(self) -> None:
        tgz = pack(self.pkgs, "@mediacore/api-client", "whatever-0.1.0.tgz")
        self.assertEqual(wb.index_tarballs(self.pkgs), {"@mediacore/api-client": tgz.resolve()})

    def test_two_tarballs_of_one_package_are_refused(self) -> None:
        pack(self.pkgs, "@mediacore/shell", "a.tgz")
        pack(self.pkgs, "@mediacore/shell", "b.tgz")
        with self.assertRaises(ValueError):
            wb.index_tarballs(self.pkgs)

    def test_rewrite_points_only_mediacore_dependencies_at_tarballs(self) -> None:
        manifest = {"dependencies": {"@mediacore/shell": "file:x", "react": "^19"}, "devDependencies": {"@mediacore/ui-kit": "file:y"}}
        done = wb.rewrite_manifest(manifest, {"@mediacore/shell": pathlib.Path("/t/s.tgz"), "@mediacore/ui-kit": pathlib.Path("/t/u.tgz")})
        self.assertEqual(done, ["dependencies.@mediacore/shell", "devDependencies.@mediacore/ui-kit"])
        self.assertEqual(manifest["dependencies"], {"@mediacore/shell": "file:/t/s.tgz", "react": "^19"})
        self.assertEqual(manifest["devDependencies"], {"@mediacore/ui-kit": "file:/t/u.tgz"})

    def test_a_dependency_without_a_tarball_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            wb.rewrite_manifest({"dependencies": {"@mediacore/shell": "file:x"}}, {})

    def test_pass_copies_without_installs_rewrites_and_runs_install_then_build(self) -> None:
        tgz = pack(self.pkgs, "@mediacore/api-client", "mediacore-api-client-0.1.0.tgz")
        run, calls = self.runner()
        r = wb.build(self.web, self.pkgs, self.work, run)
        self.assertEqual(r.status, lib.PASS, r.details)
        copy = self.work / "web"
        self.assertEqual([c[0] for c in calls], [["npm", "install", "--no-audit", "--no-fund"], ["npm", "run", "build"]])
        self.assertTrue(all(c[1] == copy for c in calls))
        self.assertFalse((copy / "node_modules").exists())
        self.assertFalse((copy / "dist").exists())
        self.assertTrue((copy / "src/app.ts").is_file())
        manifest = json.loads((copy / "package.json").read_text())
        self.assertEqual(manifest["dependencies"]["@mediacore/api-client"], f"file:{tgz.resolve()}")
        # the checkout's own manifest is untouched
        self.assertIn("file:../core/web", (self.web / "package.json").read_text())

    def test_a_failing_install_or_build_fails(self) -> None:
        pack(self.pkgs, "@mediacore/api-client", "a.tgz")
        for step in ("npm install", "npm run"):
            with self.subTest(step=step):
                shutil_tree = self.work / "web"
                if shutil_tree.exists():
                    import shutil

                    shutil.rmtree(shutil_tree)
                run, _ = self.runner(fail=step)
                r = wb.build(self.web, self.pkgs, self.work, run)
                self.assertEqual(r.status, lib.FAIL)
                self.assertTrue(any("npm ERR! boom" in d for d in r.details))

    def test_a_web_with_no_mediacore_dependency_fails_as_vacuous(self) -> None:
        self.write_manifest({"dependencies": {"react": "^19"}})
        pack(self.pkgs, "@mediacore/api-client", "a.tgz")
        run, calls = self.runner()
        r = wb.build(self.web, self.pkgs, self.work, run)
        self.assertEqual(r.status, lib.FAIL)
        self.assertIn("vacuous", r.measured)
        self.assertEqual(calls, [])

    def test_a_missing_tarball_directory_is_an_error(self) -> None:
        run, _ = self.runner()
        self.assertEqual(wb.build(self.web, self.root / "nope", self.work, run).status, lib.ERROR)


if __name__ == "__main__":
    unittest.main()
