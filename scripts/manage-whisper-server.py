# file: scripts/manage-whisper-server.py
# version: 3.0.0
# guid: f7a8b9c0-d1e2-3456-fabc-678901234567
# last-edited: 2026-11-17
#
# /// script
# requires-python = ">=3.11"
# dependencies = ["pywinrm>=0.4.3"]
# ///
#
# Manages the Whisper transcription server on the Windows machine (GPU or CPU) via
# WinRM (HTTP port 5985). Works from macOS or Linux — no WSMan client library,
# no OpenSSH, no PowerShell for macOS needed.
#
# Prerequisites:
#   1. Run scripts/setup-winrm-windows.ps1 as Administrator on the Windows machine
#   2. uv installed locally: curl -LsSf https://astral.sh/uv/install.sh | sh
#
# The host is REQUIRED via the ABK_GPU_HOST environment variable (hostname or
# IP, e.g. <windows-gpu-host>) — there is no internal-network default. The
# Windows account (WHISPER_WIN_USER) is needed only by --deploy and --restart;
# --status is a plain HTTP request and needs neither the account nor a password.
# --deploy copies whisper_server.py AND the three modules it imports
# (whisper_path_pipeline.py, whisper_machine_id.py, whisper_decode_child.py) into
# that account's profile folder: the four go together or the server will not start.
#
# Port: ONE source of truth, WHISPER_PORT in this script's environment (default
# 19847, the server's own default), used for the health check, for finding the old
# process to kill, and for the launched server (--port).
#
# Bind: --restart launches the server on 127.0.0.1 (loopback) unless you pass
# --bind-host. The server has no authentication and its path routes return
# transcripts of files under WHISPER_PATH_MAP, so a LAN worker is an explicit
# opt-in. A loopback worker does not answer --status from this machine (the check
# runs from here, over the network); reach it through an SSH tunnel, or run the
# check on the Windows machine itself.
#
# WinRM command length: pywinrm sends a script as `powershell -encodedcommand
# <base64 of UTF-16>` on a command line that cmd.exe limits to 8191 characters, so
# a script can carry about 3 KB of text. The files here are 8-47 KB, and the old
# whole-file-in-one-command deploy could not work for them. --deploy now writes each
# file in CHUNK_BYTES pieces (an encoded command of about 5 KB) and verifies the
# SHA-256 of what landed before it moves on.
#
# Usage:
#   ABK_GPU_HOST=<windows-gpu-host> uv run scripts/manage-whisper-server.py --deploy
#   uv run scripts/manage-whisper-server.py --restart           # kill + relaunch
#   uv run scripts/manage-whisper-server.py --deploy --restart  # both (most common)
#   uv run scripts/manage-whisper-server.py --restart --device cpu   # force CPU
#   uv run scripts/manage-whisper-server.py --status            # check /health
#
# Credentials: prompted on first run; pass via env vars to skip the prompt:
#   WHISPER_WIN_PASSWORD=... uv run scripts/manage-whisper-server.py --deploy --restart

import argparse
import base64
import getpass
import hashlib
import os
import sys
import urllib.error
import urllib.request

try:
    import winrm
except ImportError:
    print("ERROR: pywinrm not found. Run via: uv run scripts/manage-whisper-server.py")
    sys.exit(1)

COMPUTER = os.environ.get("ABK_GPU_HOST")
if not COMPUTER:
    print(
        "ERROR: ABK_GPU_HOST environment variable is required "
        "(hostname or IP of the Windows GPU machine, e.g. <windows-gpu-host>)"
    )
    sys.exit(1)
WIN_USER = os.environ.get("WHISPER_WIN_USER")  # required only by --deploy / --restart
PORT = int(os.environ.get("WHISPER_PORT") or 19847)
HEALTH_URL = f"http://{COMPUTER}:{PORT}/health"
# The server imports these from its own folder; they are deployed with it.
SERVER_FILES = (
    "whisper_server.py",
    "whisper_path_pipeline.py",
    "whisper_machine_id.py",
    "whisper_decode_child.py",
)
CHUNK_BYTES = 1200  # raw bytes per WinRM command; see the header on the 8191 limit


def require_win_user() -> str:
    if not WIN_USER:
        print(
            "ERROR: WHISPER_WIN_USER is required for --deploy and --restart "
            "(the Windows account name)",
            file=sys.stderr,
        )
        sys.exit(1)
    return WIN_USER


def remote_dir() -> str:
    return rf"C:\Users\{require_win_user()}"


def ps_quote(text: str) -> str:
    """A PowerShell single-quoted literal: nothing inside is interpreted, and an
    embedded quote is doubled."""
    return "'" + text.replace("'", "''") + "'"


def start_process_arg(arg: str) -> str:
    """One element of Start-Process -ArgumentList as a PowerShell literal. Start-Process
    joins the elements with spaces and does NOT quote them, so a value with a space (a
    profile folder, "John Smith") must carry its own double quotes to stay one
    argument."""
    if arg and not any(c.isspace() or c == '"' for c in arg):
        return ps_quote(arg)
    return ps_quote('"' + arg.replace('"', '\\"') + '"')


def build_kill_script(port: int) -> str:
    """PowerShell that stops whatever LISTENS on `port` (the local address column, not
    a connection that merely has the port as its remote end)."""
    return r"""
$pids = (netstat -ano | Select-String ':__PORT__\s+\S+\s+LISTENING') -replace '.*\s(\d+)$','$1' |
        Sort-Object -Unique
foreach ($p in $pids) {
    if ($p -match '^\d+$') {
        Stop-Process -Id $p -Force -ErrorAction SilentlyContinue
        Write-Host "Killed PID $p"
    }
}
Start-Sleep -Seconds 1
""".replace("__PORT__", str(int(port)))


def build_launch_script(
    remote_folder: str, device: str, model: str, bind_host: str, port: int
) -> str:
    """PowerShell that finds uv and starts the server hidden. Runs under Windows
    PowerShell 5.1, the one WinRM gives: no `?.`, no `??`, no PowerShell 7 syntax, and
    every value is a single-quoted literal."""
    server_args = [rf"{remote_folder}\whisper_server.py", "--device", device]
    if model:
        server_args += ["--model", model]
    server_args += ["--host", bind_host, "--port", str(int(port))]
    args_ps = ", ".join(start_process_arg(a) for a in ["run"] + server_args)
    return rf"""
$uvCmd = Get-Command uv -ErrorAction SilentlyContinue
if ($uvCmd) {{ $uvExe = $uvCmd.Source }} else {{ $uvExe = Join-Path $env:USERPROFILE '.local\bin\uv.exe' }}
if (-not (Test-Path -LiteralPath $uvExe)) {{
    Write-Error 'uv not found. Install: winget install --id astral-sh.uv'
    exit 1
}}
$log = Join-Path $env:USERPROFILE 'whisper_server.log'
$p = Start-Process `
    -FilePath $uvExe `
    -ArgumentList {args_ps} `
    -RedirectStandardOutput $log `
    -RedirectStandardError ($log + '.err') `
    -WindowStyle Hidden `
    -PassThru
Write-Host ('Started PID ' + $p.Id)
"""


def get_session(password: str) -> winrm.Session:
    return winrm.Session(
        f"http://{COMPUTER}:5985/wsman",
        auth=(require_win_user(), password),
        transport="ntlm",  # NTLM works for non-domain (workgroup) machines
        server_cert_validation="ignore",
    )


def run_ps(session: winrm.Session, script: str) -> str:
    result = session.run_ps(script)
    out = result.std_out.decode(errors="replace").strip()
    err = result.std_err.decode(errors="replace").strip()
    if result.status_code != 0:
        raise RuntimeError(f"PowerShell error (exit {result.status_code}):\n{err}")
    if err:
        print(f"  [stderr] {err}", file=sys.stderr)
    return out


def cmd_status():
    print(f"==> Health check: {HEALTH_URL}")
    try:
        with urllib.request.urlopen(HEALTH_URL, timeout=5) as r:
            import json

            data = json.loads(r.read())
            print(
                f"  RUNNING — device={data.get('device')} model={data.get('model')} "
                f"batch_pipeline={data.get('batch_pipeline')}"
            )
    except (urllib.error.URLError, OSError):
        print(
            "  NOT RESPONDING — the server may be stopped, or bound to loopback only "
            "(the default), which is not reachable from this machine"
        )


def _deploy_one(session: winrm.Session, local_path: str, remote_path: str) -> None:
    with open(local_path, "rb") as f:
        data = f.read()
    expected = hashlib.sha256(data).hexdigest()
    tmp = remote_path + ".part"
    # First chunk creates/truncates the file; each later chunk appends.
    for i in range(0, max(len(data), 1), CHUNK_BYTES):
        encoded = base64.b64encode(data[i : i + CHUNK_BYTES]).decode()
        mode = "Create" if i == 0 else "Append"
        run_ps(
            session,
            f"""
$b = [Convert]::FromBase64String('{encoded}')
$fs = [System.IO.File]::Open('{tmp}', [System.IO.FileMode]::{mode}, [System.IO.FileAccess]::Write)
$fs.Write($b, 0, $b.Length)
$fs.Close()
""",
        )
    got = run_ps(
        session,
        f"(Get-FileHash -Algorithm SHA256 -LiteralPath '{tmp}').Hash.ToLower()",
    ).strip()
    if got != expected:
        raise RuntimeError(
            f"{remote_path}: SHA-256 mismatch after copy (sent {expected}, got {got})"
        )
    run_ps(session, f"Move-Item -Force -LiteralPath '{tmp}' -Destination '{remote_path}'")
    print(f"  {len(data)} bytes, sha256 {expected[:12]} verified")


def cmd_deploy(session: winrm.Session):
    # The server files live next to this script.
    here = os.path.dirname(os.path.abspath(__file__))
    for name in SERVER_FILES:
        local_path = os.path.join(here, name)
        if not os.path.exists(local_path):
            print(f"ERROR: cannot find {local_path}", file=sys.stderr)
            sys.exit(1)

    for name in SERVER_FILES:
        local_path = os.path.join(here, name)
        remote_path = rf"{remote_dir()}\{name}"
        print(f"==> Deploying {local_path} → {COMPUTER}:{remote_path}")
        _deploy_one(session, local_path, remote_path)
    print("  Deployed OK")


def cmd_restart(session: winrm.Session, device: str, model: str, bind_host: str):
    print(
        f"==> Restarting Whisper server on {COMPUTER} "
        f"(model={model or '(server default for the device)'}, device={device}, "
        f"bind={bind_host}, port={PORT})"
    )
    if bind_host not in ("127.0.0.1", "::1", "localhost"):
        print(
            "  WARNING: the worker has no authentication; any host that can reach "
            f"{bind_host}:{PORT} can obtain transcripts of files under WHISPER_PATH_MAP. "
            "Firewall it to the server only.",
            file=sys.stderr,
        )

    out = run_ps(session, build_kill_script(PORT))
    if out:
        print(f"  {out}")

    out = run_ps(session, build_launch_script(remote_dir(), device, model, bind_host, PORT))
    print(f"  {out}")

    # Wait for model load then check health.
    print("  Waiting 15s for model load...")
    import time

    time.sleep(15)
    cmd_status()


def main():
    parser = argparse.ArgumentParser(description="Manage the remote Whisper server")
    parser.add_argument(
        "--deploy",
        action="store_true",
        help="Copy whisper_server.py and its three sibling modules to Windows",
    )
    parser.add_argument("--restart", action="store_true", help="Kill + relaunch the server")
    parser.add_argument("--status", action="store_true", help="Check /health (no auth needed)")
    parser.add_argument(
        "--device",
        choices=("auto", "cpu", "cuda"),
        default="auto",
        help="inference device for --restart (default: auto = CUDA if it loads, else CPU)",
    )
    parser.add_argument(
        "--model",
        default="",
        help="model for --restart (default: unset, so the server picks by device: "
        "large-v2 on cuda, base.en on cpu)",
    )
    parser.add_argument(
        "--bind-host",
        default="127.0.0.1",
        help="address the server binds (default 127.0.0.1, loopback only; the server has "
        "no authentication, so a wider bind is an explicit opt-in)",
    )
    args = parser.parse_args()

    if not any([args.deploy, args.restart, args.status]):
        args.status = True

    if args.status and not args.deploy and not args.restart:
        cmd_status()
        return

    # Auth only needed for deploy/restart.
    password = os.environ.get("WHISPER_WIN_PASSWORD") or getpass.getpass(
        f"Windows password for {require_win_user()}@{COMPUTER}: "
    )
    session = get_session(password)

    if args.status:
        cmd_status()
    if args.deploy:
        cmd_deploy(session)
    if args.restart:
        cmd_restart(session, args.device, args.model, args.bind_host)


if __name__ == "__main__":
    main()
