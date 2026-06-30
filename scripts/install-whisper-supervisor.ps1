<#
.SYNOPSIS
    Installs whisper-supervisor.ps1 as a durable Scheduled Task on the Windows
    GPU box, stops any manually-started whisper_server.py, and starts the
    supervisor (which relaunches whisper under supervision).

.DESCRIPTION
    Run this ONCE from a normal (non-elevated) PowerShell. After this, the
    Whisper server is supervised: it auto-restarts on crash, and you can apply
    changes cleanly via the stop-sentinel (see "Applying changes" below).

    The scheduled task runs at your NORMAL user token (RunLevel Limited) — not
    elevated — so the user-writable script path is not a privilege-escalation
    vector. Whisper needs no admin rights (it is launched unelevated manually).

    Run from your repo clone so it picks up the committed supervisor script:
      cd C:\Users\jdfal\audiobook-organizer
      git pull
      powershell -ExecutionPolicy Bypass -File scripts\install-whisper-supervisor.ps1

.PARAMETER RepoScript
    Path to the committed supervisor script. Default: .\scripts\whisper-supervisor.ps1
    (relative to where you run this from).

.PARAMETER InstallPath
    Where the supervisor is copied to and run from. Default: C:\Users\jdfal\whisper-supervisor.ps1

.PARAMETER TaskName
    Scheduled Task name. Default: WhisperSupervisor

.NOTES
    Applying a new whisper_server.py (or restarting cleanly):
      New-Item C:\Users\jdfal\whisper-stop.flag
      Get-CimInstance Win32_Process -Filter "name='python.exe'" |
        Where-Object { $_.CommandLine -like '*whisper_server.py*' } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
      # ...replace whisper_server.py...
      Remove-Item C:\Users\jdfal\whisper-stop.flag
      Start-ScheduledTask -TaskName WhisperSupervisor

    Stop supervision entirely:
      Stop-ScheduledTask -TaskName WhisperSupervisor
      Unregister-ScheduledTask -TaskName WhisperSupervisor -Confirm:$false

    Watch it:
      Get-Content C:\Users\jdfal\whisper-supervisor.log -Wait
#>

[CmdletBinding()]
param(
    [string]$RepoScript  = ".\scripts\whisper-supervisor.ps1",
    [string]$InstallPath = "C:\Users\jdfal\whisper-supervisor.ps1",
    [string]$TaskName    = "WhisperSupervisor"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Info($m) { Write-Host "[install] $m" }

# No elevation required. The task runs at the user's NORMAL token (see step 5):
# whisper_server.py needs no admin rights (it's launched manually unelevated),
# and registering a task for your own account does not require admin. We
# deliberately do NOT use -RunLevel Highest — that would run a script from a
# user-writable path (C:\Users\jdfal\...) elevated, which is a privilege-
# escalation vector (any unprivileged write to the script = elevated code at
# next logon). Normal-level + user-writable path crosses no privilege boundary.

if (-not (Test-Path $RepoScript)) {
    Write-Error "Supervisor script not found at $RepoScript. Run from your repo clone (cd C:\Users\jdfal\audiobook-organizer) after 'git pull'."
    exit 1
}

# 1. Copy the committed supervisor to the install path.
Copy-Item $RepoScript $InstallPath -Force
Info "copied supervisor -> $InstallPath"

# 2. Verify it parses before we rely on it.
$errs = $null
[System.Management.Automation.Language.Parser]::ParseFile($InstallPath, [ref]$null, [ref]$errs) | Out-Null
if ($errs.Count -gt 0) {
    Write-Error "Supervisor script has parse errors; aborting. $($errs | ForEach-Object { $_.Message })"
    exit 1
}
Info "supervisor parse OK"

# 3. Stop any manually-started whisper so the supervisor's instance can bind 19847.
$whisper = Get-CimInstance Win32_Process -Filter "name='python.exe'" |
    Where-Object { $_.CommandLine -like '*whisper_server.py*' }
foreach ($w in $whisper) {
    Info "stopping existing whisper pid $($w.ProcessId)"
    Stop-Process -Id $w.ProcessId -Force -ErrorAction SilentlyContinue
}
Start-Sleep -Seconds 2

# 4. Clear any stale stop-sentinel.
Remove-Item "C:\Users\jdfal\whisper-stop.flag" -Force -ErrorAction SilentlyContinue

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
$action = New-ScheduledTaskAction -Execute "powershell.exe" `
    -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$InstallPath`""
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
Info "waiting for whisper /health on :19847 (model load can take up to ~90s)..."
$ok = $false
for ($i = 0; $i -lt 30; $i++) {
    Start-Sleep -Seconds 3
    try {
        $h = (Invoke-WebRequest -UseBasicParsing -Uri "http://localhost:19847/health" -TimeoutSec 4).Content
        if ($h -match '"status"\s*:\s*"ok"') { Info "whisper healthy: $h"; $ok = $true; break }
    } catch { }
}
if (-not $ok) {
    Write-Warning "whisper did not report healthy yet. Check the log:"
    Write-Warning "  Get-Content C:\Users\jdfal\whisper-supervisor.log -Tail 40"
} else {
    Info "DONE. Supervisor installed and whisper is up."
    Info "Watch: Get-Content C:\Users\jdfal\whisper-supervisor.log -Wait"
}
