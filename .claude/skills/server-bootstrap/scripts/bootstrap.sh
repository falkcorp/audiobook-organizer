#!/bin/bash
# file: .claude/skills/server-bootstrap/scripts/bootstrap.sh
# version: 1.2.0
# guid: b354ac0d-cda1-4b2a-81da-38c5a3f33bd0
# last-edited: 2026-11-22
# Restart audiobook-organizer, read the bootstrap token file THIS restart wrote,
# and exchange it for an API key. Works before and after the F4-08 identity flip:
#   HEAD mode      journal has "Emergency access token written"
#                  -> POST /api/v1/auth/bootstrap, key at .data.api_key
#   identity mode  journal has "auth: bootstrap token written"
#                  -> POST /api/core/v1/auth/bootstrap, key at .key (mck_ core key)
# Requires: ssh, curl, jq. Never derive the token path from a bare token_file=
# grep: the startup read-only key logs one too (.readonly-key, library.view).

set -euo pipefail

# Load server config from .env if present
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [ -f "$REPO_ROOT/.env" ]; then
    set -a
    # shellcheck disable=SC1090,SC1091
    source "$REPO_ROOT/.env"
    set +a
fi

SERVER_IP="${1:-${AUDIOBOOK_SERVER_IP:-}}"
if [ -z "$SERVER_IP" ]; then
    echo "❌ Server IP required: pass as arg or set AUDIOBOOK_SERVER_IP in .env"
    exit 1
fi
API_PORT="${2:-${AUDIOBOOK_API_PORT:-8484}}"
TOKEN_FILE=".claude/.api-token"
# Matches the server's default bootstrap key lifetime (8h), so the file goes
# when the key stops working.
EXPIRES_IN_SECONDS=$((8 * 3600))

if ! command -v jq >/dev/null 2>&1; then
    echo "❌ jq is required to parse the bootstrap response"
    exit 1
fi

# The remote half. Runs under `ssh -tt` because production sudo needs a tty.
# Prints ABO_* marker lines (the only lines the local half reads) and exits
# non-zero with ABO_ERROR on any failure. POSIX sh only.
read -r -d '' REMOTE_SCRIPT <<'REMOTE_EOF' || true
UNIT=audiobook-organizer.service
# Fallback state dir when the unit's environment does not set one: the constant
# the skill documents (identity mode reads core.paths.state_dir).
DEFAULT_STATE_DIR=/var/lib/audiobook-organizer
SINCE=$(date '+%Y-%m-%d %H:%M:%S')
sudo systemctl restart "$UNIT" || { echo "ABO_ERROR=service restart failed"; exit 1; }
# Wait before reading: the previous token file stays visible while the service
# initializes and a stale token answers 401.
sleep 90
LOG=$(journalctl -u "$UNIT" --since "$SINCE" --no-pager 2>/dev/null)

if printf '%s\n' "$LOG" | grep -qF "auth: bootstrap token written"; then
    MODE=identity
    LOGGED=$(printf '%s\n' "$LOG" | grep -F "auth: bootstrap token written" \
        | grep -o ' file=[^ ]*' | tail -1 | cut -d= -f2- | tr -d '"')
    STATE_DIR=$(systemctl show "$UNIT" -p Environment --value 2>/dev/null | tr ' ' '\n' \
        | grep '^"\{0,1\}ABO_CORE_PATHS_STATE_DIR=' | tail -1 | cut -d= -f2- | tr -d '"')
    [ -n "$STATE_DIR" ] || STATE_DIR=$DEFAULT_STATE_DIR
    NAME=.bootstrap-token
    if [ "$LOGGED" != "$NAME" ]; then
        echo "ABO_ERROR=journal names file '$LOGGED', expected '$NAME'"; exit 1
    fi
    FILE="$STATE_DIR/$NAME"
elif printf '%s\n' "$LOG" | grep -qF "Emergency access token written"; then
    MODE=head
    # Match THAT message only: the startup read-only key logs its own token_file.
    FILE=$(printf '%s\n' "$LOG" | grep -F "Emergency access token written" \
        | grep -o 'token_file=[^ ]*' | tail -1 | cut -d= -f2- | tr -d '"')
else
    echo "ABO_ERROR=neither 'auth: bootstrap token written' nor 'Emergency access token written' in the journal since $SINCE"
    exit 1
fi

[ -n "$FILE" ] || { echo "ABO_ERROR=no token file path found"; exit 1; }
TOKEN=$(sudo cat "$FILE") || { echo "ABO_ERROR=cannot read $FILE"; exit 1; }
echo "ABO_MODE=$MODE"
echo "ABO_TOKEN_FILE=$FILE"
echo "ABO_TOKEN=$TOKEN"
REMOTE_EOF

echo "[1/4] Connecting to $SERVER_IP, restarting audiobook-organizer, waiting 90s for the fresh token..."
REMOTE_OUT=$(ssh -tt "$SERVER_IP" "$REMOTE_SCRIPT" | tr -d '\r') || {
    echo "❌ Remote step failed:"
    echo "$REMOTE_OUT" | grep '^ABO_ERROR=' | cut -d= -f2- || true
    exit 1
}

field() { printf '%s\n' "$REMOTE_OUT" | grep "^$1=" | tail -1 | cut -d= -f2-; }
MODE=$(field ABO_MODE)
SRC_FILE=$(field ABO_TOKEN_FILE)
BOOTSTRAP_TOKEN=$(field ABO_TOKEN)

if [ -z "$MODE" ] || [ -z "$BOOTSTRAP_TOKEN" ]; then
    echo "❌ Could not read a bootstrap token:"
    printf '%s\n' "$REMOTE_OUT" | grep '^ABO_ERROR=' | cut -d= -f2- || true
    exit 1
fi
echo "✓ Mode: $MODE, token file: $SRC_FILE, token: ${BOOTSTRAP_TOKEN:0:8}..."

# Step 2: Exchange the token for an API key
echo "[2/4] Exchanging bootstrap token for API key ($MODE route)..."
if [ "$MODE" = "identity" ]; then
    # Core's route: body is the token alone, response is bare (no data envelope).
    URL="https://$SERVER_IP:$API_PORT/api/core/v1/auth/bootstrap"
    BODY=$(jq -n --arg t "$BOOTSTRAP_TOKEN" '{token:$t}')
    KEY_FILTER='(.key // .data.key)'
    ID_FILTER='(.key_id // .data.key_id)'
    USER_FILTER='(.user.username // .data.user.username)'
else
    URL="https://$SERVER_IP:$API_PORT/api/v1/auth/bootstrap"
    BODY=$(jq -n --arg t "$BOOTSTRAP_TOKEN" --arg n "Claude-workspace-$(date +%s)" '{token:$t,key_name:$n}')
    KEY_FILTER='.data.api_key'
    ID_FILTER='.data.key_id'
    USER_FILTER='.data.username'
fi
RESPONSE=$(curl -sk -X POST "$URL" -H "Content-Type: application/json" -d "$BODY" || echo "")

if [ -z "$RESPONSE" ]; then
    echo "❌ Bootstrap endpoint request failed"
    exit 1
fi

API_KEY=$(printf '%s' "$RESPONSE" | jq -r "$KEY_FILTER // empty" 2>/dev/null || true)
KEY_ID=$(printf '%s' "$RESPONSE" | jq -r "$ID_FILTER // empty" 2>/dev/null || true)
USERNAME=$(printf '%s' "$RESPONSE" | jq -r "$USER_FILTER // empty" 2>/dev/null || true)

if [ -z "$API_KEY" ]; then
    echo "❌ Failed to exchange token for API key (error bodies carry no secret)"
    echo "Response: $RESPONSE"
    exit 1
fi
echo "✓ Got API key: ${API_KEY:0:8}..."

# Step 3: Write token file
echo "[3/4] Writing token to .claude/.api-token..."
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

# Step 4: Schedule cleanup
echo "[4/4] Scheduling cleanup in ${EXPIRES_IN_SECONDS}s..."
nohup sh -c "sleep $EXPIRES_IN_SECONDS && rm -f '$TOKEN_FILE' && echo 'Cleaned up $TOKEN_FILE'" > /dev/null 2>&1 &

echo ""
echo "✅ Bootstrap complete! ($MODE mode)"
echo "   API Key: ${API_KEY:0:12}...${API_KEY: -5}"
echo "   Expires: $(date -d @$EXPIRES_AT '+%Y-%m-%d %H:%M:%S')"
echo ""
echo "This API key is shared across all worktrees. Other worktrees can now use it."
