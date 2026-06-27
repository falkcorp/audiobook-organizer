# file: scripts/setup-openssh-windows.ps1
# version: 1.0.0
# guid: b3c4d5e6-f7a8-9012-bcde-234567890123
# last-edited: 2026-06-27
#
# Run ONCE as Administrator on the Windows GPU machine (172.16.3.22).
# Enables OpenSSH Server so you can SSH/SCP in from Mac or Linux.
#
#   Set-ExecutionPolicy Bypass -Scope Process -Force
#   .\scripts\setup-openssh-windows.ps1

#Requires -RunAsAdministrator
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Install OpenSSH Server (built-in optional feature since Win10 1809)
$cap = Get-WindowsCapability -Online -Name "OpenSSH.Server*"
if ($cap.State -ne "Installed") {
    Write-Host "Installing OpenSSH.Server..."
    Add-WindowsCapability -Online -Name "OpenSSH.Server~~~~0.0.1.0" | Out-Null
}

# Start service and set to auto-start
Set-Service -Name sshd -StartupType Automatic
Start-Service sshd
Write-Host "sshd running"

# Firewall rule for port 22
if (-not (Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -DisplayName "OpenSSH Server" `
        -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 | Out-Null
    Write-Host "Firewall rule created"
}

# Ensure the admin authorized_keys file exists with correct permissions.
# Windows OpenSSH ignores ~/.ssh/authorized_keys for admin users and reads
# from this central file instead.
$keyFile = "C:\ProgramData\ssh\administrators_authorized_keys"
if (-not (Test-Path $keyFile)) {
    New-Item -Path $keyFile -ItemType File -Force | Out-Null
}
icacls $keyFile /inheritance:r /grant "Administrators:F" /grant "SYSTEM:F" | Out-Null
Write-Host "administrators_authorized_keys ready at $keyFile"

Write-Host ""
Write-Host "Done. Now run scripts/setup-ssh-from-mac.sh on your Mac."
Write-Host "sshd config: C:\ProgramData\ssh\sshd_config"
