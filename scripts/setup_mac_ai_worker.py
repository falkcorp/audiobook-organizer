#!/usr/bin/env python3
# file: scripts/setup_mac_ai_worker.py
# version: 1.0.0
# guid: 3b8e6d21-7f4a-4c19-9e52-0a6d4f8c1b73
# last-edited: 2026-09-13
"""Set up an Apple Silicon Mac as an AI worker for audiobook-organizer.

It installs and configures three things:

  * Ollama (brew service) holding the models prod uses: qwen2.5:7b-instruct for
    filename parsing and bge-m3 for embeddings, with a 30m keep-alive.
  * N MLX Whisper workers (scripts/whisper_mlx_server.py) on 127.0.0.1, one
    launchd agent per port.
  * ONE reverse SSH tunnel publishing all of them to the server's loopback.

Everything stays bound to loopback on the Mac. The server reaches the workers
only through the tunnel, which is authenticated by the SSH key.

Each Mac gets its own block of server-side ports, chosen by --host-index:

    index 0 (the original Mac): ollama 11434, whisper 19848..
    index N:                    ollama 11434+N, whisper 19848+8N..

The ports on the Mac itself are always 11434 and 19848.. . Only the
server-side ports differ, and those must never collide: the tunnel sets
ExitOnForwardFailure, so a taken port kills the WHOLE tunnel, whisper included.

By default this prints the plan and changes nothing. Pass --apply to run it.

    python3 scripts/setup_mac_ai_worker.py --host-index 1 --prod user@server
    python3 scripts/setup_mac_ai_worker.py --host-index 1 --prod user@server --apply

Related: docs/operations/metal-whisper-worker.md (the single-Mac runbook).
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import plistlib
import shutil
import subprocess
import sys
import time
import urllib.request
from dataclasses import dataclass
from pathlib import Path

OLLAMA_LOCAL_PORT = 11434
WHISPER_LOCAL_BASE = 19848
WHISPER_REMOTE_STRIDE = 8  # server-side ports reserved per Mac
MAX_WORKERS = WHISPER_REMOTE_STRIDE
DEFAULT_MODELS = ("qwen2.5:7b-instruct", "bge-m3")
DEFAULT_WHISPER_MODEL = "mlx-community/whisper-small.en-mlx"
KEEP_ALIVE_LINE = "OLLAMA_KEEP_ALIVE=30m"
BREW_PATH = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
TUNNEL_LABEL = "com.jdfalk.ai-tunnel"


@dataclass(frozen=True)
class PortPlan:
    host_index: int
    ollama_remote: int
    whisper_local: tuple[int, ...]
    whisper_remote: tuple[int, ...]

    def forwards(self, include_ollama: bool) -> list[str]:
        """Return the -R specs, remote:127.0.0.1:local."""
        out = [f"{r}:127.0.0.1:{l}" for r, l in zip(self.whisper_remote, self.whisper_local)]
        if include_ollama:
            out.append(f"{self.ollama_remote}:127.0.0.1:{OLLAMA_LOCAL_PORT}")
        return out


def plan_ports(host_index: int, workers: int) -> PortPlan:
    if host_index < 0:
        raise ValueError("host index must be >= 0")
    if not 1 <= workers <= MAX_WORKERS:
        raise ValueError(f"workers must be between 1 and {MAX_WORKERS}")
    local = tuple(WHISPER_LOCAL_BASE + i for i in range(workers))
    remote_base = WHISPER_LOCAL_BASE + WHISPER_REMOTE_STRIDE * host_index
    remote = tuple(remote_base + i for i in range(workers))
    return PortPlan(host_index, OLLAMA_LOCAL_PORT + host_index, local, remote)


def whisper_label(port: int) -> str:
    return f"com.jdfalk.whisper-mlx-{port}"


def whisper_plist(port: int, repo: Path, home: Path, model: str) -> dict:
    # No ProcessType/Nice: background QoS pins the worker to the efficiency
    # cores and starves Metal (4.92 s vs >240 s measured 2026-08-31).
    # PATH must include Homebrew or mlx_whisper cannot find ffmpeg.
    return {
        "Label": whisper_label(port),
        "ProgramArguments": ["/opt/homebrew/bin/uv", "run", "scripts/whisper_mlx_server.py"],
        "WorkingDirectory": str(repo),
        "EnvironmentVariables": {
            "WHISPER_BIND": "127.0.0.1",
            "WHISPER_PORT": str(port),
            "WHISPER_MLX_MODEL": model,
            "PATH": BREW_PATH,
        },
        "RunAtLoad": True,
        "KeepAlive": {"SuccessfulExit": False},
        "ThrottleInterval": 30,
        "StandardOutPath": str(home / "Library/Logs" / f"whisper-mlx-{port}.log"),
        "StandardErrorPath": str(home / "Library/Logs" / f"whisper-mlx-{port}.err"),
    }


def tunnel_plist(prod: str, forwards: list[str], home: Path) -> dict:
    # ControlMaster=no / ControlPath=none: the tunnel must own its connection,
    # or it rides an interactive session's master and dies with it.
    args = [
        "/usr/bin/ssh", "-N",
        "-o", "ControlMaster=no",
        "-o", "ControlPath=none",
        "-o", "ExitOnForwardFailure=yes",
        "-o", "ServerAliveInterval=30",
        "-o", "ServerAliveCountMax=3",
        "-o", "BatchMode=yes",
        "-o", "StrictHostKeyChecking=accept-new",
    ]
    for f in forwards:
        args += ["-R", f]
    args.append(prod)
    return {
        "Label": TUNNEL_LABEL,
        "ProgramArguments": args,
        "RunAtLoad": True,
        "KeepAlive": True,
        "ThrottleInterval": 30,
        "ProcessType": "Background",
        "StandardOutPath": str(home / "Library/Logs/ai-tunnel.log"),
        "StandardErrorPath": str(home / "Library/Logs/ai-tunnel.log"),
    }


def ensure_env_line(text: str, line: str) -> tuple[str, bool]:
    """Return (new_text, changed). Replace an existing assignment of the same key."""
    key = line.split("=", 1)[0]
    lines = text.splitlines()
    for i, cur in enumerate(lines):
        if cur.strip().startswith(key + "="):
            if cur.strip() == line:
                return text, False
            lines[i] = line
            return "\n".join(lines) + "\n", True
    lines.append(line)
    return "\n".join(l for l in lines if l or len(lines) == 1).strip("\n") + "\n", True


def prod_endpoint_entries(plan: PortPlan, label: str) -> list[dict]:
    """WHISPER_ENDPOINTS entries for the server's systemd drop-in."""
    return [
        {"url": f"http://127.0.0.1:{p}", "priority": 50, "concurrency": 1,
         "require_gpu": True, "capabilities": ["local", "mac", label]}
        for p in plan.whisper_remote
    ]


# ---------------------------------------------------------------- side effects


def run(cmd: list[str], check: bool = True) -> subprocess.CompletedProcess:
    print("  $", " ".join(cmd))
    return subprocess.run(cmd, check=check, text=True, capture_output=True)


def launchd_reload(plist_path: Path, label: str) -> None:
    # kickstart -k does NOT re-read a changed plist; bootout + bootstrap does.
    uid = os.getuid()
    run(["launchctl", "bootout", f"gui/{uid}/{label}"], check=False)
    run(["launchctl", "bootstrap", f"gui/{uid}", str(plist_path)])


def http_ok(url: str, timeout: float = 5) -> bool:
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:  # noqa: S310 (loopback only)
            return r.status == 200
    except OSError:
        return False


def wait_for(url: str, seconds: int) -> bool:
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if http_ok(url):
            return True
        time.sleep(3)
    return False


def apply(args: argparse.Namespace, plan: PortPlan) -> int:
    home = Path.home()
    agents = home / "Library/LaunchAgents"
    agents.mkdir(parents=True, exist_ok=True)
    (home / "Library/Logs").mkdir(parents=True, exist_ok=True)

    if shutil.which("brew") is None:
        print("ERROR: Homebrew is required: https://brew.sh", file=sys.stderr)
        return 1

    print("\n[1/5] Homebrew packages")
    pkgs = ["uv", "ffmpeg"] + ([] if args.skip_ollama else ["ollama"])
    for p in pkgs:
        if run(["brew", "list", "--formula", p], check=False).returncode != 0:
            run(["brew", "install", p])

    if not args.skip_ollama:
        print("\n[2/5] Ollama service, keep-alive and models")
        env_file = home / ".homebrew/services/ollama.env"
        env_file.parent.mkdir(parents=True, exist_ok=True)
        old = env_file.read_text() if env_file.exists() else ""
        new, changed = ensure_env_line(old, KEEP_ALIVE_LINE)
        if changed:
            env_file.write_text(new)
        # brew services regenerates the plist; the .env file is the supported override.
        run(["brew", "services", "restart", "ollama"])
        if not wait_for(f"http://127.0.0.1:{OLLAMA_LOCAL_PORT}/api/tags", 60):
            print("ERROR: ollama did not come up on 127.0.0.1:11434", file=sys.stderr)
            return 1
        for m in args.models:
            print(f"  pulling {m} (can take several minutes)")
            subprocess.run(["ollama", "pull", m], check=True)

    if not args.skip_whisper:
        print("\n[3/5] Whisper workers")
        for port in plan.whisper_local:
            path = agents / f"{whisper_label(port)}.plist"
            path.write_bytes(plistlib.dumps(whisper_plist(port, args.repo, home, args.whisper_model)))
            launchd_reload(path, whisper_label(port))
        for port in plan.whisper_local:
            # First start downloads the model (~500 MB).
            ok = wait_for(f"http://127.0.0.1:{port}/health", 300)
            print(f"  whisper :{port} {'ok' if ok else 'NOT HEALTHY (see ~/Library/Logs/whisper-mlx-%d.err)' % port}")

    print("\n[4/5] SSH key to the server")
    if run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", args.prod, "true"], check=False).returncode != 0:
        print(f"ERROR: passwordless ssh to {args.prod} failed. Run: ssh-copy-id {args.prod}", file=sys.stderr)
        return 1

    print("\n[5/5] Reverse tunnel")
    forwards = plan.forwards(include_ollama=not args.skip_ollama)
    tpath = agents / f"{TUNNEL_LABEL}.plist"
    tpath.write_bytes(plistlib.dumps(tunnel_plist(args.prod, forwards, home)))
    launchd_reload(tpath, TUNNEL_LABEL)
    time.sleep(5)
    alive = run(["pgrep", "-f", "ssh -N -o ControlMaster=no"], check=False).returncode == 0
    print(f"  tunnel process {'running' if alive else 'NOT RUNNING (see ~/Library/Logs/ai-tunnel.log)'}")
    return 0 if alive else 1


def print_plan(args: argparse.Namespace, plan: PortPlan) -> None:
    print(f"Mac AI worker, host index {plan.host_index}, server {args.prod}")
    print(f"  repo:            {args.repo}")
    if not args.skip_ollama:
        print(f"  ollama:          Mac 127.0.0.1:{OLLAMA_LOCAL_PORT} -> server 127.0.0.1:{plan.ollama_remote}")
        print(f"  models:          {', '.join(args.models)} (keep-alive 30m)")
    if not args.skip_whisper:
        for l, r in zip(plan.whisper_local, plan.whisper_remote):
            print(f"  whisper:         Mac 127.0.0.1:{l} -> server 127.0.0.1:{r}")
    print(f"  tunnel agent:    {TUNNEL_LABEL}")
    if not args.skip_whisper:
        entries = prod_endpoint_entries(plan, f"mac{plan.host_index}")
        print("\nAfter it is healthy, add these to WHISPER_ENDPOINTS on the server:")
        for e in entries:
            print("  " + json.dumps(e, separators=(",", ":")))
    if not args.skip_ollama:
        print(
            "\nNote: the server can use only ONE Ollama endpoint until the AI capability "
            "routing work lands (PLAN.md, PR 4). This Mac's Ollama is ready for that; "
            f"until then it can be tested from the server at http://127.0.0.1:{plan.ollama_remote}/api/tags."
        )


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--host-index", type=int, required=True,
                    help="1, 2, ... for each new Mac; 0 is the original Mac's port block")
    ap.add_argument("--prod", required=True, help="ssh target for the server, user@host")
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--repo", type=Path, default=Path(__file__).resolve().parent.parent)
    ap.add_argument("--models", nargs="+", default=list(DEFAULT_MODELS))
    ap.add_argument("--whisper-model", default=DEFAULT_WHISPER_MODEL)
    ap.add_argument("--skip-ollama", action="store_true")
    ap.add_argument("--skip-whisper", action="store_true")
    ap.add_argument("--allow-index-0", action="store_true",
                    help="reuse the original Mac's server ports (only when replacing that Mac)")
    ap.add_argument("--apply", action="store_true", help="make changes (default: print the plan only)")
    args = ap.parse_args(argv)

    if args.host_index == 0 and not args.allow_index_0:
        ap.error("index 0 is the original Mac's port block; pass --allow-index-0 only when replacing it")
    plan = plan_ports(args.host_index, args.workers)
    print_plan(args, plan)
    if not args.apply:
        print("\nDry run. Re-run with --apply to make these changes.")
        return 0
    if sys.platform != "darwin" or platform.machine() != "arm64":
        print("ERROR: this needs an Apple Silicon Mac (MLX Whisper runs on Metal)", file=sys.stderr)
        return 1
    return apply(args, plan)


if __name__ == "__main__":
    sys.exit(main())
