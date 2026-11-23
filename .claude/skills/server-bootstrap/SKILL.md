---
name: server-bootstrap
description: Initialize server authentication and retrieve API key. SSH to the audiobook-organizer server, restart the service, detect from the journal whether it runs in HEAD mode or core-identity mode (the F4-08 flip), read the bootstrap token from the file THIS restart wrote (never logged in plaintext - pen-test CRIT-1), and exchange it for an API key via POST /api/v1/auth/bootstrap (HEAD) or POST /api/core/v1/auth/bootstrap (identity). Writes the key to .claude/.api-token (shared across worktrees, auto-cleanup after 8 hours). Use when starting fresh or when the API key has expired.
---
<!-- file: .claude/skills/server-bootstrap/SKILL.md -->
<!-- version: 1.7.0 -->
<!-- guid: c84a3747-3844-4bce-bf01-ed434f6d1bd2 -->
<!-- last-edited: 2026-11-22 -->

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
3. **Detect the mode** from the journal since this restart, then read the bootstrap token from the file that mode names (the raw token is never logged, pen-test CRIT-1). The same file name, `.bootstrap-token`, is used in both modes, but a different writer, directory and log line:

   | Mode | Journal line that identifies it | Token file |
   |---|---|---|
   | **HEAD** (before the flip) | `Emergency access token written token_file=<path> expires_at=...` | the `token_file=` on **that** line |
   | **identity** (after the F4-08 flip) | `auth: bootstrap token written file=<name> expires_at=...` | `<state dir>/.bootstrap-token` |

   - If neither line appears since the restart, stop with a clear error; do not guess a path.
   - **Identity mode: `file=` is a bare name, not a path.** The state dir is core's `core.paths.state_dir`, set in the unit's environment as `ABO_CORE_PATHS_STATE_DIR` (formerly `ABK_STATE_DIR`). Read it with `systemctl show audiobook-organizer.service -p Environment`; if it is unset use `/var/lib/audiobook-organizer`. Check that the logged `file=` equals the basename you are about to read. (A value set only through an `EnvironmentFile=` does not show in `-p Environment`; then the default applies, and a failed `cat` means check the unit.)
   - HEAD's `Emergency access token written` line is **not** logged under identity.

   > **⚠️ TRAP: never take the path from a bare `token_file=` grep.** Books also logs
   > `Read-only API key created (library.view) ... token_file=<path>` for the startup
   > read-only key (when `write_startup_readonly_key` is on). After the flip that line
   > is the only `token_file=` in the journal, so the old
   > `grep -o "token_file=[^ ]*" | tail -1` returns `.readonly-key`, and the exchange
   > would be fed a read-only API key as a bootstrap token. In HEAD mode that line can
   > also follow the emergency line. Always match the mode's own message first.

   ```bash
   # The file is mode 0600 owned by the service user, so sudo is required, and
   # production sudo requires a pseudo-terminal (-tt). Keep the delay between
   # restart and cat so this reads the newly written token, not the previous file.
   ssh -tt <server> '
     UNIT=audiobook-organizer.service
     SINCE=$(date "+%Y-%m-%d %H:%M:%S")
     sudo systemctl restart $UNIT
     sleep 90
     LOG=$(journalctl -u $UNIT --since "$SINCE" --no-pager)
     if printf "%s\n" "$LOG" | grep -qF "auth: bootstrap token written"; then
       echo "mode = identity"
       NAME=$(printf "%s\n" "$LOG" | grep -F "auth: bootstrap token written" \
         | grep -o " file=[^ ]*" | tail -1 | cut -d= -f2-)
       DIR=$(systemctl show $UNIT -p Environment --value | tr " " "\n" \
         | grep "^ABO_CORE_PATHS_STATE_DIR=" | tail -1 | cut -d= -f2-)
       TOKEN_FILE="${DIR:-/var/lib/audiobook-organizer}/.bootstrap-token"
       [ "$NAME" = ".bootstrap-token" ] || { echo "unexpected file name: $NAME"; exit 1; }
     elif printf "%s\n" "$LOG" | grep -qF "Emergency access token written"; then
       echo "mode = head"
       TOKEN_FILE=$(printf "%s\n" "$LOG" | grep -F "Emergency access token written" \
         | grep -o "token_file=[^ ]*" | tail -1 | cut -d= -f2-)
     else
       echo "no bootstrap token line in the journal since $SINCE"; exit 1
     fi
     echo "token_file = $TOKEN_FILE"
     sudo cat "$TOKEN_FILE"
   '
   ```
   `scripts/bootstrap.sh` does the same and the exchange in one run.

   > **The HEAD-mode token directory is fixed as of 2026-09-09.** It is
   > `/var/lib/audiobook-organizer/`, a constant, no longer
   > `filepath.Dir(database_path)`. The existing NOPASSWD sudoers rule names that
   > path and keeps working, and it will not need editing the next time the
   > database moves between pools.
   >
   > Between the database relocation and this fix, the token was briefly written
   > to `/mnt/bigdata/books/audiobook-organizer/.appdata/.bootstrap-token`, a
   > mode-0700 directory no sudoers rule named. If you are reading a host that
   > has not been redeployed since, that is where its token is.
   >
   > **⚠️ A LEFTOVER TOKEN STILL ANSWERS AT EITHER PATH.** Token files are not
   > cleaned up on restart, only overwritten in the directory currently in use.
   > So a file from before or after the move can sit there indefinitely, and
   > `sudo cat` on it SUCCEEDS and returns a well-formed token string, one
   > that expired ten minutes after it was written. Reading the wrong one gets
   > you a plausible token and a `401` with nothing pointing at why.
   >
   > This is exactly what matching the mode's own log line protects you from:
   > it names the file *this* restart wrote. Do not shortcut it with a
   > remembered path. Known stale files that should be deleted (needs root):
   > ```
   > /var/lib/audiobook-organizer/.bootstrap-token                       # pre-move, expired (HEAD hosts only; in use under identity if that is the state dir)
   > /mnt/bigdata/books/audiobook-organizer/.appdata/.bootstrap-token    # interim, expired
   > ```
4. POST the token to the mode's route and take the key from its response:
   - HEAD: `POST /api/v1/auth/bootstrap`, key at `.data.api_key`.
   - identity: `POST /api/core/v1/auth/bootstrap` with `{"token":"..."}`, key at `.key`.
     `POST /api/v1/auth/bootstrap` answers `401 invalid bootstrap token` under identity, always.
5. Write key + expiry to `.claude/.api-token` (shared, .gitignored)
6. Schedule cleanup after 8 hours (non-blocking background process). That
   is also when the server-side key expires (default `bootstrap_key_ttl` = 8h).

> Note: the journalctl startup log prints a file name or path + expiry (not the secret), so journalctl confirms *when* a fresh token was written, but the token value only lives in the file above.

## The Token File

The `.api-token` file format:
```
api_key=abbs_xxxxx        # mck_xxxxx after the identity flip
key_id=...
username=admin
server_ip=<server>
api_port=8484
expires_at=<unix-timestamp-8h-from-now>
```

Other worktrees read this file to get the shared API key. The cleanup process removes the file after 8 hours.

## Bootstrap Token Exchange

The bootstrap token (read from the file) is one-time-use and valid for 10 minutes. Use the route for the mode you detected.

### HEAD mode: `POST /api/v1/auth/bootstrap`

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
`jq -er '.data.api_key'`, not `.api_key`.

### Identity mode (after the F4-08 flip): `POST /api/core/v1/auth/bootstrap`

```bash
curl -sk -X POST https://<server>:8484/api/core/v1/auth/bootstrap \
  -H "Content-Type: application/json" \
  -d '{"token":"<contents of .bootstrap-token>"}'
```

The body is the token alone; there is no `key_name` (the key is always named
`bootstrap`). Returns a **bare** JSON object, not wrapped in `{"data": ...}`:
```json
{
  "key": "mck_xxxxx",
  "key_id": "...",
  "expires_at": "2026-11-23T12:00:00Z",
  "user": {"id": "...", "username": "admin", "roles": ["..."]},
  "generated_password": "..."
}
```

Extract the key with `jq -er '.key'` (`jq -er '.key // .data.key'` accepts either
shape). `generated_password` appears only when the call created the account.
A wrong, expired or spent token is `401 bootstrap_invalid`.

The key is a core key (`mck_` prefix) with the single scope `core:admin`, TTL 8h
by default (`core.auth.bootstrap.key_ttl`, capped at 24h). The same "no API key may
change credentials" rule below applies to it.

In both modes the bootstrap token is consumed by a successful exchange even if a
client subsequently fails to parse the response.

`expires_at` is the server-side expiry of the issued key. Since 2026-10-07
it is **8 hours** by default (config `bootstrap_key_ttl`, a Go duration such
as `8h`; capped at 24h). The 8-hour `.claude/.api-token` cleanup above now
matches it: when the file disappears, the key behind it has expired too.
Until 2026-10-07 the server default was 30 days and only the local file went
away after 8 hours.

The old setting `bootstrap_key_ttl_days` is deprecated. A config file or
environment that still sets it is honoured, capped at 24 hours, with a startup
warning. Replace it with `bootstrap_key_ttl`.

### What a bootstrap key cannot do

A bootstrap key is a full-scope admin API key (HEAD) or an `mck_` core key with `core:admin` (identity), and **no API key may change
credentials or identity.** These answer 403 ("API keys cannot change
passwords, users, roles, invites, sessions or sign-in settings; sign in to do
this"):

- `PUT /api/v1/auth/me/password` (own change or the `user_id` admin reset)
- `PATCH /api/v1/auth/me` (email)
- `POST /api/v1/auth/temp-tokens`, `POST /api/v1/users/:id/reset-password`
- `POST /api/v1/users/invite`, `POST /api/v1/users/:id/deactivate`, `.../reactivate`
- `POST /api/v1/auth/api-keys` with another user's `user_id` (and the retired
  `POST /api/v1/auth/api-keys/:id/rotate` on another user's key, which is still
  refused before it answers 410)
- `POST /api/v1/backup/restore`
- `PUT /api/v1/config` when it changes a sign-in setting (`enable_auth`,
  `basic_auth_*`, `oauth_*`, `cf_access_*`, `abs_api_enabled`,
  `abs_auth_modes`, the ABS token lifetimes, `write_startup_readonly_key`)

To create a user (for example a `claude_*` login) or reset a password, the
owner does it from the web UI while signed in. A key the bootstrap key creates
can't outlive it, so it also expires within 8 hours. To replace a key, create a
new one with `POST /api/v1/auth/api-keys` and revoke the old one with
`DELETE /api/v1/auth/api-keys/:id`; `POST .../:id/rotate` answers 410.

A key that creates a key must name the new key's scopes in the request body
(for example `"scopes": ["library.view"]`), and they must be a subset of the
calling key's own. A request with no `scopes`, or an empty list, answers 403
("an API key must name the scopes of the key it creates") since R0-02.

See [references/bootstrap-api.md](references/bootstrap-api.md) for full API details.

## Troubleshooting

- **Token file missing / empty**: The service takes time to initialize and write the fresh file. Wait before reading it.
- **`401 invalid bootstrap token` immediately after restart**: The token file was read before the fresh startup token replaced the previous file. Restart once, wait 90 seconds, then read and exchange the new value.
- **Successful exchange but the key is empty**: HEAD mode parses `.data.api_key` (standard `data` envelope); identity mode parses `.key` (bare body). The one-time bootstrap token has already been consumed, so restart and repeat if the issued key was discarded.
- **`401 invalid bootstrap token` from `/api/v1/auth/bootstrap` on an identity host**: expected; that route always refuses under identity. Use `/api/core/v1/auth/bootstrap`.
- **`401 bootstrap_invalid` from the core route**: stale or wrong file. Confirm the token came from `<state dir>/.bootstrap-token` and not `.readonly-key`, and that it was read after the 90 s wait.
- **Permission denied reading the token file**: The file is mode 0600 owned by the service user, so use `sudo cat` (over `ssh -tt`). If sudo is unavailable, `journalctl` shows the file name or path but not the value; you'll need filesystem access to that path.
- **Neither journal line appears**: the service has not finished starting (wait longer) or failed to write a token; check `journalctl -u audiobook-organizer.service`. Do not fall back to a bare `token_file=` grep (see the trap above).
- **"Token expired"**: Server restart required. The bootstrap token has a fixed 10-minute TTL from service startup.
- **SSH connection fails**: Check server IP and network connectivity.
- **Rate limited**: If you fail the token exchange 5 times in an hour, you'll be rate-limited. Wait or restart the service for a fresh token.
- **Rate limited with only a few failures of your own**: the bootstrap exchange also has a service-wide ceiling shared by every caller, and loopback and local-network callers count toward it like anyone else (they used to be exempt). Other callers' failed attempts can use it up. The limiter is in memory, so restarting the service clears it; the fresh token the restart issues is what you need anyway.

## When to Use This Skill

- Starting a new worktree for the first time
- After a server restart (old token no longer valid)
- When you get 401 Unauthorized on API calls
- After the automatic 8-hour cleanup of the token file
