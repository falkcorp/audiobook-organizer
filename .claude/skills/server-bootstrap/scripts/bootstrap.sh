#!/bin/bash
# file: .claude/skills/server-bootstrap/scripts/bootstrap.sh
# version: 1.1.0
# guid: b354ac0d-cda1-4b2a-81da-38c5a3f33bd0
# last-edited: 2026-10-07
# Restart audiobook-organizer service, read the bootstrap token file, exchange for API key

set -euo pipefail

# Load server config from .env if present
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [ -f "$REPO_ROOT/.env" ]; then
    # shellcheck disable=SC1090
    set -a; source "$REPO_ROOT/.env"; set +a
fi

command -v jq >/dev/null || { echo "❌ jq is required"; exit 1; }

SERVER_IP="${1:-${AUDIOBOOK_SERVER_IP:-}}"
if [ -z "$SERVER_IP" ]; then
    echo "❌ Server IP required: pass as arg or set AUDIOBOOK_SERVER_IP in .env"
    exit 1
fi
API_PORT="${2:-${AUDIOBOOK_API_PORT:-8484}}"
TOKEN_FILE="./.api-token"
# Matches the server's default bootstrap key lifetime (core.auth.bootstrap.key_ttl,
# 8h), so the file goes when the key stops working.
EXPIRES_IN_SECONDS=$((8 * 3600))

# Step 1: Restart the service so it mints a fresh bootstrap token
echo "[1/4] Connecting to $SERVER_IP and restarting audiobook-organizer service..."
ssh "$SERVER_IP" 'sudo systemctl restart audiobook-organizer.service' || {
    echo "❌ SSH failed or service restart failed"
    exit 1
}

# Step 2: Read the token from the state directory. Core writes it to
# <state_dir>/.bootstrap-token (leading dot, mode 0600, owned by the service
# user) and logs only the file NAME, never the path or the value, so the path is
# the state directory, not something to derive from the journal. The directory
# is core.paths.state_dir (ABO_CORE_PATHS_STATE_DIR); this host's default is the
# value below.
# NOTE: This server takes ~52s to start (plugin registration). Wait 90s so the
# file read is the one THIS restart wrote.
STATE_DIR="${AUDIOBOOK_STATE_DIR:-/var/lib/audiobook-organizer}"
echo "[2/4] Waiting 90s for service to fully initialize (plugin registration takes ~52s)..."
sleep 90

BOOTSTRAP_TOKEN=""
for i in {1..12}; do
    # -tt: production sudo needs a pseudo-terminal, which adds a CR to the output.
    BOOTSTRAP_TOKEN=$(ssh -tt "$SERVER_IP" "sudo cat '$STATE_DIR/.bootstrap-token'" 2>/dev/null | tr -d '\r\n' || true)
    if [ -n "$BOOTSTRAP_TOKEN" ]; then
        break
    fi
    echo "  attempt $i: token file not yet readable, retrying in 5s..."
    sleep 5
done

if [ -z "$BOOTSTRAP_TOKEN" ]; then
    echo "❌ Could not read $STATE_DIR/.bootstrap-token"
    exit 1
fi
echo "✓ Got bootstrap token: ${BOOTSTRAP_TOKEN:0:8}..."

# Step 3: Exchange token for API key via core's native route. The answer is the
# bare body (no `data` envelope) and the request body takes `token` only: core
# refuses unknown fields, so there is no key_name.
echo "[3/4] Exchanging bootstrap token for API key..."
RESPONSE=$(curl -sk -X POST "https://$SERVER_IP:$API_PORT/api/core/v1/auth/bootstrap" \
    -H "Content-Type: application/json" \
    -d "$(jq -cn --arg t "$BOOTSTRAP_TOKEN" '{token: $t}')" || echo "")

if [ -z "$RESPONSE" ]; then
    echo "❌ Bootstrap endpoint request failed"
    exit 1
fi

# Parse response JSON
API_KEY=$(echo "$RESPONSE" | jq -er '.key' 2>/dev/null || true)
KEY_ID=$(echo "$RESPONSE" | jq -er '.key_id' 2>/dev/null || true)
USERNAME=$(echo "$RESPONSE" | jq -er '.user.username' 2>/dev/null || true)

if [ -z "$API_KEY" ]; then
    echo "❌ Failed to exchange token for API key"
    # The success body carries a key and possibly a password; a failure body is a problem document.
    echo "Response: $(echo "$RESPONSE" | jq -c '{status, code, detail}' 2>/dev/null || echo unparseable)"
    exit 1
fi
echo "✓ Got API key: ${API_KEY:0:8}..."

# Step 4: Write token file
echo "[4/4] Writing token to .claude/.api-token..."
EXPIRES_AT=$(($(date +%s) + EXPIRES_IN_SECONDS))
mkdir -p .claude

cat > "$TOKEN_FILE" <<EOF
api_key=$API_KEY
key_id=$KEY_ID
username=$USERNAME
server_ip=$SERVER_IP
api_port=$API_PORT
expires_at=$EXPIRES_AT
EOF

chmod 600 "$TOKEN_FILE"
echo "✓ Token written to $TOKEN_FILE"

# Step 5: Schedule cleanup
echo "[5/5] Scheduling cleanup in ${EXPIRES_IN_SECONDS}s..."
nohup sh -c "sleep $EXPIRES_IN_SECONDS && rm -f '$TOKEN_FILE' && echo 'Cleaned up $TOKEN_FILE'" > /dev/null 2>&1 &

echo ""
echo "✅ Bootstrap complete!"
echo "   API Key: ${API_KEY:0:20}...${API_KEY: -5}"
echo "   Expires: $(date -d @$EXPIRES_AT '+%Y-%m-%d %H:%M:%S')"
echo ""
echo "This API key is shared across all worktrees. Other worktrees can now use it."
