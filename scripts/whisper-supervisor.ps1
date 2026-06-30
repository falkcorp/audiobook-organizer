<#
.SYNOPSIS
    Supervises the faster-whisper transcription server on the Windows GPU box,
    auto-restarting it on crash/exit while allowing deliberate stops for applying
    changes (the "claude-loop" pattern).

.DESCRIPTION
    Runs `uv run whisper_server.py <model>` in a loop. When the process exits:
      * If the STOP SENTINEL file exists -> deliberate stop; the supervisor exits.
      * If the process exited with $StopExitCode -> deliberate stop; supervisor exits.
      * Otherwise -> treated as a crash; logs it and restarts after $RestartDelaySec.

    To apply a new whisper_server.py (or restart cleanly):
      1. Create the sentinel:   New-Item C:\Users\jdfal\whisper-stop.flag
      2. Stop the running server (Ctrl-C in its window, or Stop-Process).
      3. The supervisor sees the sentinel, exits cleanly.
      4. Pull/replace whisper_server.py, delete the sentinel, re-run this script.

    All lifecycle events are appended to $LogPath with timestamps so the Linux
    side (or a human) can tail/parse it.

.PARAMETER ScriptPath
    Path to whisper_server.py. Default: C:\Users\jdfal\whisper_server.py

.PARAMETER Model
    Whisper model name passed as argv[1]. Default: large-v2

.PARAMETER Port
    TCP port (WHISPER_PORT). Default: 19847

.PARAMETER ComputeType
    WHISPER_COMPUTE_TYPE. float16 for Turing+ (RTX), int8 for Pascal. Default: float16

.PARAMETER SentinelPath
    Stop sentinel file. When present, the supervisor exits instead of restarting.
    Default: C:\Users\jdfal\whisper-stop.flag

.PARAMETER LogPath
    Lifecycle log. Default: C:\Users\jdfal\whisper-supervisor.log

.PARAMETER RestartDelaySec
    Seconds to wait before restarting after a crash. Default: 5

.PARAMETER MaxRestarts
    Crash-restart cap within $RestartWindowSec before giving up (prevents a hot
    crash-loop from pegging the GPU box). 0 = unlimited. Default: 20

.PARAMETER RestartWindowSec
    Sliding window for MaxRestarts. Default: 600 (10 min)

.EXAMPLE
    # Normal supervised run (default settings)
    powershell -ExecutionPolicy Bypass -File C:\Users\jdfal\whisper-supervisor.ps1

.EXAMPLE
    # Apply a new server build:
    New-Item C:\Users\jdfal\whisper-stop.flag ; Stop-Process -Name python -Force
    # ...replace whisper_server.py...
    Remove-Item C:\Users\jdfal\whisper-stop.flag
    powershell -ExecutionPolicy Bypass -File C:\Users\jdfal\whisper-supervisor.ps1
#>

[CmdletBinding()]
param(
    [string]$ScriptPath      = "C:\Users\jdfal\whisper_server.py",
    [string]$Model           = "large-v2",
    [int]$Port               = 19847,
    [string]$ComputeType     = "float16",
    [string]$SentinelPath    = "C:\Users\jdfal\whisper-stop.flag",
    [string]$LogPath         = "C:\Users\jdfal\whisper-supervisor.log",
    [int]$RestartDelaySec    = 5,
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

Write-Log "INFO" "supervisor starting: script=$ScriptPath model=$Model port=$Port compute=$ComputeType sentinel=$SentinelPath"

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

# A stale sentinel left over from a previous deliberate stop would make the
# supervisor exit immediately. Clear it at startup so a fresh launch always runs.
if (Test-Path $SentinelPath) {
    Write-Log "WARN" "clearing stale stop-sentinel at startup: $SentinelPath"
    Remove-Item $SentinelPath -Force
}

while ($true) {
    # Honor a sentinel created between iterations (deliberate stop).
    if (Test-Path $SentinelPath) {
        Write-Log "INFO" "stop-sentinel present; exiting supervisor (deliberate stop)"
        break
    }

    $env:WHISPER_PORT = "$Port"
    $env:WHISPER_COMPUTE_TYPE = $ComputeType

    Write-Log "INFO" "launching whisper server (uv run $ScriptPath $Model)"
    $startedAt = Get-Date

    # Start in this console so the user can Ctrl-C; capture the exit code.
    $proc = Start-Process -FilePath $uvExe `
        -ArgumentList @("run", $ScriptPath, $Model) `
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

    Write-Log "WARN" "restarting in ${RestartDelaySec}s..."
    Start-Sleep -Seconds $RestartDelaySec
}

Write-Log "INFO" "supervisor stopped."
