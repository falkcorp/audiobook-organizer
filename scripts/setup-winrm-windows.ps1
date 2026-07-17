# file: scripts/setup-winrm-windows.ps1
# version: 1.1.0
# guid: d5e6f7a8-b9c0-1234-defa-456789012345
# last-edited: 2026-07-17
#
# Run ONCE as Administrator on the Windows GPU machine (<windows-gpu-host>).
# Enables PowerShell Remoting (WinRM) so the Mac and Linux server can
# deploy and control this machine via Invoke-Command / Copy-Item.
#
# No OpenSSH required — WinRM is built into Windows.
#
# Your LAN subnet is REQUIRED (no default) — pass it as parameters or set
# the environment variables before running:
#   .\setup-winrm-windows.ps1 -LanSubnetWildcard "10.0.*" -LanSubnetCidr "10.0.0.0/16"
# or:
#   $env:ABK_LAN_SUBNET_WILDCARD = "10.0.*"
#   $env:ABK_LAN_SUBNET_CIDR    = "10.0.0.0/16"
#
# After running: test from Mac with:
#   pwsh -Command "Invoke-Command -ComputerName <windows-gpu-host> -Credential (Get-Credential) -ScriptBlock { hostname }"
#
# Or save credentials once:
#   $cred = Get-Credential
#   $cred | Export-Clixml ~/windows-gpu.cred   # stored encrypted, current user only
#   Invoke-Command -ComputerName <windows-gpu-host> -Credential (Import-Clixml ~/windows-gpu.cred) -ScriptBlock { hostname }

#Requires -RunAsAdministrator

param(
    # Wildcard form of your LAN subnet for WinRM TrustedHosts (e.g. "10.0.*").
    [string]$LanSubnetWildcard = $env:ABK_LAN_SUBNET_WILDCARD,
    # CIDR form of your LAN subnet for firewall scoping (e.g. "10.0.0.0/16").
    [string]$LanSubnetCidr = $env:ABK_LAN_SUBNET_CIDR
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not $LanSubnetWildcard -or -not $LanSubnetCidr) {
    Write-Host "ERROR: LAN subnet not specified — refusing to guess." -ForegroundColor Red
    Write-Host "Pass -LanSubnetWildcard/-LanSubnetCidr or set ABK_LAN_SUBNET_WILDCARD /"
    Write-Host "ABK_LAN_SUBNET_CIDR, e.g.:"
    Write-Host '  .\setup-winrm-windows.ps1 -LanSubnetWildcard "10.0.*" -LanSubnetCidr "10.0.0.0/16"'
    exit 1
}

function Write-Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    OK: $msg" -ForegroundColor Green }

# ── 1. Enable PowerShell Remoting ────────────────────────────────────────────
Write-Step "Enabling PowerShell Remoting (WinRM)"
Enable-PSRemoting -Force -SkipNetworkProfileCheck
Write-Ok "PSRemoting enabled"

# ── 2. Allow connections from the LAN (trusted hosts) ────────────────────────
# Restrict to your LAN subnet (from -LanSubnetWildcard / ABK_LAN_SUBNET_WILDCARD).
Write-Step "Setting trusted hosts to $LanSubnetWildcard"
$current = (Get-Item WSMan:\localhost\Client\TrustedHosts).Value
$lanSubnet = $LanSubnetWildcard
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
        -RemoteAddress $LanSubnetCidr `
        -Action        Allow | Out-Null
    Write-Ok "Firewall rule created ($LanSubnetCidr → TCP 5985)"
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
        -RemoteAddress $LanSubnetCidr `
        -Action        Allow | Out-Null
    Write-Ok "Firewall rule created ($LanSubnetCidr → TCP 8000)"
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
Write-Host '  pwsh -Command "Invoke-Command -ComputerName <windows-gpu-host> -Credential (Get-Credential) -ScriptBlock { whoami }"'
