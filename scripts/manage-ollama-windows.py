# file: scripts/manage-ollama-windows.py
# version: 1.0.1
# guid: c4d6e8f0-a2b4-4c6d-8e0f-2a4b6c8d0e2f
# last-edited: 2026-07-03
#
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
#
# Manages Ollama on the Windows GPU machine over plain SSH (NOT WinRM like
# manage-whisper-server.py — see docs/status/2026-07-02-local-cutover-and-matching.md).
# PowerShell scripts shipped over that SSH path via scp mis-parse; every
# remote command here is base64-encoded (UTF-16LE, as PowerShell requires
# for -EncodedCommand) and invoked with
# `ssh windows-gpu powershell -NoProfile -EncodedCommand <blob>`.
#
# Recreates the Windows Ollama keepalive that previously existed only as
# scratchpad scripts (setup-ollama.ps1, start-ollama.ps1) — including
# registering the "OllamaServe" scheduled task itself, which is the actual
# keepalive mechanism. The task is bound to an interactive logon session
# because `ollama serve` needs GPU access that is not available in a
# headless/service context; a logoff, reboot, or Windows Update can still
# kill it before the next interactive logon. That residual single point of
# failure is intentionally NOT fixed here — see docs/system/deploy-and-gpu-ops.md.
#
# Prerequisites:
#   - A `windows-gpu` Host alias in your own ~/.ssh/config (not committed —
#     machine-local, same as scripts/setup-ssh-from-mac.sh documents).
#   - Key-based SSH auth already working: `ssh windows-gpu hostname`
#
# Usage:
#   uv run scripts/manage-ollama-windows.py --status
#   uv run scripts/manage-ollama-windows.py --setup          # install + pull models + register task
#   uv run scripts/manage-ollama-windows.py --install-task   # register OllamaServe only
#   uv run scripts/manage-ollama-windows.py --restart        # kill + relaunch ollama serve

import argparse
import base64
import subprocess
import sys
import urllib.error
import urllib.request

HOST = "windows-gpu"
IP = "192.168.0.20"
PORT = 11434
MODELS = ("bge-m3", "qwen2.5:7b-instruct")

# Written to the remote box by --setup / --install-task; this is the body
# the "OllamaServe" scheduled task actually runs on logon, and what
# --restart re-runs interactively.
RESTART_SCRIPT = r"""
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'
$exe = Join-Path $env:LOCALAPPDATA 'Programs\Ollama\ollama.exe'

Get-Process ollama,'ollama app' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $exe
$psi.Arguments = 'serve'
$psi.EnvironmentVariables['OLLAMA_HOST'] = '0.0.0.0:11434'
$psi.UseShellExecute = $false
$psi.RedirectStandardError = $true
$psi.RedirectStandardOutput = $true
$p = [System.Diagnostics.Process]::Start($psi)
Write-Host "started ollama serve pid=$($p.Id)"
Start-Sleep -Seconds 8

Write-Host "=== netstat 11434 ==="
netstat -ano | Select-String ':11434' | ForEach-Object { $_.Line }

Write-Host "=== local api probe ==="
try { $t = Invoke-RestMethod 'http://127.0.0.1:11434/api/version' -TimeoutSec 5; Write-Host "api version: $($t.version)" } catch { Write-Host "api down: $($_.Exception.Message)" }
"""

# Installs Ollama, sets OLLAMA_HOST, opens the firewall, writes
# ollama-start.ps1 (the RESTART_SCRIPT above) to the remote box, registers
# the OllamaServe scheduled task bound to interactive logon, and pulls
# models. This is the part that actually closes OPS-2: without registering
# the task, the keepalive mechanism itself remains undocumented/unreproducible.
SETUP_SCRIPT_TEMPLATE = r"""
$ErrorActionPreference = 'Continue'

Write-Host "=== 0. Write ollama-start.ps1 ==="
$scriptPath = "$env:USERPROFILE\ollama-start.ps1"
$bytes = [Convert]::FromBase64String('__RESTART_SCRIPT_B64__')
[System.IO.File]::WriteAllBytes($scriptPath, $bytes)
Write-Host "wrote $($bytes.Length) bytes to $scriptPath"

Write-Host "=== 1. Ollama install check ==="
$exe = Get-Command ollama -ErrorAction SilentlyContinue
if (-not $exe) {
    Write-Host "installing Ollama via winget..."
    winget install --id Ollama.Ollama -e --silent --accept-source-agreements --accept-package-agreements
    $env:Path += ";$env:LOCALAPPDATA\Programs\Ollama"
    $exe = Get-Command ollama -ErrorAction SilentlyContinue
}
if ($exe) { Write-Host "ollama: $($exe.Source)" } else { Write-Host "ollama: STILL NOT FOUND (winget may have failed)" }

Write-Host "=== 2. OLLAMA_HOST=0.0.0.0:11434 (User + Machine) ==="
[Environment]::SetEnvironmentVariable('OLLAMA_HOST','0.0.0.0:11434','User')
try { [Environment]::SetEnvironmentVariable('OLLAMA_HOST','0.0.0.0:11434','Machine'); Write-Host "set Machine scope" } catch { Write-Host "Machine scope failed (needs admin); User scope set" }

Write-Host "=== 3. Firewall rule for 11434 ==="
try {
    if (-not (Get-NetFirewallRule -DisplayName 'Ollama 11434' -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -DisplayName 'Ollama 11434' -Direction Inbound -LocalPort 11434 -Protocol TCP -Action Allow -ErrorAction Stop | Out-Null
        Write-Host "firewall rule added"
    } else { Write-Host "firewall rule already present" }
} catch { Write-Host "firewall rule failed (needs admin): $($_.Exception.Message)" }

Write-Host "=== 4. Register OllamaServe scheduled task (interactive logon, current user) ==="
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -WindowStyle Hidden -File `"$scriptPath`""
$trigger = New-ScheduledTaskTrigger -AtLogOn
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
try {
    Unregister-ScheduledTask -TaskName 'OllamaServe' -Confirm:$false -ErrorAction SilentlyContinue
    Register-ScheduledTask -TaskName 'OllamaServe' -Action $action -Trigger $trigger -Principal $principal -Settings $settings | Out-Null
    Write-Host "OllamaServe scheduled task registered (AtLogOn, interactive)"
} catch { Write-Host "scheduled task registration failed: $($_.Exception.Message)" }

Write-Host "=== 5. pull models ==="
& ollama pull bge-m3
& ollama pull qwen2.5:7b-instruct

Write-Host "=== 6. status ==="
try {
    $t = Invoke-RestMethod -Uri 'http://127.0.0.1:11434/api/tags' -TimeoutSec 6
    Write-Host ("api up. models: " + (($t.models | ForEach-Object { $_.name }) -join ', '))
} catch { Write-Host "api 11434 not responding: $($_.Exception.Message)" }
"""

# Registers the OllamaServe task only, without the install/firewall/pull
# steps — useful if Ollama is already installed but the scheduled task
# needs to be (re)created.
INSTALL_TASK_SCRIPT_TEMPLATE = r"""
$ErrorActionPreference = 'Continue'

Write-Host "=== Write ollama-start.ps1 ==="
$scriptPath = "$env:USERPROFILE\ollama-start.ps1"
$bytes = [Convert]::FromBase64String('__RESTART_SCRIPT_B64__')
[System.IO.File]::WriteAllBytes($scriptPath, $bytes)
Write-Host "wrote $($bytes.Length) bytes to $scriptPath"

Write-Host "=== Register OllamaServe scheduled task (interactive logon, current user) ==="
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -WindowStyle Hidden -File `"$scriptPath`""
$trigger = New-ScheduledTaskTrigger -AtLogOn
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
try {
    Unregister-ScheduledTask -TaskName 'OllamaServe' -Confirm:$false -ErrorAction SilentlyContinue
    Register-ScheduledTask -TaskName 'OllamaServe' -Action $action -Trigger $trigger -Principal $principal -Settings $settings | Out-Null
    Write-Host "OllamaServe scheduled task registered (AtLogOn, interactive)"
} catch { Write-Host "scheduled task registration failed: $($_.Exception.Message)" }
"""


def encode_ps(script: str) -> str:
    """Base64-encode a PowerShell script as UTF-16LE, per -EncodedCommand's requirement."""
    return base64.b64encode(script.encode("utf-16-le")).decode()


def run_remote_ps(script: str) -> subprocess.CompletedProcess:
    encoded = encode_ps(script)
    return subprocess.run(
        ["ssh", HOST, "powershell", "-NoProfile", "-EncodedCommand", encoded],
        capture_output=True,
        text=True,
        check=False,
    )


def print_result(result: subprocess.CompletedProcess) -> None:
    if result.stdout:
        print(result.stdout.rstrip())
    if result.stderr:
        print(result.stderr.rstrip(), file=sys.stderr)


def cmd_status() -> int:
    """Probe http://192.168.0.20:11434/api/tags directly (no SSH needed) and
    report which models are loaded."""
    url = f"http://{IP}:{PORT}/api/tags"
    print(f"==> Checking {url}")
    try:
        with urllib.request.urlopen(url, timeout=6) as r:
            import json

            data = json.loads(r.read())
            names = [m.get("name", "?") for m in data.get("models", [])]
            print(f"  api up. models: {', '.join(names) if names else '(none)'}")
            missing = [m for m in MODELS if not any(m in n for n in names)]
            if missing:
                print(f"  WARNING: expected model(s) not found: {', '.join(missing)}")
                return 1
            print("  OK — bge-m3 and qwen2.5:7b-instruct both present")
            return 0
    except (urllib.error.URLError, OSError) as e:
        print(f"  NOT RESPONDING: {e}")
        return 1


def cmd_setup() -> int:
    print(f"==> Running --setup on {HOST} ({IP}) over SSH -EncodedCommand")
    script = SETUP_SCRIPT_TEMPLATE.replace("__RESTART_SCRIPT_B64__", encode_ps(RESTART_SCRIPT))
    result = run_remote_ps(script)
    print_result(result)
    if result.returncode != 0:
        print(f"  ssh exited {result.returncode}", file=sys.stderr)
    return result.returncode


def cmd_install_task() -> int:
    print(f"==> Registering OllamaServe scheduled task on {HOST} ({IP})")
    script = INSTALL_TASK_SCRIPT_TEMPLATE.replace(
        "__RESTART_SCRIPT_B64__", encode_ps(RESTART_SCRIPT)
    )
    result = run_remote_ps(script)
    print_result(result)
    if result.returncode != 0:
        print(f"  ssh exited {result.returncode}", file=sys.stderr)
    return result.returncode


def cmd_restart() -> int:
    print(f"==> Restarting ollama serve on {HOST} ({IP})")
    result = run_remote_ps(RESTART_SCRIPT)
    print_result(result)
    if result.returncode != 0:
        print(f"  ssh exited {result.returncode}", file=sys.stderr)
    return result.returncode


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Manage Ollama on the Windows GPU machine over ssh windows-gpu"
    )
    parser.add_argument("--status", action="store_true", help="Probe /api/tags, report loaded models")
    parser.add_argument(
        "--setup",
        action="store_true",
        help="Idempotent setup: install Ollama, set OLLAMA_HOST, open firewall, "
        "register the OllamaServe scheduled task, pull models",
    )
    parser.add_argument(
        "--install-task",
        action="store_true",
        help="Register the OllamaServe scheduled task only (also run by --setup)",
    )
    parser.add_argument(
        "--restart",
        action="store_true",
        help="Kill and relaunch ollama serve (0.0.0.0:11434), verify via /api/version",
    )
    args = parser.parse_args()

    if not any([args.status, args.setup, args.install_task, args.restart]):
        args.status = True

    rc = 0
    if args.setup:
        rc |= cmd_setup()
    if args.install_task:
        rc |= cmd_install_task()
    if args.restart:
        rc |= cmd_restart()
    if args.status:
        rc |= cmd_status()
    return rc


if __name__ == "__main__":
    sys.exit(main())
