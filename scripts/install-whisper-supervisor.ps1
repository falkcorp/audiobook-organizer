# file: scripts/install-whisper-supervisor.ps1
# version: 3.0.1
# guid: 445a38c7-e60e-4e07-a441-80bba4e8b034
# last-edited: 2026-11-17

<#
.SYNOPSIS
    Installs whisper-supervisor.ps1 as a durable Scheduled Task on a Windows
    machine (GPU or CPU), copies the Whisper server files next to it, stops any
    manually-started whisper_server.py, and starts the supervisor (which
    relaunches whisper under supervision).

.DESCRIPTION
    Run this ONCE from a normal (non-elevated) PowerShell. After this, the
    Whisper server is supervised: it auto-restarts on crash, and you can apply
    changes cleanly via the stop-sentinel (see "Applying changes" below).

    The scheduled task runs at your NORMAL user token (RunLevel Limited) — not
    elevated — so the user-writable script path is not a privilege-escalation
    vector. Whisper needs no admin rights (it is launched unelevated manually).

    Run from your repo clone so it picks up the committed supervisor script:
      cd <your clone of audiobook-organizer>
      git pull
      powershell -ExecutionPolicy Bypass -File scripts\install-whisper-supervisor.ps1

    CPU-only machine (no GPU, or a GPU you do not trust), library on a share:
      powershell -ExecutionPolicy Bypass -File scripts\install-whisper-supervisor.ps1 `
          -Device cpu -PathMap "/lib/Gotham=\\arkham\audio\lib"

.PARAMETER RepoScript
    Path to the committed supervisor script. Default: .\scripts\whisper-supervisor.ps1
    (relative to where you run this from).

.PARAMETER InstallDir
    Where the supervisor AND whisper_server.py, whisper_path_pipeline.py,
    whisper_machine_id.py and whisper_decode_child.py are copied and run from.
    Default: your user profile folder.

.PARAMETER Device, Model, ComputeType, CpuThreads, NumWorkers, PathMap, Port, BindHost
    Passed to the supervisor in the scheduled task's command line (see
    whisper-supervisor.ps1). Unset means the supervisor's and server's defaults:
    -Device auto, the model the device calls for (large-v2 on a GPU, base.en on a
    CPU), the device's compute type, and -BindHost 127.0.0.1 (loopback only).
    A worker that must be reached over the LAN needs -BindHost 0.0.0.0 AND a
    firewall rule for the server only: the worker has no authentication and the
    path routes return transcripts of files under -PathMap.

.PARAMETER TaskName
    Scheduled Task name. Default: WhisperSupervisor

.NOTES
    Applying a new whisper_server.py (or restarting cleanly):
      New-Item <InstallDir>\whisper-stop.flag
      Get-CimInstance Win32_Process -Filter "name='python.exe'" |
        Where-Object { $_.CommandLine -like '*whisper_server.py*' } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
      # ...replace whisper_server.py...
      Remove-Item <InstallDir>\whisper-stop.flag
      Start-ScheduledTask -TaskName WhisperSupervisor

    Stop supervision entirely:
      Stop-ScheduledTask -TaskName WhisperSupervisor
      Unregister-ScheduledTask -TaskName WhisperSupervisor -Confirm:$false

    Watch it:
      Get-Content <InstallDir>\whisper-supervisor.log -Wait
#>

[CmdletBinding()]
param(
    [string]$RepoScript  = ".\scripts\whisper-supervisor.ps1",
    [string]$InstallDir  = $env:USERPROFILE,
    [string]$TaskName    = "WhisperSupervisor",
    [ValidateSet("auto", "cpu", "cuda")]
    [string]$Device      = "auto",
    [string]$Model       = "",
    [string]$ComputeType = "",
    [int]$CpuThreads     = 0,
    [int]$NumWorkers     = 1,
    [string]$PathMap     = "",
    [string]$BindHost    = "127.0.0.1",
    [int]$Port           = 19847
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$InstallPath = Join-Path $InstallDir "whisper-supervisor.ps1"
$SentinelPath = Join-Path $InstallDir "whisper-stop.flag"
$LogPath = Join-Path $InstallDir "whisper-supervisor.log"
$ServerPath = Join-Path $InstallDir "whisper_server.py"

function Info($m) { Write-Host "[install] $m" }

# One argument for the task's command line, quoted so a value with a space stays
# one argument. Backslashes just before the closing quote are doubled: a value
# ending in one (a drive root "X:\") would otherwise read as \" and swallow the
# quote. A value with a quote in it cannot be quoted safely and is refused.
function Quote-Arg([string]$Value) {
    if ($Value.Contains('"')) { throw "a value with a double quote in it cannot be passed: $Value" }
    return '"' + ($Value -replace '(\\+)$', '$1$1') + '"'
}

# No elevation required. The task runs at the user's NORMAL token (see step 5):
# whisper_server.py needs no admin rights (it's launched manually unelevated),
# and registering a task for your own account does not require admin. We
# deliberately do NOT use -RunLevel Highest — that would run a script from a
# user-writable path (under the user profile) elevated, which is a privilege-
# escalation vector (any unprivileged write to the script = elevated code at
# next logon). Normal-level + user-writable path crosses no privilege boundary.

if (-not (Test-Path $RepoScript)) {
    Write-Error "Supervisor script not found at $RepoScript. Run from your repo clone after 'git pull'."
    exit 1
}

# 1. Copy the committed supervisor to the install path, and the server with its
# three sibling modules (whisper_server.py imports them from its own folder, so
# all four or none).
$repoDir = Split-Path -Parent (Resolve-Path $RepoScript)
$serverFiles = @("whisper_server.py", "whisper_path_pipeline.py", "whisper_machine_id.py", "whisper_decode_child.py")
foreach ($f in $serverFiles) {
    if (-not (Test-Path (Join-Path $repoDir $f))) {
        Write-Error "$f not found next to $RepoScript; the server cannot run without all of: $($serverFiles -join ', ')."
        exit 1
    }
}
Copy-Item $RepoScript $InstallPath -Force
Info "copied supervisor -> $InstallPath"
foreach ($f in $serverFiles) {
    Copy-Item (Join-Path $repoDir $f) (Join-Path $InstallDir $f) -Force
    Info "copied $f -> $InstallDir"
}

# 2. Verify it parses before we rely on it.
$errs = $null
[System.Management.Automation.Language.Parser]::ParseFile($InstallPath, [ref]$null, [ref]$errs) | Out-Null
if ($errs.Count -gt 0) {
    Write-Error "Supervisor script has parse errors; aborting. $($errs | ForEach-Object { $_.Message })"
    exit 1
}
Info "supervisor parse OK"

# 3. Stop any manually-started whisper so the supervisor's instance can bind port $Port.
$whisper = Get-CimInstance Win32_Process -Filter "name='python.exe'" |
    Where-Object { $_.CommandLine -like '*whisper_server.py*' }
foreach ($w in $whisper) {
    Info "stopping existing whisper pid $($w.ProcessId)"
    Stop-Process -Id $w.ProcessId -Force -ErrorAction SilentlyContinue
}
Start-Sleep -Seconds 2

# 4. Clear any stale stop-sentinel.
Remove-Item $SentinelPath -Force -ErrorAction SilentlyContinue

# 5. Register the Scheduled Task (recreate if present). Runs at the user's
# NORMAL token (RunLevel Limited) — intentionally NOT elevated, so the
# user-writable script path is not a privilege-escalation vector.
#
# Use Unregister-ScheduledTask (not `schtasks /Delete`): with
# $ErrorActionPreference='Stop', the native schtasks command writes
# "ERROR: cannot find the file specified" to stderr on first run (no task yet),
# which PowerShell turns into a TERMINATING error that halts the installer
# before it registers anything. -ErrorAction SilentlyContinue makes the
# not-present case a clean no-op.
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue | Out-Null
# The supervisor's own settings ride in the task's command line. Every value is
# quoted so a path with a space stays one argument; unset options are left out
# so the supervisor's and the server's defaults apply.
$supArgs = @(
    "-ScriptPath $(Quote-Arg $ServerPath)",
    "-SentinelPath $(Quote-Arg $SentinelPath)",
    "-LogPath $(Quote-Arg $LogPath)",
    "-Device $Device",
    "-NumWorkers $NumWorkers",
    "-BindHost $(Quote-Arg $BindHost)",
    "-Port $Port"
)
if ($Model) { $supArgs += "-Model $(Quote-Arg $Model)" }
if ($ComputeType) { $supArgs += "-ComputeType $(Quote-Arg $ComputeType)" }
if ($CpuThreads -gt 0) { $supArgs += "-CpuThreads $CpuThreads" }
if ($PathMap) { $supArgs += "-PathMap $(Quote-Arg $PathMap)" }
$action = New-ScheduledTaskAction -Execute "powershell.exe" `
    -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File $(Quote-Arg $InstallPath) $($supArgs -join ' ')"
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Settings $settings -Principal $principal -Force | Out-Null
Info "registered scheduled task '$TaskName' (runs at logon + now, normal token)"

# 6. Start it now.
Start-ScheduledTask -TaskName $TaskName
Start-Sleep -Seconds 5
Info "task state: $((Get-ScheduledTask -TaskName $TaskName).State)"

# 7. Wait for whisper to come back up (model load can take ~30-60s).
Info "waiting for whisper /health on :$Port (model load can take up to ~90s; a first run also downloads the model)..."
$ok = $false
# The server answers on the address it is bound to; a wildcard bind answers on loopback.
if ($BindHost -in @("0.0.0.0", "")) { $healthHost = "127.0.0.1" }
elseif ($BindHost -eq "::") { $healthHost = "[::1]" }
elseif ($BindHost.Contains(":")) { $healthHost = "[$BindHost]" }
else { $healthHost = $BindHost }
for ($i = 0; $i -lt 30; $i++) {
    Start-Sleep -Seconds 3
    try {
        $h = (Invoke-WebRequest -UseBasicParsing -Uri "http://${healthHost}:$Port/health" -TimeoutSec 4).Content
        if ($h -match '"status"\s*:\s*"ok"') { Info "whisper healthy: $h"; $ok = $true; break }
    } catch { }
}
if (-not $ok) {
    Write-Warning "whisper did not report healthy yet. Check the log:"
    Write-Warning "  Get-Content `"$LogPath`" -Tail 40"
} else {
    Info "DONE. Supervisor installed and whisper is up."
    Info "Watch: Get-Content `"$LogPath`" -Wait"
}
