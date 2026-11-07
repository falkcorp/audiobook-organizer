# file: scripts/tests/test_build_web.py
# version: 1.0.0
# guid: 8bca5383-6b49-4790-a24d-3799d152898c
# last-edited: 2026-11-07
"""Tests for the one web-dependency entry point, scripts/ci/build_web.sh (R1-05;
platform-core 07 §7.3.2), the ``make web-deps`` stamp target behind it, and the
rule that no other build site runs ``npm ci`` itself.

The script runs against a temp tree (a copy of the script at
``<tmp>/scripts/ci/build_web.sh`` so its root resolution is the real one) with a
stub ``npm`` first on ``PATH`` that logs its working directory, argv and
``BUILD_ID`` and can be told to fail. So what is proved is the call order, the
working directories and the exit status, not the behaviour of npm.

Stdlib only; run with ``python3 -m unittest scripts.tests.test_build_web``
(CI's ``unittest discover -s scripts`` finds it) or with pytest.
"""

from __future__ import annotations

import os
import pathlib
import re
import shutil
import subprocess
import tempfile
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO / "scripts" / "ci" / "build_web.sh"

# Logs "cwd|argv|BUILD_ID" per call; exits NPM_STUB_EXIT when the call's first two
# arguments match NPM_STUB_FAIL_ON (e.g. "ci" or "run build"), else 0.
STUB_NPM = """#!/bin/sh
printf '%s|%s|%s\\n' "$(pwd -P)" "$*" "${BUILD_ID-<unset>}" >> "$NPM_STUB_LOG"
case "${NPM_STUB_FAIL_ON:-}" in
  "") ;;
  *) case "$*" in "$NPM_STUB_FAIL_ON"*) exit "${NPM_STUB_EXIT:-1}" ;; esac ;;
esac
exit 0
"""


class Tree:
    """A temp repo root holding the script, a stub npm, and web/ (and core/web/)."""

    def __init__(self, *, core_web: bool) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        # realpath: the stub logs `pwd -P`, and macOS temp dirs sit behind a symlink.
        self.root = pathlib.Path(os.path.realpath(self._tmp.name))
        (self.root / "scripts" / "ci").mkdir(parents=True)
        shutil.copy(SCRIPT, self.root / "scripts" / "ci" / "build_web.sh")
        (self.root / "web").mkdir()
        if core_web:
            (self.root / "core" / "web").mkdir(parents=True)
            (self.root / "core" / "web" / "package.json").write_text("{}\n")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        npm = self.bin / "npm"
        npm.write_text(STUB_NPM)
        npm.chmod(0o755)
        self.log = self.root / "npm.log"

    def close(self) -> None:
        self._tmp.cleanup()

    def run(
        self,
        *args: str,
        shell: str = "sh",
        env: dict[str, str] | None = None,
        cwd: pathlib.Path | None = None,
    ) -> subprocess.CompletedProcess[str]:
        full = {
            "PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            "NPM_STUB_LOG": str(self.log),
        }
        full.update(env or {})
        return subprocess.run(
            [shell, "scripts/ci/build_web.sh", *args],
            cwd=cwd or self.root,
            env=full,
            capture_output=True,
            text=True,
            check=False,
        )

    def calls(self) -> list[tuple[str, str, str]]:
        if not self.log.exists():
            return []
        out = []
        for line in self.log.read_text().splitlines():
            cwd, argv, build_id = line.split("|")
            out.append((str(pathlib.Path(cwd).relative_to(self.root)), argv, build_id))
        return out


class BuildWebScriptTest(unittest.TestCase):
    def tree(self, *, core_web: bool) -> Tree:
        t = Tree(core_web=core_web)
        self.addCleanup(t.close)
        return t

    def test_without_core_web_runs_one_ci_in_web(self) -> None:
        t = self.tree(core_web=False)
        res = t.run("deps")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(t.calls(), [("web", "ci --no-audit --no-fund", "<unset>")])

    def test_with_core_web_builds_core_then_installs_web(self) -> None:
        t = self.tree(core_web=True)
        res = t.run("deps")
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(
            [(cwd, argv) for cwd, argv, _ in t.calls()],
            [
                ("core/web", "ci --no-audit --no-fund"),
                ("core/web", "run build -w @mediacore/api-client"),
                ("web", "ci --no-audit --no-fund"),
            ],
        )

    def test_core_web_dir_without_manifest_is_not_a_core_web(self) -> None:
        t = self.tree(core_web=True)
        (t.root / "core" / "web" / "package.json").unlink()
        self.assertEqual(t.run("deps").returncode, 0)
        self.assertEqual([c[0] for c in t.calls()], ["web"])

    def test_nonzero_npm_exit_stops_the_script_with_that_exit(self) -> None:
        # (failing call, expected number of npm calls made, expected exit)
        for fail_on, made, code in (
            ("ci", 1, 7),  # core's ci: nothing after it runs
            ("run build", 2, 9),  # the api-client build: web's ci never runs
        ):
            with self.subTest(fail_on=fail_on):
                t = self.tree(core_web=True)
                res = t.run(
                    "deps",
                    env={"NPM_STUB_FAIL_ON": fail_on, "NPM_STUB_EXIT": str(code)},
                )
                self.assertEqual(res.returncode, code)
                self.assertEqual(len(t.calls()), made)

    def test_web_ci_failure_is_the_scripts_failure(self) -> None:
        t = self.tree(core_web=False)
        res = t.run("deps", env={"NPM_STUB_FAIL_ON": "ci", "NPM_STUB_EXIT": "5"})
        self.assertEqual(res.returncode, 5)

    def test_caller_build_id_reaches_every_npm_call(self) -> None:
        t = self.tree(core_web=True)
        self.assertEqual(t.run("deps", env={"BUILD_ID": "rc-123"}).returncode, 0)
        self.assertEqual([c[2] for c in t.calls()], ["rc-123"] * 3)

    def test_script_never_sets_build_id(self) -> None:
        t = self.tree(core_web=True)
        env = {k: v for k, v in os.environ.items() if k != "BUILD_ID"}
        self.assertNotIn("BUILD_ID", env)
        res = subprocess.run(
            ["sh", "scripts/ci/build_web.sh", "deps"],
            cwd=t.root,
            env={
                **env,
                "PATH": f"{t.bin}{os.pathsep}{env['PATH']}",
                "NPM_STUB_LOG": str(t.log),
            },
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual({c[2] for c in t.calls()}, {"<unset>"})
        self.assertNotRegex(SCRIPT.read_text(), r"BUILD_ID\s*=")

    def test_unknown_or_missing_argument_exits_2_without_calling_npm(self) -> None:
        for args in ((), ("build",), ("deps", "extra"), ("",)):
            with self.subTest(args=args):
                t = self.tree(core_web=True)
                res = t.run(*args)
                self.assertEqual(res.returncode, 2)
                self.assertIn("usage", res.stderr)
                self.assertEqual(t.calls(), [])

    def test_works_from_any_working_directory(self) -> None:
        t = self.tree(core_web=True)
        res = subprocess.run(
            ["sh", str(t.root / "scripts" / "ci" / "build_web.sh"), "deps"],
            cwd=t.root / "web",
            env={
                "PATH": f"{t.bin}{os.pathsep}{os.environ['PATH']}",
                "NPM_STUB_LOG": str(t.log),
            },
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual([c[0] for c in t.calls()], ["core/web", "core/web", "web"])

    def test_caller_npm_config_reaches_npm(self) -> None:
        # How the ignore-scripts and prefer-offline sites keep their flags: npm reads
        # npm_config_* from the environment, so the script needs no flag plumbing.
        t = self.tree(core_web=False)
        npm = t.bin / "npm"
        npm.write_text(
            "#!/bin/sh\n"
            'printf "%s\\n" "${npm_config_ignore_scripts-<unset>}" >> "$NPM_STUB_LOG"\n'
        )
        self.assertEqual(t.run("deps", env={"npm_config_ignore_scripts": "true"}).returncode, 0)
        self.assertEqual(t.log.read_text().split(), ["true"])

    def test_parses_under_sh_and_dash_without_bashisms(self) -> None:
        text = SCRIPT.read_text()
        self.assertTrue(text.startswith("#!/bin/sh\n"))
        for shell in ("sh", "dash"):
            exe = shutil.which(shell)
            if exe is None:
                if shell == "dash":
                    print("dash not installed: the dash leg is skipped, sh -n still ran")
                    continue
                self.fail("no sh")
            with self.subTest(shell=shell):
                res = subprocess.run([exe, "-n", str(SCRIPT)], capture_output=True, text=True)
                self.assertEqual(res.returncode, 0, res.stderr)
        code = [
            ln for ln in text.splitlines() if ln.strip() and not ln.lstrip().startswith("#")
        ]
        for pat in (r"\[\[", r"\bfunction\b", r"\blocal\b", r"<<<", r"\$\(\(", r"\bsource\b", r"==", r"\bpushd\b", r"\barray\b"):
            self.assertFalse(
                any(re.search(pat, ln) for ln in code), f"bash-only construct {pat!r}"
            )
        # 07 §7.3.2: "a POSIX sh script under 20 lines".
        self.assertLess(len(code), 20)

    def test_runs_under_dash_when_present(self) -> None:
        dash = shutil.which("dash")
        if dash is None:
            self.skipTest("dash not installed")
        t = self.tree(core_web=True)
        res = t.run("deps", shell=dash)
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(len(t.calls()), 3)


@unittest.skipUnless(shutil.which("make"), "make not installed")
class MakefileTargetsTest(unittest.TestCase):
    def dry_run(self, *targets: str) -> str:
        # -B: pretend every prerequisite is out of date, so the dry run shows the
        # whole chain whatever stamp the working tree happens to hold.
        res = subprocess.run(
            ["make", "-nB", *targets],
            cwd=REPO,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(res.returncode, 0, res.stderr)
        return res.stdout

    def test_web_deps_runs_the_script_then_writes_the_stamp(self) -> None:
        out = self.dry_run("web-deps")
        self.assertIn("scripts/ci/build_web.sh deps", out)
        self.assertIn("web/node_modules/.web-deps.stamp", out)
        self.assertLess(out.index("build_web.sh"), out.index(".web-deps.stamp"))

    def test_the_five_web_targets_install_through_web_deps_first(self) -> None:
        for target, marker in (
            ("web-build", "npm run build"),
            ("web-test", "npm run test"),
            ("web-lint", "npm run lint"),
            ("web-typecheck", "npm run typecheck"),
            ("web-install", "build_web.sh deps"),
        ):
            with self.subTest(target=target):
                out = self.dry_run(target)
                self.assertIn("build_web.sh deps", out)
                self.assertLessEqual(out.index("build_web.sh"), out.index(marker))
                if marker != "build_web.sh deps":
                    self.assertLess(out.index("build_web.sh"), out.index(marker))

    def test_web_dev_installs_but_does_not_build_core(self) -> None:
        out = self.dry_run("web-dev")
        self.assertIn("build_web.sh deps", out)
        self.assertIn("npm run dev", out)


class NoOtherNpmCiSitesTest(unittest.TestCase):
    """09 §9.1's twelve ``npm ci`` sites all go through the entry point: the only
    file that runs ``npm ci`` is build_web.sh (the acceptance grep, kept as a test)."""

    def sites(self) -> list[str]:
        files = [REPO / "Dockerfile", REPO / "Dockerfile.build-cgo", REPO / "Makefile"]
        files += sorted((REPO / ".github" / "workflows").glob("*.y*ml"))
        files += sorted((REPO / ".woodpecker").glob("*.y*ml"))
        files += [REPO / "scripts" / "ci_remote.py"]
        hits = []
        for f in files:
            if not f.is_file():
                continue
            for n, line in enumerate(f.read_text().splitlines(), 1):
                if re.search(r"\bnpm\s+ci\b", line) and not line.lstrip().startswith("#"):
                    hits.append(f"{f.relative_to(REPO)}:{n}: {line.strip()}")
        return hits

    def test_no_build_site_runs_npm_ci_itself(self) -> None:
        self.assertEqual(self.sites(), [])

    def test_release_callers_pre_build_script_is_the_entry_point(self) -> None:
        for name in ("prerelease.yml", "release-prod.yml"):
            with self.subTest(workflow=name):
                text = (REPO / ".github" / "workflows" / name).read_text()
                self.assertIn(
                    "pre-build-script: 'sh scripts/ci/build_web.sh deps && cd web && npm run build'",
                    text,
                )

    def test_release_callers_turn_the_reusable_frontend_half_off_by_the_only_switch(self) -> None:
        # reusable-release.yml resolves `frontend-enabled: false` to "auto", not off;
        # `disabled-languages` is the only way to say "never".
        for name in ("prerelease.yml", "release-prod.yml"):
            with self.subTest(workflow=name):
                text = (REPO / ".github" / "workflows" / name).read_text()
                m = re.search(r"^\s*disabled-languages:\s*'([^']*)'", text, re.M)
                self.assertIsNotNone(m, "no disabled-languages input")
                assert m is not None
                self.assertIn("frontend", m.group(1).split(","))
                self.assertNotRegex(text, r"(?m)^\s*frontend-enabled:")


if __name__ == "__main__":
    unittest.main()
