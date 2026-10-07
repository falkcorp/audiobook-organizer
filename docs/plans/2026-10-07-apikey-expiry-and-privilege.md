<!-- file: docs/plans/2026-10-07-apikey-expiry-and-privilege.md -->
<!-- version: 1.0.0 -->
<!-- guid: 67c7aa4f-72e2-4a9c-805a-92fff01c3308 -->
<!-- last-edited: 2026-10-07 -->

# API keys: expiry, and no path from a key to a signed-in admin

Status: **approved by the owner ("sounds good"), 2026-10-07.** Branch
`fix/apikey-expiry-and-privilege`.

## Goal

1. A bootstrap-issued key (full scope, admin) expires after **8 hours** by
   default, which is what the owner believed was already true.
2. Every API key has an expiry, with a cap.
3. A request authenticated by an API key cannot change credentials or identity:
   no password change or reset, no user creation, no invite, no temp-login link,
   no key for another user, no change to the sign-in settings. Without this an
   admin key can create or reset an admin, sign in as it, and hold an
   interactive admin session, which is what Repairs owner apply (#3810) trusts.

## What was verified before planning

| Claim | Evidence |
|---|---|
| Bootstrap keys default to 30 days | `internal/config/config.go` `viper.SetDefault("bootstrap_key_ttl_days", 30)`; `internal/server/bootstrap.go` reads it, `<=0` falls back to 30 |
| The 8h was client-side only | `.claude/skills/server-bootstrap/scripts/bootstrap.sh` `EXPIRES_IN_SECONDS=$((8 * 3600))` deletes the local `.api-token` file; the server key lived on |
| `POST /auth/api-keys` never expires by default | `handlers/apikeys.go` sets `ExpiresAt` only when `expires_in_days > 0`; the web dialog offers "Never" |
| `Rotate` reads the bootstrap TTL | `handlers/apikeys.go` `Rotate` used `BootstrapKeyTTLDays`; moving bootstrap to 8h would have made every rotated key an 8h key |
| Admin password reset ignores the auth method | `handlers/auth.go` `ChangePassword` checks the caller USER's roles for `users.manage`; never the key's scopes or the method |
| The auth method is on the context | `internal/auth/context.go` (`Method`, `WithMethod`, downgrade-only), recorded by `middleware/auth.go` (`RequireAuth`, `handleAPIKeyAuth`) and `middleware/cfaccess.go` |
| `PUT /config` can rewrite sign-in settings | `config/update_service.go` JSON round-trips the payload onto `Config`, so `oauth_allowed_emails`, `oauth_default_role`, `cf_access_*`, `enable_auth`, `basic_auth_*` are all writable by any `settings.manage` caller |

## Route inventory (every credential/identity route, with a verdict)

Enumerated from `internal/server/wire_auth_routes.go`, the `/users` group in
`internal/server/wire_library_routes.go`, `PUT /config` in
`wire_system_routes.go`, and a path-literal grep over every route registration
(`(POST|PUT|PATCH|DELETE)("…(user|role|password|invite|session|token|key|/me|login|auth|perm)`).
No route changes a user's roles or a role's permissions: there is no
`PUT /users/:id`, no `/roles` write route. The only role assignment is at
invite creation (`role_id`), which is guarded.

**Guarded: an API key gets 403**

| Route | Why |
|---|---|
| `PUT /api/v1/auth/me/password` (own change and the `user_id` admin reset) | The admin-reset branch is the reported gap. The own-change branch is a credential change too; a key holder who also knows the password can sign in anyway, so refusing it costs automation nothing |
| `PATCH /api/v1/auth/me` (email) | Identity change. `oauth.ResolveUser` step 4 links an SSO identity to the local user with the same verified email, so a key that rewrites the owner's email to an allowlisted address it controls gets an SSO sign-in as the owner |
| `POST /api/v1/auth/temp-tokens` | Mints a temp-login link, i.e. a session for any user |
| `POST /api/v1/users/:id/reset-password` | Same temp-login mint |
| `POST /api/v1/users/invite` | Creates a user (with a role) on acceptance, and mints a session |
| `POST /api/v1/users/:id/deactivate` | User status (identity) change; a key could lock the owner out |
| `POST /api/v1/users/:id/reactivate` | Re-enables a sign-in identity, for example a disabled admin whose password is known |
| `POST /api/v1/auth/api-keys` with `user_id` ≠ caller | API key creation for another user (checked in the handler, because the same route stays open for a key's own user) |
| `POST /api/v1/auth/api-keys/:id/rotate` on another user's key | The response carries the new token for that user's key, so it is key creation for another user (checked in the handler) |
| `POST /api/v1/system/reset`, `POST /api/v1/system/factory-reset` | Both wipe the store, users included; the public `POST /auth/setup` then creates a fresh admin with a password, a key-to-signed-in-admin path |
| `POST /api/v1/backup/restore` | Replaces every user, password hash and session with the backup's |
| `PUT /api/v1/config` when it **changes** a sign-in setting | `enable_auth`, `basic_auth_*`, `oauth_*`, `cf_access_*`, `abs_api_enabled`, `abs_auth_modes`, `abs_*_ttl`, `abs_refresh_grace`, `bootstrap_key_ttl`, `bootstrap_key_ttl_days`, `write_startup_readonly_key`. Adding an email to the allowlist and setting the default role to admin is a key-to-SSO-admin path. Only a changed value is refused, so a GET→PUT round trip (settings export/import) by a key still works |

**Not guarded, and why**

| Route | Why not |
|---|---|
| `POST /auth/setup`, `/auth/login`, `/auth/accept-invite`, `/auth/bootstrap`, OAuth start/callback, `GET /auth/temp-login` | Public, pre-authentication; no API key involved. The bootstrap exchange (token → key, creating the admin user when none exists) stays exactly as it is |
| `GET /auth/me`, `GET /auth/sessions`, `GET /auth/api-keys[/:id]`, `GET /users`, `GET /users/invites` | Reads |
| `POST /auth/logout`, `DELETE /auth/sessions/:id`, `DELETE /users/invites/:token`, `DELETE /auth/api-keys/:id` | Revocations only reduce access; automation may need them |
| `PATCH /auth/api-keys/:id` (active/inactive) | Toggles an existing key; the caller never receives that key's token, and the key keeps its expiry |
| `POST /auth/api-keys` for the caller's own user | Allowed, but when the caller is an API key the new key may not outlive the calling key (decision D6) |
| `POST /auth/api-keys/:id/rotate` on a key of the caller's own user | Allowed; the new key keeps the old key's lifetime (D5), clamped to the calling key's expiry when an API key asks (D6) |
| `POST /backup/create`, `GET /backup/list`, `DELETE /backup/:filename` | Do not change who can sign in |
| ABS surface (`/login`, `/auth/refresh`, `/api/me/sessions/:id`, …) | No user, password, invite or key minting routes. ABS `/login` takes a password or a CF Access assertion, never an `abk_` key |

## Decisions (each with its WHY, for the owner to validate)

**D1. Allowed methods on guarded routes: `session`, `session_delegated`,
`cf_access`. Everything else is refused, including `api_key`, `abs`, the
empty method and any value not in the list.**
Why: the brief says interactive sessions keep working exactly as before and
unknown methods fail closed. An allowlist fails closed on a new method nobody
classified. `session_delegated` (temp-login, invite and pre-origin sessions) is
allowed because the reset-password flow depends on it (admin mints a link, the
user's delegated session then sets their password), and invite sessions are a
person's browser too. It cannot be reached from a key any more: every way to
mint one (temp-tokens, reset-password, invite) is now guarded. It is not
`Interactive()` and stays that way, so Repairs owner apply still refuses it.

**D2. The guard is a no-op when `enable_auth` is false.**
Why: it mirrors `s.perm`. With auth off every request is anonymous with full
access, there is no API key to distinguish, and refusing `MethodNone` would
break the settings page in that mode. With auth on, the empty method is
refused, which only affects first run (no users yet). The guarded routes need
a user anyway; the one that does not, `PUT /config`, refuses only a changed
sign-in setting, and the web app (setup wizard included) never sends one.

**D3. Bootstrap keys: new `bootstrap_key_ttl` (Go duration), default `8h`,
capped at `24h`.**
Why: the owner expects a short-lived full-admin key. A duration rather than
days, because "8 hours" is not a whole number of days. Cap at 24h: a
bootstrap key is the break-glass credential and a day covers any session that
needs one. Invalid or non-positive values fall back to 8h with a warning, never
to "never expire".

**D4. Legacy `bootstrap_key_ttl_days` is still honoured, capped at 24h, with a
warning.** Precedence: `bootstrap_key_ttl` if set and valid, else
`bootstrap_key_ttl_days` if `> 0`, else 8h. Its viper default (30) is removed;
with it in place the legacy key would always look set and the 8h default
could never apply. The warning is logged at startup and names the replacement.

**D5. Rotate keeps the old key's lifetime.** The new key's lifetime is the old
key's `ExpiresAt − CreatedAt`, capped at the 365-day maximum; if the old key
had none, 30 days. Why: rotation replaces a secret, it should not change the
policy attached to it. An 8h bootstrap key rotates into another 8h key; a
1-year integration key rotates into another 1-year key. It no longer reads the
bootstrap TTL, which would have turned every rotated key into an 8h key.

**D6. A key created or rotated by an API-key request cannot outlive the
calling key.**
Why: without it, an 8h bootstrap key could `POST /auth/api-keys` for itself
with 365 days in one call, or rotate a year-long sibling key of the same admin
and take the fresh year-long token, and D3 would mean nothing. The expiry is
clamped to the calling key's `expires_at` (the response's `note` says so); an
interactive caller is not clamped. Consequence: a key rotating itself cannot
extend itself; extending a key's life takes a signed-in session (rotate it
from the web UI).

**D7. `POST /auth/api-keys`: `expires_in_days` omitted or 0 means 30 days;
negative or over 365 is a 400.**
Why 30: a default that forces a deliberate renewal each month for keys nobody
thought about, and matches the old bootstrap default people already know.
Why 365: long enough for a set-and-forget integration (Prometheus scraper,
remote fingerprint worker) to need one renewal a year, short enough that a
leaked, forgotten key dies on its own. It is also the longest option the web
dialog already offered. Over-max is refused rather than clamped, so a script
asking for 3650 days learns it did not get them.

**D8. Existing keys with no expiry get `now + 30 days`, once.**
At startup the server lists every non-revoked key with no expiry (id, name,
user, created) at WARN, stamps each with `now + 30d`, and records the
`system:migration:apikey_expiry_stamp_v1_done` setting. Why: integrations keep
working for a month, and the log plus the web UI's expiry column say which ones
need a new key. Inactive keys are stamped too, otherwise reactivating one would
bring back a key that never expires; revoked keys are skipped (they can never
authenticate). If the key listing comes back partial (an unreadable record) or
any write fails, the flag is not set, so the next start retries; the stamp is
idempotent because a stamped key no longer has a nil expiry. After the flag is
set, a never-expiring key found at a later start (for example from a restored
database) is logged at WARN and not stamped.

**D9. Web: the "Never" expiry option is removed, and future expiry dates
render as "in 8h" / "in 29d" with the absolute date in a tooltip.**
Why: 0 now means 30 days on the server, so "Never" would lie. The old
`relativeTime` only handled past dates and rendered a future expiry as
"-29d ago".

**D10. One message for every refusal:** "API keys cannot change passwords,
users, roles, invites, sessions or sign-in settings; sign in to do this" (403).
Why: a single phrase is greppable in logs and tests can prove a 403 came from
this guard and not from a permission check.

## Bootstrap skill and `scripts/manage-credentials.sh`

- `scripts/manage-credentials.sh` only writes a local JSON file with a
  generated username/password. It makes no HTTP call, so it creates no server
  user and is unaffected.
- The server-bootstrap skill (`SKILL.md`, `references/bootstrap-api.md`,
  `scripts/bootstrap.sh`) only calls `POST /auth/bootstrap`. That exchange
  stays. Its text claimed a 30-day server TTL and is updated to 8h.
- **Workflow that does change:** creating a `claude_*` user (or any user)
  on the server with a bootstrap key, or resetting a password with one, now
  returns 403. Replacement: the owner creates the user from the web Users page
  (invite) while signed in. Agents keep using API keys, scoped to what they
  need, for everything else.

## Files to change

- `internal/auth/context.go`: `Method.MayChangeCredentials()`.
- `internal/server/middleware/auth.go` (or a new `credential_guard.go`):
  `RequireCredentialChangeMethod()` and `CredentialChangeRefusal`.
- `internal/server/wire_auth_routes.go`, `internal/server/wire_library_routes.go`:
  attach the guard (via `s.credGuard()`, a no-op when auth is off).
- `internal/server/handlers/apikeys.go`: default/max expiry, other-user guard,
  D5, D6.
- `internal/server/handlers/system/handler.go` plus a `config` helper: refuse a
  changed sign-in setting from a non-allowed method.
- `internal/config/config.go`: `BootstrapKeyTTL`, resolution, cap, warning.
- `internal/server/bootstrap.go`: use the resolved TTL; one-time stamp.
- `internal/server/server_lifecycle.go`: call the stamp at startup.
- `web/src/components/settings/APIKeysTab.tsx` (+ test).
- `.claude/skills/server-bootstrap/**`.
- Tests, changelog fragment, executive summary, TODO check-off.

## Test strategy (synthetic fixtures only)

- Router-level: real server, auth on, seeded roles, an admin user, a
  password-origin session (`CreateSessionWithOrigin(..., SessionOriginPassword)`,
  not `CreateSession`, which records no origin) and an all-scope API key. Each
  guarded route: the key gets 403 with the D10 message; the session does not.
- Unit: the guard for `cf_access`, `session_delegated` (allowed), `abs`, empty,
  and a made-up method (refused).
- Config: default 8h, explicit duration, cap at 24h, legacy days capped,
  invalid falls back.
- Bootstrap: issued key expires in about 8h.
- API keys: default 30d, 365 accepted, 366 and negative are 400, clamp to the
  calling key, rotate keeps lifetime, other-user create/rotate by a key is 403.
- Stamp: stamps nil-expiry active and inactive keys, skips revoked, sets the
  flag, a second run stamps nothing; partial listing leaves the flag unset.
- Web: vitest for the expiry formatting and the missing "Never" option.

Commands: `go build ./...`; `go vet` on touched packages;
`go test -race -short` on `internal/server`, `internal/server/handlers/...`,
`internal/auth/...`, `internal/server/middleware/...`, `internal/config`;
vitest on `web/src/components/settings`; `npx tsc --noEmit -p web`.

## Rollback

Revert the commit. No schema change. The one-time stamp is the only data
write: stamped keys keep their new `expires_at` after a revert. To undo it,
extend each key with `POST /auth/api-keys/:id/rotate` from a signed-in session,
or set a new expiry; the WARN log lists every key it stamped.

## Remaining risk (owner's call)

- **Only an identity the server cannot mint fully closes the owner-apply
  path.** This change removes the API-key routes to a signed-in admin. A
  person (or malware) holding an interactive admin session can still mint
  credentials, by design. Owner apply limited to a Cloudflare Access SSO
  identity, or a second factor at the click, is what closes that
  (`todo.d/2026-10-07-repairs-owner-apply-review-followups.md`).
- A request carrying both a CF Access assertion and an `abk_` bearer is
  recorded `cf_access` (filed in the same fragment, not changed here).
- `PUT /config` still lets a `settings.manage` key change everything that is
  not a sign-in setting, as before. Two of those are broader than this
  change and were found while enumerating, not fixed: `tools.fpcalc.custom_path`
  / `tools.ollama.custom_path` name a binary the server runs, and
  `database_path` points the server at another database on its next start.
  Either lets a `settings.manage` key run code or swap the user table, which
  is a superset of "become a signed-in admin". Owner's call whether those
  keys join the sign-in list or become environment-only.
- `auto_update.channel` only picks stable/beta from the project's own
  releases; it cannot point at another binary.
- `PATCH /auth/api-keys/:id` re-activation of an inactive key of the
  same user is allowed; the caller never receives that key's token.
