# file: scripts/tests/test_whisper_supervisor.py
# version: 1.1.0
# guid: 0d3f5c1e-6b27-4a58-b8f1-7c2e9a4d6b13
# last-edited: 2026-11-17

"""whisper-supervisor.ps1 run for real against a fake `uv`.

The crash-loop breaker must count a start that never became ready as a failure
however long it ran (the old rule reset the streak on wall-clock time alone, so a
start that hangs in a model load and dies could loop forever). The fake uv runs
whisper_fake_uv.py in one of three modes: exit at once, run without ever answering
/health, or answer /health. Uses powershell.exe on Windows, pwsh elsewhere; skipped
without either.

Standard library unittest only (no pytest): scripts/ is discovered by plain
`python3 -m unittest discover` in the Repo Guards job, which installs no packages, and
these tests must run there."""

import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import textwrap
import threading
import time
import unittest

PS = shutil.which("powershell.exe" if sys.platform == "win32" else "pwsh")
SUPERVISOR = pathlib.Path(__file__).resolve().parent.parent / "whisper-supervisor.ps1"
PORT = 19861

FAKE_UV = textwrap.dedent(
    """
    import http.server, os, sys, time
    mode = os.environ["FAKE_MODE"]
    if mode == "exit":
        sys.exit(1)
    if mode == "hang":
        time.sleep(float(os.environ["FAKE_RUN_S"]))
        sys.exit(1)
    class H(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200); self.end_headers(); self.wfile.write(b"{}")
        def log_message(self, *a): pass
    srv = http.server.HTTPServer(("127.0.0.1", int(os.environ["WHISPER_PORT"])), H)
    srv.timeout = 0.5
    end = time.time() + float(os.environ["FAKE_RUN_S"])
    while time.time() < end:
        srv.handle_request()
    sys.exit(1)
    """
)


@unittest.skipUnless(PS, "no PowerShell on this machine")
class SupervisorBreaker(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        tmp_path = pathlib.Path(tmp.name)
        prof = tmp_path / "profile"
        prof.mkdir()
        for name in (
            "whisper_server.py",
            "whisper_path_pipeline.py",
            "whisper_machine_id.py",
            "whisper_decode_child.py",
        ):
            (prof / name).write_text("")
        fake = tmp_path / "fake_uv.py"
        fake.write_text(FAKE_UV)
        bin_dir = tmp_path / "bin"
        bin_dir.mkdir()
        if sys.platform == "win32":
            (bin_dir / "uv.cmd").write_text(f'@"{sys.executable}" "{fake}" %*\r\n')
        else:
            launcher = bin_dir / "uv"
            launcher.write_text(f'#!/bin/sh\nexec "{sys.executable}" "{fake}" "$@"\n')
            launcher.chmod(0o755)
        self.rig = (prof, bin_dir)

    def _supervise(self, mode, run_s, extra):
        prof, bin_dir = self.rig
        log = prof / "sup.log"
        sentinel = prof / "stop.flag"
        env = {
            **os.environ,
            "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
            "USERPROFILE": str(prof),
            "FAKE_MODE": mode,
            "FAKE_RUN_S": str(run_s),
        }
        cmd = [
            PS,
            "-NoProfile",
            *(["-ExecutionPolicy", "Bypass"] if sys.platform == "win32" else []),
            "-File",
            str(SUPERVISOR),
            "-ScriptPath",
            str(prof / "whisper_server.py"),
            "-Port",
            str(PORT),
            "-LogPath",
            str(log),
            "-SentinelPath",
            str(sentinel),
            "-RestartDelaySec",
            "1",
            "-MaxBackoffSec",
            "1",
            *extra,
        ]
        proc = subprocess.Popen(
            cmd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True
        )
        self.addCleanup(self._reap, proc)
        return proc, log, sentinel

    @staticmethod
    def _reap(proc):
        if proc.poll() is None:
            proc.kill()
        proc.communicate()

    def test_runs_that_exit_at_once_hit_the_breaker(self):
        proc, log, _ = self._supervise("exit", 0, ["-MaxCrashStreak", "3"])
        out, _ = proc.communicate(timeout=120)
        self.assertEqual(proc.returncode, 1, out)
        self.assertIn("crash-loop breaker: 3 crashes in a row", log.read_text())

    def test_a_start_that_never_became_ready_counts_however_long_it_ran(self):
        # Each run lasts 4 s, longer than -HealthyRunSec 2, and never answers /health.
        # The old wall-clock rule reset the streak after every one of them and looped
        # forever; now the third failure ends it.
        proc, log, _ = self._supervise("hang", 4, ["-MaxCrashStreak", "3", "-HealthyRunSec", "2"])
        out, _ = proc.communicate(timeout=180)
        text = log.read_text()
        self.assertEqual(proc.returncode, 1, out + text)
        self.assertIn("crash-loop breaker: 3 crashes in a row", text)
        self.assertIn("became_healthy=False", text)
        self.assertNotIn("became_healthy=True", text)

    def test_a_healthy_long_run_resets_the_streak(self):
        # Runs answer /health and last 7 s (> HealthyRunSec 3): every crash is a fresh
        # streak of one, so a limit of 2 is never reached; the sentinel ends the test.
        proc, log, sentinel = self._supervise(
            "serve", 7, ["-MaxCrashStreak", "2", "-HealthyRunSec", "3"]
        )

        def stop_later():
            time.sleep(26)
            sentinel.write_text("x")

        threading.Thread(target=stop_later, daemon=True).start()
        out, _ = proc.communicate(timeout=180)
        text = log.read_text()
        self.assertNotIn("crash-loop breaker", text, out + text)
        self.assertGreaterEqual(text.count("became_healthy=True"), 2)
        self.assertEqual(proc.returncode, 0, out + text)


if __name__ == "__main__":
    unittest.main()
