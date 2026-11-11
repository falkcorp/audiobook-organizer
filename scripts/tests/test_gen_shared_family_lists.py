# file: scripts/tests/test_gen_shared_family_lists.py
# version: 1.0.0
# guid: 4fe30ec3-72fe-4a10-866b-0a392a0fdb53
# last-edited: 2026-11-10
"""Tests for scripts/ci/gen_shared_family_lists.py (F2-15).

The pure parts (registry and claim parsing, entry building, the census rule,
the golden's round trip) run on synthetic inputs; one test runs the generator
over the real repository with a synthetic census, so it needs no Go build.

Run: python3 -m pytest scripts/tests/test_gen_shared_family_lists.py -q
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import pathlib
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "ci" / "gen_shared_family_lists.py"


def _load():
    spec = importlib.util.spec_from_file_location("gen_shared_family_lists", SCRIPT)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    sys.modules["gen_shared_family_lists"] = mod
    spec.loader.exec_module(mod)
    return mod


g = _load()

FAMILIES_SRC = """
var keyFamilies = []KeyFamily{
	{"idx:", "secondary indexes"},
	{"setting:", "settings"},
	{"setting:repairs_last_plan_op:", "a registered child"},
	{"preference:", "preferences"},
	{"counter:", "ID counters"},
	{"u:", "users"},
	{"act:audit:", "activity"},
	{"act:batch:", "activity"},
	{"act:change:", "activity"},
	{"act:debug:", "activity"},
	{"act:digest:", "activity"},
	{"act:info:", "activity"},
	{"act:system:", "activity"},
	{"act:typ:", "activity"},
	{"usermerge:journal:", "merge journals"},
}
"""


def real_claims() -> set[tuple[str, str]]:
    return g.nested_claims(
        (REPO_ROOT / "internal/books/store/booksclaims.go").read_text(encoding="utf-8")
    )


class ParseTest(unittest.TestCase):
    def test_registered_families_in_file_order(self):
        fams = g.registered_families(FAMILIES_SRC)
        self.assertEqual(fams[:3], ["idx:", "setting:", "setting:repairs_last_plan_op:"])

    def test_registered_families_needs_the_registry(self):
        with self.assertRaises(g.ListError):
            g.registered_families("package store\n")

    def test_nested_claims(self):
        src = 'var serializedSubClaims = []storage.Claim{\n\t{Prefix: "idx:user:"},\n\t{Key: "setting:config_blob"},\n}\n'
        self.assertEqual(
            g.nested_claims(src), {("prefix", "idx:user:"), ("exact", "setting:config_blob")}
        )

    def test_real_claims_equal_rows_1_to_7(self):
        table = {(e.kind, e.value) for e in g.MOVE if e.row in g.CLAIM_ROWS}
        self.assertEqual(real_claims(), table)


class BuildTest(unittest.TestCase):
    def setUp(self):
        self.fams = g.registered_families(FAMILIES_SRC)

    def test_keep_lists_setting_children_and_uncovered_idx(self):
        entries = g.build_entries(self.fams, real_claims(), {"idx:user:", "idx:col:", "idx:role:"})
        keep = {e.value for e in entries if e.cls == "keep"}
        self.assertIn("setting:repairs_last_plan_op:", keep)
        self.assertIn("idx:col:", keep)
        self.assertIn("idx:role:", keep)  # idx:role:name: is a move, the rest of idx:role: stays
        self.assertNotIn("idx:user:", keep)  # wholly a move (row 1)

    def test_claim_drift_fails(self):
        with self.assertRaises(g.ListError) as cm:
            g.build_entries(self.fams, real_claims() | {("prefix", "idx:batcave:")}, set())
        self.assertIn("idx:batcave:", str(cm.exception))

    def test_missing_shared_parent_fails(self):
        with self.assertRaises(g.ListError):
            g.build_entries([f for f in self.fams if f != "counter:"], real_claims(), set())

    def test_rewrite_of_unregistered_family_fails(self):
        with self.assertRaises(g.ListError) as cm:
            g.build_entries([f for f in self.fams if f != "act:typ:"], real_claims(), set())
        self.assertIn("act:typ:", str(cm.exception))

    def test_entries_are_sorted_and_round_trip(self):
        entries = g.build_entries(self.fams, real_claims(), {"idx:col:"})
        self.assertEqual(entries, sorted(entries))
        self.assertEqual(g.parse_golden(g.render(entries)), entries)

    def test_bookkeeping_covers_the_legacy_ledger(self):
        entries = g.build_entries(self.fams, real_claims(), set())
        skipped = [e for e in entries if e.cls != "keep"]
        for key in ("preference:migration_68", "preference:db_version", "counter:preference"):
            self.assertTrue(any(e.covers(key) for e in skipped), key)
        self.assertFalse(any(e.covers("preference:theme") for e in skipped))


class CensusTest(unittest.TestCase):
    def setUp(self):
        self.fams = g.registered_families(FAMILIES_SRC)
        self.entries = g.build_entries(self.fams, real_claims(), {"idx:col:"})

    def test_classified_keys_pass(self):
        keys = {"overlay": ["u:01ARKHAM", "idx:user:username:bruce", "idx:col:gotham", "setting:x"]}
        self.assertEqual(g.census_problems(keys, self.fams, self.entries), [])

    def test_unregistered_family_is_reported(self):
        keys = {"overlay": ["batcave:01"]}
        self.assertEqual(
            g.census_problems(keys, self.fams, self.entries),
            ["batcave: (overlay: unregistered family)"],
        )

    def test_unlisted_idx_sub_prefix_is_reported(self):
        keys = {"stage1": ["idx:joker:card:1"]}
        self.assertEqual(
            g.census_problems(keys, self.fams, self.entries),
            ["idx:joker: (stage1: no move or keep entry)"],
        )

    def test_decode_census_hex(self):
        self.assertEqual(g.decode_census({"s": ["753a01"]}), {"s": ["u:\x01"]})


class GenerateTest(unittest.TestCase):
    def test_committed_golden_matches_a_synthetic_census(self):
        census = {"overlay": [b"u:01ARKHAM".hex(), b"idx:user:username:bruce".hex()]}
        with tempfile.TemporaryDirectory() as tmp:
            f = pathlib.Path(tmp) / "census.json"
            f.write_text(json.dumps(census), encoding="utf-8")
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = g.main(["--check", "--census", str(f)])
        self.assertEqual(rc, 0, out.getvalue())

    def test_stale_golden_fails_check(self):
        census = {"overlay": [b"u:01ARKHAM".hex()]}
        with tempfile.TemporaryDirectory() as tmp:
            f = pathlib.Path(tmp) / "census.json"
            f.write_text(json.dumps(census), encoding="utf-8")
            golden = pathlib.Path(tmp) / "golden"
            golden.write_text(
                g.GOLDEN.read_text(encoding="utf-8").replace("move\t", "keep\t", 1),
                encoding="utf-8",
            )
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(
                io.StringIO()
            ):
                self.assertEqual(
                    g.main(["--check", "--census", str(f), "--golden", str(golden)]), 1
                )

    def test_unclassified_census_key_fails(self):
        census = {"overlay": [b"riddler:01".hex()]}
        with tempfile.TemporaryDirectory() as tmp:
            f = pathlib.Path(tmp) / "census.json"
            f.write_text(json.dumps(census), encoding="utf-8")
            err = io.StringIO()
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
                self.assertEqual(g.main(["--check", "--census", str(f)]), 1)
        self.assertIn("riddler:", err.getvalue())


if __name__ == "__main__":
    unittest.main()
