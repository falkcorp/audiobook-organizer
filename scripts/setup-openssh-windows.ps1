# file: scripts/setup-openssh-windows.ps1
# version: 1.0.0
# guid: b3c4d5e6-f7a8-9012-bcde-234567890123
# last-edited: 2026-06-27
#
# Installs and configures OpenSSH Server on Windows 10/11 so the Linux
# production server (172.16.2.30) can SSH/SCP in without a password.
#
# Run as Administrator in PowerShell:
#   Set-ExecutionPolicy Bypass -Scope Process -Force
#   .\scripts\setup-openssh-windows.ps1
#
# What it does:
#   1. Installs OpenSSH.Server Windows capability (built-in since Win10 1809)
#   2. Starts sshd and sets it to auto-start
#   3. Ensures Windows Firewall allows TCP 22 inbound
#   4. Sets PowerShell as the default SSH shell
#   5. Writes the Linux server's public key to administrators_authorized_keys
#      (Windows admin users need keys here, NOT in ~/.ssh/authorized_keys)
#   6. Locks down permissions on the key file (sshd rejects world-readable files)
#
# After running: test from Linux prod server with:
#   ssh <YourWindowsUsername>@172.16.3.22 "echo ok"

#Requires -RunAsAdministrator

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    OK: $msg" -ForegroundColor Green }
function Write-Warn($msg) { Write-Host "    WARN: $msg" -ForegroundColor Yellow }

# ── 1. Install OpenSSH Server ────────────────────────────────────────────────
Write-Step "Checking OpenSSH Server feature"
$cap = Get-WindowsCapability -Online -Name "OpenSSH.Server*"
if ($cap.State -ne "Installed") {
    Write-Host "    Installing OpenSSH.Server (requires internet)..."
    Add-WindowsCapability -Online -Name "OpenSSH.Server~~~~0.0.1.0" | Out-Null
    Write-Ok "Installed"
} else {
    Write-Ok "Already installed"
}

# ── 2. Start sshd + set to automatic ────────────────────────────────────────
Write-Step "Configuring sshd service"
Set-Service -Name sshd -StartupType Automatic
Start-Service sshd
Write-Ok "sshd running, startup=Automatic"

# ── 3. Firewall rule for port 22 ─────────────────────────────────────────────
Write-Step "Ensuring Windows Firewall allows TCP 22"
$rule = Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -ErrorAction SilentlyContinue
if (-not $rule) {
    New-NetFirewallRule `
        -Name        "OpenSSH-Server-In-TCP" `
        -DisplayName "OpenSSH SSH Server (sshd)" `
        -Enabled     True `
        -Direction   Inbound `
        -Protocol    TCP `
        -Action      Allow `
        -LocalPort   22 | Out-Null
    Write-Ok "Firewall rule created"
} else {
    # Make sure it's enabled even if it existed before.
    Enable-NetFirewallRule -Name "OpenSSH-Server-In-TCP"
    Write-Ok "Firewall rule already exists (enabled)"
}

# ── 4. Set PowerShell as default shell ───────────────────────────────────────
Write-Step "Setting PowerShell as default SSH shell"
$psPath = (Get-Command pwsh -ErrorAction SilentlyContinue)?.Source
if (-not $psPath) { $psPath = (Get-Command powershell).Source }
$regKey = "HKLM:\SOFTWARE\OpenSSH"
if (-not (Test-Path $regKey)) { New-Item -Path $regKey -Force | Out-Null }
Set-ItemProperty -Path $regKey -Name DefaultShell -Value $psPath
Write-Ok "Default shell → $psPath"

# ── 5. Authorized key for the Linux prod server ──────────────────────────────
Write-Step "Writing Linux prod server public key (172.16.2.30)"

# The public key for the Linux prod server's jdfalk user.
# Generated with: ssh-keygen -t ed25519 -C "audiobook-organizer@unimatrixzero"
# Run scripts/push-windows-ssh-key.sh on the prod server to get the current key,
# or paste it here manually.
#
# If you haven't generated a key on the Linux server yet, run there first:
#   ssh-keygen -t ed25519 -C "audiobook-organizer@$(hostname)" -f ~/.ssh/id_ed25519_windows -N ""
#   cat ~/.ssh/id_ed25519_windows.pub
# then paste the output below.
$linuxPubKey = $env:LINUX_SSH_PUBKEY
if (-not $linuxPubKey) {
    Write-Warn "LINUX_SSH_PUBKEY env var not set."
    Write-Host @"

    To add the key manually, run this on the Linux server (172.16.2.30):
        cat ~/.ssh/id_ed25519.pub   # or id_ed25519_windows.pub

    Then re-run this script with the key:
        `$env:LINUX_SSH_PUBKEY = "ssh-ed25519 AAAA... user@host"
        .\scripts\setup-openssh-windows.ps1

    Or add it manually to:
        C:\ProgramData\ssh\administrators_authorized_keys
"@
} else {
    # For Administrator-group users on Windows, authorized_keys lives here —
    # NOT in %USERPROFILE%\.ssh\ (which sshd ignores for admins by default).
    $authKeysDir  = "C:\ProgramData\ssh"
    $authKeysFile = Join-Path $authKeysDir "administrators_authorized_keys"

    if (-not (Test-Path $authKeysDir)) {
        New-Item -ItemType Directory -Path $authKeysDir -Force | Out-Null
    }

    # Append only if the key isn't already there.
    $existing = if (Test-Path $authKeysFile) { Get-Content $authKeysFile } else { @() }
    if ($existing -notcontains $linuxPubKey.Trim()) {
        Add-Content -Path $authKeysFile -Value $linuxPubKey.Trim()
        Write-Ok "Key appended to $authKeysFile"
    } else {
        Write-Ok "Key already present in $authKeysFile"
    }

    # ── 6. Fix permissions (sshd refuses world/group-writable key files) ────
    Write-Step "Locking down permissions on administrators_authorized_keys"
    # Remove inherited permissions, then grant Administrators + SYSTEM full control only.
    icacls $authKeysFile /inheritance:r /grant "Administrators:F" /grant "SYSTEM:F" | Out-Null
    Write-Ok "Permissions set (Administrators + SYSTEM only)"
}

# ── Done ─────────────────────────────────────────────────────────────────────
Write-Host ""
Write-Host "OpenSSH Server is ready." -ForegroundColor Green
Write-Host ""
Write-Host "Test from the Linux server (172.16.2.30):"
Write-Host "  ssh $env:USERNAME@172.16.3.22 'echo connected'"
Write-Host ""
Write-Host "sshd config: C:\ProgramData\ssh\sshd_config"
Write-Host "sshd logs:   Get-EventLog -LogName Application -Source sshd"
