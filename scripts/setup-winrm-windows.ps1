# file: scripts/setup-winrm-windows.ps1
# version: 1.0.0
# guid: d5e6f7a8-b9c0-1234-defa-456789012345
# last-edited: 2026-06-27
#
# Run ONCE as Administrator on the Windows GPU machine (172.16.3.22).
# Enables PowerShell Remoting (WinRM) so the Mac and Linux server can
# deploy and control this machine via Invoke-Command / Copy-Item.
#
# No OpenSSH required — WinRM is built into Windows.
#
# After running: test from Mac with:
#   pwsh -Command "Invoke-Command -ComputerName 172.16.3.22 -Credential (Get-Credential) -ScriptBlock { hostname }"
#
# Or save credentials once:
#   $cred = Get-Credential
#   $cred | Export-Clixml ~/windows-gpu.cred   # stored encrypted, current user only
#   Invoke-Command -ComputerName 172.16.3.22 -Credential (Import-Clixml ~/windows-gpu.cred) -ScriptBlock { hostname }

#Requires -RunAsAdministrator

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    OK: $msg" -ForegroundColor Green }

# ── 1. Enable PowerShell Remoting ────────────────────────────────────────────
Write-Step "Enabling PowerShell Remoting (WinRM)"
Enable-PSRemoting -Force -SkipNetworkProfileCheck
Write-Ok "PSRemoting enabled"

# ── 2. Allow connections from the LAN (trusted hosts) ────────────────────────
# Restrict to the 172.16.x.x subnet; adjust if your LAN differs.
Write-Step "Setting trusted hosts to 172.16.*"
$current = (Get-Item WSMan:\localhost\Client\TrustedHosts).Value
$lanSubnet = "172.16.*"
if ($current -notlike "*$lanSubnet*") {
    $newValue = if ($current) { "$current,$lanSubnet" } else { $lanSubnet }
    Set-Item WSMan:\localhost\Client\TrustedHosts -Value $newValue -Force
    Write-Ok "TrustedHosts → $newValue"
} else {
    Write-Ok "TrustedHosts already includes $lanSubnet"
}

# ── 3. Firewall: allow WinRM HTTP (5985) inbound from LAN ───────────────────
Write-Step "Ensuring Windows Firewall allows TCP 5985 from LAN"
$ruleName = "WinRM-HTTP-LAN"
$existing = Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue
if (-not $existing) {
    New-NetFirewallRule `
        -Name          $ruleName `
        -DisplayName   "WinRM HTTP (LAN only)" `
        -Enabled       True `
        -Direction     Inbound `
        -Protocol      TCP `
        -LocalPort     5985 `
        -RemoteAddress "172.16.0.0/16" `
        -Action        Allow | Out-Null
    Write-Ok "Firewall rule created (172.16.0.0/16 → TCP 5985)"
} else {
    Enable-NetFirewallRule -Name $ruleName
    Write-Ok "Firewall rule already exists (enabled)"
}

# ── 4. Also allow Whisper server port 8000 if not already open ───────────────
Write-Step "Ensuring TCP 8000 (Whisper server) is open to LAN"
$w8000 = Get-NetFirewallRule -Name "Whisper-Server-LAN" -ErrorAction SilentlyContinue
if (-not $w8000) {
    New-NetFirewallRule `
        -Name          "Whisper-Server-LAN" `
        -DisplayName   "Whisper Server (LAN only)" `
        -Enabled       True `
        -Direction     Inbound `
        -Protocol      TCP `
        -LocalPort     8000 `
        -RemoteAddress "172.16.0.0/16" `
        -Action        Allow | Out-Null
    Write-Ok "Firewall rule created (172.16.0.0/16 → TCP 8000)"
} else {
    Write-Ok "Whisper Server firewall rule already exists"
}

# ── Done ─────────────────────────────────────────────────────────────────────
Write-Host ""
Write-Host "WinRM is ready." -ForegroundColor Green
Write-Host ""
Write-Host "From macOS — install pwsh once: brew install powershell"
Write-Host ""
Write-Host "Then deploy whisper_server.py and restart:"
Write-Host "  pwsh scripts/manage-whisper-server.ps1 -Deploy -Restart"
Write-Host ""
Write-Host "Or just run a quick command:"
Write-Host '  pwsh -Command "Invoke-Command -ComputerName 172.16.3.22 -Credential (Get-Credential) -ScriptBlock { whoami }"'
