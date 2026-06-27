#!/usr/bin/env bash
# file: scripts/push-windows-ssh-key.sh
# version: 1.0.0
# guid: c4d5e6f7-a8b9-0123-cdef-345678901234
# last-edited: 2026-06-27
#
# Run on the Linux prod server (172.16.2.30) AFTER setup-openssh-windows.ps1
# has been run on the Windows GPU machine (172.16.3.22).
#
# What it does:
#   1. Generates an ed25519 key pair for this server if one doesn't exist
#   2. Prints the public key (so you can paste it into $env:LINUX_SSH_PUBKEY
#      and re-run setup-openssh-windows.ps1 on Windows if needed)
#   3. Attempts to copy the key to Windows via ssh-copy-id (works if password
#      auth is still enabled on the Windows sshd — default is yes)
#   4. Verifies the connection
#
# Usage:
#   bash scripts/push-windows-ssh-key.sh [windows-username] [windows-ip]
#
# Defaults:
#   windows-username = jdfalk
#   windows-ip       = 172.16.3.22

set -euo pipefail

WINDOWS_USER="${1:-jdfalk}"
WINDOWS_IP="${2:-172.16.3.22}"
KEY_FILE="$HOME/.ssh/id_ed25519_windows"

echo "==> Ensuring SSH key exists: $KEY_FILE"
if [[ ! -f "$KEY_FILE" ]]; then
    ssh-keygen -t ed25519 -C "audiobook-organizer@$(hostname)" \
        -f "$KEY_FILE" -N ""
    echo "    Generated new key pair"
else
    echo "    Key already exists"
fi

echo ""
echo "==> Public key (also paste this into \$env:LINUX_SSH_PUBKEY on Windows if needed):"
echo "----"
cat "${KEY_FILE}.pub"
echo "----"
echo ""

echo "==> Copying public key to $WINDOWS_USER@$WINDOWS_IP"
echo "    (you may be prompted for the Windows password)"
ssh-copy-id -i "${KEY_FILE}.pub" "${WINDOWS_USER}@${WINDOWS_IP}"

echo ""
echo "==> Verifying passwordless SSH connection"
ssh -i "$KEY_FILE" -o StrictHostKeyChecking=accept-new \
    "${WINDOWS_USER}@${WINDOWS_IP}" "echo 'SSH OK'"

echo ""
echo "Connection established. Add this to ~/.ssh/config on the Linux server"
echo "for convenience:"
echo ""
echo "  Host windows-gpu"
echo "    HostName $WINDOWS_IP"
echo "    User $WINDOWS_USER"
echo "    IdentityFile $KEY_FILE"
echo "    ServerAliveInterval 30"
echo ""
echo "Then you can do:"
echo "  ssh windows-gpu 'uv run C:/path/to/whisper_server.py'"
echo "  scp scripts/whisper_server.py windows-gpu:whisper_server.py"
