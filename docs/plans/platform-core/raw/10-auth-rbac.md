<!-- file: docs/plans/platform-core/raw/10-auth-rbac.md -->
<!-- version: 1.0.0 -->
<!-- guid: 89ce6c2e-f9aa-4f8b-831e-42a31bc38edb -->
<!-- last-edited: 2026-11-03 -->

# 10 - Authentication and authorization (as built today)

Evidence commit: `748fd787a` (checked-out HEAD is 2 docs-only commits ahead). Anchors are repo-relative `path:line`. Route-level facts come from the real-router dump described in `09-http-api.md` section 0. No credential values appear here; token formats are described only.

## 1. Summary

| Question | Answer today |
|---|---|
| Identity model | One flat `User` (username, email, password hash+algo, `Roles []string`, `Status`, `CredentialEpoch`); no tenant/app dimension (`internal/database/store.go:734-780`) |
| AuthN methods on `/api/v1` | web session (cookie or Bearer), `abk_` API key, Cloudflare Access SSO JWT, OAuth login (GitHub, Google) that ends in a session, temp-login link, invite link, one-time bootstrap token -> API key |
| AuthN on the ABS-compatible surface | separate `ABSRequireAuth` (ABS JWT HS256 + refresh, `abk_` key, CF service token/Access) |
| Permission representation | plain `string` constants `"<area>.<verb>"` in one Go file; `type Permission = string` (alias, not a distinct type) (`internal/auth/permissions.go:20,24-85`) |
| Number of permission atoms | 13 (section 3) |
| Roles | 4 seed roles, defined in code, upserted at every boot (`internal/auth/seed.go:33-38,86-129`); NO role CRUD route exists |
| Enforcement | mostly middleware (`s.perm(atom)` per route: 430 of 497 routes carry a route-level permission atom, 16 more an admin-role gate) + a role-NAME admin gate + in-handler `auth.Can` for dynamic cases (op defs, maintenance jobs, repairs) |
| API key scopes | list of the same permission strings; effective = role permissions INTERSECT scopes; empty scopes = full role permissions |
| Secrets at rest | passwords: argon2id (bcrypt legacy); API keys, bootstrap token: SHA-256 hex of a 256-bit random token; sessions: random ULID stored raw as the DB key; 4 config secrets: AES-GCM with a key file; several other provider/OAuth secrets sit in the plaintext config blob (section 7) |
| Audit | structured logs for ABS auth (`event=abs.auth`), "audit"-typed activity rows only for 3 families (guest service tokens, email displacement, user merge) + admin-debug edits; no general audit of login/role/key events (section 8) |

## 2. AuthN components

### 2.1 Package map

| Package / file | Role | Anchor |
|---|---|---|
| `internal/auth` | permission atoms, seed roles, request-context plumbing (`WithUser`, `WithPermissions`, `Can`, `Method`), owner-proof helpers | `internal/auth/permissions.go`, `seed.go`, `context.go`, `owner.go` |
| `internal/server/middleware/auth.go` | `RequireAuth`, API-key auth, `RequirePermission`, `RequireAdmin`, `CheckPermission/HoldsPermission` | `internal/server/middleware/auth.go:152,321,464,486,498,522` |
| `internal/server/middleware/{cfaccess,absauth,absauthprobe,basicauth,credential_guard,owner_guard,ratelimit,actor}.go` | Cloudflare Access binder, ABS identity, optional HTTP Basic, credential-change guard, owner guard, IP rate limiter | `internal/server/middleware/` |
| `internal/server/absauth` | password hashing/verify (argon2id/bcrypt), ABS JWT + refresh derivation, failed-login throttle, saturation limiter, `abs.auth` audit | `internal/server/absauth/password.go`, `token.go`, `refresh.go`, `throttle.go`, `audit.go` |
| `internal/oauth` | GitHub OAuth2, Google OIDC (go-oidc), Cloudflare Access JWT verifier, user resolution/linking by `(provider, subject)`, signed state codec (PKCE S256) | `internal/oauth/providers.go:29,109`, `resolve.go:65`, `cfaccess.go:68-97`, `state.go:55-132` |
| `internal/server/handlers/{auth,apikeys,oauth_login,user,invite_delivery}.go` | login/logout/me/sessions/password, API-key CRUD, SSO start/callback, user admin, invites | `internal/server/handlers/` |
| `internal/server/{bootstrap,auth_temp_login,auth_accept_invite,auth_reset_password,login_ceiling,invite_rate_limit,bootstrap_rate_limit,credential_routes,wire_admin_role}.go` | bootstrap token, temp-login, invite accept, admin reset, login/invite/bootstrap rate limits, credential/admin route helpers | `internal/server/` |
| `internal/cfaccesspolicy` | Cloudflare API client that edits an Access policy (add/remove an email rule or a service-token rule); used to admit invited guests | `internal/cfaccesspolicy/client.go:83-325` |
| `internal/cfservicetoken` | Cloudflare API client minting per-guest service tokens (name prefix `aorg-guest-`); secret value redacted in every `String/Format/LogValue` | `internal/cfservicetoken/client.go:53,170-191` |
| `internal/mtls` | CA/PSK directory + TLS configs requiring client certs + stdio bridge. NOT part of HTTP auth; only importer is `cmd/mtls-bridge` | `internal/mtls/transport.go:14`, `cmd/mtls-bridge/main.go:12` |
| `internal/security/{safehttp,safepath,pathvalidation}` | SSRF-safe HTTP clients, path confinement - not identity | `internal/security/safehttp/safehttp.go` |
| `internal/server/peer_allowlist.go` | network-layer peer allow-list in front of gin (`ALLOWED_PEER_CIDRS`) | `internal/server/peer_allowlist.go:237,277` |
| `internal/database` auth stores | `APIKeyStore`, `RoleStore`, `SessionStore`, `InviteStore` interfaces; Pebble implementation | `internal/database/iface_auth.go:52,60,70,83`, `pebble_store_auth.go` |

There are no `internal/users`, `internal/roles` or `internal/sessions` packages; that logic lives in `internal/database` (stores), `internal/server/handlers` (HTTP) and `internal/usermerge` (account merge).

### 2.2 Request authentication order (`RequireAuth`, `internal/server/middleware/auth.go:152-262`)

1. `store == nil` -> pass (:154). 
2. `CountUsers()==0` -> pass: first-run bootstrap, EVERYTHING reachable and every permission check bypassed until the first user exists (:165-169; permission bypass `:498-504`).
3. A presented `abk_` key (Bearer or the session cookie value) wins over everything else, replacing any earlier-bound identity (:173-176, `APIKeyFromRequest` `:83`).
4. Identity already bound by `CloudflareAccessAuth` (`contextUserKey`) -> skip session check (:180-186).
5. Session token from Bearer or `session_id` cookie (`SessionTokenFromRequest` `:59-72`); lookup, revoked/expired check, user lookup, `CredentialsCurrent` epoch check, `Status` must be active (:195-240).
6. Context gets `User`, effective permissions, and `auth.Method` = `session` (interactive, password/OAuth origin) or `session_delegated` (temp-login, invite, pre-origin sessions) (:243-262).
7. Worker-only confinement (credential whose permission set is exactly `fingerprint.worker` is refused outside `/api/v1/fingerprint/worker/`) (:271-306).

Methods: `MethodSession`, `MethodSessionDelegated`, `MethodAPIKey`, `MethodCFAccess`, `MethodABS` (`internal/auth/context.go:36-57`). `WithMethod` can only downgrade interactivity (:99-107). `MayChangeCredentials` allowlists session, delegated session and CF Access only (:83-90).

### 2.3 Credential types

| Credential | Format / lifetime | Storage | Anchor |
|---|---|---|---|
| Web session | cookie `session_id` (HttpOnly, SameSite=Strict, Secure on HTTPS) or Bearer; id = ULID from `crypto/rand`; TTL 24 h, "remember me" 7 d | session JSON keyed by the raw id in Pebble, plus `idx:sess:user:<user>:<id>` | `internal/server/handlers/auth.go:90-96,226-236`, `internal/database/pebble_store_auth.go:1656-1670`, `database/pebble_store.go:1050` |
| API key | `abk_` + base64url(32 random bytes); shown once; every key has an expiry (default 30 d, max 365 d; keys without one are stamped at startup, and a key with none is refused) | only SHA-256 hex hash stored (`APIKey.TokenHash`) | `internal/database/apikey_token.go:16-35`, `internal/server/handlers/apikeys.go:27-33,186`, `internal/server/middleware/auth.go:309-317`, `internal/server/apikey_expiry_stamp.go` |
| Bootstrap token | `abbs_` + base64url(32 random bytes); minted every startup, 10 min TTL; plaintext written to a 0600 file in the secure state dir (never logged); hash+expiry in settings; single-use, redeemed under a mutex; exchanged at public `POST /api/v1/auth/bootstrap` for a full-scope admin API key (default TTL 8 h, max 24 h) and, if no admin exists, a generated admin with a generated password returned once | hash in settings table | `internal/server/bootstrap.go:37-41,144-170,184-239,279-292,305-470` |
| Startup read-only key | optional (`WriteStartupReadOnlyKey`), 24 h, 0600 file | hash | `internal/server/bootstrap.go:52-131` |
| Temp-login token | single use, 15 min; consumer is public `GET /auth/temp-login`, creates a delegated session (24 h) | token record, deleted on use | `internal/server/auth_temp_login.go:161,227`, `internal/server/handlers/auth.go:92-93` |
| Invite | single-use token in URL; bound to `RoleID`; public lookup + accept endpoints rate-limited per token/client/global | `Invite` rows | `internal/database/store.go:1004-1030`, `internal/server/auth_accept_invite.go:40`, `internal/server/invite_rate_limit.go:334-465` |
| Cloudflare Access JWT | `Cf-Access-Jwt-Assertion` header or `CF_Authorization` cookie; verified (issuer, AUD, expiry); resolves a user only if email allowlisted; service-token assertions mark "verified Access" but resolve no user | none | `internal/server/middleware/cfaccess.go:80-139`, `internal/oauth/cfaccess.go:68-97` |
| OAuth login | GitHub (OAuth2, no PKCE), Google (OIDC, PKCE S256); signed state codec; links by `(provider, subject)` never by email; email allowlist; new users get `OAuthDefaultRole`; `OAuthIdentity` rows | identities in store | `internal/oauth/providers.go`, `internal/oauth/resolve.go:65-205`, `internal/database/oauth_identity.go:21-28`, `internal/server/handlers/oauth_login.go:44-168` |
| ABS tokens | HS256 JWT access tokens (only HS256 accepted) + deterministic refresh tokens derived from a per-session seed and a server secret that lives only in the environment | ABS sessions in store | `internal/server/absauth/token.go:35-81`, `refresh.go:39-52`, `internal/database/pebble_store_abssession.go:49` |
| HTTP Basic | optional global gate (`basic_auth_enabled`), exempts health, assets and ABS-authed routes | config (encrypted secret, section 7) | `internal/server/middleware/basicauth.go:24` |

OIDC: only Google uses go-oidc discovery; there is NO generic/self-configurable OIDC provider (`internal/oauth/oauth.go:32-36` provider constants: github, google, cfaccess).

Password policy: setup requires username + password >= 8 chars (`internal/server/handlers/auth.go:279-282`); hashing is `absauth.HashPassword` = argon2id m=64 MiB, t=3, p=2, 16-byte salt (`internal/server/absauth/password.go:40-47,131`); legacy bcrypt verifies then triggers rehash (`:165-190`); `oauth` algo and empty hash never authenticate (`:165-175`). Login is throttled per visitor + aggregate ceilings (`absauth/throttle.go`, `internal/server/login_ceiling.go:24-60`), with a dummy-hash derivation for unknown users.

Session invalidation: `User.CredentialEpoch` bumps on password change/reset/deactivate/reactivate; sessions stamped with the epoch are refused on mismatch (`internal/database/store.go:745-764`, `middleware/auth.go:229`); writes to those fields are restricted to dedicated setters (`ErrUserProtectedField`, `internal/database/pebble_store_auth.go:555-563`).

### 2.4 Credential-change guard and owner proof

- `credRoute`/`credRouteWhen` wrap routes that change credentials or identity; API keys, ABS tokens and "no method" are refused with one fixed 403 message (`internal/server/credential_routes.go`, `internal/server/middleware/credential_guard.go:25-87`, `internal/auth/context.go:70-90`). 26 routes carry it (`09` section 1).
- `RequireOwner(ownerEmail, perm, applies)`: owner actions (user merge, guest service tokens, system reset/factory-reset/restore while an owner is set) require a verified Cloudflare Access JWT whose email equals the configured `owner_email` (ASCII-only, case-insensitive compare); a password/OAuth/temp session or an API key is never enough (`internal/auth/owner.go:44-108`, `internal/server/middleware/owner_guard.go:51-89`). 9 routes.
- Admin-gate routes: `adminRole()` (no-op when auth off) vs "always-authenticated" `RequireAdmin` for wipe/reset verbs (`internal/server/wire_admin_role.go`). 20 routes.

## 3. Permissions and roles (full list)

### 3.1 Permission atoms (`internal/auth/permissions.go`)

| Atom (string) | Const | Line | Declared meaning | Routes guarded at route level (default build) |
|---|---|---|---|---|
| `library.view` | `PermLibraryView` | 24 | read books/authors/series/playlists/activity; baseline for every seed role | 147 |
| `library.edit_metadata` | `PermLibraryEditMetadata` | 29 | title/author/series/tag edits, metadata fetch/apply, batch edits | 111 |
| `library.delete` | `PermLibraryDelete` | 33 | delete books (soft/hard) and cascades | 22 |
| `library.organize` | `PermLibraryOrganize` | 37 | file rename/move/relayout | 9 |
| `scan.trigger` | `PermScanTrigger` | 40 | start scans and imports | 15 |
| `integrations.manage` | `PermIntegrationsManage` | 45 | configure/trigger iTunes, deluge, openai, openlibrary, hardcover, audible | 12 |
| `users.manage` | `PermUsersManage` | 50 | users, invites, roles assignment, passwords, keys for other users | 20 |
| `settings.manage` | `PermSettingsManage` | 53 | global/system configuration; default permission for an op def that names none | 83 |
| `playlists.create` | `PermPlaylistsCreate` | 57 | create/edit own playlists | 0 (defined, not route-enforced) |
| `collections.manage` | `PermCollectionsManage` | 65 | server-wide collections write | 6 |
| `requests.create` | `PermRequestsCreate` | 70 | reserved: "request a book" | 0 |
| `requests.approve` | `PermRequestsApprove` | 74 | reserved: request approval | 0 |
| `fingerprint.worker` | `PermFingerprintWorker` | 85 | remote fingerprint-worker API only; credential holding only this atom is confined to that API | 5 |

(`users.manage` count: routes where it appears in the chain, including combinations with `credGuard`/`RequireOwner`/`RequireAdmin`.) `All()` (`permissions.go:91`) and `IsKnown()` (`:112`) enumerate atoms by a hand-maintained list (no registry): adding an atom means editing both functions and `seed.go`.

### 3.2 Seed roles (`internal/auth/seed.go`)

| Role id | Const line | Permissions | Anchor |
|---|---|---|---|
| `admin` | `seed.go:33` | all atoms (`All()`, recomputed each boot) | `seed.go:44-46` |
| `editor` | `seed.go:34` | library.view, library.edit_metadata, library.delete, library.organize, scan.trigger, playlists.create, requests.create, requests.approve | `seed.go:50-61` |
| `viewer` | `seed.go:35` | library.view, requests.create | `seed.go:72-77` |
| `fp-worker` | `seed.go:38` | fingerprint.worker only | `seed.go:66-68` |
| `_system` pseudo-user | `seed.go:134` | no password, status `system`, audit-attribution only | `seed.go:139-152` |

Roles are persisted (`database.Role{ID,Name,Description,Permissions []string,IsSeed}`, `internal/database/store.go:968-977`) but SeedRoles is the only writer found: no `CreateRole/UpdateRole/DeleteRole` call in `internal/server` or `cmd` outside seeding, and no `/roles` route exists. Custom roles are therefore not creatable via API (a custom role row would only come from a direct store write). Role assignment: invite carries `RoleID` (`store.go:1004`), `OAuthDefaultRole` for SSO users (`internal/config/config.go:1597-1599`), bootstrap admin gets `admin`. Users can hold several roles; effective permissions = union (`EffectivePermissionsFor`, `middleware/auth.go:430-455`).

### 3.3 Op-definition permissions

Each operation definition declares `Permissions []auth.Permission` (`internal/operations/registry/types.go:165-166`); starting by `def_id` requires ALL of them, defaulting to `settings.manage` (`internal/server/handlers/operations_v2.go:781-800`). Maintenance jobs optionally implement `PermissionAware` (`internal/server/maintenance_dispatcher.go:37,93-96`).

## 4. Where checks are enforced

| Mechanism | Used by | Anchor |
|---|---|---|
| `s.perm(atom)` route middleware (no-op if `EnableAuth=false`; first-run bypass) | the large majority of `protected` routes | `internal/server/server_lifecycle.go:1693-1698`, `middleware/auth.go:464-505` |
| `RequireAdmin` = `slices.Contains(user.Roles, "admin")` (role NAME, not a permission) | `/admin/*`, system reset/factory-reset, maintenance wipe, master-list admin group | `internal/server/middleware/auth.go:522-537`, `internal/server/wire_admin_role.go:80-106`, `internal/server/wire_master_list_routes.go:41-42` |
| Also role-name checks in handlers | user email set, user admin | `internal/server/handlers/user.go:285,441,453` |
| `credGuard`, `RequireOwner` | credential/identity routes | section 2.4 |
| In-handler `auth.Can` / `CheckPermission` | op defs, v3 ops, repairs, maintenance, audiobook helpers, ABS collections, API-key cross-user ops, session cross-user ops | `09` section 4.1; `internal/server/handlers/apikeys.go:170`, `handlers/auth.go:599` |
| Query-time scoping to the caller | reading state, playlists, "viewer" logic | `internal/server/handlers/audiobooks/handler.go:986` (viewer vs `users.manage`) |
| Worker confinement by path prefix | fingerprint worker credentials | `middleware/auth.go:271-306` |
| Peer allow-list | every listener, before gin | `internal/server/peer_allowlist.go:237` |

Route coverage (default build, 497 routes): 15 public; 36 authenticated with no route-level permission (listed in `09` section 4); the rest carry a permission or admin gate. 45 routes are retired `gone()` stubs that still run auth and permission first.

## 5. Bootstrap / first-run flow (sequence)

1. Startup seeds roles + `_system` user (`internal/server/server_lifecycle.go:1098-1112`), stamps never-expiring API keys (`:1114-1118`).
2. `InitBootstrapToken` writes a fresh `abbs_...` token (0600) into the secure state dir and its hash + expiry into settings (`internal/server/server_lifecycle.go:1133-1140`, `bootstrap.go:147-170`). Optional startup read-only key likewise.
3. With zero users the whole API is open (`RequireAuth` pass-through) and public `POST /api/v1/auth/setup` creates the first admin (min password 8 chars; 409 once any user exists) (`handlers/auth.go:267-292`).
4. Alternatively an operator reads the token file and calls public `POST /api/v1/auth/bootstrap`: rate-limited per client key, token validated first and then spent (`RedeemBootstrapToken` under `bootstrapMu`), admin found/created inside the spend, full-scope API key returned once with expiry (`bootstrap.go:305-470`).
5. Not part of the code path but documented in the repo skill `server-bootstrap` (not read here).

## 6. Audit logging of auth events

| Event | Where recorded | Anchor |
|---|---|---|
| ABS auth success/failure/denied/throttled/error | slog attribute `event=abs.auth` with an `Outcome` enum; credential values never logged | `internal/server/absauth/audit.go:14-40,93` |
| Credential-change refusal | warn log with method, user id, key id, route | `internal/server/middleware/credential_guard.go:46-60` |
| Failed web login | failure counted by throttle; error log only for lookup failures; no audit row for success/failure | `internal/server/handlers/auth.go:357-375` |
| API key create | slog info (id, user, name, scopes, expiry) | `internal/server/handlers/apikeys.go:243` |
| Guest service token minted/failed/revoked | activity-log rows ("audit" types) | `internal/server/guest_service_tokens.go:77-83,409,460,476,527` |
| Email displaced on verified SSO login | activity row type `auth_email_displaced` | `internal/activity/user_data.go:38-41` |
| User merge | activity row type `user_merge` | `internal/activity/user_data.go:47-48` |
| Admin debug edits and undo | activity rows with an actor | `internal/activity/user_data.go:49-52` |
| Activity log read access | `library.view`; account/identity rows withheld from callers without `users.manage` | `internal/activity/user_data.go:78-90` |
| Cloudflare Access verification failure | warn log (the log line includes the verified email on a not-admitted identity) | `internal/server/middleware/cfaccess.go:108-122` |

Gap: no uniform, queryable auth audit stream for the `/api/v1` surface (login success/failure, logout, role/permission changes, key revoke, session revoke); the `tier` stored on activity rows is derived from log level (`change`/`debug`), not an audit tier (`internal/activity/writer.go:188-201`). UNKNOWN whether other audit-typed rows exist beyond those greppable in `internal/activity`.

## 7. Secret storage

| Secret | Storage | Anchor |
|---|---|---|
| User passwords | argon2id PHC string in `User.PasswordHash` (+ `PasswordHashAlgo`); bcrypt legacy | `internal/server/absauth/password.go:131-190` |
| API keys | SHA-256 hex of raw token; raw never stored, shown once; list shows an identifier made from the first 8 hash chars | `internal/database/apikey_token.go:19-35`, `handlers/apikeys.go:143-146` |
| Bootstrap token | SHA-256 hex in settings + plaintext 0600 file (by design, for operator pickup) | `internal/server/bootstrap.go:147-170` |
| Session ids | stored raw (the id IS the bearer secret; 80-bit-entropy ULID from `crypto/rand`) - not hashed | `internal/database/pebble_store_auth.go:1656-1663`, `database/pebble_store.go:1050` |
| Encrypted settings | AES-GCM (random nonce, base64) keyed by a key file `.encryption_key` (0600) in the data dir, auto-generated if absent (refuses to overwrite an unreadable one) | `internal/database/settings.go:42-52,110-125,149,189-235` |
| Which secrets use it | exactly 4 (`openai_api_key`, `google_books_api_key`, `hardcover_api_token`, `basic_auth_password`), stored as individual `IsSecret` rows and zeroed in the config blob | `internal/config/persistence.go:1959-1962,1996-2008` |
| NOT encrypted at rest (INFERRED: the blob builder zeroes only the 4 fields above, so these stay in the plaintext `config_blob` setting or env/file) | `acoustid_api_key`, OAuth client secrets (GitHub, Google), Deluge web password, `DownloadClient...Deluge.Password`, `MetadataSources[].Credentials` (map), CF Access settings (not secrets); `acoustid_api_key` is in the "secret field" handling list but not in the encrypted-row list | `internal/config/config.go:1141,1588,1590,1777`, `internal/config/update_service.go:437-443` vs `internal/config/persistence.go:1996-2001` |
| Not in blob at all | `ABSJWTSecret` (`json:"-"`, environment only), `SMTPPassword` (`json:"-"`) | `internal/config/config.go:1636,1678` |
| API exposure | `GET /config` masks known secrets (`MaskSecrets`), restores masked values on round-trip; deep-copies metadata-source credentials | `internal/config/update_service.go:88-150`, `:362-372` |
| Log redaction | `?token=`/`api_key=` query masking in access log and recovery dump; invite token redaction in spans | `internal/server/server.go:695-704`, `internal/server/middleware/trace_redact.go` |
| Key rotation | none for the settings encryption key (single static key file) - UNKNOWN beyond grep; ABS secret rotation invalidates all ABS tokens (documented in `absauth/refresh.go:52`) | `internal/server/absauth/refresh.go:52` |

## 8. Gaps relative to a multi-app core with per-app permission scopes

Target examples: `books:edit`, `tv:request`, `core:admin`.

| # | Gap | Evidence | Consequence for the platform core |
|---|---|---|---|
| G1 | Permission vocabulary is a closed, hard-coded Go list; `IsKnown` rejects any atom not in the list, and key creation validates scopes through it | `internal/auth/permissions.go:91-129`, `internal/server/handlers/apikeys.go:200-203` | Apps cannot register their own atoms; need a registry (`Register(namespace, atoms...)`) consulted by `IsKnown`, seeds and key validation |
| G2 | No namespace: atoms are `area.verb` with the single app being implicit (`library.*`, `scan.*`), and `settings.manage`/`users.manage` are global | `permissions.go:24-85` | Introduce `<app>:<verb>` or `<app>.<verb>` namespacing; map existing atoms to `core:*` and `books:*`; migration for stored `Role.Permissions` and `APIKey.Scopes` |
| G3 | Roles are code-defined and seed-only; no role CRUD API, no per-app roles; seed upsert OVERWRITES a seed role's permissions at every boot | `internal/auth/seed.go:86-129`, no `/roles` route | Apps need to contribute seed roles (and operators need custom roles); seed merge must not clobber operator edits; `IsSeed` semantics need definition |
| G4 | Admin check by role NAME ("admin"), not permission, in middleware and handlers | `middleware/auth.go:522-537`, `handlers/user.go:285,441,453`, `wire_master_list_routes.go:41` | `core:admin` must be a permission (or the admin role must be checked by a role-capability, not string equality) so per-app admins are expressible |
| G5 | Route-to-permission binding is imperative (`s.perm(atom)` at each registration) with no machine-readable route metadata; 36 authenticated routes carry no permission, `playlists.create`/`requests.*` are defined but unenforced | `09` sections 1 and 4 | A core `Route{Method,Path,Perm,Class}` declaration would let the core audit coverage (e.g. a test that fails on route without permission) and generate docs |
| G6 | Scoped API keys: scope = same strings as permissions, effective = intersection; empty scopes = unrestricted; no per-app or resource-level scoping | `middleware/auth.go:405-425`, `handlers/apikeys.go:199-212` | Works for `app:verb` scopes once G1/G2 are fixed; consider "empty scope = nothing" for new keys, and per-key app binding |
| G7 | Single-tenant identity: one user table, one library; no per-app membership (a user cannot be admin of TV but viewer of Books except by atom composition) | `internal/database/store.go:734-780` | Roles-with-namespaced-atoms covers it; ownership of per-user data (playlists, reading state) is app-specific |
| G8 | Plugin route mount bypasses auth (`s.router.Group("/api/v1/plugins")`, not `protected`) | `internal/server/plugins_init.go:42`, `internal/plugin/router.go:23-27` | If apps/plugins register routes through `PluginRouter`, they would be unauthenticated; the core must hand apps an authenticated group |
| G9 | Auth wiring is inline in `setupRoutes` and closes over `*Server` and process-global `config.AppConfig.EnableAuth` | `internal/server/server_lifecycle.go:1693,1841-1848` | Extract an `auth.Module` (middleware factory taking a store interface + config) importable by a Sonarr-like app |
| G10 | Bootstrap and admin-recovery assume one binary: token file in the secure state dir, `findOrCreateAdminUser` picks "first active user with `users.manage`" | `internal/server/bootstrap.go:484-570` | For multiple apps sharing one core DB the bootstrap must be core-owned and app-agnostic |
| G11 | Owner/Cloudflare-Access-specific machinery (`owner_email`, guest service tokens, access-policy editing) is deployment-specific and embedded in the auth layer | `internal/auth/owner.go`, `internal/cfaccesspolicy`, `internal/cfservicetoken` | Keep out of the core or behind an optional "identity provider" extension |
| G12 | Secrets at rest: only 4 config secrets are encrypted; others plaintext in config blob; sessions stored unhashed; no key rotation | section 7 | Core "secret store" abstraction (encrypt-on-write for any field tagged secret, e.g. download-client and indexer credentials that Sonarr/Radarr-like apps will add in volume) |
| G13 | No uniform auth audit trail | section 6 | Core audit log (actor, method, route, permission, outcome) for all apps |
| G14 | Rate limiting / lockout are partly in-process memory structures keyed on client IP derivation tied to Cloudflare tunnel topology | `internal/server/login_ceiling.go`, `internal/server/bootstrap_rate_limit.go`, `peer_allowlist.go` | Fine for a single process; document as core behaviour, parameterize connector config |
| G15 | Two parallel auth stacks (web `RequireAuth` and ABS `ABSRequireAuth`) with separate session stores and token types | `internal/server/middleware/absauth.go:621`, `internal/database/pebble_store_abssession.go:49` | A compatibility surface of an app (like ABS) should stay in the app, but its identity must resolve to the same core `User` and permission set (it does today via `EffectivePermissionsFor`, `middleware/absauth.go:783`) |

Positive building blocks already core-shaped: `internal/auth` is small and dependency-light (imports `internal/database` only for the `User`/`Role` types and the two seed store interfaces: `seed.go:20-30`), the `Method`/`MayChangeCredentials` classification, `credRoute` pattern, intersection-based key scoping, per-def op permissions, and the golden "permission-prop" tests (`internal/auth/permissions_prop_test.go`).
