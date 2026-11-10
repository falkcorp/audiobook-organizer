# file: scripts/tests/test_write_r0_corpus.py
# version: 1.1.1
# guid: e61bb4fe-2238-459c-be02-94efbf991c39
# last-edited: 2026-11-10
"""Tests for scripts/r2corpus/write_r0_corpus.py (F2-12).

No R0 binary is needed: the case list is checked statically against the
committed manifest.json, and the dry mode lists the API calls in order.

Run: python3 -m pytest scripts/tests/test_write_r0_corpus.py -q
"""

from __future__ import annotations

import contextlib
import datetime
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


# The brief's coverage table ("What the corpus must contain"), row by row: each
# needle must name a family (or a row kind in a case's "how") of a stage-1 case
# with that step, or a not-writable family listed for F2-13 under that step.
BRIEF_TABLE = {
    "M2": ("setting:config_blob", "setting:<secret>", "config.yaml (gap-fill)", "env"),
    "M3": ("user", "status inactive", "admin", "editor", "viewer", "fp-worker",
           "apikey", "scoped", "empty-scope", "every-atom", "admin-owned",
           "invite", "inviteallow:", "session", "oauthid:"),
    "M4": ("opv2:run (queued)", "opv2:run (succeeded)", "opv2:run (failed)",
           "opv2:run (canceled)", "operations pause marker", "scheduler task config",
           "setting:registry.scan_standdown", "opv2:run (running, stopping)"),
    "M5": ("syslog:",),
    "M6": ("setting:config_blob (integration fields)", "webhook", "importpath", "aijob"),
    "books": ("book", "playlist", "abs_sess:", "ABS progress", "playback sessions"),
}  # fmt: skip


class CoverageTest(unittest.TestCase):
    def test_every_step_row_has_a_case(self):
        steps = {c.step for c in w.CASES}
        for step in w.STEPS:
            self.assertIn(step, steps, f"no case covers coverage-table row {step}")

    def test_every_family_of_the_brief_table_is_written_or_listed(self):
        self.assertEqual(set(BRIEF_TABLE), set(w.STEPS))
        for step, needles in BRIEF_TABLE.items():
            text = " | ".join(
                [f"{c.family} | {c.how}" for c in w.CASES if c.step == step]
                + [g["family"] for g in w.NOT_WRITABLE if g["step"] == step]
            )
            for needle in needles:
                self.assertIn(needle, text, f"coverage row {step}: nothing names {needle!r}")

    def test_queued_runs_are_written_not_deferred(self):
        deferred = " ".join(g["family"] for g in w.NOT_WRITABLE)
        self.assertNotIn("queued", deferred)
        self.assertIn("opv2:run (queued)", " ".join(c.family for c in w.CASES))

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
        # Stage 2 (F2-13, build_overlay.py) fills overlay and blockers;
        # scripts/tests/test_build_overlay.py checks their contents.
        self.assertIsInstance(manifest["overlay"], list)
        self.assertIsInstance(manifest["blockers"], list)
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


class FakeClient:
    """Answers the writer's requests from a script of (method, path-prefix) -> responses."""

    def __init__(self, script):
        self.script = script  # list of (method, path prefix, status, body)
        self.calls = []
        self.keys = {}
        self.jars = {}

    def request(self, method, path, body=None, auth="admin", headers=None, timeout=0):
        self.calls.append((method, path, body))
        for i, (m, prefix, status, resp) in enumerate(self.script):
            if m == method and path.startswith(prefix):
                del self.script[i]
                return w.Response(status, {}, resp, b"")
        raise AssertionError(f"unscripted request {method} {path}")


def _op(status):
    return {"data": {"operation": {"status": status}}}


class HookTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.layout = w.Layout(pathlib.Path(self.tmp.name))
        self.sleep = w.time.sleep
        w.time.sleep = lambda _s: None

    def tearDown(self):
        w.time.sleep = self.sleep
        self.tmp.cleanup()

    def test_an_accepted_cancel_that_ends_completed_is_retried(self):
        # Attempt 1: DELETE answers 204 but the run had already finished, so the
        # row ends completed (R0's cancel-races-completion path). Attempt 2 wins.
        c = FakeClient([
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "run-1"}),
            ("GET", "/api/v1/operations/v2/run-1", 200, _op("running")),
            ("DELETE", "/api/v1/operations/v2/run-1", 204, None),
            ("GET", "/api/v1/operations/v2/run-1", 200, _op("completed")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "run-2"}),
            ("GET", "/api/v1/operations/v2/run-2", 200, _op("running")),
            ("DELETE", "/api/v1/operations/v2/run-2", 204, None),
            ("GET", "/api/v1/operations/v2/run-2", 200, _op("canceled")),
        ])  # fmt: skip
        ctx = w.Ctx(client=c, proc=None, layout=self.layout)
        w._start_and_cancel_scan(ctx)
        self.assertEqual(ctx.ids["op_canceled"], "run-2")
        self.assertEqual(c.script, [])
        # The scan targets the slow tree, not the fixture library.
        self.assertEqual(c.calls[0][2]["params"]["folder_path"], str(self.layout.slow_tree))

    def test_cancel_gives_up_after_the_attempt_budget(self):
        script = []
        for i in range(w.CANCEL_ATTEMPTS):
            script += [
                ("POST", "/api/v1/operations/v2", 202, {"op_id": f"run-{i}"}),
                ("GET", f"/api/v1/operations/v2/run-{i}", 200, _op("completed")),
                ("GET", f"/api/v1/operations/v2/run-{i}", 200, _op("completed")),
            ]
        ctx = w.Ctx(client=FakeClient(script), proc=None, layout=self.layout)
        with self.assertRaises(w.CorpusError):
            w._start_and_cancel_scan(ctx)

    def test_standdown_leaves_a_quiesced_and_a_queued_scan(self):
        ctx = w.Ctx(client=FakeClient([
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "scan-1"}),
            ("GET", "/api/v1/operations/v2/scan-1", 200, _op("running")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "refresh-1"}),
            ("GET", "/api/v1/operations/v2/refresh-1", 200, _op("completed")),
            ("GET", "/api/v1/operations/v2/scan-1", 200, _op("interrupted_quiesced")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "scan-2"}),
            ("GET", "/api/v1/operations/v2/scan-2", 200, _op("queued")),
        ]), proc=None, layout=self.layout, ids={"import_dir": "/tmp/r2corpus/stage1/import"})  # fmt: skip
        w._quiesce_scan_under_standdown(ctx)
        self.assertEqual((ctx.ids["op_quiesced"], ctx.ids["op_queued"]), ("scan-1", "scan-2"))
        posts = [b for m, _, b in ctx.client.calls if m == "POST"]
        self.assertEqual(
            [b["def_id"] for b in posts],
            ["library.scan", "maintenance.metadata-refresh", "library.scan"],
        )
        # Both scans target the slow tree, which a replay's copy does not have.
        for b in (posts[0], posts[2]):
            self.assertEqual(b["params"]["folder_path"], str(self.layout.slow_tree))

    def test_standdown_retries_a_scan_that_finished_first(self):
        # Attempt 1: the scan finished before the holder acquired, so the
        # release armed the grace with nothing quiesced. The writer ends that
        # grace (1 s grace, one more holder run, grace back to 600) and retries.
        ctx = w.Ctx(client=FakeClient([
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "scan-1"}),
            ("GET", "/api/v1/operations/v2/scan-1", 200, _op("running")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "refresh-1"}),
            ("GET", "/api/v1/operations/v2/refresh-1", 200, _op("completed")),
            ("GET", "/api/v1/operations/v2/scan-1", 200, _op("completed")),
            ("PUT", "/api/v1/config", 200, {}),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "refresh-2"}),
            ("GET", "/api/v1/operations/v2/refresh-2", 200, _op("completed")),
            ("PUT", "/api/v1/config", 200, {}),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "scan-2"}),
            ("GET", "/api/v1/operations/v2/scan-2", 200, _op("running")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "refresh-3"}),
            ("GET", "/api/v1/operations/v2/refresh-3", 200, _op("completed")),
            ("GET", "/api/v1/operations/v2/scan-2", 200, _op("interrupted_quiesced")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "scan-3"}),
            ("GET", "/api/v1/operations/v2/scan-3", 200, _op("queued")),
        ]), proc=None, layout=self.layout, ids={"import_dir": "/x"})  # fmt: skip
        w._quiesce_scan_under_standdown(ctx)
        self.assertEqual(ctx.ids["op_quiesced"], "scan-2")
        self.assertEqual(ctx.ids["op_queued"], "scan-3")
        self.assertEqual(ctx.client.script, [])
        graces = [b["scan_standdown_grace_seconds"] for m, _, b in ctx.client.calls if m == "PUT"]
        self.assertEqual(graces, [1, w.STANDDOWN_GRACE_SECONDS])

    def test_standdown_refuses_a_scan_that_was_dispatched(self):
        ctx = w.Ctx(client=FakeClient([
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "scan-1"}),
            ("GET", "/api/v1/operations/v2/scan-1", 200, _op("running")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "refresh-1"}),
            ("GET", "/api/v1/operations/v2/refresh-1", 200, _op("completed")),
            ("GET", "/api/v1/operations/v2/scan-1", 200, _op("interrupted_quiesced")),
            ("POST", "/api/v1/operations/v2", 202, {"op_id": "scan-2"}),
            ("GET", "/api/v1/operations/v2/scan-2", 200, _op("running")),
        ]), proc=None, layout=self.layout, ids={"import_dir": "/x"})  # fmt: skip
        with self.assertRaises(w.CorpusError):
            w._quiesce_scan_under_standdown(ctx)

    def test_bootstrap_revokes_the_exchange_key(self):
        self.layout.state.mkdir(parents=True)
        (self.layout.state / ".bootstrap-token").write_text("abbs_fixture\n")
        data = {"user_id": "u1", "username": "admin", "api_key": "abk_" + "Z" * 32,
                "key_id": "key-1", "generated_password": "gen"}  # fmt: skip
        c = FakeClient([
            ("POST", "/api/v1/auth/bootstrap", 200, {"data": data}),
            ("POST", "/api/v1/auth/login", 200, {}),
            ("PUT", "/api/v1/auth/me/password", 200, {}),
            ("POST", "/api/v1/auth/login", 200, {}),
            ("DELETE", "/api/v1/auth/api-keys/key-1", 200, {}),
        ])  # fmt: skip
        ctx = w.Ctx(client=c, proc=None, layout=self.layout)
        w.hook_bootstrap(ctx)
        self.assertEqual(c.script, [])
        self.assertNotIn("bootstrap-admin", c.keys)


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
    def test_no_lapsing_expiry(self):
        self.assertEqual(w.KEY_TTL_DAYS, 365)  # R0's MaxAPIKeyTTLDays
        self.assertGreaterEqual(w.INVITE_TTL_HOURS, 5 * 365 * 24)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            w.main(["--dry-run"])
        out = buf.getvalue()
        self.assertNotIn('"expires_in_days": 30', out)
        self.assertNotIn('"expires_in": 72,', out)

    def test_lapses_keep_the_answers_valid_for_most_of_a_year(self):
        now = datetime.datetime(2026, 11, 10, 11, 0, tzinfo=datetime.timezone.utc)
        lapses = w.lapses(now)
        self.assertEqual(lapses["answers_valid_until"], "2027-11-10T11:00:00Z")
        self.assertEqual(lapses["web_sessions"], "2026-11-17T11:00:00Z")
        self.assertGreater(lapses["abs_sessions"], lapses["answers_valid_until"])
        env = w.boot_env(w.Layout(pathlib.Path(w.DEFAULT_WORKDIR)), w.DEFAULT_PORT)
        self.assertEqual(env["ABS_REFRESH_TOKEN_TTL"], f"{w.ABS_SESSION_TTL_HOURS}h")

    def test_grace_guard(self):
        w.check_grace_left({})
        w.check_grace_left({"standdown_grace_started": 100.0}, now=100.0 + 60)
        with self.assertRaises(w.CorpusError):
            w.check_grace_left(
                {"standdown_grace_started": 100.0},
                now=100.0 + w.STANDDOWN_GRACE_SECONDS - w.GRACE_MARGIN_SECONDS + 1,
            )

    def test_standdown_rows_are_read_back_from_the_store(self):
        def chunk(payload):
            return b"\x00" * 4 + len(payload).to_bytes(2, "little") + b"\x01" + payload

        def rec(op, status):
            # Go writes compact JSON, and the lookup prefix assumes it.
            return json.dumps(
                {"ID": op, "DefID": "library.scan", "Status": status}, separators=(",", ":")
            ).encode()

        ids = {"op_quiesced": "Q" * 26, "op_queued": "N" * 26}
        with tempfile.TemporaryDirectory() as d:
            db = pathlib.Path(d) / "audiobooks.pebble"
            db.mkdir()
            wal = db / "000014.log"
            # The newest write of each key wins: running, then interrupted_quiesced.
            wal.write_bytes(
                chunk(b"k" + rec("Q" * 26, "running")) + chunk(b"k" + rec("Q" * 26, "interrupted_quiesced"))
                + chunk(b"k" + rec("N" * 26, "queued"))
            )  # fmt: skip
            self.assertEqual(
                w.check_standdown_rows(db, ids),
                ["op_quiesced=interrupted_quiesced", "op_queued=queued"],
            )
            wal.write_bytes(
                chunk(b"k" + rec("Q" * 26, "interrupted_quiesced")) + chunk(b"k" + rec("N" * 26, "queued"))
                + chunk(b"k" + rec("N" * 26, "completed"))
            )  # fmt: skip
            with self.assertRaises(w.CorpusError):
                w.check_standdown_rows(db, ids)

    def test_the_committed_store_holds_the_standdown_rows(self):
        db = REPO_ROOT / w.STAGE1_REL / "db" / "audiobooks.pebble"
        rec = w.pebble_bytes.latest_record(db, b'{"key":"registry.scan_standdown"')
        self.assertIsNotNone(rec)
        self.assertEqual(rec["value"], "")

    def test_maintenance_window_guard(self):
        utc = datetime.timezone.utc
        for hour in (1, 2, 4, 5):
            with self.assertRaises(w.CorpusError):
                w.check_outside_maintenance_window(
                    datetime.datetime(2026, 11, 10, hour, 30, tzinfo=utc)
                )
        for hour in (0, 6, 12, 23):
            w.check_outside_maintenance_window(
                datetime.datetime(2026, 11, 10, hour, 30, tzinfo=utc)
            )

    def test_slow_tree_is_outside_the_committed_store(self):
        layout = w.Layout(pathlib.Path(w.DEFAULT_WORKDIR))
        self.assertNotIn(layout.store, layout.slow_tree.parents)
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(w.lay_out_slow_tree(pathlib.Path(d) / "t", fanout=3), 27)
            self.assertEqual(len(list((pathlib.Path(d) / "t").glob("*/*/*"))), 27)

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

    SECRET = b"abk_" + b"A" * 32

    def _snappy_secret(self):
        # "abk_A" as a literal, then a copy of 31 bytes at offset 1: the
        # compressed block never holds the secret's bytes in a row.
        lit = b"abk_A"
        return (
            bytes([len(self.SECRET)])
            + bytes([(len(lit) - 1) << 2])
            + lit
            + bytes([(31 - 1) << 2 | 2])
            + (1).to_bytes(2, "little")
        )

    def _table(self, blocks):
        pb = w.pebble_bytes
        out = b""
        for typ, raw in blocks:
            crc = pb.mask_crc(pb.crc32c(raw + bytes([typ])))
            out += raw + bytes([typ]) + crc.to_bytes(4, "little")
        return out + b"\x00" * 53 + pb.PEBBLE_MAGIC

    def test_snappy_decoder(self):
        pb = w.pebble_bytes
        self.assertEqual(pb.snappy_decompress(self._snappy_secret()), self.SECRET)
        self.assertEqual(pb.snappy_decompress(b"\x03\x08abc"), b"abc")
        with self.assertRaises(pb.StoreDecodeError):
            pb.snappy_decompress(b"\x05\x08abc")  # header says 5 bytes, 3 decoded

    def test_scan_finds_a_secret_inside_a_compressed_sst_block(self):
        with tempfile.TemporaryDirectory() as d:
            store = pathlib.Path(d) / "audiobooks.pebble"
            store.mkdir()
            sst = store / "000016.sst"
            sst.write_bytes(self._table([(0, b"Batman: Year One" * 4), (1, self._snappy_secret())]))
            self.assertIsNone(w.ABK_RE.search(sst.read_bytes().decode("latin-1")))
            blocks = w.pebble_bytes.table_blocks(sst.read_bytes())
            self.assertEqual([b[2] for b in blocks], [b"Batman: Year One" * 4, self.SECRET])
            self.assertIn("000016.sst: abk_ API key", "\n".join(w.scan_for_leaks(pathlib.Path(d))))

    def test_a_final_block_of_an_unknown_compression_type_is_refused(self):
        pb = w.pebble_bytes
        # Type 2 (zlib) in a short final block: once it would have been taken
        # for the footer; now its checksum finds it and the type is refused.
        table = self._table([(0, b"Batman: Year One" * 4), (2, b"x" * 20)])
        with self.assertRaises(pb.StoreDecodeError):
            pb.table_blocks(table)
        # A tail that is not a known footer length is refused too.
        with self.assertRaises(pb.StoreDecodeError):
            pb.table_blocks(self._table([(0, b"Batman")])[:-61] + b"\x00" * 40 + pb.PEBBLE_MAGIC)

    def test_zap_stored_fields_are_decompressed(self):
        pb = w.pebble_bytes
        zap = REPO_ROOT / w.STAGE1_REL / "db" / "library.bleve" / "store"
        segs = sorted(zap.glob("*.zap"))
        self.assertTrue(segs)
        docs = pb.zap_stored_fields(segs[0].read_bytes())
        self.assertTrue(docs)
        self.assertTrue(any(b"Knightfall" in d for d in docs))
        bad = bytearray(segs[0].read_bytes())
        bad[-8:-4] = (99).to_bytes(4, "big")
        with self.assertRaises(pb.StoreDecodeError):
            pb.zap_stored_fields(bytes(bad))

    def test_scan_fails_closed_on_an_unreadable_table(self):
        with tempfile.TemporaryDirectory() as d:
            store = pathlib.Path(d) / "audiobooks.pebble"
            store.mkdir()
            (store / "000017.sst").write_bytes(b"\x07" * 400 + w.pebble_bytes.PEBBLE_MAGIC)
            (store / "000018.sst").write_bytes(b"not a table at all")
            found = "\n".join(w.scan_for_leaks(pathlib.Path(d)))
            self.assertIn("000017.sst: cannot be decoded", found)
            self.assertIn("000018.sst: cannot be decoded", found)

    def test_scan_finds_a_secret_split_across_wal_blocks(self):
        pb = w.pebble_bytes

        def chunk(typ, payload):
            return b"\x00" * 4 + len(payload).to_bytes(2, "little") + bytes([typ]) + payload

        # FIRST chunk fills block 0 and ends inside the secret; LAST chunk opens
        # block 1 with the rest of it, behind a 7-byte header.
        record = b"x" * (pb.WAL_BLOCK - 7 - 10) + self.SECRET
        first, last = record[: pb.WAL_BLOCK - 7], record[pb.WAL_BLOCK - 7 :]
        wal = chunk(2, first) + chunk(4, last) + b"\x00" * 64
        self.assertIsNone(w.ABK_RE.search(wal.decode("latin-1")))
        self.assertEqual(pb.wal_payload(wal), record)
        with tempfile.TemporaryDirectory() as d:
            store = pathlib.Path(d) / "audiobooks.pebble"
            store.mkdir()
            (store / "000014.log").write_bytes(wal)
            self.assertIn("000014.log: abk_ API key", "\n".join(w.scan_for_leaks(pathlib.Path(d))))

    def test_the_committed_store_decodes(self):
        store = REPO_ROOT / w.STAGE1_REL / "db" / "audiobooks.pebble"
        tables = sorted(store.glob("*.sst"))
        self.assertTrue(tables)
        for t in tables:
            self.assertTrue(w.pebble_bytes.table_blocks(t.read_bytes()))
        for log in store.glob("*.log"):
            w.pebble_bytes.wal_payload(log.read_bytes())

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
