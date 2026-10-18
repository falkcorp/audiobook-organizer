# file: scripts/tests/test_transcribe_monitor.py
# version: 1.0.0
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
            {
                "status": "completed",
                "progress_message": "PREVIEW (dry_run, nothing written) — would transcribe 3 books",
            }
        )
        self.assertEqual(record["relaunch_check"], "preview")
        self.assertIn("ALERT[ERROR] RELAUNCH_IS_PREVIEW", out)
        self.assertIn("RELAUNCH CAME BACK AS A PREVIEW", err)
        self.assertNotIn("pending_relaunch", state)

    def test_live_relaunch_is_quiet(self):
        record, state, out, err = self.poll(
            {"status": "running", "progress_message": "Page 1/4 — 0 books transcribed"}
        )
        self.assertEqual(record["relaunch_check"], "live")
        self.assertNotIn("RELAUNCH_IS_PREVIEW", out)
        self.assertEqual(err, "")
        self.assertNotIn("pending_relaunch", state)

    def test_unreported_relaunch_is_checked_next_poll(self):
        record, state, out, _ = self.poll({"status": "queued"})
        self.assertEqual(record["relaunch_check"], "unknown")
        self.assertEqual(state["pending_relaunch"]["op_id"], "01OPX")
        self.assertNotIn("RELAUNCH_IS_PREVIEW", out)


if __name__ == "__main__":
    unittest.main()
