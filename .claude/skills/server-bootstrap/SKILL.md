---
name: server-bootstrap
description: Initialize server authentication and retrieve API key. SSH to the audiobook-organizer server, restart the service, read the bootstrap token from the .bootstrap-token file in the state directory, exchange it for an API key via POST /api/core/v1/auth/bootstrap (core's route; the books route /api/v1/auth/bootstrap answers 401 since the identity flip), and write the key to .claude/.api-token (shared across worktrees, auto-cleanup after 8 hours). Use when starting fresh or when the API key has expired.
---
<!-- file: .claude/skills/server-bootstrap/SKILL.md -->
<!-- version: 1.6.1 -->
<!-- guid: c84a3747-3844-4bce-bf01-ed434f6d1bd2 -->
<!-- last-edited: 2026-11-08 -->

# Server Bootstrap

Initializes authentication for the audiobook-organizer server and stores the API key for use across all worktrees. The key is written to `.claude/.api-token` with a timestamp; cleanup happens automatically after 8 hours.

> **After the identity flip the bootstrap lives in core.** The token file keeps
> its name (`.bootstrap-token`), but it is written and redeemed by core:
> `POST /api/core/v1/auth/bootstrap`. The books route `POST /api/v1/auth/bootstrap`
> now answers `401 {"error":"invalid bootstrap token","code":"UNAUTHORIZED","status":401}`
> to every token, a valid one included, and reads nothing. Keys minted before the
> flip keep working after it (M3 imports them); only a NEW bootstrap uses the new
> route. `scripts/bootstrap.sh` does all of this for you.

## Quick Start

When you need an API key (first time, or after restart):

```
Server IP: <user will prompt>
```

The skill will:
1. SSH to the server and restart `audiobook-organizer.service`.
2. **Wait 90 seconds before reading the token file.** The previous file may remain visible while the service initializes; reading immediately after `systemctl restart` can return that stale token and cause a `401 bootstrap_invalid` response.
3. Read the bootstrap token from the **`.bootstrap-token` file** in the state directory (the raw token is never logged):
   ```bash
   # The state directory is the constant /var/lib/audiobook-organizer/ (it does
   # not move with the database). The file is mode 0600 owned by the service
   # user, so sudo is required, and production sudo requires a pseudo-terminal.
   # Keep the delay between restart and cat so this reads the newly written
   # token, not the previous file.
   ssh -tt <server> '
     sudo systemctl restart audiobook-organizer.service
     sleep 90
     journalctl -u audiobook-organizer.service --since "-3 min" --no-pager \
       | grep "bootstrap token written" | tail -1
     sudo cat /var/lib/audiobook-organizer/.bootstrap-token
   '
   ```

   The log line is core's: `auth: bootstrap token written file=.bootstrap-token
   expires_at=<RFC 3339>`. It names the file by its bare name (there is no
   `token_file=<path>` for the bootstrap token any more: that key now appears
   only on the **read-only key's** line, and `sudo cat` of THAT path prints a
   read-only key, not a token). Use the line to confirm the token you are about
   to read was written by THIS restart: `expires_at` is ten minutes after it, so
   it must be in the future and about ten minutes ahead of now.

   > A leftover file is harmless to you but confusing: a token is only good for
   > the boot that wrote it, and core overwrites the file on every start. The
   > interim location used before 2026-09-09
   > (`/mnt/bigdata/books/audiobook-organizer/.appdata/.bootstrap-token`) is
   > never written any more; if you find a file there it is expired and should
   > be deleted (needs root).
   >
   > If `core.auth.bootstrap.enabled` is false the boot writes no file and
   > retires the stored token; every redeem answers `401 bootstrap_invalid`.
4. POST to `/api/core/v1/auth/bootstrap` to exchange the token for an API key
5. Write key + expiry to `.claude/.api-token` (shared, .gitignored)
6. Schedule cleanup after 8 hours (non-blocking background process). That
   is also when the server-side key expires (default `core.auth.bootstrap.key_ttl` = 8h).

## The Token File

The `.api-token` file format:
```
api_key=mck_xxxxx
key_id=...
username=admin
expires_at=<unix-timestamp-8h-from-now>
```

Other worktrees read this file to get the shared API key. The cleanup process removes the file after 8 hours. The key's prefix is `mck_` (core's), not `abk_`/`abbs_`.

## Bootstrap Token Exchange

The bootstrap token (from the file above) is single use and valid for 10 minutes from service startup. A restart is what mints the next one. The POST request:

```bash
curl -sk -X POST https://<server>:8484/api/core/v1/auth/bootstrap \
  -H "Content-Type: application/json" \
  -d '{"token":"<token from the file>"}'
```

The body is `{"token": "..."}` and nothing else. **`key_name` is gone**: core
ignores it and always names the key `bootstrap`, so an operator-chosen name such
as `workspace-key` no longer applies (tell keys apart by `key_id`, or rename
nothing: the name is fixed).

Returns (200, **no `data` envelope**):
```json
{
  "key": "mck_xxxxx",
  "key_id": "...",
  "expires_at": "2026-11-22T20:00:00Z",
  "user": {"id": "...", "username": "admin", "roles": ["admin"], "status": "active"},
  "generated_password": "..."
}
```

Extract the bearer key with `jq -er '.key'` (HEAD's `.data.api_key` and `.api_key`
are both gone). `generated_password` is present only when the redeem had to
create the admin account (no active admin held core:admin); it is shown once.
The redeem spends the token even if a client then fails to parse the response.

A wrong, expired or spent token is `401` with a problem document whose code is
`bootstrap_invalid`; the three cases answer identically. The response carries no `scopes` field: read them from
`GET /api/core/v1/users/<user.id>/api-keys` (the key named `bootstrap`).

### What the minted key is, compared with HEAD's recovery key

| | HEAD (`internal/server/bootstrap.go` at origin/main) | After the flip (core) |
|---|---|---|
| Route | `POST /api/v1/auth/bootstrap` (now 401) | `POST /api/core/v1/auth/bootstrap` |
| Request | `{"token","key_name"}` | `{"token"}` (`key_name` ignored) |
| Key name | `key_name`, or "Bootstrap recovery key" (`:392`) | always `bootstrap` (`core/auth/bootstrap.go:186`) |
| Key prefix | `abk_` | `mck_` |
| Scopes | `auth.All()` (`:402`) | `["core:admin"]` (`core/auth/bootstrap.go:186`) |
| Key lifetime | 8h default, 24h max (`ResolveBootstrapKeyTTL`, `:410`; `internal/config/env_only_settings.go:273`) | 8h default, 24h max (`core.auth.bootstrap.key_ttl`, `core/auth/service.go:54-58`): the same |
| Key interactive? | no | no (non-interactive; see below) |
| Token lifetime | 10 minutes | 10 minutes (`DefaultBootstrapTokenTTL`, `core/auth/bootstrap.go:31`) |
| Token use | once | once, and the file is removed after a successful redeem |
| Owner of the key | first active admin, or creates `admin` | first active user holding `core:admin` who may sign in, or creates `admin` (then `admin-2`... up to 100) |

`expires_at` is the server-side expiry of the issued key, 8 hours after the
redeem by default. The 8-hour `.claude/.api-token` cleanup matches it: when the
file disappears, the key behind it has expired too. HEAD's `bootstrap_key_ttl`
(and the deprecated `bootstrap_key_ttl_days`, converted) is carried by the
config cutover into `core.auth.bootstrap.key_ttl`, so a host that tuned it keeps
its value; the cap is 24h either way.

### Rate limits

Core's redeem is throttled per source (05-authn section 4.9): a failed
credential check (a wrong, malformed or unknown token) is charged to the
caller's address, and 15 failures in 15 minutes answer 429 until the window
passes, then the route's own per-address request limit applies on top. HEAD's
separate "5 failed attempts an hour, plus a service-wide ceiling" limiter
(`core/auth/bootstrap_limit.go`, still in the tree) is used only by the
retired books route and no longer applies. The limiter is in memory, so
restarting the service clears it; the fresh token the restart issues is what
you need anyway.

### What a bootstrap key cannot do

This list is derived from core's rules, not carried over from HEAD's. The key
is a `core:admin` API key, which is non-interactive (an API key is not a
sign-in: `auth.Method.MayChangeCredentials()` is false for it). Two rules
follow (05-rbac section 4.4, 05-authn section 4.8):

1. **Sensitive scopes.** A scope declared `Sensitive` fails the check for a
   non-interactive principal, `core:admin` included. Today those are
   `core:roles.manage` and `core:backup.restore`
   (`core/rbac/catalog.go`). So the key cannot create, edit or delete roles
   and cannot restore a backup. `AuthRequired` scopes (`core:audit.view`,
   `core:system.manage`, `core:backup.restore`) are never held by the
   auth-disabled principal; they say nothing about keys.
2. **Interactive-only routes (`Guards.Interactive`).** Stage 13 refuses an API
   key (and an ABS token, and auth-disabled) with `403 interactive_required`
   (`core/httpapi/stage13.go:118`). The routes that carry it:
   - own account: `PATCH /api/core/v1/me`, the own-password change, ending a
     session of one's own;
   - users: invites (create and delete), disable and enable, delete,
     `reset-password`, `PUT .../roles`, `temp-login`, revoking another user's
     session (`core/internal/authhttp/users.go`);
   - keys: `POST /api/core/v1/users/{id}/api-keys`, minting a key for another
     user (`keys.go`); re-enabling a key needs an interactive sign-in too;
   - role writes (`core/internal/rbachttp/routes.go`), backup restore and
     download (`core/internal/backuphttp/routes.go`), storage administration
     (`core/internal/storagehttp/routes.go`), updater admin routes,
     the outbox's guarded route and the health diagnostics download
     (a session is required there).
   `Guards.Owner` routes (backup restore and download, storage) need an
   interactive `core:admin` and, when an owner is set, the owner.
3. **Config keys.** Core refuses a key a write to any config key declared
   `protect:"interactive"`, `admin`, `owner` or `operator`, because
   `admin` and `owner` need an interactive actor (`core/config/apply.go`
   `meets`). That covers the sign-in settings: `core.auth.mode` (owner),
   `core.auth.setup_requires_token`, `core.auth.password.*`,
   `core.auth.session.*`, `core.auth.bootstrap.*`, `core.auth.login.*`,
   `core.auth.oidc.redirect_base_url` and `.providers` (owner), the read-only
   key's settings. A config key with no protection (for example
   `core.auth.invite.ttl`) a `core:admin` key may write, as HEAD allowed for any
   setting that was not a sign-in setting.

A key CAN still mint a key for itself, `POST /api/core/v1/me/api-keys`, with
scopes it holds; list and revoke keys
(`GET /api/core/v1/api-keys`, `DELETE /api/core/v1/api-keys/{id}`); and use
every non-guarded `core:admin` route and every books route whose requirement
`core:admin` satisfies.

To create a user (for example a `claude_*` login), reset a password or invite
someone, the owner does it from the web UI while signed in. A key the bootstrap
key creates can't outlive it, so it also expires within 8 hours. To replace a
key, create a new one with `POST /api/core/v1/me/api-keys` and revoke the old
one with `DELETE /api/core/v1/api-keys/{id}`.

Spec: `docs/specs/platform-core/05-authn.md` section 4.4 (API keys), 4.6
(bootstrap), 4.8 (guards), 4.9 (throttling), 4.13 (core auth routes);
`docs/specs/platform-core/05-rbac.md` section 4.4 (Sensitive, non-interactive
principals).

See [references/bootstrap-api.md](references/bootstrap-api.md) for full API details.

## Troubleshooting

- **Token file missing / empty**: The service takes time to initialize and write the fresh file. Wait before reading it. If `core.auth.bootstrap.enabled` is false there is no file at all.
- **`401 bootstrap_invalid` immediately after restart**: The token file was read before the fresh startup token replaced the previous file. Restart once, wait 90 seconds, then read and exchange the new value.
- **`401 {"error":"invalid bootstrap token",...}` (no `bootstrap_invalid`)**: you posted to the books route `/api/v1/auth/bootstrap`. Use `/api/core/v1/auth/bootstrap`.
- **Successful exchange but the key is empty**: parse `.key`; core's bootstrap answer has no `data` envelope. The one-time token has already been consumed, so restart and repeat if the issued key was discarded.
- **Permission denied reading the token file**: The file is mode 0600 owned by the service user, so use `sudo cat`. If sudo is unavailable you need filesystem access to the state directory.
- **`401 bootstrap_invalid` on a fresh token**: the token is wrong, expired (ten minutes from the start) or spent; the three look the same. Restart for a new one.
- **SSH connection fails**: Check server IP and network connectivity.
- **429**: see Rate limits above. Restart the service for a fresh limiter and a fresh token.
- **403 `interactive_required`**: the route needs a signed-in user; see "What a bootstrap key cannot do".

## When to Use This Skill

- Starting a new worktree for the first time
- After a server restart (old token no longer valid)
- When you get 401 Unauthorized on API calls
- After the automatic 8-hour cleanup of the token file
