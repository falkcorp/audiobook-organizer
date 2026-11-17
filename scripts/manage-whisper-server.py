# file: scripts/manage-whisper-server.py
# version: 1.2.0
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
# IP, e.g. <windows-gpu-host>) — there is no internal-network default — and the
# Windows account via WHISPER_WIN_USER. --deploy copies whisper_server.py AND the
# two modules it imports (whisper_path_pipeline.py, whisper_machine_id.py) into
# that account's profile folder: the three go together or the server will not start.
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
WIN_USER = os.environ.get("WHISPER_WIN_USER")
if not WIN_USER:
    print("ERROR: WHISPER_WIN_USER environment variable is required (the Windows account name)")
    sys.exit(1)
REMOTE_DIR = rf"C:\Users\{WIN_USER}"
REMOTE_PATH = rf"{REMOTE_DIR}\whisper_server.py"
# The server imports these from its own folder; they are deployed with it.
SERVER_FILES = ("whisper_server.py", "whisper_path_pipeline.py", "whisper_machine_id.py")
MODEL = "base.en"
HEALTH_URL = f"http://{COMPUTER}:8000/health"


def get_session(password: str) -> winrm.Session:
    return winrm.Session(
        f"http://{COMPUTER}:5985/wsman",
        auth=(WIN_USER, password),
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
                f"  RUNNING — model={data.get('model')} batch_pipeline={data.get('batch_pipeline')}"
            )
    except urllib.error.URLError:
        print("  NOT RESPONDING — server may be stopped")


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
        remote_path = rf"{REMOTE_DIR}\{name}"
        print(f"==> Deploying {local_path} → {COMPUTER}:{remote_path}")
        with open(local_path, "rb") as f:
            encoded = base64.b64encode(f.read()).decode()

        # Decode on Windows and write to disk.
        run_ps(
            session,
            f"""
$bytes = [Convert]::FromBase64String('{encoded}')
[System.IO.File]::WriteAllBytes('{remote_path}', $bytes)
Write-Host "Written $($bytes.Length) bytes to {remote_path}"
""",
        )
    print("  Deployed OK")


def cmd_restart(session: winrm.Session, device: str):
    print(f"==> Restarting Whisper server on {COMPUTER} (model={MODEL}, device={device})")

    # Kill any process listening on port 8000.
    kill_script = r"""
$pids = (netstat -ano | Select-String ':8000\s') -replace '.*\s(\d+)$','$1' |
        Sort-Object -Unique
foreach ($p in $pids) {
    if ($p -match '^\d+$') {
        Stop-Process -Id $p -Force -ErrorAction SilentlyContinue
        Write-Host "Killed PID $p"
    }
}
Start-Sleep -Seconds 1
"""
    out = run_ps(session, kill_script)
    if out:
        print(f"  {out}")

    # Locate uv and launch the server in the background.
    launch_script = rf"""
$uvExe = (Get-Command uv -ErrorAction SilentlyContinue)?.Source
if (-not $uvExe) {{ $uvExe = "$env:USERPROFILE\.local\bin\uv.exe" }}
if (-not (Test-Path $uvExe)) {{
    Write-Error "uv not found. Install: winget install --id astral-sh.uv"
    exit 1
}}
$log = "$env:USERPROFILE\whisper_server.log"
$p = Start-Process `
    -FilePath $uvExe `
    -ArgumentList "run", "{REMOTE_PATH}", "{MODEL}", "--device", "{device}" `
    -RedirectStandardOutput $log `
    -RedirectStandardError  "$log.err" `
    -WindowStyle Hidden `
    -PassThru
Write-Host "Started PID $($p.Id)"
"""
    out = run_ps(session, launch_script)
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
        help="Copy whisper_server.py and its two sibling modules to Windows",
    )
    parser.add_argument("--restart", action="store_true", help="Kill + relaunch the server")
    parser.add_argument("--status", action="store_true", help="Check /health (no auth needed)")
    parser.add_argument(
        "--device",
        choices=("auto", "cpu", "cuda"),
        default="auto",
        help="inference device for --restart (default: auto = CUDA if it loads, else CPU)",
    )
    args = parser.parse_args()

    if not any([args.deploy, args.restart, args.status]):
        args.status = True

    if args.status and not args.deploy and not args.restart:
        cmd_status()
        return

    # Auth only needed for deploy/restart.
    password = os.environ.get("WHISPER_WIN_PASSWORD") or getpass.getpass(
        f"Windows password for {WIN_USER}@{COMPUTER}: "
    )
    session = get_session(password)

    if args.status:
        cmd_status()
    if args.deploy:
        cmd_deploy(session)
    if args.restart:
        cmd_restart(session, args.device)


if __name__ == "__main__":
    main()
