<!-- file: .claude/skills/server-bootstrap/references/bootstrap-api.md -->
<!-- version: 1.5.0 -->
<!-- guid: b8a2de28-0304-4440-9d73-c79f227e1235 -->
<!-- last-edited: 2026-11-22 -->

# Bootstrap API Reference

Two routes, one per mode. Detect the mode from the journal since the restart:
`Emergency access token written` is HEAD (use `/api/v1/auth/bootstrap`);
`auth: bootstrap token written` is core identity, after the F4-08 flip (use
`/api/core/v1/auth/bootstrap`). Under identity `/api/v1/auth/bootstrap` answers
`401 invalid bootstrap token` always.

## POST /api/core/v1/auth/bootstrap (identity mode)

Exchanges core's one-time bootstrap token for an administrator API key. Public
route, rate limited.

### Request

```json
{ "token": "<contents of <state dir>/.bootstrap-token>" }
```

- **token** (required). There is no `key_name`: the key is always named `bootstrap`.

### Response (200 OK)

The body is bare, not wrapped in `{"data": ...}`:

```json
{
  "key": "mck_xxxxxxxxxxxxx",
  "key_id": "ulid-...",
  "expires_at": "2026-11-23T12:00:00Z",
  "user": { "id": "...", "username": "admin", "roles": ["..."], "status": "active" },
  "generated_password": "only when the account was created"
}
```

- **key**: raw key, prefix `mck_`, shown once. Scope `core:admin` only.
- **expires_at**: 8 hours after issue by default (`core.auth.bootstrap.key_ttl`, capped at 24h).
- **generated_password**: present only when the call created the account.
- Parse with `jq -er '.key'` (`.key // .data.key` accepts either shape).
- A wrong, expired or spent token is `401 bootstrap_invalid`. Setup closed answers `403 setup_closed`.
- The same rule applies as for HEAD keys: no API key may change credentials or identity.

### The token file (identity mode)

Core writes `.bootstrap-token` into the app state dir (`core.paths.state_dir`; env
`ABO_CORE_PATHS_STATE_DIR`, formerly `ABK_STATE_DIR`; default
`/var/lib/audiobook-organizer`), mode 0600, and logs
`auth: bootstrap token written file=<name only> expires_at=...`. `file=` is a
name, not a path: build the path from the state dir and check the name matches.

**Never take the path from a bare `token_file=` grep.** Books still logs
`Read-only API key created (library.view) ... token_file=<path>` for the startup
read-only key (`write_startup_readonly_key`), and under identity that is the only
`token_file=` line. It would hand a read-only API key to the bootstrap route.

## POST /api/v1/auth/bootstrap (HEAD mode)

Exchanges a one-time bootstrap token for a full-privilege API key.

### Request

```json
{
  "token": "abbs_xxxxxxxxxxxxx",
  "key_name": "optional-key-name"
}
```

- **token** (required): Bootstrap token read from the `.bootstrap-token` file (format: `abbs_*`; no longer logged in plaintext — pen-test CRIT-1)
- **key_name** (optional): Human-readable name for the API key. Defaults to "Bootstrap recovery key".

### Response (200 OK)

```json
{
  "data": {
    "api_key": "abbs_xxxxxxxxxxxxx",
    "key_id": "ulid-...",
    "user_id": "ulid-...",
    "username": "admin",
    "scopes": ["all"],
    "message": "Bootstrap token consumed. This key will not be shown again.",
    "expires_at": "2026-09-24T12:00:00Z",
    "generated_password": "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23",
    "password_message": "Admin account created. Change this password after logging in."
  }
}
```

- The API wraps successful responses in the standard `data` envelope. Extract
  the bearer key with `jq -er '.data.api_key'`.
- **data.api_key**: The actual API token to use in subsequent requests. Store securely. Only shown once.
- **key_id**: Internal ID for the key.
- **user_id**: Admin user ID.
- **username**: The existing active admin, or for a bootstrap-created admin the
  first free name of "admin", "admin-2", ... (since 2026-10-20).
- **scopes**: API scopes (always "all" for bootstrap).
- **expires_at**: When the key stops working: 8 hours after issue by default
  (config `bootstrap_key_ttl`, a Go duration, capped at 24h; the deprecated
  `bootstrap_key_ttl_days` is honoured but capped at 24h with a warning).
  Before 2026-10-07 this defaulted to 30 days.
- **generated_password**: (Only when bootstrap created the admin) Temporary password for
  that user: 28 base32 symbols in groups of four (140 bits from crypto/rand) since
  2026-10-20; before that, a word-word-word-number passphrase.

### Error Responses

#### 400 Bad Request
Missing or empty token field.

#### 401 Unauthorized
- Token is invalid
- Token has expired (> 10 minutes old)
- Token has already been consumed

Wait for service restart to generate a new bootstrap token.

#### 429 Too Many Requests
More than 5 failed bootstrap attempts in an hour from the same IP.

Wait 1 hour or restart the service.

#### 500 Internal Server Error
Database or key generation failure. Check server logs.

## Using the API Key

Once obtained, use the API key in subsequent requests:

```bash
curl -H "Authorization: Bearer abbs_xxxxxxxxxxxxx" \
  http://server:8484/api/v1/audiobooks
```

The API key has full permissions (`scopes: ["all"]`), except that no API key
may change credentials or identity: password changes and resets, invites,
temp-login links, user (de)activation, keys for another user and sign-in
settings answer 403. Do those signed in to the web UI. See SKILL.md, "What a
bootstrap key cannot do".

## Token File Format (.api-token)

After bootstrap.sh runs, the token file contains:

```
api_key=abbs_xxxxxxxxxxxxx   # mck_... after the identity flip
key_id=ulid-...
username=admin
server_ip=<server-ip>
api_port=8484
expires_at=1716470400
```

- **expires_at**: Unix timestamp. File is automatically deleted after this time.
- All subsequent API calls use the `api_key` value.

## Bootstrapping the Server

Only one bootstrap token is valid at a time. To get a new token (both modes; the
mode is detected from the journal, and the path comes from that mode's own line):

```bash
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

**Match the mode's own message, never a bare `token_file=`.** In HEAD mode the
token is written to `/var/lib/audiobook-organizer/` (a constant since 2026-09-09,
it does not move when the database moves); reading the path out of the startup
log the app just wrote identifies the file written by *this* restart rather than
one left over from an earlier one.

The 90-second delay must occur before `cat`. The previous token file can remain
visible during initialization, so reading it immediately after the restart can
return a stale token that the new process rejects. Production sudo also requires
the SSH pseudo-terminal supplied by `-tt`.

The journalctl line confirms *when* a token was written and, crucially, WHERE.
HEAD mode:
```
msg="Emergency access token written" token_file=<data-dir>/.bootstrap-token expires_at=...
msg="Token expires in 10 minutes..."
```
Identity mode (a file name, not a path):
```
msg="auth: bootstrap token written" file=.bootstrap-token expires_at=...
```

### ⚠️ A leftover token still answers, at whichever path it sits

Token files are never cleaned up on restart — they are only overwritten in the
directory currently in use. A file from an earlier configuration therefore stays
on disk indefinitely, and `sudo cat` on it SUCCEEDS, handing back a well-formed
token string that expired ten minutes after it was written.

That is the worst failure shape available here: the runbook appears to work, and
the exchange returns `401 invalid bootstrap token` with nothing explaining why.
Deriving the path from the mode's own log line is what prevents it.

Two paths are known to hold expired files on the prod host and should be deleted
(needs root):

```
/var/lib/audiobook-organizer/.bootstrap-token                     # pre-2026-09-09
/mnt/bigdata/books/audiobook-organizer/.appdata/.bootstrap-token  # the interim location
```

**Historical note.** Between the database relocation and PR #3171 the token was
written beside the database, so it briefly lived in `.appdata/` — mode 0700,
named by no sudoers rule, and unreadable by the operator. Hosts not redeployed
since are still in that state. The credential directory is now a constant
precisely so this cannot recur.

The token is valid for exactly 10 minutes from service startup.
