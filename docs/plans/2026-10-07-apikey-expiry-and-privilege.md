<!-- file: docs/plans/2026-10-07-apikey-expiry-and-privilege.md -->
<!-- version: 1.1.0 -->
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
| `POST /api/v1/auth/api-keys` with `user_id` ≠ caller | API key creation for another user (`credRouteWhen`: the route predicate binds the body with the handler's own struct, so the guard and the handler read the same `user_id`) |
| `POST /api/v1/auth/api-keys/:id/rotate` on another user's key | The response carries the new token for that user's key, so it is key creation for another user (`credRouteWhen`; a lookup error or a missing key counts as guarded) |
| `PATCH /api/v1/auth/api-keys/:id` | Re-enabling a key an admin deactivated hands a credential back (added after the security review) |
| `POST /api/v1/tools/:name/install`, `POST /api/v1/update/apply` | Install a program the server runs / replace the server binary (added after the review) |
| `POST /api/v1/import-paths` | Adds a scan root: a path the server opens (added after the review) |
| `PUT /api/v1/plugins/:id/settings` | Free-form plugin settings, which can name paths and programs (added after the review) |
| `POST /api/v1/system/reset`, `POST /api/v1/system/factory-reset` | Both wipe the store, users included; the public `POST /auth/setup` then creates a fresh admin with a password, a key-to-signed-in-admin path |
| `POST /api/v1/backup/restore` | Replaces every user, password hash and session with the backup's |
| `PUT /api/v1/config` when it **changes** a protected setting (D13) | `enable_auth`, `basic_auth_*`, `oauth_*`, `cf_access_*`, `abs_api_enabled`, `abs_auth_modes`, `abs_*_ttl`, `abs_refresh_grace`, `bootstrap_key_ttl`, `bootstrap_key_ttl_days`, `write_startup_readonly_key`. Adding an email to the allowlist and setting the default role to admin is a key-to-SSO-admin path. Only a changed value is refused, so a GET→PUT round trip (settings export/import) by a key still works |

**Not guarded, and why**

| Route | Why not |
|---|---|
| `POST /auth/setup`, `/auth/login`, `/auth/accept-invite`, `/auth/bootstrap`, OAuth start/callback, `GET /auth/temp-login` | Public, pre-authentication; no API key involved. The bootstrap exchange (token → key, creating the admin user when none exists) stays exactly as it is |
| `GET /auth/me`, `GET /auth/sessions`, `GET /auth/api-keys[/:id]`, `GET /users`, `GET /users/invites` | Reads |
| `POST /auth/logout`, `DELETE /auth/sessions/:id`, `DELETE /users/invites/:token`, `DELETE /auth/api-keys/:id` | Revocations only reduce access; automation may need them |
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

**D10. One message for every refusal:** `auth.CredentialChangeRefusedMessage`,
"API keys cannot change passwords, users, roles, invites, sessions, keys of
other users, sign-in settings, executables, database locations or server
paths; sign in to do this" (403), used by the route guard and by PUT /config.
Why: a single phrase is greppable in logs and tests can prove a 403 came from
this guard and not from a permission check.

## Security review follow-up (2026-10-07): three findings, and what each was

**Finding 1, parser differential (`config/signin_settings.go`).** The sign-in
check looked payload keys up exactly (`payload["oauth_default_role"]`), while
`decodeConfigPayload` uses `encoding/json`, which matches a key to a field
case-insensitively. An API key's `{"OAuth_Default_Role":"admin"}` therefore
passed the check, was decoded onto `oauth_default_role`, and answered 200. The
same differential reached the immutable, secret and removed-key lists, which
are exact-key too. Fix, two layers:
- One source of truth: the check runs inside `UpdateService.UpdateConfig` on
  the DECODED candidate config versus the stored one
  (`config.ChangedProtectedFields`, `config/protected_fields.go`), so it sees
  exactly what would be stored however the request spelled it. It runs before
  `Validate` (so a key cannot probe paths through validation errors) and
  after (so a normalization cannot carry a change past it). A field counts as
  changed only when it differs from both the stored config and its normalized
  form, so a same-value round trip is never refused.
- Fail closed on ambiguity: `lookupJSONField` is exact-only, so a key that
  matches a field only by case is refused as unknown (400) before decoding.
  `signin_settings.go` is deleted.

**Finding 2, sibling-path gate parity (`wire_auth_routes.go`).** The guard was
attached route by route (`s.credGuard()`) plus three in-handler checks, and
the inventory was a grep. Re-enumerating from gin's `Routes()` (478 routes, 280
state-changing) found five more credential/path routes with no guard: `PATCH
/auth/api-keys/:id` (re-enable a deactivated key), `POST /tools/:name/install`,
`POST /update/apply`, `POST /import-paths`, `PUT /plugins/:id/settings`. Fix:
credential routes register only through `s.credRoute` / `s.credRouteWhen`
(`internal/server/credential_routes.go`), which attach the one shared
middleware and record the route; the per-handler checks are removed.
`credential_routes_test.go` walks `Routes()` and fails on any state-changing
route that is neither recorded nor on an exempt list with a reason; a route
whose path looks sensitive (auth, users, keys, config, tools, plugins, backup,
restore, …) must be exempted by exact route, never by a prefix. A second test
sends an all-scope key to every recorded route and expects the guard's 403.

**Finding 3 (same class: a check against a nil time fails open).** Both API
key paths, `/api/v1` (`handleAPIKeyAuth`) and the ABS surface
(`ResolveAPIKey`), checked `ExpiresAt != nil && now.After(*ExpiresAt)`, so a
key with no expiry (a restored backup, a direct write, a stamp that failed)
was valid forever; and `clampToCallingKey` returned the requested expiry
unclamped when the calling key had none. Fix: one check,
`middleware.APIKeyExpiryRefusal`, refuses a nil or zero expiry on both paths;
the clamp refuses (403) when the caller is not a person and has no calling-key
expiry to clamp to. Same re-audit, same class: ABS `/api/authorize` minted an
ABS access token for a request authenticated by an `abk_` key (a second bearer
credential with its own lifetime); it now echoes the presented key.

**D13. Settings that name an executable, a path the server opens or runs, or
the database location need an interactive session, like sign-in settings; a
same-value round trip is allowed.** One classification,
`configFieldRules` in `internal/config/protected_fields.go`, is the only list;
`protected_fields_test.go` walks every `Config` leaf and fails when a field
whose name looks like a path, program, endpoint, database or sign-in setting
has no entry (protected or not, with a reason), and when an entry names a
field that no longer exists. Why each is protected:

| Class | Keys | Why |
|---|---|---|
| executable | `tools.managed_dir`, `tools.{ollama,fpcalc}.custom_path`, `tools.{ollama,fpcalc}.mode` | a program the server runs, or the switch between the managed binary and a caller-named one |
| database | `database_path`, `database_type` (also immutable), `activity_backend`, `activity_db_path`, `activity_db_move_on_change` | where the user table and activity log live; pointing elsewhere swaps who can sign in |
| server path | `root_dir`, `path_aliases`, `playlist_dir`, `backup_dir`, `openlibrary_dump_dir`, `whisper_clip_cache_dir`, `folder_naming_pattern`, `file_naming_pattern`, `protected_paths`, `itunes.library_write_path`, `itunes.library_read_path`, `itunes.windows_root_path`, `itunes.media_root`, `itunes.path_mappings`, `itunes.libraries.{original,ao}` (itl/xml paths), `itunes.libraries.{pointed_at,import_source}`, `plugins[].settings` | paths the server scans, writes, serves, backs up to or restores from, or the selectors between them; plugin settings are free-form and can name paths |
| sign-in | `enable_auth`, `basic_auth_*`, `oauth_*`, `cf_access_*`, `owner_email`, `abs_api_enabled`, `abs_auth_modes`, `abs_*_ttl`, `abs_refresh_grace`, `write_startup_readonly_key`, `enable_rate_limit`, `auth_rate_limit_per_minute` | who can sign in and how hard guessing is |

Import/scan roots that live outside `Config` (`POST /import-paths`) are
guarded at the route. Left unprotected, with the reason in the rule:
outbound endpoints (`openai_base_url`, `embedding.base_url`,
`ai_backend.local_base_url`, `ai_endpoints`, `whisper_*` URLs,
`metadata_sources[].base_url`, `otel_exporter_otlp_endpoint`,
`deluge_web_url`, `download_client`), third-party credentials, exclude
patterns, plugin on/off. See Remaining risk for the outbound endpoints.

**D11. Owner proof is Cloudflare Access only (owner decision 2026-10-07).**
Owner-only actions (Repairs owner apply, and the `OwnerITunesDatabaseOnly`
guard exception that applies only under an owner grant) need a request that
carries a VERIFIED Cloudflare Access JWT whose email equals the new
`owner_email` setting, case-insensitively. Password, OAuth, temp-login and
invite sessions keep working for everything else but are not the owner. The
unsigned `Cf-Access-Authenticated-User-Email` header never counts: the Access
middleware records the email from the verified claims
(`auth.WithAccessEmail`) and nothing else writes it. `owner_email` is a
sign-in setting (D13), so only an interactive session or the environment
(`OWNER_EMAIL`) sets it; unset refuses every owner action.
`repairs.OwnerGrants.Issue` refuses any grant that is not `cf_access` with an
Access email, and `ResolveOwnerApproval` re-checks it, so the exception cannot
be reached by another caller of `Issue`. `GET /repairs/owner-status` lets the
Repairs page disable the button and say why ("Owner actions need you to sign
in through Cloudflare Access (<the host the browser used>)").
**Why:** every other identity is one the server can create or reset itself — a
password can be changed or stolen, an admin can be created, an OAuth
allowlist edited, a temp-login minted. The Access identity is issued by
Cloudflare against the owner's own IdP account; the server can only verify it.
A stolen password or a newly created admin cannot pass this check.

**D12. When a request has both an Access assertion and an API key, the key
wins (owner decision 2026-10-07).** A request that presents an `abk_` key
(the bearer or the session cookie on `/api/v1`; the bearer or `?token=` on
the ABS surface) is authenticated by the key and recorded `api_key`, with
every key restriction, whatever Access assertion or session rides along.
`CloudflareAccessAuth` skips such a request, `RequireAuth` checks the key
first, the ABS resolver resolves the key and nothing else, and ABS `Bind`
records an API-key identity as `api_key` (it used to be `abs`).
**Why:** the stronger fact about the request is that automation's credential is
on it; recording it as the person's Access login would hand a key the
person's standing.

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

- **Outbound endpoints are not protected.** `openai_base_url`,
  `deluge_web_url`, `download_client.*.host` and similar name hosts the server
  CALLS, often with a stored credential attached (the OpenAI key, a download
  client password). A `settings.manage` key can point one elsewhere and
  receive that credential on the next call. They are outside "a path the
  server opens or runs", so they are classified unprotected with that reason;
  making them interactive-only is one line each in `configFieldRules`
  (`todo.d/2026-10-07-outbound-endpoints-and-path-arguments.md`).
- **Per-request path arguments** (`POST /import/file`, `POST
  /audiobooks/:id/relocate`, `POST /itunes/relocate`, `POST
  /discovery/import`) take a path in the request body for one action; they
  are not settings and are exempt in the route table with that reason (same
  fragment).
- `POST /maintenance/wipe` (admin-only, typed confirm) wipes library data,
  not users or keys, and is exempt.
- `auto_update.channel` only picks stable/beta from the project's own
  releases; it cannot point at another binary.
