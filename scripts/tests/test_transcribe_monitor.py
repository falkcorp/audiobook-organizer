# file: scripts/tests/test_transcribe_monitor.py
# version: 1.1.0
# guid: dfe7a9ce-87f7-43ec-a68b-293743361ffc
# last-edited: 2026-10-18
"""Tests for scripts/transcribe_monitor.py's relaunch check (PR 05-PR9).

transcribe-book-intros previews unless told dry_run=false. The monitor's
relaunch sends dry_run=false, but the server decides the mode (and an older
build could answer with an already-active preview run), so the monitor reads
the queued op back and raises RELAUNCH_IS_PREVIEW when it is one. HTTP is
stubbed; nothing here talks to a server.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import pathlib
import types
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "transcribe_monitor.py"


def load_monitor():
    spec = importlib.util.spec_from_file_location("transcribe_monitor", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def make_args():
    return types.SimpleNamespace(
        base="https://192.0.2.10:8484",
        insecure=True,
        alerts_log="",
        metrics_log="",
        whisper_url="",
        relaunch=True,
        relaunch_cooldown=0,
        relaunch_verify_wait=0,
        relaunch_verify_max=1800,
        stall_secs=300,
    )


# Real message shapes, as internal/plugins/maintenance/intro_transcribe.go
# reports them (TestIntroTranscribe_ProgressNamesTheMode pins the prefixes).
SELECTING_PREVIEW = "PREVIEW (dry_run, nothing written) — Selecting work — reading 83228 books"
SELECTING_LIVE = "LIVE — Selecting work — reading 83228 books"
PREVIEW_SUMMARY = (
    "PREVIEW (dry_run, nothing written) — would transcribe 2 books (of 83228 listed, starting at 0); "
    "81226 skipped (12 of them known-broken, not retried), 0 unreadable; first ids: [b1, b2]. "
    "Pass dry_run=false to run."
)
LIVE_PAGE = "LIVE — Page 1/11 — 200 books transcribed"
LIVE_DONE = (
    "LIVE — Done — 1990 ok, 3 source-missing, 0 ffmpeg-err, 0 whisper-err, 7 empty, 0 deferred"
)


class ClassifyRelaunchTest(unittest.TestCase):
    def setUp(self):
        self.tm = load_monitor()

    def classify(self, msg, status="running"):
        return self.tm.classify_relaunch({"status": status, "progress_message": msg})

    def test_markers_match_the_go_constants(self):
        self.assertEqual(self.tm.PREVIEW_MARKER, "PREVIEW (dry_run, nothing written) — ")
        self.assertEqual(self.tm.LIVE_MARKER, "LIVE — ")

    def test_preview_selecting_line_is_preview(self):
        # Both modes report the selecting line first; before the markers a
        # preview's selecting line read as live.
        self.assertEqual(self.classify(SELECTING_PREVIEW), "preview")
        self.assertEqual(self.classify(PREVIEW_SUMMARY, status="completed"), "preview")

    def test_live_messages_are_live(self):
        for msg in (SELECTING_LIVE, LIVE_PAGE, LIVE_DONE):
            self.assertEqual(self.classify(msg), "live", msg)

    def test_anything_else_is_unknown(self):
        for msg in (
            "",
            "Selecting work — reading 83228 books",  # a build without the markers
            "Page 1/11 — 200 books transcribed",
            "maintenance.intro-transcribe: decode params: unexpected EOF",
            "preview (dry_run, nothing written) — lower case",
        ):
            self.assertEqual(self.classify(msg), "unknown", msg)
        self.assertEqual(self.classify("", status="completed"), "unknown")


class RelaunchCheckTest(unittest.TestCase):
    def poll(self, op_view):
        """One idle poll that relaunches, with the queued op reading back as op_view."""
        tm = load_monitor()
        tm.http_post = lambda url, token, insecure, body, timeout=15: {"op_id": "01OPX"}

        def get(url, token, insecure, timeout=15):
            if url.endswith("/api/v1/maintenance/transcribe-stats"):
                return {"data": {"done": True, "attempted": 0, "ok": 0}}
            self.assertTrue(url.endswith("/api/v1/operations/v2/01OPX"), url)
            return {"data": {"operation": op_view}}

        tm.http_get = get
        state: dict = {}
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            record = tm.run_once(make_args(), "token", state)
        return record, state, out.getvalue(), err.getvalue()

    def test_preview_relaunch_is_loud(self):
        record, state, out, err = self.poll(
            {"status": "running", "progress_message": SELECTING_PREVIEW}
        )
        self.assertEqual(record["relaunch_check"], "preview")
        self.assertIn("ALERT[ERROR] RELAUNCH_IS_PREVIEW", out)
        self.assertIn("RELAUNCH CAME BACK AS A PREVIEW", err)
        self.assertNotIn("pending_relaunch", state)

    def test_live_relaunch_is_quiet(self):
        record, state, out, err = self.poll({"status": "running", "progress_message": LIVE_PAGE})
        self.assertEqual(record["relaunch_check"], "live")
        self.assertIn("RELAUNCH_LIVE", out)
        self.assertNotIn("RELAUNCH_IS_PREVIEW", out)
        self.assertEqual(err, "")
        self.assertNotIn("pending_relaunch", state)

    def test_unreported_relaunch_is_checked_next_poll(self):
        record, state, out, _ = self.poll({"status": "queued"})
        self.assertEqual(record["relaunch_check"], "unknown")
        self.assertEqual(state["pending_relaunch"]["op_id"], "01OPX")
        self.assertNotIn("RELAUNCH_IS_PREVIEW", out)
        self.assertNotIn("RELAUNCH_UNVERIFIED", out)

    def test_settled_without_marker_is_unverified_not_live(self):
        record, state, out, _ = self.poll(
            {"status": "failed", "progress_message": "Selecting work — reading 83228 books"}
        )
        self.assertEqual(record["relaunch_check"], "unknown")
        self.assertIn("ALERT[WARN] RELAUNCH_UNVERIFIED", out)
        self.assertNotIn("RELAUNCH_LIVE", out)
        self.assertNotIn("pending_relaunch", state)


if __name__ == "__main__":
    unittest.main()
