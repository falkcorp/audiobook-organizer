### Admin API-key management: all-keys view + generate-for-a-user + email delivery

Admins need to (a) see **every** user's API keys with usage metadata, and (b) generate a
key *on behalf of another user* and have it delivered to that user by email without the
admin ever seeing the full token (a short reference prefix is fine).

**Already built (do NOT rebuild):**
- `database.APIKey` already carries `LastUsedAt`, `LastUsedIP`, `UseCount`, `Status`,
  `ExpiresAt`, `CreatedAt`, revoke/deactivate timestamps, and stores only `TokenHash`
  (raw token is never persisted — hash-only, so "admin can't read it back" is the default).
- Store already has `ListAllAPIKeys()`, `ListAPIKeysForUser`, `CreateAPIKey`,
  `RevokeAPIKey`, `SetAPIKeyStatus`, `SetAPIKeyExpiry`, `TouchAPIKeyLastUsed(id, at, ip)`.
- HTTP routes today (`wire_auth_routes.go`) are all current-user scoped:
  `POST/GET /api-keys`, `GET/DELETE /api-keys/:id`, `POST /api-keys/:id/rotate`.
- Frontend: `web/src/components/settings/APIKeysTab.tsx` (per-user only).

**Three independent slices (ship separately):**
1. **Admin all-keys view (small).** Expose `ListAllAPIKeys()` behind an `adminOnly` route
   (e.g. `GET /api/v1/admin/api-keys`) returning owner + last-used + IP + use-count +
   status + expiry; add an admin frontend table. Almost entirely wiring — the data already
   accrues on every authenticated request.
2. **Generate-for-a-user + reference prefix (medium).** `APIKey` has **no prefix field** —
   add a `KeyPrefix` (first N chars of the raw token, or a separate random public ref)
   persisted at create time so the admin sees a 4–8 char reference without the token.
   Add an admin create-on-behalf endpoint that sets `UserID` = target user and returns
   only the prefix to the admin (never the raw token).
3. **Email delivery without the admin seeing the key (larger — new dependency).** There is
   **no SMTP/email transport** in the codebase (`auth_temp_login.go` says so explicitly).
   Needs a mail transport + config (SMTP creds or a provider), and a one-time delivery flow
   that emails the raw token straight to the recipient. Design the flow so the raw token
   never lands in the admin's HTTP response — it goes only into the outbound email.

**Open questions:** email transport choice (self-hosted SMTP vs provider); whether the
reference prefix should be token-derived or an independent random public id; whether
generate-on-behalf should force a short expiry / first-use rotation for safety.
