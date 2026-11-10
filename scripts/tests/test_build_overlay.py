# file: scripts/tests/test_build_overlay.py
# version: 1.0.0
# guid: 4e8b1f27-6c3a-4d95-a0e2-9b7c5d1f3a86
# last-edited: 2026-11-10
"""Tests for scripts/r2corpus/overlay/build_overlay.py and the stage-2 corpus (F2-13).

The driver's CASES, the Go program's case lists and the committed manifest must
name the same cases; every blocker store must be listed with its files; the
overlay's quarantine set is exactly the cases labelled quarantined; and no
stage-1 entry may have changed shape.

Run: python3 -m pytest scripts/tests/test_build_overlay.py -q
"""

from __future__ import annotations

import importlib.util
import json
import pathlib
import re
import subprocess
import sys
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
PROGRAM = REPO_ROOT / "scripts" / "r2corpus" / "overlay"
SCRIPT = PROGRAM / "build_overlay.py"
CORPUS = REPO_ROOT / "internal" / "books" / "testdata" / "r2corpus"
MANIFEST = CORPUS / "manifest.json"


def load_driver():
    name = "build_overlay"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod  # before exec_module: dataclasses look the module up
    spec.loader.exec_module(mod)
    return mod


b = load_driver()


def manifest():
    return json.loads(MANIFEST.read_text(encoding="utf-8"))


def go_case_lists() -> tuple[list[str], list[tuple[str, str]]]:
    """The Go program's overlayCases and blockerCases, read from its source."""
    src = "\n".join(p.read_text(encoding="utf-8") for p in sorted(PROGRAM.glob("*.go")))

    def block(name: str) -> str:
        m = re.search(r"var " + name + r" = \[\]\w+\{\n(.*?)\n\}", src, re.S)
        assert m, name
        return m.group(1)

    overlay = re.findall(r'^\s*\{"([^"]+)", \w+\},', block("overlayCases"), re.M)
    blockers = re.findall(r'^\s*\{"([^"]+)", "([^"]+)", \w+\},', block("blockerCases"), re.M)
    return overlay, blockers


class DriverTest(unittest.TestCase):
    def test_help_exits_zero(self):
        p = subprocess.run([sys.executable, str(SCRIPT), "--help"], capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("--regenerate", p.stdout)

    def test_without_regenerate_nothing_is_written(self):
        p = subprocess.run([sys.executable, str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(p.returncode, 2)

    def test_list_prints_every_case(self):
        p = subprocess.run([sys.executable, str(SCRIPT), "--list"], capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        labels = [line.split("\t")[2] for line in p.stdout.splitlines()]
        self.assertEqual(labels, [c.label for c in b.CASES])

    def test_cases_are_well_formed(self):
        labels = [c.label for c in b.CASES]
        self.assertEqual(len(labels), len(set(labels)), "duplicate case label")
        for c in b.CASES:
            self.assertIn(c.group, ("overlay", "blockers"), c.label)
            self.assertIn(c.outcome, b.OUTCOMES, c.label)
            self.assertIn(c.step, ("M2", "M3", "M4", "M5", "M6"), c.label)
            self.assertTrue(c.expected and c.spec and c.family and c.key_prefix, c.label)
            self.assertTrue(re.fullmatch(r"[a-z0-9]+(-[a-z0-9]+)*", c.label), c.label)
        for c in b.overlay_cases():
            self.assertNotIn(
                c.outcome, (b.REFUSED, b.NOT_BLOCKING), f"{c.label}: the overlay must boot"
            )
        for c in b.blocker_cases():
            self.assertIn(c.outcome, (b.REFUSED, b.NOT_BLOCKING), c.label)
            self.assertEqual(c.step, "M3", c.label)

    def test_every_refused_blocker_has_its_own_store(self):
        stores: dict[str, list[str]] = {}
        for c in b.blocker_cases():
            stores.setdefault(c.store or c.label, []).append(c.label)
        for c in b.blocker_cases():
            if c.outcome == b.REFUSED:
                self.assertEqual(stores[c.store or c.label], [c.label], c.label)

    def test_go_program_writes_exactly_the_cases(self):
        overlay, blockers = go_case_lists()
        self.assertEqual(overlay, [c.label for c in b.overlay_cases()])
        self.assertEqual(blockers, [(c.label, c.store or c.label) for c in b.blocker_cases()])

    def test_every_go_file_carries_the_build_constraint(self):
        files = sorted(PROGRAM.glob("*.go"))
        self.assertTrue(files)
        for p in files:
            self.assertIn("\n//go:build r2corpus_overlay\n", p.read_text(encoding="utf-8"), p.name)


class CommittedManifestTest(unittest.TestCase):
    def test_overlay_and_blockers_match_cases(self):
        m = manifest()
        self.assertEqual([e["label"] for e in m["overlay"]], [c.label for c in b.overlay_cases()])
        self.assertEqual([e["label"] for e in m["blockers"]], [c.label for c in b.blocker_cases()])
        by_label = {c.label: c for c in b.CASES}
        for e in m["overlay"] + m["blockers"]:
            c = by_label[e["label"]]
            self.assertEqual(e["case"], c.label)
            for field in (
                "family",
                "step",
                "key_prefix",
                "outcome",
                "reported",
                "expected",
                "spec",
            ):
                self.assertEqual(e[field], getattr(c, field), f"{c.label}.{field}")
            self.assertEqual(e["store"], c.store_rel(), c.label)
            self.assertTrue(e["keys"], c.label)

    def test_every_store_is_listed_with_its_files(self):
        m = manifest()
        want = {b.OVERLAY_REL} | {f"{b.BLOCKERS_REL}/{s}" for s in b.blocker_stores()}
        for rel in want:
            self.assertIn(rel, m["stores"], rel)
            self.assertTrue(m["stores"][rel], rel)
            self.assertTrue((REPO_ROOT / rel).is_dir(), rel)
        listed = {
            k for k in m["stores"] if k == b.OVERLAY_REL or k.startswith(b.BLOCKERS_REL + "/")
        }
        self.assertEqual(listed, want)
        on_disk = {
            f"{b.BLOCKERS_REL}/{p.name}"
            for p in (REPO_ROOT / b.BLOCKERS_REL).iterdir()
            if p.is_dir()
        }
        self.assertEqual(on_disk, want - {b.OVERLAY_REL})

    def test_quarantine_set_is_the_quarantined_cases(self):
        m = manifest()
        quarantined = sorted(e["label"] for e in m["overlay"] if e["outcome"] == b.QUARANTINED)
        self.assertEqual(
            quarantined,
            sorted(
                [
                    "blob-field-unmapped",
                    "corrupt-aijob",
                    "corrupt-import-root",
                    "corrupt-inviteallow",
                    "corrupt-role",
                    "corrupt-run",
                    "corrupt-settings-secret",
                    "corrupt-syslog",
                    "role-all-unknown",
                ]
            ),
        )

    def test_keys_start_with_their_family(self):
        for e in manifest()["overlay"] + manifest()["blockers"]:
            prefix = e["key_prefix"]
            self.assertTrue(
                any(k.startswith(prefix) for k in e["keys"]), f"{e['label']}: no {prefix} key"
            )

    def test_stage1_entries_are_untouched_and_stage2_built_at_the_tag(self):
        m = manifest()
        stage1 = f"{b.w.CORPUS_REL}/stage1"
        self.assertIn(stage1, m["stores"])  # F2-12's store keeps its key
        self.assertTrue(m["stage1"])
        for e in m["stage1"]:
            self.assertEqual(set(e), {"case", "family", "step", "how", "store"}, e.get("case"))
            self.assertEqual(e["store"], stage1, e["case"])
        self.assertEqual(m["stage2"]["built_at"], m["r0_tag"])
        self.assertEqual(m["stage2"]["build_tag"], b.BUILD_TAG)

    def test_overlay_answers_are_listed_and_exist(self):
        answers = manifest().get("overlay_answers") or []
        self.assertTrue(answers)
        for a in answers:
            path = REPO_ROOT / a["file"]
            self.assertTrue(path.is_file(), a["file"])
            data = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(data["name"], a["name"])
            self.assertIsInstance(a["changed"], bool)

    def test_no_store_file_is_git_ignored(self):
        p = subprocess.run(
            ["git", "-C", str(REPO_ROOT), "status", "--porcelain", "--ignored", str(CORPUS)],
            capture_output=True,
            text=True,
        )
        if p.returncode != 0:
            self.skipTest("not a git checkout")
        self.assertEqual([line for line in p.stdout.splitlines() if line.startswith("!!")], [])


if __name__ == "__main__":
    unittest.main()
