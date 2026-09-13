# file: scripts/tests/test_setup_mac_ai_worker.py
# version: 1.0.0
# guid: 8d4f2a6c-1e9b-4b7d-a3c5-6f0e2d9b4a18
# last-edited: 2026-09-13
"""Tests for scripts/setup_mac_ai_worker.py (pure functions only, no side effects)."""

import importlib.util
import sys
from pathlib import Path

import pytest

_SPEC = importlib.util.spec_from_file_location(
    "setup_mac_ai_worker", Path(__file__).resolve().parents[1] / "setup_mac_ai_worker.py"
)
assert _SPEC is not None and _SPEC.loader is not None
m = importlib.util.module_from_spec(_SPEC)
# @dataclass resolves its module through sys.modules, so register before exec.
sys.modules[_SPEC.name] = m
_SPEC.loader.exec_module(m)


def test_index_zero_matches_original_mac():
    p = m.plan_ports(0, 4)
    assert p.ollama_remote == 11434
    assert p.whisper_remote == (19848, 19849, 19850, 19851)
    assert p.whisper_local == (19848, 19849, 19850, 19851)


def test_new_macs_never_collide():
    seen = set()
    for idx in range(0, 6):
        p = m.plan_ports(idx, m.MAX_WORKERS)
        ports = set(p.whisper_remote) | {p.ollama_remote}
        assert not ports & seen, f"index {idx} collides"
        seen |= ports


def test_local_ports_are_same_on_every_mac():
    assert m.plan_ports(3, 4).whisper_local == m.plan_ports(0, 4).whisper_local


@pytest.mark.parametrize("idx,workers", [(-1, 4), (1, 0), (1, m.MAX_WORKERS + 1)])
def test_rejects_bad_input(idx, workers):
    with pytest.raises(ValueError):
        m.plan_ports(idx, workers)


def test_forwards_map_remote_to_local():
    p = m.plan_ports(1, 2)
    assert p.forwards(True) == ["19856:127.0.0.1:19848", "19857:127.0.0.1:19849", "11435:127.0.0.1:11434"]
    assert p.forwards(False) == ["19856:127.0.0.1:19848", "19857:127.0.0.1:19849"]


def test_tunnel_owns_connection_and_fails_on_taken_port():
    pl = m.tunnel_plist("u@h", ["1:127.0.0.1:2"], Path("/Users/x"))
    args = pl["ProgramArguments"]
    for opt in ("ControlMaster=no", "ControlPath=none", "ExitOnForwardFailure=yes", "BatchMode=yes"):
        assert opt in args
    assert args[-1] == "u@h"
    assert args[args.index("-R") + 1] == "1:127.0.0.1:2"


def test_whisper_worker_not_demoted_and_has_brew_path():
    pl = m.whisper_plist(19848, Path("/repo"), Path("/Users/x"), "model")
    assert "ProcessType" not in pl and "Nice" not in pl
    assert pl["EnvironmentVariables"]["WHISPER_BIND"] == "127.0.0.1"
    assert pl["EnvironmentVariables"]["WHISPER_PORT"] == "19848"
    assert "/opt/homebrew/bin" in pl["EnvironmentVariables"]["PATH"]
    assert pl["Label"] == "com.jdfalk.whisper-mlx-19848"


@pytest.mark.parametrize(
    "before,after,changed",
    [
        ("", "OLLAMA_KEEP_ALIVE=30m\n", True),
        ("OLLAMA_KEEP_ALIVE=30m\n", "OLLAMA_KEEP_ALIVE=30m\n", False),
        ("OLLAMA_KEEP_ALIVE=5m\n", "OLLAMA_KEEP_ALIVE=30m\n", True),
        ("# c\nFOO=1\n", "# c\nFOO=1\nOLLAMA_KEEP_ALIVE=30m\n", True),
    ],
)
def test_env_line_idempotent(before, after, changed):
    assert m.ensure_env_line(before, "OLLAMA_KEEP_ALIVE=30m") == (after, changed)


def test_endpoint_entries_use_remote_ports():
    e = m.prod_endpoint_entries(m.plan_ports(2, 2), "mac2")
    assert [x["url"] for x in e] == ["http://127.0.0.1:19864", "http://127.0.0.1:19865"]
    assert all(x["require_gpu"] and "mac2" in x["capabilities"] for x in e)


def test_dry_run_changes_nothing(capsys):
    assert m.main(["--host-index", "1", "--prod", "u@h"]) == 0
    assert "Dry run" in capsys.readouterr().out


def test_index_zero_needs_explicit_flag():
    with pytest.raises(SystemExit):
        m.main(["--host-index", "0", "--prod", "u@h"])
