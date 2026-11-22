<!-- file: .claude/skills/server-bootstrap/references/bootstrap-api.md -->
<!-- version: 1.4.0 -->
<!-- guid: b8a2de28-0304-4440-9d73-c79f227e1235 -->
<!-- last-edited: 2026-10-20 -->

# Bootstrap API Reference

## POST /api/core/v1/auth/bootstrap

Exchanges a one-time bootstrap token for an administrator API key. This is
core's route (`core/internal/authhttp/entry.go`, logic in
`core/auth/bootstrap.go`). The books route `POST /api/v1/auth/bootstrap` answers
`401 {"error":"invalid bootstrap token","code":"UNAUTHORIZED","status":401}` to
every request after the identity flip and reads nothing.

### Request

```json
{
  "token": "<the token read from .bootstrap-token>"
}
```

- **token** (required): the bootstrap token from the `.bootstrap-token` file in
  the state directory (never logged). There is no `key_name`: core ignores it
  and names the key `bootstrap`.

### Response (200 OK, no `data` envelope)

```json
{
  "key": "mck_xxxxxxxxxxxxx",
  "key_id": "ulid-...",
  "expires_at": "2026-11-22T20:00:00Z",
  "user": {"id": "ulid-...", "username": "admin", "roles": ["admin"], "status": "active"},
  "generated_password": "..."
}
```

- Extract the bearer key with `jq -er '.key'`.
- **key**: the API token (`mck_` prefix), scopes `["core:admin"]`, name
  `bootstrap`. Shown once.
- **expires_at**: 8 hours after the redeem by default
  (`core.auth.bootstrap.key_ttl`, capped at 24h; HEAD's `bootstrap_key_ttl` is
  carried into it by the config cutover).
- **user**: the first active user holding `core:admin`, or the admin the redeem
  created (`admin`, then `admin-2` ... `admin-100`).
- **generated_password**: only when the redeem created the admin; shown once.
- No `scopes` field: list them with `GET /api/core/v1/users/<user.id>/api-keys`.

### Error Responses

- **401 `bootstrap_invalid`**: the token is wrong, expired (ten minutes after
  the service started) or already spent. The three answer identically. Restart
  the service for a new token. Also the answer when
  `core.auth.bootstrap.enabled` is false.
- **429**: the source has failed too many credential checks (15 in 15
  minutes, 05-authn section 4.9). Restart the service or wait.

## Using the API Key

```bash
curl -H "Authorization: Bearer mck_xxxxxxxxxxxxx" \
  https://<server>:8484/api/v1/audiobooks
```

The key holds `core:admin` and is non-interactive: Sensitive scopes
(`core:roles.manage`, `core:backup.restore`) and every `Guards.Interactive`
route (credential, user, session, invite, temp-login, other-user key, role
write, restore and storage routes) answer 403, and protected config keys
(`admin`, `owner`, `interactive`) are refused. See SKILL.md, "What a bootstrap
key cannot do".

## Token File Format (.api-token)

After bootstrap.sh runs, the token file contains:

```
api_key=mck_xxxxxxxxxxxxx
key_id=ulid-...
username=admin
server_ip=<server-ip>
api_port=8484
expires_at=1716470400
```

- **expires_at**: Unix timestamp. File is automatically deleted after this time.
- All subsequent API calls use the `api_key` value.

## Bootstrapping the Server

Only one bootstrap token is valid at a time: every start replaces the stored
token and rewrites the file. To get a new token:

```bash
ssh -tt <server> '
  sudo systemctl restart audiobook-organizer.service
  sleep 90
  journalctl -u audiobook-organizer.service --since "-3 min" --no-pager \
    | grep "bootstrap token written" | tail -1
  sudo cat /var/lib/audiobook-organizer/.bootstrap-token
'
```

The state directory is the constant `/var/lib/audiobook-organizer/` (it does not
move with the database). The 90-second delay must occur before `cat`: the
previous token file can remain visible during initialization, and a token from
an earlier start is rejected. Production sudo also requires the SSH
pseudo-terminal supplied by `-tt`.

The log line is core's: `auth: bootstrap token written file=.bootstrap-token
expires_at=...`. It names the file by its bare name and the expiry ten minutes
after the start; use it to confirm the file you read is this restart's. (The
`token_file=<path>` key that older versions logged is gone for the bootstrap
token; it now appears only on the read-only key's line.)

A successful redeem removes the file. The token is valid for exactly 10 minutes
from service startup (`DefaultBootstrapTokenTTL`).

The interim path
`/mnt/bigdata/books/audiobook-organizer/.appdata/.bootstrap-token` is no longer
written; a file left there is expired and should be deleted (needs root).
