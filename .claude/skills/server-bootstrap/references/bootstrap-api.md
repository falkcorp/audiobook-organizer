<!-- file: .claude/skills/server-bootstrap/references/bootstrap-api.md -->
<!-- version: 1.0.0 -->
<!-- guid: b8a2de28-0304-4440-9d73-c79f227e1235 -->
<!-- last-edited: 2026-08-25 -->

# Bootstrap API Reference

## POST /api/v1/auth/bootstrap

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
    "generated_password": "Word-Word-Word-123",
    "password_message": "Admin account created. Change this password after logging in."
  }
}
```

- The API wraps successful responses in the standard `data` envelope. Extract
  the bearer key with `jq -er '.data.api_key'`.
- **data.api_key**: The actual API token to use in subsequent requests. Store securely. Only shown once.
- **key_id**: Internal ID for the key.
- **user_id**: Admin user ID.
- **username**: Always "admin" for bootstrap-created users.
- **scopes**: API scopes (always "all" for bootstrap).
- **generated_password**: (Only on first-time bootstrap) Temporary password for the admin user.

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

The API key has full permissions (`scopes: ["all"]`).

## Token File Format (.api-token)

After bootstrap.sh runs, the token file contains:

```
api_key=abbs_xxxxxxxxxxxxx
key_id=ulid-...
username=admin
server_ip=<server-ip>
api_port=8484
expires_at=1716470400
```

- **expires_at**: Unix timestamp. File is automatically deleted after this time.
- All subsequent API calls use the `api_key` value.

## Bootstrapping the Server

Only one bootstrap token is valid at a time. To get a new token:

```bash
ssh -tt <server> 'sudo systemctl restart audiobook-organizer.service; sleep 90; sudo cat /var/lib/audiobook-organizer/.bootstrap-token'
```

The 90-second delay must occur before `cat`. The previous token file can remain
visible during initialization, so reading it immediately after the restart can
return a stale token that the new process rejects. Production sudo also requires
the SSH pseudo-terminal supplied by `-tt`.

The journalctl line now only confirms *when* a token was written and where:
```
msg="Emergency access token written" token_file=/var/lib/audiobook-organizer/.bootstrap-token expires_at=...
msg="Token expires in 10 minutes..."
```

The token is valid for exactly 10 minutes from service startup.
