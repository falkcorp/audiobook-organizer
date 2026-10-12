# file: scripts/tests/test_run_gates.py
# version: 1.1.0
# guid: 5b9d2e74-c1a6-4380-9f2e-d4a7b6c30e18
# last-edited: 2026-10-12
"""Tests for scripts/ci/run_gates.py and the one-way ratchet rule (07-G4)."""

from __future__ import annotations

import importlib.util
import re
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
            rg.parse_manifest(f"a{T}b{T}-{T}weird{T}-{T}shared\n")
        with self.assertRaises(rg.ManifestError):
            rg.parse_manifest(f"a{T}b{T}-{T}ratchet{T}-{T}shared\n")  # ratchet needs baseline
        with self.assertRaises(rg.ManifestError):
            rg.parse_manifest(
                f"a{T}b{T}-{T}check{T}-{T}shared\na{T}b{T}-{T}check{T}-{T}shared\n"
            )  # dup

    def test_select(self):
        gates = rg.parse_manifest(
            f"a{T}x{T}-{T}check{T}-{T}shared\nb{T}y{T}-{T}check{T}-{T}shared\n"
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

    def test_bot_never_edits_baseline_contents_in_repo(self):
        # a rise written by hand would still pass here; this only pins that the
        # ratchet scripts expose --measure for the bot
        for script in ("check-errcheck-ratchet.sh", "check-interface-width.sh"):
            self.assertIn("--measure", (ROOT / "scripts" / script).read_text())


class OwnJobTests(unittest.TestCase):
    def _ci(self) -> str:
        return (ROOT / ".github/workflows/ci.yml").read_text()

    def test_gates_invocation_runs_exactly_non_own_job_gates(self):
        gates = rg.parse_manifest((ROOT / "ci/gates.txt").read_text())
        m = re.search(r"python3 scripts/ci/run_gates\.py([^\n]*)", self._ci())
        self.assertIsNotNone(m)
        flags = m.group(1).split()
        self.assertEqual(flags, ["--exclude-own-jobs"])
        ran = {g.name for g in rg.select(gates, None, None, True)}
        self.assertEqual(ran, {g.name for g in gates if not g.own_job})
        self.assertEqual(
            {g.name for g in gates if g.own_job},
            {"errcheck", "interface-width", "deadcode-ratchet"},
        )

    def test_ci_yml_names_no_gates(self):
        names = {g.name for g in rg.parse_manifest((ROOT / "ci/gates.txt").read_text())}
        m = re.search(r"python3 scripts/ci/run_gates\.py([^\n]*)", self._ci())
        for tok in re.split(r"[\s,=]+", m.group(1)):
            self.assertNotIn(tok, names, "ci.yml must not name gates; use the manifest")
        self.assertNotIn("--skip", m.group(1))
        self.assertNotIn("--only", m.group(1))


if __name__ == "__main__":
    unittest.main()
