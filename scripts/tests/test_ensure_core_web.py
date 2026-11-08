# file: scripts/tests/test_ensure_core_web.py
# version: 1.0.0
# guid: c4e7a1d2-3b58-4f6a-8d09-2a5f1e7b6c30
# last-edited: 2026-11-08
"""Tests for scripts/ci/ensure_core_web.sh (R1-30): the guard web/'s build, typecheck
and test scripts run first, so a caller that only runs ``npm ci`` in web/ (the reusable
frontend job behind frontend-ci.yml) still finds a built @mediacore/api-client.

Same method as test_build_web.py: a temp tree holding a copy of the script and a stub
``npm`` that logs its working directory and argv, so what is proved is whether npm is
called, in which directory and in which order, and the exit status. File times are set
explicitly, never left to the clock.

Stdlib only; run with ``python3 -m unittest scripts.tests.test_ensure_core_web``.
"""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO / "scripts" / "ci" / "ensure_core_web.sh"

STUB_NPM = """#!/bin/sh
printf '%s|%s\\n' "$(pwd -P)" "$*" >> "$NPM_STUB_LOG"
case "${NPM_STUB_FAIL_ON:-}" in
  "") ;;
  *) case "$*" in "$NPM_STUB_FAIL_ON"*) exit "${NPM_STUB_EXIT:-1}" ;; esac ;;
esac
exit 0
"""

BUILD = "run build -w @mediacore/api-client"
INSTALL = "ci --no-audit --no-fund"
T0 = 1_700_000_000


class Tree:
    def __init__(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(os.path.realpath(self._tmp.name))
        (self.root / "scripts" / "ci").mkdir(parents=True)
        shutil.copy(SCRIPT, self.root / "scripts" / "ci" / "ensure_core_web.sh")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        npm = self.bin / "npm"
        npm.write_text(STUB_NPM)
        npm.chmod(0o755)
        self.log = self.root / "npm.log"
        self.core = self.root / "core" / "web"
        self.pkg = self.core / "packages" / "api-client"

    def close(self) -> None:
        self._tmp.cleanup()

    def with_core_web(self) -> None:
        (self.pkg / "src").mkdir(parents=True)
        (self.pkg / "generator").mkdir()
        (self.core / "package.json").write_text("{}\n")
        (self.core / "package-lock.json").write_text("{}\n")
        (self.pkg / "src" / "index.ts").write_text("export {}\n")
        (self.pkg / "generator" / "cli.ts").write_text("export {}\n")
        self._stamp(self.core / "package.json", 0)
        self._stamp(self.pkg / "src" / "index.ts", 0)
        self._stamp(self.pkg / "generator" / "cli.ts", 0)
        self._stamp(self.core / "package-lock.json", 10)

    def mark_built(self) -> None:
        """What a finished `npm ci` and build leave: install marker, then dist/."""
        (self.core / "node_modules").mkdir()
        marker = self.core / "node_modules" / ".package-lock.json"
        marker.write_text("{}\n")
        self._stamp(marker, 20)
        (self.pkg / "dist").mkdir()
        for name in ("index.js", "index.d.ts"):
            (self.pkg / "dist" / name).write_text("")
            self._stamp(self.pkg / "dist" / name, 100)

    @staticmethod
    def _stamp(path: pathlib.Path, offset: int) -> None:
        os.utime(path, (T0 + offset, T0 + offset))

    def touch_newer(self, path: pathlib.Path) -> None:
        self._stamp(path, 500)

    def run(
        self, env: dict[str, str] | None = None, shell: str = "sh"
    ) -> subprocess.CompletedProcess[str]:
        full = {
            "PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            "NPM_STUB_LOG": str(self.log),
        }
        full.update(env or {})
        return subprocess.run(
            [shell, "scripts/ci/ensure_core_web.sh"],
            cwd=self.root,
            env=full,
            capture_output=True,
            text=True,
            check=False,
        )

    def calls(self) -> list[tuple[str, str]]:
        if not self.log.exists():
            return []
        out = []
        for line in self.log.read_text().splitlines():
            cwd, argv = line.split("|")
            out.append((str(pathlib.Path(cwd).relative_to(self.root)), argv))
        return out


class EnsureCoreWebTest(unittest.TestCase):
    def tree(self) -> Tree:
        t = Tree()
        self.addCleanup(t.close)
        return t

    def test_without_core_web_does_nothing(self) -> None:
        t = self.tree()
        res = t.run()
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(t.calls(), [])

    def test_no_dist_installs_then_builds_in_core_web_only(self) -> None:
        t = self.tree()
        t.with_core_web()
        res = t.run()
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(t.calls(), [("core/web", INSTALL), ("core/web", BUILD)])

    def test_fresh_dist_calls_nothing(self) -> None:
        t = self.tree()
        t.with_core_web()
        t.mark_built()
        res = t.run()
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(t.calls(), [])

    def test_a_newer_source_rebuilds_without_reinstalling(self) -> None:
        for rel in (("src", "index.ts"), ("generator", "cli.ts")):
            with self.subTest(changed="/".join(rel)):
                t = self.tree()
                t.with_core_web()
                t.mark_built()
                t.touch_newer(t.pkg.joinpath(*rel))
                self.assertEqual(t.run().returncode, 0)
                self.assertEqual(t.calls(), [("core/web", BUILD)])

    def test_a_new_file_in_a_source_directory_rebuilds(self) -> None:
        t = self.tree()
        t.with_core_web()
        t.mark_built()
        added = t.pkg / "src" / "added.ts"
        added.write_text("export {}\n")
        t.touch_newer(added)
        self.assertEqual(t.run().returncode, 0)
        self.assertEqual(t.calls(), [("core/web", BUILD)])

    def test_a_newer_manifest_or_lockfile_rebuilds(self) -> None:
        # The lockfile is also newer than the install marker, so it reinstalls first.
        t = self.tree()
        t.with_core_web()
        t.mark_built()
        t.touch_newer(t.core / "package-lock.json")
        self.assertEqual(t.run().returncode, 0)
        self.assertEqual(t.calls(), [("core/web", INSTALL), ("core/web", BUILD)])

    def test_a_missing_declaration_file_rebuilds(self) -> None:
        t = self.tree()
        t.with_core_web()
        t.mark_built()
        (t.pkg / "dist" / "index.d.ts").unlink()
        self.assertEqual(t.run().returncode, 0)
        self.assertEqual(t.calls(), [("core/web", BUILD)])

    def test_files_under_dist_and_node_modules_never_count_as_changed(self) -> None:
        t = self.tree()
        t.with_core_web()
        t.mark_built()
        (t.pkg / "node_modules").mkdir()
        (t.pkg / "node_modules" / "dep.js").write_text("")
        t.touch_newer(t.pkg / "node_modules" / "dep.js")
        (t.pkg / "dist" / "chunk.js").write_text("")
        t.touch_newer(t.pkg / "dist" / "chunk.js")
        self.assertEqual(t.run().returncode, 0)
        self.assertEqual(t.calls(), [])

    def test_npm_failure_is_the_scripts_failure(self) -> None:
        for fail_on, made, code in ((INSTALL.split()[0], 1, 7), ("run build", 2, 9)):
            with self.subTest(fail_on=fail_on):
                t = self.tree()
                t.with_core_web()
                res = t.run({"NPM_STUB_FAIL_ON": fail_on, "NPM_STUB_EXIT": str(code)})
                self.assertEqual(res.returncode, code)
                self.assertEqual(len(t.calls()), made)

    def test_works_from_any_working_directory(self) -> None:
        t = self.tree()
        t.with_core_web()
        (t.root / "web").mkdir()
        res = subprocess.run(
            ["sh", str(t.root / "scripts" / "ci" / "ensure_core_web.sh")],
            cwd=t.root / "web",
            env={"PATH": f"{t.bin}{os.pathsep}{os.environ['PATH']}", "NPM_STUB_LOG": str(t.log)},
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(t.calls(), [("core/web", INSTALL), ("core/web", BUILD)])

    def test_runs_under_dash_when_present(self) -> None:
        dash = shutil.which("dash")
        if dash is None:
            self.skipTest("dash not installed")
        t = self.tree()
        t.with_core_web()
        res = t.run(shell=dash)
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(len(t.calls()), 2)

    def test_has_no_bashisms_and_sets_no_build_id(self) -> None:
        text = SCRIPT.read_text()
        for shell in ("sh", "dash", "bash"):
            exe = shutil.which(shell)
            if exe:
                res = subprocess.run([exe, "-n", str(SCRIPT)], capture_output=True, text=True)
                self.assertEqual(res.returncode, 0, res.stderr)
        code = [ln for ln in text.splitlines() if ln.strip() and not ln.lstrip().startswith("#")]
        for token in ("[[", "<<<", "$((", "pushd", "BUILD_ID"):
            self.assertFalse(any(token in ln for ln in code), token)


class WebScriptsTest(unittest.TestCase):
    def test_every_web_script_that_needs_the_client_runs_the_guard_first(self) -> None:
        scripts = json.loads((REPO / "web" / "package.json").read_text())["scripts"]
        guard = "sh ../scripts/ci/ensure_core_web.sh && "
        for name in (
            "build",
            "typecheck",
            "typecheck:ts6",
            "test",
            "test:coverage",
            "test:ui",
            "test:all",
        ):
            with self.subTest(script=name):
                self.assertTrue(scripts[name].startswith(guard), scripts[name])

    def test_both_docker_frontend_stages_copy_the_guard_with_the_entry_point(self) -> None:
        # `RUN npm run build` runs the guard; a stage without the file fails there.
        for name in ("Dockerfile", "Dockerfile.build-cgo"):
            with self.subTest(file=name):
                self.assertIn(
                    "COPY scripts/ci/build_web.sh scripts/ci/ensure_core_web.sh ../scripts/ci/",
                    (REPO / name).read_text(),
                )


if __name__ == "__main__":
    unittest.main()
