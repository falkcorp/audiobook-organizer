---
name: server-bootstrap
description: Initialize server authentication and retrieve API key. SSH to the audiobook-organizer server, restart the service, read the bootstrap token from the .bootstrap-token file (no longer logged in plaintext — pen-test CRIT-1), exchange it for an API key via POST /api/v1/auth/bootstrap, and write the key to .claude/.api-token (shared across worktrees, auto-cleanup after 8 hours). Use when starting fresh or when the API key has expired.
---

# Server Bootstrap

Initializes authentication for the audiobook-organizer server and stores the API key for use across all worktrees. The key is written to `.claude/.api-token` with a timestamp; cleanup happens automatically after 8 hours.

## Quick Start

When you need an API key (first time, or after restart):

```
Server IP: <user will prompt>
```

The skill will:
1. SSH to the server and restart `audiobook-organizer.service`
2. **Wait 90 seconds** for the service to fully initialize — this server takes ~52 seconds to register all plugins before writing the bootstrap token. Do NOT read the token file until the wait is complete.
3. Read the bootstrap token from the **`.bootstrap-token` file** (the raw token is no longer logged to journalctl — pen-test finding CRIT-1):
   ```bash
   # Path is <data-dir>/.bootstrap-token, where <data-dir> is the directory
   # holding the PebbleDB. On prod the DB is /var/lib/audiobook-organizer/audiobooks.pebble,
   # so the token file is /var/lib/audiobook-organizer/.bootstrap-token.
   # The file is mode 0600 owned by the service user, so sudo is required.
   ssh <server> 'sudo cat /var/lib/audiobook-organizer/.bootstrap-token'
   ```
4. POST to `/api/v1/auth/bootstrap` to exchange token for API key
5. Write key + expiry to `.claude/.api-token` (shared, .gitignored)
6. Schedule cleanup after 8 hours (non-blocking background process)

> Note: the journalctl startup log still prints a `token_file` path + expiry (not the secret), so journalctl confirms *when* a fresh token was written — but the token value only lives in the file above.

## The Token File

The `.api-token` file format:
```
api_key=abbs_xxxxx
key_id=...
username=admin
expires_at=<unix-timestamp-8h-from-now>
```

Other worktrees read this file to get the shared API key. The cleanup process removes the file after 8 hours.

## Bootstrap Token Exchange

The bootstrap token (from logs) is one-time-use and valid for 10 minutes. The POST request:

```bash
curl -sk -X POST https://<server>:8484/api/v1/auth/bootstrap \
  -H "Content-Type: application/json" \
  -d '{"token":"abbs_...", "key_name":"workspace-key"}'
```

Returns:
```json
{
  "api_key": "abbs_xxxxx",
  "key_id": "...",
  "user_id": "...",
  "username": "admin",
  "scopes": ["all"],
  "expires_at": "2026-08-02T12:00:00Z"
}
```

`expires_at` is the server-side expiry of the issued key (default 30 days,
config `bootstrap_key_ttl_days`; SEC-1/PROC-6). This is unrelated to the
8-hour client-side `.claude/.api-token` cleanup convention above — the
server TTL is much longer, so there's no conflict between the two.

See [references/bootstrap-api.md](references/bootstrap-api.md) for full API details.

## Troubleshooting

- **Token file missing / empty**: The service takes ~52 seconds to start on this server. If `sudo cat .../.bootstrap-token` returns nothing or "No such file", you read it too early — wait and retry. Do NOT restart again; just wait and re-read.
- **Permission denied reading the token file**: The file is mode 0600 owned by the service user — use `sudo cat`. If sudo is unavailable, `journalctl` will show the `token_file` path but not the value; you'll need filesystem access to that path.
- **"Token expired"**: Server restart required. The bootstrap token has a fixed 10-minute TTL from service startup.
- **SSH connection fails**: Check server IP and network connectivity.
- **Rate limited**: If you fail the token exchange 5 times in an hour, you'll be rate-limited. Wait or restart the service for a fresh token.

## When to Use This Skill

- Starting a new worktree for the first time
- After a server restart (old token no longer valid)
- When you get 401 Unauthorized on API calls
- After the automatic 8-hour cleanup of the token file
