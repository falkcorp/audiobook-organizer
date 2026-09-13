# file: scripts/tests/test_setup_mac_ai_worker.py
# version: 1.1.0
# guid: 8d4f2a6c-1e9b-4b7d-a3c5-6f0e2d9b4a18
# last-edited: 2026-09-13
"""Tests for scripts/setup_mac_ai_worker.py (pure functions only, no side effects).

stdlib unittest only: the Repo Guards job runs these without pytest installed.
"""

import contextlib
import importlib.util
import io
import sys
import unittest
from pathlib import Path

_SPEC = importlib.util.spec_from_file_location(
    "setup_mac_ai_worker", Path(__file__).resolve().parents[1] / "setup_mac_ai_worker.py"
)
assert _SPEC is not None and _SPEC.loader is not None
m = importlib.util.module_from_spec(_SPEC)
# @dataclass resolves its module through sys.modules, so register before exec.
sys.modules[_SPEC.name] = m
_SPEC.loader.exec_module(m)


class PortPlanTest(unittest.TestCase):
    def test_index_zero_matches_original_mac(self):
        p = m.plan_ports(0, 4)
        self.assertEqual(p.ollama_remote, 11434)
        self.assertEqual(p.whisper_remote, (19848, 19849, 19850, 19851))
        self.assertEqual(p.whisper_local, (19848, 19849, 19850, 19851))

    def test_new_macs_never_collide(self):
        seen = set()
        for idx in range(0, 6):
            p = m.plan_ports(idx, m.MAX_WORKERS)
            ports = set(p.whisper_remote) | {p.ollama_remote}
            self.assertFalse(ports & seen, f"index {idx} collides")
            seen |= ports

    def test_local_ports_are_same_on_every_mac(self):
        self.assertEqual(m.plan_ports(3, 4).whisper_local, m.plan_ports(0, 4).whisper_local)

    def test_rejects_bad_input(self):
        for idx, workers in [(-1, 4), (1, 0), (1, m.MAX_WORKERS + 1)]:
            with self.subTest(idx=idx, workers=workers), self.assertRaises(ValueError):
                m.plan_ports(idx, workers)

    def test_forwards_map_remote_to_local(self):
        p = m.plan_ports(1, 2)
        self.assertEqual(
            p.forwards(True),
            ["19856:127.0.0.1:19848", "19857:127.0.0.1:19849", "11435:127.0.0.1:11434"],
        )
        self.assertEqual(p.forwards(False), ["19856:127.0.0.1:19848", "19857:127.0.0.1:19849"])


class PlistTest(unittest.TestCase):
    def test_tunnel_owns_connection_and_fails_on_taken_port(self):
        args = m.tunnel_plist("u@h", ["1:127.0.0.1:2"], Path("/Users/x"))["ProgramArguments"]
        for opt in ("ControlMaster=no", "ControlPath=none", "ExitOnForwardFailure=yes", "BatchMode=yes"):
            self.assertIn(opt, args)
        self.assertEqual(args[-1], "u@h")
        self.assertEqual(args[args.index("-R") + 1], "1:127.0.0.1:2")

    def test_whisper_worker_not_demoted_and_has_brew_path(self):
        pl = m.whisper_plist(19848, Path("/repo"), Path("/Users/x"), "model")
        self.assertNotIn("ProcessType", pl)
        self.assertNotIn("Nice", pl)
        env = pl["EnvironmentVariables"]
        self.assertEqual(env["WHISPER_BIND"], "127.0.0.1")
        self.assertEqual(env["WHISPER_PORT"], "19848")
        self.assertIn("/opt/homebrew/bin", env["PATH"])
        self.assertEqual(pl["Label"], "com.jdfalk.whisper-mlx-19848")


class EnvFileTest(unittest.TestCase):
    def test_env_line_idempotent(self):
        cases = [
            ("", "OLLAMA_KEEP_ALIVE=30m\n", True),
            ("OLLAMA_KEEP_ALIVE=30m\n", "OLLAMA_KEEP_ALIVE=30m\n", False),
            ("OLLAMA_KEEP_ALIVE=5m\n", "OLLAMA_KEEP_ALIVE=30m\n", True),
            ("# c\nFOO=1\n", "# c\nFOO=1\nOLLAMA_KEEP_ALIVE=30m\n", True),
        ]
        for before, after, changed in cases:
            with self.subTest(before=before):
                self.assertEqual(m.ensure_env_line(before, "OLLAMA_KEEP_ALIVE=30m"), (after, changed))


class CliTest(unittest.TestCase):
    def test_endpoint_entries_use_remote_ports(self):
        e = m.prod_endpoint_entries(m.plan_ports(2, 2), "mac2")
        self.assertEqual([x["url"] for x in e], ["http://127.0.0.1:19864", "http://127.0.0.1:19865"])
        self.assertTrue(all(x["require_gpu"] and "mac2" in x["capabilities"] for x in e))

    def test_dry_run_changes_nothing(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = m.main(["--host-index", "1", "--prod", "u@h"])
        self.assertEqual(rc, 0)
        self.assertIn("Dry run", out.getvalue())

    def test_index_zero_needs_explicit_flag(self):
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            m.main(["--host-index", "0", "--prod", "u@h"])


if __name__ == "__main__":
    unittest.main()
