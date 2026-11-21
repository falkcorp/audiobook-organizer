---
name: server-bootstrap
description: Initialize server authentication and retrieve API key. SSH to the audiobook-organizer server, restart the service, read the bootstrap token from the bootstrap-token file in the state directory (never logged), exchange it for an API key via POST /api/core/v1/auth/bootstrap, and write the key to .claude/.api-token (shared across worktrees, auto-cleanup after 8 hours). Use when starting fresh or when the API key has expired.
---
<!-- file: .claude/skills/server-bootstrap/SKILL.md -->
<!-- version: 1.6.1 -->
<!-- guid: c84a3747-3844-4bce-bf01-ed434f6d1bd2 -->
<!-- last-edited: 2026-11-08 -->

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
3. Read the bootstrap token from the **`bootstrap-token` file** in core's state directory (the raw token is never logged to journalctl):
   ```bash
   # The file is <state_dir>/bootstrap-token (no leading dot), mode 0600, owned
   # by the service user, so sudo is required, and production sudo requires a
   # pseudo-terminal. Core logs only the file NAME ("auth: bootstrap token
   # written file=bootstrap-token expires_at=..."), not its directory: the
   # directory is core.paths.state_dir (ABO_CORE_PATHS_STATE_DIR), which
   # defaults to /var/lib/audiobook-organizer on the prod host.
   # Keep the delay between restart and cat so this reads the newly written
   # token, not the previous file.
   ssh -tt <server> '
     sudo systemctl restart audiobook-organizer.service
     sleep 90
     journalctl -u audiobook-organizer.service --since "-3 min" --no-pager \
       | grep "bootstrap token written" | tail -1
     sudo cat /var/lib/audiobook-organizer/bootstrap-token
   '
   ```

   > **A leftover file can still answer.** A restart overwrites the token file
   > in the state directory in use, but a file from an earlier configuration
   > (HEAD named it `.bootstrap-token`, with a leading dot) is never cleaned up
   > and `sudo cat` on it succeeds with a well-formed but expired value. The
   > exchange then answers `401 bootstrap_invalid` with nothing pointing at
   > why. Read `bootstrap-token` (no dot), and confirm the `bootstrap token
   > written` log line is from THIS restart before trusting what you read.
4. POST to `/api/core/v1/auth/bootstrap` to exchange token for API key
5. Write key + expiry to `.claude/.api-token` (shared, .gitignored)
6. Schedule cleanup after 8 hours (non-blocking background process). That
   is also when the server-side key expires (default `bootstrap_key_ttl` = 8h).

> Note: the journalctl startup log prints the file name and the token's expiry (not the secret), so journalctl confirms *when* a fresh token was written — but the token value only lives in the file above.

## The Token File

The `.api-token` file format:
```
api_key=mck_xxxxx
key_id=...
username=admin
expires_at=<unix-timestamp-8h-from-now>
```

Other worktrees read this file to get the shared API key. The cleanup process removes the file after 8 hours.

## Bootstrap Token Exchange

The bootstrap token (from the token file) is one-time-use and valid for 10 minutes. The POST request:

```bash
curl -sk -X POST https://<server>:8484/api/core/v1/auth/bootstrap \
  -H "Content-Type: application/json" \
  -d '{"token":"mcb_..."}'
```

The body takes `token` only: core refuses a body with unknown fields.

Returns the bare body (no `data` envelope):
```json
{
  "key": "mck_xxxxx",
  "key_id": "...",
  "expires_at": "2026-09-24T12:00:00Z",
  "user": { "id": "...", "username": "admin", "roles": ["..."], "status": "active" },
  "generated_password": "..."
}
```

Extract the bearer key with `jq -er '.key'`. `generated_password` is present
only when the exchange created the admin account. The bootstrap token is
consumed by a successful exchange even if a client subsequently fails to parse
the response.

`expires_at` is the server-side expiry of the issued key: 8 hours by default
(`core.auth.bootstrap.key_ttl`, a Go duration, capped at 24h). The 8-hour
`.claude/.api-token` cleanup above matches it: when the file disappears, the
key behind it has expired too.

### What a bootstrap key cannot do

A bootstrap key is a full-scope admin API key, but routes that change
credentials or identity need an interactive sign-in, not a key. They answer 403
`interactive_required` ("this route needs an interactive sign-in, not an API
key"):

- `PUT /api/core/v1/me/password`, `PATCH /api/core/v1/me`
- `POST /api/core/v1/users/{id}/reset-password`, `POST /api/core/v1/users/{id}/temp-login`
- `POST /api/core/v1/users/invites`, `DELETE /api/core/v1/users/invites/{id}`
- `POST /api/core/v1/users/{id}/disable`, `.../enable`, `DELETE /api/core/v1/users/{id}`
- `PUT /api/core/v1/users/{id}/roles`, `DELETE /api/core/v1/users/{id}/sessions/{publicID}`
- `POST /api/core/v1/users/{id}/api-keys` (a key for another user)
- re-enabling a key (`PATCH /api/core/v1/me/api-keys/{id}` or `/api-keys/{id}` to `active`)
- `POST /api/v1/backup/restore`
- `PUT /api/v1/config` when it changes a sign-in setting

(The `backup/restore` and `config` lines are carried over from before the
identity flip and have not been re-checked against their successor routes.)

To create a user (for example a `claude_*` login) or reset a password, the
owner does it from the web UI while signed in. A key the bootstrap key creates
can't outlive it, so it also expires within 8 hours. To replace a key, mint a
new one with `POST /api/core/v1/me/api-keys` and revoke the old one with
`DELETE /api/core/v1/me/api-keys/{id}`. There is no rotate route.

A key that creates a key must name the new key's scopes in the request body
(for example `"scopes": ["library.view"]`) and a `name`, and every scope must be
one the calling key holds now. A request with no scopes is refused as invalid.

See [references/bootstrap-api.md](references/bootstrap-api.md) for full API details.

## Troubleshooting

- **Token file missing / empty**: The service takes time to initialize and write the fresh file. Wait before reading it. If the service has no state filesystem it mints no token and says so in the log.
- **`401 bootstrap_invalid` immediately after restart**: The token file was read before the fresh startup token replaced the previous file. Restart once, wait 90 seconds, then read and exchange the new value.
- **Successful exchange but `.key` is empty**: Parse `.key`; the native routes answer the bare body, with no `data` envelope. The one-time bootstrap token has already been consumed, so restart and repeat if the issued key was discarded.
- **Permission denied reading the token file**: The file is mode 0600 owned by the service user — use `sudo cat`. If sudo is unavailable you need filesystem access to the state directory.
- **`401 bootstrap_invalid` for an expired token**: Server restart required. The bootstrap token has a fixed 10-minute TTL from service startup.
- **SSH connection fails**: Check server IP and network connectivity.
- **Rate limited**: If you fail the token exchange 5 times in an hour, you'll be rate-limited. Wait or restart the service for a fresh token.
- **Rate limited with only a few failures of your own**: the bootstrap exchange also has a service-wide ceiling shared by every caller, and loopback and local-network callers count toward it like anyone else (they used to be exempt). Other callers' failed attempts can use it up. The limiter is in memory, so restarting the service clears it; the fresh token the restart issues is what you need anyway.

## When to Use This Skill

- Starting a new worktree for the first time
- After a server restart (old token no longer valid)
- When you get 401 Unauthorized on API calls
- After the automatic 8-hour cleanup of the token file
