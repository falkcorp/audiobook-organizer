# file: scripts/tests/test_write_r0_corpus.py
# version: 1.0.1
# guid: e61bb4fe-2238-459c-be02-94efbf991c39
# last-edited: 2026-11-10
"""Tests for scripts/r2corpus/write_r0_corpus.py (F2-12).

No R0 binary is needed: the case list is checked statically against the
committed manifest.json, and the dry mode lists the API calls in order.

Run: python3 -m pytest scripts/tests/test_write_r0_corpus.py -q
"""

from __future__ import annotations

import contextlib
import hashlib
import importlib.util
import io
import json
import pathlib
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "r2corpus" / "write_r0_corpus.py"
MANIFEST = REPO_ROOT / "internal" / "books" / "testdata" / "r2corpus" / "manifest.json"


def load_writer():
    name = "write_r0_corpus"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod  # before exec_module: dataclasses look the module up
    spec.loader.exec_module(mod)
    return mod


w = load_writer()


class CoverageTest(unittest.TestCase):
    def test_every_step_row_has_a_case(self):
        steps = {c.step for c in w.CASES}
        for step in w.STEPS:
            self.assertIn(step, steps, f"no case covers coverage-table row {step}")

    def test_case_names_unique_and_described(self):
        names = [c.case for c in w.CASES]
        self.assertEqual(len(names), len(set(names)))
        for c in w.CASES:
            self.assertTrue(c.family and c.how and c.steps, c.case)
            self.assertIn(c.step, w.STEPS, c.case)

    def test_manifest_stage1_matches_case_list(self):
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        got = [(e["case"], e["family"], e["step"], e["how"]) for e in manifest["stage1"]]
        want = [(c.case, c.family, c.step, c.how) for c in w.CASES]
        self.assertEqual(got, want, "manifest.json stage1 is stale: rerun --regenerate")
        for e in manifest["stage1"]:
            self.assertEqual(e["store"], w.STAGE1_REL)
        stage1_steps = {e["step"] for e in manifest["stage1"]}
        self.assertTrue(set(w.STEPS) <= stage1_steps)

    def test_manifest_records_tag_header_and_gaps(self):
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        self.assertEqual(next(iter(manifest)), "_header", "_header must be the first key")
        self.assertEqual(manifest["_header"]["file"], f"{w.CORPUS_REL}/manifest.json")
        self.assertEqual(manifest["r0_tag"], w.read_r0_tag())
        self.assertEqual(manifest["overlay"], [])
        self.assertEqual(manifest["blockers"], [])
        self.assertIn(w.STAGE1_REL, manifest["stores"])
        gaps = {g["family"] for g in manifest["not_writable_by_r0_api"]}
        self.assertIn("oauthid:", gaps)


class DryRunTest(unittest.TestCase):
    def test_dry_run_lists_calls_without_a_binary(self):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = w.main(["--dry-run"])
        self.assertEqual(rc, 0)
        out = buf.getvalue()
        for needle in (
            "HOOK bootstrap exchange",
            "POST /api/v1/users/invite",
            "POST /api/v1/auth/api-keys",
            "PUT /api/v1/config",
            "POST /api/v1/import-paths",
            "PUT /api/v1/tasks/",
            "POST /api/v1/operations/v2",
            "POST /api/v1/operations/pause",
            "POST /api/v1/playlists",
            "PATCH /api/me/progress/",
        ):
            self.assertIn(needle, out)
        for case in w.CASES:
            self.assertIn(f"## {case.step} {case.case}:", out)

    def test_dry_run_order_is_deterministic(self):
        a, b = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(a):
            w.main(["--dry-run"])
        with contextlib.redirect_stdout(b):
            w.main(["--dry-run"])
        self.assertEqual(a.getvalue(), b.getvalue())

    def test_no_flag_writes_nothing(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(w.main([]), 2)

    def test_foreign_binary_refuses_the_committed_corpus(self):
        with self.assertRaises(w.CorpusError):
            w.write_corpus(
                w.build_parser().parse_args(
                    ["--regenerate", "--binary", str(SCRIPT), "--allow-foreign-binary"]
                )
            )


class TagTest(unittest.TestCase):
    def _tag(self, text):
        with tempfile.TemporaryDirectory() as d:
            p = pathlib.Path(d) / "r0_release.txt"
            p.write_text(text, encoding="utf-8")
            return w.read_r0_tag(p)

    def test_reader_rule(self):
        self.assertEqual(self._tag("# file: x\n# c\n\nv1.2.3\n"), "v1.2.3")

    def test_zero_or_two_tag_lines_fail(self):
        with self.assertRaises(w.CorpusError):
            self._tag("# only comments\n")
        with self.assertRaises(w.CorpusError):
            self._tag("v1.2.3\nv1.2.4\n")

    def test_committed_file(self):
        self.assertRegex(w.read_r0_tag(), r"^v\d+\.\d+\.\d+$")


class HygieneTest(unittest.TestCase):
    def test_scan_for_leaks(self):
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / "clean.log").write_bytes(b"Batman: Year One\n")
            self.assertEqual(w.scan_for_leaks(root), [])
            (root / "000002.log").write_bytes(b"\x00key abk_" + b"A" * 32 + b"\x00")
            (root / "x.log").write_bytes(b"peer 192.168.12.7 said hi")
            found = "\n".join(w.scan_for_leaks(root))
            self.assertIn("000002.log: abk_ API key", found)
            self.assertIn("x.log: private IPv4", found)

    def test_one_boot_credentials_are_removed(self):
        with tempfile.TemporaryDirectory() as d:
            layout = w.Layout(pathlib.Path(d))
            layout.state.mkdir(parents=True)
            for name in (".bootstrap-token", ".readonly-key", ".encryption_key"):
                (layout.state / name).write_text("x")
            removed = w.scrub_one_boot_credentials(layout)
            self.assertEqual(sorted(removed), [".bootstrap-token", ".readonly-key"])
            self.assertTrue((layout.state / ".encryption_key").exists())

    def test_fixture_audio_files_are_distinct(self):
        with tempfile.TemporaryDirectory() as d:
            rels = w.lay_out_import(pathlib.Path(d))
            hashes = {hashlib.sha256((pathlib.Path(d) / r).read_bytes()).hexdigest() for r in rels}
            self.assertEqual(len(hashes), len(rels), "two fixture files are byte-identical")

    def test_boot_spec_paths_stay_in_the_workdir(self):
        layout = w.Layout(pathlib.Path(w.DEFAULT_WORKDIR))
        spec = w.boot_spec(layout, w.DEFAULT_PORT)
        for arg in spec["argv"]:
            if arg.startswith("/"):
                self.assertTrue(arg.startswith(w.DEFAULT_WORKDIR), arg)
        # The import root and root_dir must not sit inside the database's
        # directory: R0's sweeps never descend into it.
        self.assertNotIn(layout.dbdir, layout.import_dir.parents)
        self.assertNotIn(layout.dbdir, layout.library.parents)


if __name__ == "__main__":
    unittest.main()
