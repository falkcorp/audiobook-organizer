# file: scripts/tests/test_run_gates.py
# version: 1.2.0
# guid: 5b9d2e74-c1a6-4380-9f2e-d4a7b6c30e18
# last-edited: 2026-10-12
"""Tests for scripts/ci/run_gates.py and the one-way ratchet rule (07-G4)."""

from __future__ import annotations

import importlib.util
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location("run_gates", ROOT / "scripts/ci/run_gates.py")
rg = importlib.util.module_from_spec(_spec)
sys.modules["run_gates"] = rg
_spec.loader.exec_module(rg)

T = "\t"


class ManifestTests(unittest.TestCase):
    def test_real_manifest_lists_all_gates(self):
        gates = rg.parse_manifest((ROOT / "ci/gates.txt").read_text())
        self.assertEqual(
            {g.name for g in gates},
            {
                "errcheck",
                "interface-width",
                "deadcode-ratchet",
                "store-width",
                "store-references",
                "layering",
                "appconfig-reads",
                "slog-guard",
            },
        )

    def test_rejects_bad_rows(self):
        with self.assertRaises(rg.ManifestError):
            rg.parse_manifest("a\tb\t-\tcheck\t-\n")  # 4 columns
        with self.assertRaises(rg.ManifestError):
            rg.parse_manifest(f"a{T}b{T}-{T}weird{T}-{T}go-suite\n")
        with self.assertRaises(rg.ManifestError):
            rg.parse_manifest(f"a{T}b{T}-{T}ratchet{T}-{T}go-suite\n")  # ratchet needs baseline
        with self.assertRaises(rg.ManifestError):
            rg.parse_manifest(
                f"a{T}b{T}-{T}check{T}-{T}go-suite\na{T}b{T}-{T}check{T}-{T}go-suite\n"
            )  # dup

    def test_select(self):
        gates = rg.parse_manifest(
            f"a{T}x{T}-{T}check{T}-{T}go-suite\nb{T}y{T}-{T}check{T}-{T}go-suite\n"
        )
        self.assertEqual([g.name for g in rg.select(gates, "a", None)], ["a"])
        self.assertEqual([g.name for g in rg.select(gates, None, "a")], ["b"])
        with self.assertRaises(rg.ManifestError):
            rg.select(gates, "nope", None)


class LowerTests(unittest.TestCase):
    TEXT = "# header\n\n779\n"

    def test_only_lowers(self):
        self.assertEqual(rg.lowered_int_baseline(self.TEXT, 770), "# header\n\n770\n")
        self.assertIsNone(rg.lowered_int_baseline(self.TEXT, 779))  # equal
        self.assertIsNone(rg.lowered_int_baseline(self.TEXT, 800))  # rise: never written

    def _run(self, baseline: str, measured_cmd: str, kind: str = "ratchet"):
        d = Path(tempfile.mkdtemp())
        (d / "b").write_text(baseline)
        gate = rg.Gate("g", "true", "b", kind, measured_cmd)
        rc = rg.lower_all(d, [gate], None)
        return rc, (d / "b").read_text()

    def test_lower_rewrites_downward_only(self):
        self.assertEqual(self._run("# c\n10\n", "echo 7"), (0, "# c\n7\n"))
        self.assertEqual(self._run("# c\n10\n", "echo 10"), (0, "# c\n10\n"))
        self.assertEqual(self._run("# c\n10\n", "echo 12"), (0, "# c\n10\n"))

    def test_unmeasurable_is_error_not_a_zero(self):
        rc, text = self._run("10\n", "echo oops")
        self.assertEqual((rc, text), (2, "10\n"))
        rc, text = self._run("10\n", "exit 3")
        self.assertEqual((rc, text), (2, "10\n"))

    def test_set_prune_that_adds_is_reverted(self):
        rc, text = self._run("a\nb\n", "printf 'a\\nb\\nc\\n' > b", kind="ratchet-set")
        self.assertEqual((rc, text), (2, "a\nb\n"))
        rc, text = self._run("a\nb\n", "printf 'a\\n' > b", kind="ratchet-set")
        self.assertEqual((rc, text), (0, "a\n"))


class RunTests(unittest.TestCase):
    def test_exit_codes(self):
        d = Path(tempfile.mkdtemp())
        ok = rg.Gate("ok", "true", "-", "check", "-")
        bad = rg.Gate("bad", "false", "-", "check", "-")
        self.assertEqual(rg.run_all(d, [ok]), 0)
        self.assertEqual(rg.run_all(d, [ok, bad]), 1)


class OneWayRuleTests(unittest.TestCase):
    """A ratchet script must not fail when the count falls (D47)."""

    def test_no_went_down_failure_branch(self):
        gates = rg.parse_manifest((ROOT / "ci/gates.txt").read_text())
        checked = 0
        for g in gates:
            if g.kind != "ratchet":
                continue
            m = re.search(r"scripts/\S+", g.command)
            self.assertIsNotNone(m, g.name)
            src = (ROOT / m.group(0)).read_text()
            checked += 1
            self.assertNotRegex(src, r"FAIL:[^\n]*went DOWN", g.name)
            # the "actual below baseline" block must not exit at all
            blk = re.search(
                r'if \[\[ "\$actual" -lt "\$baseline" \]\]; then\n(.*?)\nfi\n', src, re.S
            )
            self.assertIsNotNone(blk, f"{g.name}: no actual<baseline branch found")
            self.assertNotIn("exit", blk.group(1), f"{g.name}: falling count must not exit")
        self.assertGreaterEqual(checked, 2)

    def test_ratchet_scripts_expose_measure(self):
        for script in ("check-errcheck-ratchet.sh", "check-interface-width.sh"):
            self.assertIn('"--measure"', (ROOT / "scripts" / script).read_text())


class LowerReportTests(unittest.TestCase):
    def test_lower_touches_only_the_baseline_file(self):
        d = Path(tempfile.mkdtemp())
        (d / "b").write_text("9\n")
        (d / "other.go").write_text("const x = 9\n")
        rg.lower_all(d, [rg.Gate("g", "true", "b", "ratchet", "echo 3", "own-job")], None)
        self.assertEqual((d / "b").read_text(), "3\n")
        self.assertEqual((d / "other.go").read_text(), "const x = 9\n")

    def test_const_hint_reported_not_written(self):
        d = Path(tempfile.mkdtemp())
        (d / "t.go").write_text("const base = 9\n")
        note = "NOTE (not a failure): now 5; lower base"
        gate = rg.Gate("c", f"echo '{note}'", "t.go", "ratchet-const", "-", "go-suite")
        rep = d / "rep.md"
        self.assertEqual(rg.lower_all(d, [gate], rep), 0)
        self.assertEqual((d / "t.go").read_text(), "const base = 9\n")
        self.assertIn(note, rep.read_text())

    def test_failed_measurement_still_writes_report_and_keeps_other_lowerings(self):
        d = Path(tempfile.mkdtemp())
        (d / "ok").write_text("9\n")
        (d / "bad").write_text("9\n")
        gates = [
            rg.Gate("good", "true", "ok", "ratchet", "echo 4", "own-job"),
            rg.Gate("broken", "true", "bad", "ratchet", "exit 3", "own-job"),
        ]
        rep = d / "rep.md"
        self.assertEqual(rg.lower_all(d, gates, rep), 2)
        self.assertEqual((d / "ok").read_text(), "4\n")
        self.assertEqual((d / "bad").read_text(), "9\n")
        text = rep.read_text()
        self.assertIn("9 -> 4", text)
        self.assertIn("broken", text)


class CiCoverageTests(unittest.TestCase):
    """Every manifest gate must be executed by CI, and ci.yml names no gates."""

    def _gates(self):
        return rg.parse_manifest((ROOT / "ci/gates.txt").read_text())

    def _ci_lines(self) -> str:
        text = (ROOT / ".github/workflows/ci.yml").read_text()
        return "\n".join(ln for ln in text.splitlines() if not ln.lstrip().startswith("#"))

    def test_ci_yml_names_no_gates(self):
        ci = self._ci_lines()
        self.assertNotIn("run_gates.py", ci)
        self.assertNotIn("ci/gates.txt", ci)

    def test_own_job_gates_run_in_a_ci_job(self):
        ci = self._ci_lines()
        makefile = (ROOT / "Makefile").read_text()
        for g in (g for g in self._gates() if g.job == "own-job"):
            script = re.search(r"scripts/\S+", g.command).group(0)
            via_make = any(
                script in recipe and re.search(rf"make\b[^\n]*\b{re.escape(tgt)}\b", ci)
                for tgt, recipe in re.findall(
                    r"^([A-Za-z0-9_-]+):[^\n]*\n((?:\t[^\n]*\n?)+)", makefile, re.M
                )
            )
            self.assertTrue(script in ci or via_make, f"{g.name}: no ci.yml step runs {script}")

    def test_go_suite_gates_are_tests_that_run_under_short(self):
        for g in (g for g in self._gates() if g.job == "go-suite"):
            m = re.match(r"go test (\./\S+?)/?(?: -run (\w+))?(?: |$)", g.command)
            self.assertIsNotNone(m, f"{g.name}: not a plain go test command")
            pkg, run = m.group(1), m.group(2)
            funcs = {}
            for f in (ROOT / pkg).glob("*_test.go"):
                src = f.read_text()
                for fm in re.finditer(
                    r"^func (Test\w+)\(t \*testing\.T\) \{\n(.*?)^\}", src, re.M | re.S
                ):
                    funcs[fm.group(1)] = fm.group(2)
            wanted = {run} if run else set(funcs)
            runnable = [n for n in wanted if n in funcs and "testing.Short()" not in funcs[n]]
            self.assertTrue(runnable, f"{g.name}: no Test function that runs under -short in {pkg}")
            if run:
                self.assertIn(run, funcs, f"{g.name}: {run} does not exist")
                self.assertEqual(runnable, [run], f"{g.name}: {run} skips under -short")


class PushBotBranchTests(unittest.TestCase):
    """scripts/ci/push_bot_branch.sh must update a bot branch that already exists."""

    def _git(self, cwd, *args):
        return subprocess.run(
            ["git", "-c", "user.name=t", "-c", "user.email=t@example.com", *args],
            cwd=cwd,
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()

    def _push(self, clone, remote):
        return subprocess.run(
            ["bash", str(ROOT / "scripts/ci/push_bot_branch.sh"), remote, "bot/x"],
            cwd=clone,
            capture_output=True,
            text=True,
        )

    def test_creates_then_replaces_existing_branch_by_url(self):
        d = Path(tempfile.mkdtemp())
        remote = str(d / "remote.git")
        self._git(d, "init", "--bare", "-q", remote)
        clones = []
        for n in ("a", "b"):
            c = d / n
            self._git(d, "init", "-q", str(c))
            (c / "f").write_text(n)
            self._git(c, "add", "f")
            self._git(c, "commit", "-q", "-m", n)
            clones.append(c)
        first = self._push(clones[0], remote)  # branch absent: empty lease
        self.assertEqual(first.returncode, 0, first.stderr)
        second = self._push(clones[1], remote)  # branch exists, unrelated history
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(
            self._git(remote, "rev-parse", "refs/heads/bot/x"),
            self._git(clones[1], "rev-parse", "HEAD"),
        )


if __name__ == "__main__":
    unittest.main()
