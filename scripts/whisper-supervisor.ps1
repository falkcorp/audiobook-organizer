# file: scripts/whisper-supervisor.ps1
# version: 2.0.0
# guid: f2fb096e-b2fc-40e4-90d0-01c3501a6296
# last-edited: 2026-11-17

<#
.SYNOPSIS
    Supervises the faster-whisper transcription server on a Windows machine
    (GPU or CPU), auto-restarting it on crash/exit while allowing deliberate
    stops for applying changes (the "claude-loop" pattern).

.DESCRIPTION
    Runs `uv run whisper_server.py ...` in a loop. When the process exits:
      * If the STOP SENTINEL file exists -> deliberate stop; the supervisor exits.
      * If the process exited with $StopExitCode -> deliberate stop; supervisor exits.
      * Otherwise -> treated as a crash; logs it and restarts after a delay that
        doubles with each crash in a row (RestartDelaySec up to MaxBackoffSec) and
        starts over once the server has stayed up for HealthyRunSec.

    The server runs on a CUDA GPU or on the CPU. -Device auto (the default) lets
    the server use CUDA when it finds a working device and otherwise fall back to
    the CPU with one log line; -Device cpu never touches CUDA. Everything else
    (model, threads, workers) has a server-side default that suits the device, so
    leave -Model, -ComputeType and -CpuThreads unset unless you mean to change them.

    whisper_server.py is NOT a single file any more: it imports
    whisper_path_pipeline.py and whisper_machine_id.py from its own directory.
    The three must be deployed together; the supervisor refuses to start without
    them (a missing sibling is a crash-loop otherwise).

    To apply a new build (or restart cleanly):
      1. Create the sentinel:   New-Item (Join-Path $env:USERPROFILE whisper-stop.flag)
      2. Stop the running server (Ctrl-C in its window, or Stop-Process).
      3. The supervisor sees the sentinel, exits cleanly.
      4. Replace the three .py files, delete the sentinel, re-run this script.

    All lifecycle events are appended to $LogPath with timestamps so the Linux
    side (or a human) can tail/parse it.

.PARAMETER ScriptPath
    Path to whisper_server.py; its two sibling modules must be in the same folder.
    Default: whisper_server.py in your user profile folder.

.PARAMETER Device
    auto | cpu | cuda (server --device). Default: auto. Use cpu on a machine whose
    GPU is gone or unreliable: it never probes CUDA, so there is nothing to fail.

.PARAMETER Model
    Whisper model name. Default: unset, so the server's own default applies (base.en).
    On a GPU, pass large-v2 (the previous default of this script) for quality.
    On a CPU, base.en is the realistic size; small.en only if the machine keeps up.

.PARAMETER Port
    TCP port (WHISPER_PORT). Default: 19847

.PARAMETER ComputeType
    Server --compute-type. Default: unset (float16 on cuda, int8 on cpu). Do not
    set float16 together with -Device cpu: the CPU has no float16 and the server
    refuses to start, which this script would then restart in a backoff loop.

.PARAMETER CpuThreads
    Threads per CPU decode (server --cpu-threads). 0 = the server's default
    (logical cores minus two, at most 8).

.PARAMETER NumWorkers
    Decodes that may run at once (server --num-workers). Default: 1.

.PARAMETER PathMap
    WHISPER_PATH_MAP for the path pipeline: server-path=local-path pairs, comma
    separated. The LOCAL side is a drive path or a UNC share, e.g.
    "/lib/Gotham=\\arkham\audio\lib". Empty = leave WHISPER_PATH_MAP as the
    machine/user environment already has it.

.PARAMETER SentinelPath
    Stop sentinel file. When present, the supervisor exits instead of restarting.
    Default: whisper-stop.flag in your user profile folder.

.PARAMETER LogPath
    Lifecycle log. Default: whisper-supervisor.log in your user profile folder.

.PARAMETER RestartDelaySec
    First delay before restarting after a crash; doubles per crash in a row.
    Default: 5

.PARAMETER MaxBackoffSec
    Longest restart delay. Default: 300

.PARAMETER HealthyRunSec
    A run at least this long ends the crash streak (the next crash starts the
    backoff over). Default: 120

.PARAMETER MaxRestarts
    Crash-restart cap within $RestartWindowSec before giving up (prevents a hot
    crash-loop from pegging the machine). 0 = unlimited. Default: 20

.PARAMETER RestartWindowSec
    Sliding window for MaxRestarts. Default: 600 (10 min)

.EXAMPLE
    # CPU-only machine, UNC library, small defaults:
    powershell -ExecutionPolicy Bypass -File whisper-supervisor.ps1 -Device cpu `
        -PathMap "/lib/Gotham=\\arkham\audio\lib"

.EXAMPLE
    # GPU machine, big model:
    powershell -ExecutionPolicy Bypass -File whisper-supervisor.ps1 -Device cuda -Model large-v2

.EXAMPLE
    # Apply a new server build:
    New-Item (Join-Path $env:USERPROFILE whisper-stop.flag) ; Stop-Process -Name python -Force
    # ...replace the three .py files...
    Remove-Item (Join-Path $env:USERPROFILE whisper-stop.flag)
    powershell -ExecutionPolicy Bypass -File whisper-supervisor.ps1
#>

[CmdletBinding()]
param(
    [string]$ScriptPath      = (Join-Path $env:USERPROFILE "whisper_server.py"),
    [ValidateSet("auto", "cpu", "cuda")]
    [string]$Device          = "auto",
    [string]$Model           = "",
    [int]$Port               = 19847,
    [string]$ComputeType     = "",
    [int]$CpuThreads         = 0,
    [int]$NumWorkers         = 1,
    [string]$PathMap         = "",
    [string]$SentinelPath    = (Join-Path $env:USERPROFILE "whisper-stop.flag"),
    [string]$LogPath         = (Join-Path $env:USERPROFILE "whisper-supervisor.log"),
    [int]$RestartDelaySec    = 5,
    [int]$MaxBackoffSec      = 300,
    [int]$HealthyRunSec      = 120,
    [int]$StopExitCode       = 42,
    [int]$MaxRestarts        = 20,
    [int]$RestartWindowSec   = 600
)

$ErrorActionPreference = "Stop"

function Write-Log {
    param([string]$Level, [string]$Message)
    $ts = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss.fffzzz")
    $line = "$ts [$Level] $Message"
    # Machine-parseable: ISO-8601 timestamp, bracketed level, free-text message.
    Add-Content -Path $LogPath -Value $line
    Write-Host $line
}

# Restart-rate bookkeeping: keep timestamps of recent crash-restarts so we can
# trip the MaxRestarts circuit-breaker if the server is hot-looping.
$restartTimes = New-Object System.Collections.ArrayList
# Crashes in a row since the last healthy run: drives the exponential backoff.
$crashStreak = 0

Write-Log "INFO" "supervisor starting: script=$ScriptPath device=$Device model=$(if ($Model) { $Model } else { '(server default)' }) port=$Port compute=$(if ($ComputeType) { $ComputeType } else { '(server default)' }) sentinel=$SentinelPath"

# Resolve uv once. Prefer PATH; fall back to the standard per-user install dir.
$uv = (Get-Command uv -ErrorAction SilentlyContinue)
if ($uv) {
    $uvExe = $uv.Source
} elseif (Test-Path "$env:USERPROFILE\.local\bin\uv.exe") {
    $uvExe = "$env:USERPROFILE\.local\bin\uv.exe"
} else {
    Write-Log "FATAL" "uv not found on PATH or in %USERPROFILE%\.local\bin. Install: https://docs.astral.sh/uv/"
    exit 1
}
Write-Log "INFO" "using uv at $uvExe"

if (-not (Test-Path $ScriptPath)) {
    Write-Log "FATAL" "whisper_server.py not found at $ScriptPath"
    exit 1
}

# whisper_server.py imports two sibling modules. Missing one is not a crash to
# retry: every restart would fail the same way, so stop here with the reason.
$scriptDir = Split-Path -Parent (Resolve-Path $ScriptPath)
foreach ($module in @("whisper_path_pipeline.py", "whisper_machine_id.py")) {
    if (-not (Test-Path (Join-Path $scriptDir $module))) {
        Write-Log "FATAL" "$module is missing next to $ScriptPath. whisper_server.py, whisper_path_pipeline.py and whisper_machine_id.py must be deployed together."
        exit 1
    }
}

# The path pipeline cuts clips with ffmpeg. Without it /health still answers but
# every path job fails worker_misconfigured, so say so now rather than leave it
# to the first job. Not fatal: the upload endpoints do not need it.
if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) {
    Write-Log "WARN" "ffmpeg is not on PATH: path jobs will fail until it is installed (winget install Gyan.FFmpeg)"
}

# A stale sentinel left over from a previous deliberate stop would make the
# supervisor exit immediately. Clear it at startup so a fresh launch always runs.
if (Test-Path $SentinelPath) {
    Write-Log "WARN" "clearing stale stop-sentinel at startup: $SentinelPath"
    Remove-Item $SentinelPath -Force
}

# Build the server's argument list once. Quoted by hand: Start-Process joins an
# -ArgumentList array with spaces and does not quote elements, so a script path
# under a profile folder with a space would otherwise split into two arguments.
$serverArgs = @("run", "`"$ScriptPath`"")
if ($Model) { $serverArgs += @("--model", $Model) }
$serverArgs += @("--device", $Device)
if ($ComputeType) { $serverArgs += @("--compute-type", $ComputeType) }
if ($CpuThreads -gt 0) { $serverArgs += @("--cpu-threads", "$CpuThreads") }
$serverArgs += @("--num-workers", "$NumWorkers")

while ($true) {
    # Honor a sentinel created between iterations (deliberate stop).
    if (Test-Path $SentinelPath) {
        Write-Log "INFO" "stop-sentinel present; exiting supervisor (deliberate stop)"
        break
    }

    $env:WHISPER_PORT = "$Port"
    if ($PathMap) { $env:WHISPER_PATH_MAP = $PathMap }

    Write-Log "INFO" "launching whisper server (uv $($serverArgs -join ' '))"
    $startedAt = Get-Date

    # Start in this console so the user can Ctrl-C; capture the exit code.
    $proc = Start-Process -FilePath $uvExe `
        -ArgumentList $serverArgs `
        -NoNewWindow -PassThru -Wait
    $exitCode = $proc.ExitCode
    $ranFor = [int]((Get-Date) - $startedAt).TotalSeconds

    Write-Log "INFO" "whisper server exited: code=$exitCode ran_for_sec=$ranFor"

    # Deliberate stop paths: sentinel appeared, or the agreed clean-exit code.
    if (Test-Path $SentinelPath) {
        Write-Log "INFO" "stop-sentinel present after exit; not restarting (deliberate stop)"
        break
    }
    if ($exitCode -eq $StopExitCode) {
        Write-Log "INFO" "server exited with stop-code $StopExitCode; not restarting (deliberate stop)"
        break
    }

    # A run that stayed up this long was healthy, so what ended it is a fresh
    # problem, not a continuation of a crash loop: start the backoff over.
    if ($ranFor -ge $HealthyRunSec) { $crashStreak = 0 }
    $crashStreak++

    # Crash path: record the restart and trip the breaker if hot-looping.
    if ($MaxRestarts -gt 0) {
        $now = Get-Date
        [void]$restartTimes.Add($now)
        # Drop timestamps outside the sliding window.
        $cutoff = $now.AddSeconds(-$RestartWindowSec)
        $recent = @($restartTimes | Where-Object { $_ -ge $cutoff })
        $restartTimes.Clear()
        foreach ($t in $recent) { [void]$restartTimes.Add($t) }
        if ($recent.Count -ge $MaxRestarts) {
            Write-Log "FATAL" "crash-loop breaker: $($recent.Count) restarts in ${RestartWindowSec}s (>= $MaxRestarts). Giving up. Investigate, then re-run the supervisor."
            exit 1
        }
        Write-Log "WARN" "crash restart $($recent.Count)/$MaxRestarts within ${RestartWindowSec}s window"
    }

    # Exponential backoff: RestartDelaySec, x2 per crash in a row, capped. The
    # exponent is capped too so the multiplication cannot overflow on a long streak.
    $delay = [int][Math]::Min($MaxBackoffSec, $RestartDelaySec * [Math]::Pow(2, [Math]::Min($crashStreak - 1, 16)))
    Write-Log "WARN" "restarting in ${delay}s (crash $crashStreak in a row)..."
    Start-Sleep -Seconds $delay
}

Write-Log "INFO" "supervisor stopped."
