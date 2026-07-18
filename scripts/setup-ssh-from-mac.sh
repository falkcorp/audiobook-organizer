#!/usr/bin/env bash
# file: scripts/setup-ssh-from-mac.sh
# version: 1.1.0
# guid: c4d5e6f7-a8b9-0123-cdef-345678901234
# last-edited: 2026-07-18
#
# Run on Mac AFTER setup-openssh-windows.ps1 has been run on Windows.
# Generates an SSH key and installs it on the Windows machine.
#
#   ABK_GPU_HOST=<windows-gpu-host> bash scripts/setup-ssh-from-mac.sh
#
# You'll be prompted for your Windows password once, then never again.

set -euo pipefail

WIN_USER="jdfalk"
WIN_IP="${ABK_GPU_HOST:?ERROR: ABK_GPU_HOST environment variable is required (hostname or IP of the Windows GPU machine, e.g. <windows-gpu-host>)}"
KEY="$HOME/.ssh/id_ed25519_windows"

# Generate key if it doesn't exist
if [[ ! -f "$KEY" ]]; then
    ssh-keygen -t ed25519 -f "$KEY" -N "" -C "mac-to-windows-gpu"
    echo "Generated $KEY"
fi

PUB=$(cat "${KEY}.pub")
echo ""
echo "Public key: $PUB"
echo ""

# Copy the key into the Windows admin authorized_keys file.
# (ssh-copy-id puts it in ~/.ssh/authorized_keys which Windows OpenSSH
# ignores for admin users — so we write to the correct location directly.)
echo "Enter your Windows password when prompted..."
ssh -o StrictHostKeyChecking=accept-new "${WIN_USER}@${WIN_IP}" \
    "powershell -Command \"Add-Content 'C:\\ProgramData\\ssh\\administrators_authorized_keys' '${PUB}'\""

echo ""
echo "Testing passwordless connection..."
ssh -i "$KEY" -o StrictHostKeyChecking=accept-new "${WIN_USER}@${WIN_IP}" "echo connected"

echo ""
echo "Add to ~/.ssh/config for convenience:"
echo ""
echo "  Host windows-gpu"
echo "    HostName $WIN_IP"
echo "    User $WIN_USER"
echo "    IdentityFile $KEY"
echo ""
echo "Then: ssh windows-gpu, scp scripts/whisper_server.py windows-gpu:"
