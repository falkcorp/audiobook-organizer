# file: scripts/manage-whisper-server.ps1
# version: 1.0.0
# guid: e6f7a8b9-c0d1-2345-efab-567890123456
# last-edited: 2026-06-27
#
# Run from macOS or Linux (requires pwsh — brew install powershell).
# Manages the Whisper transcription server on the Windows GPU machine
# via PowerShell Remoting (WinRM). No SSH needed.
#
# Prerequisites:
#   1. setup-winrm-windows.ps1 has been run on the Windows machine
#   2. pwsh is installed locally: brew install powershell
#
# Usage:
#   pwsh scripts/manage-whisper-server.ps1 -Deploy            # copy whisper_server.py to Windows
#   pwsh scripts/manage-whisper-server.ps1 -Restart           # kill + relaunch server
#   pwsh scripts/manage-whisper-server.ps1 -Deploy -Restart   # both (most common)
#   pwsh scripts/manage-whisper-server.ps1 -Status            # check if server is up
#
# Credentials are prompted once and can be saved locally:
#   pwsh scripts/manage-whisper-server.ps1 -SaveCreds         # encrypt + save to ~/.windows-gpu.cred
# On subsequent runs the saved creds are used automatically.

param(
    [string] $ComputerName = "172.16.3.22",
    [string] $WindowsUser  = "jdfalk",
    [string] $CredsFile    = "$HOME/.windows-gpu.cred",
    [string] $RemotePath   = "C:\Users\jdfalk\whisper_server.py",
    [string] $Model        = "base.en",
    [switch] $Deploy,
    [switch] $Restart,
    [switch] $Status,
    [switch] $SaveCreds
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# ── Credentials ──────────────────────────────────────────────────────────────
function Get-WindowsCred {
    param([string]$CredsFile, [string]$User, [string]$Computer)
    if (Test-Path $CredsFile) {
        Write-Host "Using saved credentials from $CredsFile" -ForegroundColor DarkGray
        return Import-Clixml $CredsFile
    }
    Write-Host "Enter password for $User@$Computer" -ForegroundColor Cyan
    return Get-Credential -UserName $User -Message "Windows GPU machine ($Computer)"
}

$cred = Get-WindowsCred -CredsFile $CredsFile -User $WindowsUser -Computer $ComputerName

if ($SaveCreds) {
    $cred | Export-Clixml $CredsFile
    Write-Host "Credentials saved to $CredsFile (encrypted, current user only)" -ForegroundColor Green
}

$psOpts = @{
    ComputerName = $ComputerName
    Credential   = $cred
    # HTTP (not HTTPS) is fine on a trusted LAN; WinRM encrypts the payload.
    SessionOption = New-PSSessionOption -SkipCACheck -SkipCNCheck
}

# ── Status ────────────────────────────────────────────────────────────────────
if ($Status -or (-not $Deploy -and -not $Restart -and -not $SaveCreds)) {
    Write-Host "`n==> Checking Whisper server health at http://${ComputerName}:8000/health" -ForegroundColor Cyan
    try {
        $resp = Invoke-RestMethod -Uri "http://${ComputerName}:8000/health" -TimeoutSec 5
        Write-Host "    RUNNING — model=$($resp.model) batch_pipeline=$($resp.batch_pipeline)" -ForegroundColor Green
    } catch {
        Write-Host "    NOT RESPONDING — server may be stopped" -ForegroundColor Yellow
    }
    # Also show whether the python process is running on the remote machine.
    Invoke-Command @psOpts -ScriptBlock {
        $procs = Get-Process -Name python, pythonw, uvicorn -ErrorAction SilentlyContinue
        if ($procs) {
            $procs | Select-Object Name, Id, CPU, StartTime | Format-Table -AutoSize
        } else {
            Write-Host "    No python/uvicorn process found on remote machine"
        }
    }
}

# ── Deploy ────────────────────────────────────────────────────────────────────
if ($Deploy) {
    $localScript = Join-Path $PSScriptRoot "whisper_server.py"
    if (-not (Test-Path $localScript)) {
        # Fall back to repo root.
        $localScript = Join-Path (Split-Path $PSScriptRoot) "scripts/whisper_server.py"
    }
    if (-not (Test-Path $localScript)) {
        Write-Error "Cannot find whisper_server.py. Run from the repo root or scripts/ directory."
    }
    Write-Host "`n==> Copying $localScript → ${ComputerName}:$RemotePath" -ForegroundColor Cyan
    $session = New-PSSession @psOpts
    try {
        Copy-Item -Path $localScript -Destination $RemotePath -ToSession $session -Force
        Write-Host "    Copied OK" -ForegroundColor Green
    } finally {
        Remove-PSSession $session
    }
}

# ── Restart ───────────────────────────────────────────────────────────────────
if ($Restart) {
    Write-Host "`n==> Restarting Whisper server on $ComputerName (model=$Model)" -ForegroundColor Cyan
    Invoke-Command @psOpts -ScriptBlock {
        param($remotePath, $model)
        # Kill any running python/uvicorn that's serving on port 8000.
        $listeners = netstat -ano | Select-String ":8000 " | ForEach-Object {
            ($_ -split "\s+")[-1]
        } | Sort-Object -Unique
        foreach ($pid in $listeners) {
            if ($pid -match "^\d+$") {
                Stop-Process -Id $pid -Force -ErrorAction SilentlyContinue
                Write-Host "Killed PID $pid"
            }
        }
        Start-Sleep -Seconds 1

        # Launch via uv run so dependencies are auto-managed.
        $uvExe = (Get-Command uv -ErrorAction SilentlyContinue)?.Source
        if (-not $uvExe) { $uvExe = "$env:USERPROFILE\.local\bin\uv.exe" }
        if (-not (Test-Path $uvExe)) {
            Write-Error "uv not found. Install: winget install --id astral-sh.uv"
        }
        $logFile = "$env:USERPROFILE\whisper_server.log"
        $process = Start-Process `
            -FilePath $uvExe `
            -ArgumentList "run", $remotePath, $model `
            -RedirectStandardOutput $logFile `
            -RedirectStandardError  "$logFile.err" `
            -WindowStyle Hidden `
            -PassThru
        Write-Host "Started PID $($process.Id) — logs at $logFile"
    } -ArgumentList $RemotePath, $Model

    # Give uvicorn ~10s to load the model, then check health.
    Write-Host "    Waiting 10s for model to load..." -ForegroundColor DarkGray
    Start-Sleep -Seconds 10
    try {
        $resp = Invoke-RestMethod -Uri "http://${ComputerName}:8000/health" -TimeoutSec 5
        Write-Host "    READY — model=$($resp.model) batch_pipeline=$($resp.batch_pipeline)" -ForegroundColor Green
    } catch {
        Write-Host "    Still loading (base model takes ~30s on first run). Check manually:" -ForegroundColor Yellow
        Write-Host "    curl http://${ComputerName}:8000/health"
    }
}
