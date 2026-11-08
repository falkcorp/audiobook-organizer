# file: scripts/tests/test_fold_split_readiness.py
# version: 1.0.0
# guid: 3fca1fc2-f7e7-4839-9285-72dbdcf11d35
# last-edited: 2026-11-08
"""Tests for scripts/ci/fold_split_readiness.py (12 §12.3.11; the nightly split-readiness.yml fold-in)."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import pathlib
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location("fold_split_readiness", ROOT / "scripts/ci/fold_split_readiness.py")
assert _spec and _spec.loader
fold = importlib.util.module_from_spec(_spec)
sys.modules["fold_split_readiness"] = fold
_spec.loader.exec_module(fold)
lib = fold.lib

DOC = (ROOT / fold.DOC).read_text(encoding="utf-8")


def night(sc1: str = lib.FAIL, sc8: str = lib.PASS) -> dict[str, lib.Result]:
    return {
        "SC1": lib.Result("SC1", "Public API stable", sc1, "0 checkpoint(s) recorded | none"),
        "SC3": lib.Result("SC3", "Second consumer", lib.PASS, "builds"),
        "SC5": lib.Result("SC5", "Coverage", lib.FAIL, "total 81.3%"),
        "SC6": lib.Result("SC6", "Documented", lib.NOT_APPLICABLE, "not applicable"),
        "SC8": lib.Result("SC8", "npm packages", sc8, "builds"),
        "SC9": lib.Result("SC9", "No vulnerabilities", lib.PASS, "found nothing"),
    }


class FoldTest(unittest.TestCase):
    def test_the_committed_document_has_its_markers_and_folds(self) -> None:
        folded = fold.fold(DOC, "2026-11-09", "a" * 40, night())
        self.assertIn("Night of 2026-11-09, commit `aaaaaaaaaaaa`.", folded)
        self.assertIn("| 2026-11-09 | `aaaaaaaaaaaa` | FAIL | PASS | FAIL | N/A | PASS | PASS |", folded)
        self.assertNotIn("No night has been folded in yet.", folded)

    def test_only_the_two_regions_and_the_header_change(self) -> None:
        folded = fold.fold(DOC, "2026-11-09", "a" * 40, night())
        before = fold.HISTORY_RE.sub("", fold.LATEST_RE.sub("", DOC))
        after = fold.HISTORY_RE.sub("", fold.LATEST_RE.sub("", folded))
        diff = [(a, b) for a, b in zip(before.splitlines(), after.splitlines()) if a != b]
        self.assertEqual([a for a, _ in diff], ["<!-- version: 1.0.0 -->", "<!-- last-edited: 2026-11-08 -->"])
        self.assertIn("<!-- version: 1.0.1 -->", folded)
        self.assertIn("<!-- last-edited: 2026-11-09 -->", folded)
        self.assertIn("<!-- guid: 54041305-9707-4448-b5af-133d66d4696a -->", folded)

    def test_an_identical_rerun_changes_nothing_and_does_not_bump(self) -> None:
        once = fold.fold(DOC, "2026-11-09", "a" * 40, night())
        self.assertEqual(fold.fold(once, "2026-11-09", "a" * 40, night()), once)

    def test_a_rerun_the_same_date_replaces_that_dates_row(self) -> None:
        once = fold.fold(DOC, "2026-11-09", "a" * 40, night(sc1=lib.FAIL))
        twice = fold.fold(once, "2026-11-09", "b" * 40, night(sc1=lib.PASS))
        rows = [ln for ln in twice.splitlines() if ln.startswith("| 2026-11-09")]
        self.assertEqual(rows, ["| 2026-11-09 | `bbbbbbbbbbbb` | PASS | PASS | FAIL | N/A | PASS | PASS |"])

    def test_history_is_newest_first_and_capped(self) -> None:
        text = DOC
        for day in range(1, 6):
            text = fold.fold(text, f"2026-12-0{day}", "c" * 40, night(), keep=3)
        dates = [ln.split("|")[1].strip() for ln in text.splitlines() if ln.startswith("| 2026-12")]
        self.assertEqual(dates, ["2026-12-05", "2026-12-04", "2026-12-03"])

    def test_a_pipe_in_a_measured_value_does_not_break_the_table(self) -> None:
        folded = fold.fold(DOC, "2026-11-09", "a" * 40, night())
        row = next(ln for ln in folded.splitlines() if ln.startswith("| SC1 "))
        self.assertIn("recorded \\| none", row)

    def test_a_document_without_markers_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            fold.fold("<!-- version: 1.0.0 -->\n# nothing\n", "2026-11-09", "a" * 40, night())


class MainTest(unittest.TestCase):
    def test_main_reads_the_report_and_the_extra_results(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            criteria = [vars(r) for r in night().values() if r.id in ("SC1", "SC5", "SC6", "SC9")]
            (root / "readiness.json").write_text(json.dumps({"date": "2026-11-09", "commit": "d" * 40, "criteria": criteria}))
            lib.write_result(root, lib.Result("SC3", "Second consumer", lib.PASS, "builds"))
            # SC8 deliberately not written: it must read as ERROR, not as a pass.
            doc = root / "split-readiness.md"
            doc.write_text(DOC)
            argv = ["--report", str(root / "readiness.json"), "--extra", str(root), "--doc", str(doc)]
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(fold.main(argv), 0)
                first = doc.read_text()
                self.assertEqual(fold.main(argv), 0)
            self.assertIn("| 2026-11-09 | `dddddddddddd` | FAIL | PASS | FAIL | N/A | ERROR | PASS |", first)
            self.assertEqual(doc.read_text(), first)

    def test_main_fails_on_a_missing_report(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            with contextlib.redirect_stdout(io.StringIO()):
                code = fold.main(["--report", f"{tmp}/none.json", "--extra", tmp])
            self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
