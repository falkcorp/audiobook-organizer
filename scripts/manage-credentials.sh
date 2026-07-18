#!/bin/bash
# file: scripts/manage-credentials.sh
# version: 1.1.0
# guid: a001a982-8d13-4ad6-8f85-9c16c388fa90
# last-edited: 2026-07-18
# Manage per-worktree credentials (username/password for API access)

set -euo pipefail

CRED_DIR=".claude/.credentials"
API_HOST="${API_HOST:-http://localhost:8484}"

# Helper: Get current branch name
get_branch() {
    git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown"
}

# Helper: Sanitize branch name for username
sanitize_branch() {
    echo "$1" | sed 's/[^a-zA-Z0-9_-]/_/g' | tr '[:upper:]' '[:lower:]' | sed 's/^-//; s/-$//'
}

# Helper: Generate a high-entropy password.
# 15 random bytes base64-encoded = 120 bits of entropy (vs. the previous
# 3-word+4-digit scheme's ~27 bits — found LOW in the 2026-07-17
# multi-discipline review, finding #10).
generate_password() {
    openssl rand -base64 15
}

# Helper: Generate credential filename
cred_file() {
    local branch="${1:-$(get_branch)}"
    echo "$CRED_DIR/${branch}.json"
}

# Command: create
cmd_create() {
    local branch="${1:-$(get_branch)}"
    local cred_path=$(cred_file "$branch")

    # Check if creds already exist
    if [ -f "$cred_path" ]; then
        echo "✓ Credentials already exist for branch '$branch'"
        cat "$cred_path" | jq .
        return 0
    fi

    # Generate credentials
    mkdir -p "$CRED_DIR"
    local username="claude_$(sanitize_branch "$branch")"
    local password=$(generate_password)

    # Create JSON file
    cat > "$cred_path" <<EOF
{
  "branch": "$branch",
  "username": "$username",
  "password": "$password",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "api_host": "$API_HOST"
}
EOF

    chmod 600 "$cred_path"

    echo "✅ Credentials created for branch '$branch'"
    echo ""
    echo "Username: $username"
    echo "File:     $cred_path"
    echo ""
    echo "The password is not echoed here — read it from the file above"
    echo "(chmod 600, .gitignored), or run: $0 get $branch"
}

# Command: get
cmd_get() {
    local branch="${1:-$(get_branch)}"
    local cred_path=$(cred_file "$branch")

    if [ ! -f "$cred_path" ]; then
        echo "❌ No credentials found for branch '$branch'"
        echo ""
        echo "Create them first:"
        echo "  $0 create $branch"
        exit 1
    fi

    echo "Credentials for branch '$branch':"
    cat "$cred_path" | jq .
}

# Command: use (for current branch)
cmd_use() {
    local branch="${1:-$(get_branch)}"
    local cred_path=$(cred_file "$branch")

    if [ ! -f "$cred_path" ]; then
        echo "❌ No credentials found for branch '$branch'"
        exit 1
    fi

    # Export as environment variables
    local username=$(jq -r '.username' "$cred_path")
    local password=$(jq -r '.password' "$cred_path")

    echo "# Add these to your shell:"
    echo "export API_USERNAME='$username'"
    echo "export API_PASSWORD='$password'"
    echo ""
    echo "# Or use directly in curl:"
    echo "curl -u '$username:$password' http://localhost:8484/api/v1/audiobooks"
}

# Command: delete
cmd_delete() {
    local branch="${1:-$(get_branch)}"
    local cred_path=$(cred_file "$branch")

    if [ ! -f "$cred_path" ]; then
        echo "No credentials to delete for branch '$branch'"
        return 0
    fi

    rm -f "$cred_path"
    echo "✓ Deleted credentials for branch '$branch'"
}

# Command: cleanup (delete all)
cmd_cleanup() {
    if [ ! -d "$CRED_DIR" ]; then
        echo "No credentials directory"
        return 0
    fi

    local count=$(find "$CRED_DIR" -type f -name "*.json" 2>/dev/null | wc -l)
    if [ "$count" -eq 0 ]; then
        echo "No credentials to clean up"
        return 0
    fi

    echo "⚠️  This will delete all $count credential files in $CRED_DIR"
    read -p "Continue? (y/n) " -n 1 -r
    echo

    if [[ $REPLY =~ ^[Yy]$ ]]; then
        rm -rf "$CRED_DIR"
        echo "✓ Cleaned up $count credential files"
    else
        echo "Cancelled"
    fi
}

# Command: list
cmd_list() {
    if [ ! -d "$CRED_DIR" ]; then
        echo "No credentials stored yet"
        return 0
    fi

    echo "Stored credentials:"
    for file in "$CRED_DIR"/*.json; do
        if [ -f "$file" ] 2>/dev/null; then
            local branch=$(basename "$file" .json)
            local username=$(jq -r '.username' "$file" 2>/dev/null || echo "?")
            local created=$(jq -r '.created_at' "$file" 2>/dev/null || echo "?")
            printf "  %-30s | user: %-25s | created: %s\n" "$branch" "$username" "$created"
        fi
    done
}

# Main
COMMAND="${1:-list}"

case "$COMMAND" in
    create)
        cmd_create "${2:-}"
        ;;
    get)
        cmd_get "${2:-}"
        ;;
    use)
        cmd_use "${2:-}"
        ;;
    delete)
        cmd_delete "${2:-}"
        ;;
    cleanup)
        cmd_cleanup
        ;;
    list)
        cmd_list
        ;;
    *)
        echo "Usage: $0 <command> [branch]"
        echo ""
        echo "Commands:"
        echo "  create [branch]   Create credentials for a branch (auto-generates username/password)"
        echo "  get [branch]      Show credentials for a branch"
        echo "  use [branch]      Show curl usage for a branch"
        echo "  delete [branch]   Delete credentials for a branch"
        echo "  list              List all stored credentials"
        echo "  cleanup           Delete all credentials (asks for confirmation)"
        echo ""
        echo "If branch is omitted, uses current git branch."
        echo ""
        echo "Examples:"
        echo "  $0 create                 # Create creds for current branch"
        echo "  $0 get fix-auth           # Show creds for 'fix-auth' branch"
        echo "  $0 use                    # Show how to use current branch's creds"
        echo "  $0 delete fix-auth        # Delete creds for 'fix-auth' branch"
        exit 1
        ;;
esac
