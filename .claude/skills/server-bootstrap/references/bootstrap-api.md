<!-- file: .claude/skills/server-bootstrap/references/bootstrap-api.md -->
<!-- version: 1.4.0 -->
<!-- guid: b8a2de28-0304-4440-9d73-c79f227e1235 -->
<!-- last-edited: 2026-10-20 -->

# Bootstrap API Reference

## POST /api/core/v1/auth/bootstrap

Exchanges a one-time bootstrap token for a full-privilege API key. Public route
(no credential needed), rate limited as an auth route.

### Request

```json
{
  "token": "mcb_xxxxxxxxxxxxx"
}
```

- **token** (required): Bootstrap token read from the `bootstrap-token` file in the state directory (format: `mcb_*`; never logged).

Core refuses a body with any other field, so there is no key name to send.

### Response (200 OK)

The bare body, with no `data` envelope:

```json
{
  "key": "mck_xxxxxxxxxxxxx",
  "key_id": "ulid-...",
  "expires_at": "2026-09-24T12:00:00Z",
  "user": {
    "id": "ulid-...",
    "username": "admin",
    "roles": ["..."],
    "status": "active"
  },
  "generated_password": "..."
}
```

- **key**: The API token to use in subsequent requests. Only shown once.
- **key_id**: Internal ID for the key.
- **user**: The existing active admin, or the account the exchange created.
- **expires_at**: When the key stops working: 8 hours after issue by default
  (`core.auth.bootstrap.key_ttl`, a Go duration, capped at 24h).
- **generated_password**: (Only when the exchange created the admin account.)

### Error Responses

Errors are problem documents with a `code`.

- **401 `bootstrap_invalid`**: the token is wrong, expired (10 minutes after
  service start), already spent, or bootstrap is disabled
  (`core.auth.bootstrap.enabled`). All are the same answer. Restart the service
  for a new token.
- **429**: the exchange is limited to 5 attempts an hour per client, with a
  service-wide ceiling above that; the limiter is in memory, so a restart
  clears it.
- Other statuses (a malformed body, a store failure) are problem documents too;
  check the server logs for a 5xx.

## Using the API Key

Once obtained, use the API key in subsequent requests:

```bash
curl -H "Authorization: Bearer mck_xxxxxxxxxxxxx" \
  http://server:8484/api/v1/audiobooks
```

The key has full permissions, except that routes that change credentials or
identity need an interactive sign-in and answer 403 `interactive_required` to
a key. See SKILL.md, "What a bootstrap key cannot do".

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

Only one bootstrap token is valid at a time: each start deletes the stored ones
and mints a new one. To get a new token:

```bash
ssh -tt <server> '
  sudo systemctl restart audiobook-organizer.service
  sleep 90
  journalctl -u audiobook-organizer.service --since "-3 min" --no-pager \
    | grep "bootstrap token written" | tail -1
  sudo cat /var/lib/audiobook-organizer/bootstrap-token
'
```

The file is `<state_dir>/bootstrap-token`, mode 0600, owned by the service user.
The state directory is `core.paths.state_dir` (`ABO_CORE_PATHS_STATE_DIR`),
which defaults to `/var/lib/audiobook-organizer` on the prod host. The log line
carries the file name and the token's expiry, never the directory or the
value.

The 90-second delay must occur before `cat`: the previous token file can remain
visible during initialization, so reading it immediately after the restart can
return a stale token that the new process rejects. Production sudo also requires
the SSH pseudo-terminal supplied by `-tt`.

A file named `.bootstrap-token` (leading dot) is HEAD's and is never cleaned up;
it answers `sudo cat` with a well-formed, long-expired value. Read the file
without the dot.

The token is valid for exactly 10 minutes from service startup.
