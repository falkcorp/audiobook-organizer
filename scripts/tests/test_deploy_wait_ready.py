# file: scripts/tests/test_deploy_wait_ready.py
# version: 1.2.0
# guid: f22657cf-f7bf-4b6c-a942-2799f4bd4890
# last-edited: 2026-11-09

"""Tests for scripts/deploy-wait-ready.sh (09 §9.4 R1 "Content: deploy
tooling" and its Verification row; 09 §9.2.2; R1-27).

Each test runs the script as the recipes do, over scripts/tests/fake_ssh.py
(a stub `ssh` on PATH answering `systemctl show` from a scripted sequence)
and a local HTTP server answering /health. DEPLOY_WAIT_INTERVAL shortens the
poll interval so the whole file runs in seconds.

Run with:
    python3 -m unittest discover -s scripts -p 'test_deploy_wait_ready.py' -v
"""

from __future__ import annotations

import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import fake_ssh  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts" / "deploy-wait-ready.sh"
HOST = "wayne-manor.invalid"
UNIT = "audiobook-organizer.service"
SHOW = r"^systemctl show -p ActiveState,SubState,NRestarts,ExecMainStatus\S* audiobook-organizer\.service$"

# What the real server answers: every API route wraps its body in {"data": ...}
# (httputil.RespondWithOK), /health included. A fixture with a bare body is
# how a wait that required a top-level "ready" passed its tests and exited 2
# against every real server.
READY = (200, '{"data":{"status":"ok","ready":true,"memdb_ready":true,"degraded":[]}}')
WARMING = (200, '{"data":{"status":"ok","ready":false,"memdb_ready":false,"degraded":[]}}')


class WaitReadyTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.sandbox = Path(self._tmp.name)
        self.server: fake_ssh.HealthServer | None = None

    def tearDown(self) -> None:
        if self.server is not None:
            self.server.close()
        self._tmp.cleanup()

    def run_wait(
        self, shows, health, timeout="2", ssh=True, extra_env=None, rules=None, invocation=None
    ):
        """Run the script; return (completed process, ssh invocations, seconds)."""
        self.server = fake_ssh.HealthServer(health)
        all_rules = list(rules or [])
        if shows is not None:
            all_rules.append({"match": SHOW, "responses": shows})
        h = fake_ssh.install(self.sandbox, all_rules)
        env = h["env"]
        env["DEPLOY_WAIT_INTERVAL"] = "0.05"
        env.update(extra_env or {})
        args = ["bash", str(SCRIPT), self.server.url, timeout]
        if ssh:
            args += [HOST, UNIT]
        if invocation is not None:
            args.append(invocation)
        start = time.monotonic()
        proc = subprocess.run(args, env=env, capture_output=True, text=True, timeout=60)
        took = time.monotonic() - start
        calls = [e for e in fake_ssh.read_log(h["log"]) if e["kind"] == "ssh"]
        return proc, calls, took

    def test_activating_longer_than_timeout_then_ready_passes(self) -> None:
        """activating counts as progress: the TIMEOUT clock starts when the
        unit leaves activating (a long migration under EXTEND_TIMEOUT)."""
        shows = [fake_ssh.show("activating", "start")] * 60 + [fake_ssh.show("active")]
        proc, calls, took = self.run_wait(shows, [WARMING, WARMING, READY], timeout="1")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertGreater(took, 1.0, "the activating phase alone outlasts TIMEOUT")
        self.assertIn("ready", proc.stdout)
        self.assertTrue(all(c["host"] == HOST for c in calls))

    def test_failed_fails_at_once(self) -> None:
        proc, _, took = self.run_wait(
            [fake_ssh.show("failed", "failed", status=1)], [WARMING], timeout="30"
        )
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertLess(took, 10)
        self.assertIn("failed", proc.stderr)
        self.assertEqual(self.server.requests, 0, "a failed unit is not polled over HTTP")

    def test_inactive_fails_at_once(self) -> None:
        proc, _, took = self.run_wait([fake_ssh.show("inactive", "dead")], [WARMING], timeout="30")
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertLess(took, 10)
        self.assertIn("inactive", proc.stderr)

    def test_rising_nrestarts_fails_at_once(self) -> None:
        """A crash during the warmup restarts the unit; it must not count as
        progress just because the new process reads active again."""
        shows = [
            fake_ssh.show("active", restarts=0),
            fake_ssh.show("active", restarts=0),
            fake_ssh.show("active", restarts=1, status=2),
        ]
        proc, _, took = self.run_wait(shows, [WARMING], timeout="30")
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertLess(took, 10)
        self.assertIn("NRestarts", proc.stderr)

    def test_exit_78_is_reported_by_name(self) -> None:
        """09 §9.2.2 (OPS6-5): a store refusal is reported as "install the
        release the log names first", not as a crash, with exit 78."""
        proc, _, took = self.run_wait(
            [fake_ssh.show("failed", "failed", status=78)], [WARMING], timeout="30"
        )
        self.assertEqual(proc.returncode, 78, proc.stderr)
        self.assertLess(took, 10)
        self.assertIn("install the release the log names first", proc.stderr)
        self.assertNotIn("crash", proc.stderr.lower())

    def test_exit_78_under_a_unit_that_still_restarts_it(self) -> None:
        """A host still on the old unit (no RestartPreventExitStatus=78)
        restarts the refused binary; the rising NRestarts with status 78 is
        still named."""
        shows = [
            fake_ssh.show("activating", "auto-restart", restarts=1, status=78),
            fake_ssh.show("activating", "auto-restart", restarts=2, status=78),
        ]
        proc, _, _ = self.run_wait(shows, [WARMING], timeout="30")
        self.assertEqual(proc.returncode, 78, proc.stderr)
        self.assertIn("install the release the log names first", proc.stderr)

    def test_stale_exec_main_status_78_is_not_read_while_starting(self) -> None:
        """ExecMainStatus is the previous run's until the new one exits: a
        78 left from an earlier refusal must not fail a start that succeeds."""
        shows = [fake_ssh.show("activating", "start", status=78)] * 3 + [
            fake_ssh.show("active", status=78)
        ]
        proc, _, _ = self.run_wait(shows, [READY])
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_requested_restart_exit_75_is_progress(self) -> None:
        """Exit 75 is the restart the service asks for after staging a
        restore (05-backup §5B.3.4); a rise with status 75 is not a crash."""
        shows = [
            fake_ssh.show("active", restarts=0),
            fake_ssh.show("activating", "auto-restart", restarts=1, status=75),
            fake_ssh.show("active", restarts=1),
        ]
        proc, _, _ = self.run_wait(shows, [WARMING, WARMING, READY])
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("75", proc.stdout)

    def test_timeout_while_active_but_not_ready(self) -> None:
        proc, _, took = self.run_wait([fake_ssh.show("active")], [WARMING], timeout="1")
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertGreaterEqual(took, 1.0)
        self.assertIn("not ready", proc.stderr)

    def test_answer_without_ready_field_fails_with_2(self) -> None:
        for body in ('{"data":{"status":"ok"}}', '{"status":"ok"}', '{"data":"ready"}', "[]"):
            with self.subTest(body=body):
                proc, _, _ = self.run_wait([fake_ssh.show("active")], [(200, body)], timeout="30")
                self.assertEqual(proc.returncode, 2, proc.stderr)
                self.assertIn("no ready field", proc.stderr)

    def test_ready_inside_the_data_envelope_is_read(self) -> None:
        """Warming keeps polling and ready passes, read from data.ready."""
        proc, _, _ = self.run_wait([fake_ssh.show("active")], [WARMING, WARMING, READY])
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertGreaterEqual(self.server.requests, 3)

    def test_a_bare_body_is_read_too(self) -> None:
        """A body without the envelope (a proxy's own health shim) still works."""
        proc, _, _ = self.run_wait(
            [fake_ssh.show("active")], [(200, '{"status":"ok","ready":true}')]
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_ready_must_be_the_boolean_true(self) -> None:
        for body in ('{"data":{"ready":"true"}}', '{"data":{"ready":1}}'):
            with self.subTest(body=body):
                proc, _, _ = self.run_wait([fake_ssh.show("active")], [(200, body)], timeout="1")
                self.assertEqual(proc.returncode, 1, proc.stderr)
                self.assertIn("not ready", proc.stderr)

    def test_unparseable_unit_answer_fails_with_2(self) -> None:
        shows = [{"stdout": "Failed to parse bus message\n", "exit": 0}]
        proc, _, _ = self.run_wait(shows, [READY], timeout="30")
        self.assertEqual(proc.returncode, 2, proc.stderr)
        self.assertIn("ActiveState", proc.stderr)

    def test_unreachable_host_times_out_naming_ssh(self) -> None:
        shows = [
            {
                "stderr": "ssh: connect to host wayne-manor.invalid: Connection refused\n",
                "exit": 255,
            }
        ]
        proc, _, _ = self.run_wait(shows, [READY], timeout="1")
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertIn("Connection refused", proc.stderr)

    def test_no_ssh_host_polls_the_url_only(self) -> None:
        """Docker and launchd: no unit to read, so only /health is polled."""
        proc, calls, _ = self.run_wait(None, [(503, ""), WARMING, READY], ssh=False)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(calls, [], "no ssh without SSH_HOST")
        self.assertGreaterEqual(self.server.requests, 3)

    def test_no_ssh_host_uses_the_larger_url_timeout(self) -> None:
        """Without the unit the wait cannot tell a long start from a hang, so
        TIMEOUT is raised to DEPLOY_READY_URL_TIMEOUT (default 1800 s)."""
        proc, _, took = self.run_wait(
            None, [WARMING], timeout="1", ssh=False, extra_env={"DEPLOY_READY_URL_TIMEOUT": "2"}
        )
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertGreaterEqual(took, 2.0)
        self.assertIn("limit 2s", proc.stderr)

    def test_unchanged_invocation_is_not_ready(self) -> None:
        """The restart never ran (sudo refused it, or ssh dropped before it
        reached systemd): the old process is still active and ready. With
        the InvocationID read before the restart, ready needs a new one."""
        shows = [fake_ssh.show("active", invocation="0a0a")]
        proc, _, took = self.run_wait(
            shows,
            [READY],
            timeout="30",
            invocation="0a0a",
            extra_env={"DEPLOY_RESTART_GRACE": "0.3"},
        )
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertLess(took, 10)
        self.assertIn("not restarted", proc.stderr)
        self.assertIn("InvocationID", proc.stderr)

    def test_new_invocation_is_ready(self) -> None:
        shows = [fake_ssh.show("active", invocation="0a0a")] * 2 + [
            fake_ssh.show("active", invocation="0b0b")
        ]
        proc, _, _ = self.run_wait(
            shows,
            [READY],
            timeout="30",
            invocation="0a0a",
            extra_env={"DEPLOY_RESTART_GRACE": "30"},
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_empty_baseline_accepts_any_new_invocation(self) -> None:
        """A unit that was stopped before the start has no invocation to
        compare against; any non-empty one is the new process."""
        proc, _, _ = self.run_wait(
            [fake_ssh.show("active", invocation="0b0b")], [READY], invocation=""
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_requested_restart_needs_exactly_one_more_restart(self) -> None:
        shows = [
            fake_ssh.show("active", restarts=0),
            fake_ssh.show("activating", "auto-restart", restarts=2, status=75),
        ]
        proc, _, _ = self.run_wait(shows, [WARMING], timeout="30")
        self.assertEqual(proc.returncode, 1, proc.stderr)
        self.assertIn("NRestarts", proc.stderr)

    def test_usage_errors_exit_2(self) -> None:
        for args in (
            [],
            ["http://127.0.0.1:1"],
            ["http://127.0.0.1:1", "x"],
            ["http://127.0.0.1:1", "5", HOST],
        ):
            proc = subprocess.run(
                ["bash", str(SCRIPT), *args], capture_output=True, text=True, timeout=30
            )
            self.assertEqual(proc.returncode, 2, (args, proc.stderr))
            self.assertIn("usage", proc.stderr.lower())


if __name__ == "__main__":
    unittest.main()
