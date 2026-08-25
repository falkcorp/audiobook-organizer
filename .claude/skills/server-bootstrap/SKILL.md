---
name: server-bootstrap
description: Initialize server authentication and retrieve API key. SSH to the audiobook-organizer server, restart the service, read the bootstrap token from the .bootstrap-token file (no longer logged in plaintext — pen-test CRIT-1), exchange it for an API key via POST /api/v1/auth/bootstrap, and write the key to .claude/.api-token (shared across worktrees, auto-cleanup after 8 hours). Use when starting fresh or when the API key has expired.
---
<!-- file: .claude/skills/server-bootstrap/SKILL.md -->
<!-- version: 1.0.0 -->
<!-- guid: c84a3747-3844-4bce-bf01-ed434f6d1bd2 -->
<!-- last-edited: 2026-08-25 -->

# Server Bootstrap

Initializes authentication for the audiobook-organizer server and stores the API key for use across all worktrees. The key is written to `.claude/.api-token` with a timestamp; cleanup happens automatically after 8 hours.

## Quick Start

When you need an API key (first time, or after restart):

```
Server IP: <user will prompt>
```

The skill will:
1. SSH to the server and restart `audiobook-organizer.service`.
2. **Wait 90 seconds before reading the token file.** The previous file may remain visible while the service initializes; reading immediately after `systemctl restart` can return that stale token and cause a `401 invalid bootstrap token` response.
3. Read the bootstrap token from the **`.bootstrap-token` file** (the raw token is no longer logged to journalctl — pen-test finding CRIT-1):
   ```bash
   # Path is <data-dir>/.bootstrap-token, where <data-dir> is the directory
   # holding the PebbleDB. On prod the DB is /var/lib/audiobook-organizer/audiobooks.pebble,
   # so the token file is /var/lib/audiobook-organizer/.bootstrap-token.
   # The file is mode 0600 owned by the service user, so sudo is required.
   # Production sudo requires a pseudo-terminal. Keep the delay between restart
   # and cat so this reads the newly written token rather than the previous file.
   ssh -tt <server> 'sudo systemctl restart audiobook-organizer.service; sleep 90; sudo cat /var/lib/audiobook-organizer/.bootstrap-token'
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
  "data": {
    "api_key": "abbs_xxxxx",
    "key_id": "...",
    "user_id": "...",
    "username": "admin",
    "scopes": ["all"],
    "expires_at": "2026-09-24T12:00:00Z"
  }
}
```

The API uses the standard success envelope, so extract the bearer key with
`jq -er '.data.api_key'`, not `.api_key`. The bootstrap token is consumed by a
successful exchange even if a client subsequently fails to parse the response.

`expires_at` is the server-side expiry of the issued key (default 30 days,
config `bootstrap_key_ttl_days`; SEC-1/PROC-6). This is unrelated to the
8-hour client-side `.claude/.api-token` cleanup convention above — the
server TTL is much longer, so there's no conflict between the two.

See [references/bootstrap-api.md](references/bootstrap-api.md) for full API details.

## Troubleshooting

- **Token file missing / empty**: The service takes time to initialize and write the fresh file. Wait before reading it.
- **`401 invalid bootstrap token` immediately after restart**: The token file was read before the fresh startup token replaced the previous file. Restart once, wait 90 seconds, then read and exchange the new value.
- **Successful exchange but `.api_key` is empty**: Parse `.data.api_key`; all successful API responses use the standard `data` envelope. The one-time bootstrap token has already been consumed, so restart and repeat if the issued key was discarded.
- **Permission denied reading the token file**: The file is mode 0600 owned by the service user — use `sudo cat`. If sudo is unavailable, `journalctl` will show the `token_file` path but not the value; you'll need filesystem access to that path.
- **"Token expired"**: Server restart required. The bootstrap token has a fixed 10-minute TTL from service startup.
- **SSH connection fails**: Check server IP and network connectivity.
- **Rate limited**: If you fail the token exchange 5 times in an hour, you'll be rate-limited. Wait or restart the service for a fresh token.

## When to Use This Skill

- Starting a new worktree for the first time
- After a server restart (old token no longer valid)
- When you get 401 Unauthorized on API calls
- After the automatic 8-hour cleanup of the token file
