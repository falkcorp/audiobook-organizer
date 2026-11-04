<!-- file: docs/plans/platform-core/reviews/spec-round-1-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: a90040f0-8861-44b9-96c1-6ba21555fe50 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 1: security and RBAC lens

Reviewed: `docs/specs/platform-core/` (03 v1.2.0 normative). Code anchors are at `748fd787a`
(no code under `internal/` or `cmd/` differs between that commit and the spec checkout HEAD).
The fixed decisions D1–D9 are not attacked here.

Summary: **4 BLOCKER, 17 MAJOR, 9 MINOR** (30 findings).

| ID | Sev | Title |
|---|---|---|
| SEC-1 | BLOCKER | Rollback mirror can turn a narrow API key into a full-access key |
| SEC-2 | BLOCKER | The auth-disabled principal holds `core:admin`, which defeats `AuthRequired` |
| SEC-3 | BLOCKER | Worker-only confinement is lost, and the authn and rbac files contradict each other about it |
| SEC-4 | BLOCKER | Users with `core:users.manage` can take over more-privileged accounts (reset link, temp-login, set password) |
| SEC-5 | MAJOR | Facade-key confinement exists only in 08; 05-indexer requires the opposite scope |
| SEC-6 | MAJOR | Config keys for sign-in, proxies and the updater carry no `Protection`, so `core:settings.manage` can become `core:admin` |
| SEC-7 | MAJOR | OIDC linking by verified email across arbitrary generic providers lets one account take over another |
| SEC-8 | MAJOR | Seeding `core:system.view` gives viewers the system logs and status, which today need `settings.manage` |
| SEC-9 | MAJOR | Removing the task-start floor lets viewers start `settings.manage`-free jobs (bulk-apply preview) |
| SEC-10 | MAJOR | No CSRF or cross-origin defence in the chain; SameSite=Strict does not cover same-site sibling apps |
| SEC-11 | MAJOR | One default session-cookie name for every app on a host: tokens leak between apps and logins lock out |
| SEC-12 | MAJOR | Stage 11 is specified both as "never rejects" and as "a presented invalid credential is a 401" |
| SEC-13 | MAJOR | During the transition the legacy mount is authorized only by gin; the bridge is unspecified; S2 and H0 conflict |
| SEC-14 | MAJOR | The Basic-auth perimeter exemption becomes mount-wide, widening today's per-route ABS exemption |
| SEC-15 | MAJOR | SSRF and credential theft through integration instances (facade-pushed URLs, URL change keeps the secret) |
| SEC-16 | MAJOR | Restore trusts any age-encrypted archive; revoked API keys come back; the authn hook does not exist |
| SEC-17 | MAJOR | Audit gaps: restore erases its own audit record; no audit for integrations, diagnostics or export |
| SEC-18 | MAJOR | Client-IP trust migration (`TUNNEL_CONNECTOR_ADDRS`, zoned link-local) is missing; tunnels collapse throttles |
| SEC-19 | MAJOR | Session migration: 05-authn hashes only, but 09 mirrors raw tokens and keeps the fallback for the whole window |
| SEC-20 | MAJOR | Secrets key migration: 05-config, 09 S3 and 14 Q-E describe three incompatible keyrings |
| SEC-21 | MAJOR | Invite tokens sit in a core URL path that no redactor covers, and viewers can read the journal |
| SEC-22 | MINOR | `apikey` and `token` query values can reach trace span attributes |
| SEC-23 | MINOR | How `Sensitive` interacts with `core:admin` is undefined; 08 uses permission fields that 05-rbac does not have |
| SEC-24 | MINOR | Task approval: 05-tasks lets an API key approve, 05-rbac says approval needs an interactive method |
| SEC-25 | MINOR | Seed merge and `DefaultGrants` can silently push Sensitive or AuthRequired scopes into existing roles |
| SEC-26 | MINOR | `auth.System` and `rbac.All` are public SDK, so any module can mint a superuser principal |
| SEC-27 | MINOR | The updater signature does not bind app, version or channel; the signing key may be shared across apps |
| SEC-28 | MINOR | `DeriveKey` from the active master changes on rotation; the AAD binding to AppID makes a rename fatal |
| SEC-29 | MINOR | Secret naming and scoping disagree (06 `secrets.Scope` `<ID>/…` vs 05-config path names) |
| SEC-30 | MINOR | Setup's loopback exception is defeated by a local reverse proxy or tunnel connector |

---

## SEC-1 — BLOCKER — Rollback mirror can turn a narrow API key into a full-access key

**Where:** 09 §9.2.6 (lines 225-229, "dropped from the mirrored row, never widened"), 09 S4 Rollback
(line 341, "keys created on S4 work with reverse-mapped (possibly narrower) scopes"), 05-rbac §6.1
(lines 577-578), §6.3 (line 607).

**Evidence:** in the legacy binary, an empty scope list means "every permission of the owner's roles":
`internal/server/middleware/auth.go:405-408` (`if len(scopes) == 0 { return rolePerms }`). The reverse
rule drops every canonical scope that has no legacy atom. So a key created natively on S4 with scopes
made only of such permissions (`core:metrics.read`, `core:audit.view`, `core:tasks.view`,
`core:self`, `books:reading`, `core:system.view`, `core:roles.view`, and `core:admin` on a
bootstrap-style key) is mirrored as `apikey:` with `scopes: []`. After a rollback to S3 that key holds
**everything its owner's roles hold**. A Prometheus scrape key owned by an admin becomes an admin key.
"Never widened" holds for individual atoms. It fails for the set as a whole, because in the legacy
model an empty set is the widest value there is.

**Fix:** in 09 §9.2.3 / §9.2.6 and 05-rbac §6.3, add the rule: *"API-key reverse projection: if the
projected legacy scope set is empty while the native set is non-empty, the mirror row is written
with `Revoked=true` (or `ExpiresAt` = the creation time). A native key whose projection is a proper
subset is mirrored with the subset. A native key is never mirrored with `scopes: []` unless it is
`LegacyFullScope`."* Add `TestMirror_NarrowKeyNeverBecomesEmptyScope` (a property test over random
native scope sets, checking that the legacy effective set ⊆ the native effective set). Extend
`TestLegacyRoleReachUnchanged` to cover API keys with scopes, not only the 4 seed roles.

## SEC-2 — BLOCKER — The auth-disabled principal holds `core:admin`, which defeats `AuthRequired`

**Where:** 05-authn §4.11 ("all scopes except those declared AuthRequired"); 05-rbac §3.4 `All(c *Catalog)`,
§4.4 (the checker carries an `admin` flag: `Has(s)` = admin ∨ …), §4.6 ("`core:admin` … satisfies every
requirement"), §6.1 (lines 580-584: only `core:system.manage` and `books:admin.debug` are flagged
`AuthRequired`; `core:admin` is not).

**Evidence:** `core:admin` is a catalog permission and is not `AuthRequired`, so `All(catalog) \ AuthRequired`
still contains it. The compiled checker then sets `admin=true`, and `Has("core:system.manage")` returns
true. The "always-authenticated" class (store wipes, debug edits, credential surfaces) becomes reachable
by an anonymous LAN caller whenever auth is off. Today those routes are unreachable when auth is off:
`internal/server/wire_admin_role.go:18-45` keeps `RequireAdmin`, and `credential_guard.go:28` refuses
because no method is set. 05-authn also never classifies `MethodAuthDisabled` for `Interactive()` or
`MayChangeCredentials()`. If an implementer marks it interactive so the shell works, every
`Guards.Interactive` credential route opens.

**Fix:** in 05-rbac §6.1, flag `core:admin` itself as `AuthRequired`. In §3.4, state that
`All(c)` builds a checker with `admin=false` and no implication from `core:admin`, and contains
only concrete non-`AuthRequired` scopes. In 05-authn §3.1, state that `MethodAuthDisabled` is
non-interactive and `MayChangeCredentials=false` (fail-closed, as today). Say that in disabled mode
stage 11 ignores every presented credential, so a stolen key does not regain `AuthRequired` scopes.
Tests: `TestAuthDisabled_CheckerHasNoAdmin`, and `TestAuthDisabled_InteractiveGuardRefuses`.

## SEC-3 — BLOCKER — Worker-only confinement is lost, and the authn and rbac files contradict each other about it

**Where:** 05-authn §4.2, last paragraph ("refused to principals whose effective set is empty or worker-only");
05-rbac §3.1 `AnyAuthenticated`, §4.10 row "AnyAuthenticated" ("≥1 scope in the route owner's namespace or
`core:self`"); 05-http-api §8.2 (all 36 permission-less legacy routes become `rbac.AnyAuthenticated`);
05-rbac §6.2 (`fp-worker` = `books:fingerprint.worker` only).

**Evidence:** today a credential holding only `fingerprint.worker` is confined to
`/api/v1/fingerprint/worker/` (`internal/server/middleware/auth.go:264-306`, applied at `:180` and
`:254`). Under the rbac rule, `books:fingerprint.worker` *is* a scope in the books namespace, so the
worker key passes every books `AnyAuthenticated` route: reading progress, playlists, and every
other route among the 36. The authn claim that worker-only principals are refused is not implemented by
the rule that 05-rbac makes normative. The same rule also (a) lets facade keys (`tv:arr.*`) through
every TV `AnyAuthenticated` route (see SEC-5), and (b) refuses module-only keys on core routes that
serve module resources. Examples: a key holding only `books:scan` (the translation of `scan.trigger`)
fails the `AnyAuthenticated` floor of `POST /api/core/v1/tasks/definitions/{kind}/runs` (05-tasks
§5T.3.7) and of `GET /api/core/v1/events` (05-realtime §4.1), and so does the alias from
`/api/v3/ops/...`.

**Fix:** replace the namespace heuristic in 05-rbac §4.10 with two explicit mechanisms.
(1) `PermissionSpec.Confining bool`: a principal whose concrete scopes are all `Confining` may only
reach routes, topics and tasks whose requirement names one of those scopes explicitly. Never
`AnyAuthenticated`, and never a requirement that merely implies the scope. Flag
`books:fingerprint.worker` and every `<m>:arr.*` scope as `Confining`.
(2) `AnyAuthenticated` = the principal holds `core:self` or any non-`Confining` scope. Core routes that
serve module resources use per-resource dynamic checks, not the namespace of the route owner.
Update 05-authn §4.2 to cite this. Add `TestConfining_WorkerKeyRefusedOnPlaylists` (a regression
test for `auth.go:289`) and `TestConfining_FacadeKeyRefusedOnNative`.

## SEC-4 — BLOCKER — Users with `core:users.manage` can take over more-privileged accounts

**Where:** 05-authn §4.13 (`POST /users/{id}/temp-login`, `…/reset-password`, `…/disable`,
`PUT /users/{id}/roles`: perm `core:users.manage` + Interactive); 05-rbac §4.7 rule 2 (`CanAssign`
bounds only role *assignment*).

**Evidence:** `internal/server/auth_reset_password.go:18-60` hands the caller a temp-login link for
**any** user. `auth_temp_login.go:225-227` lets the minter be any `users.manage` holder. The doc comment
at `auth_reset_password.go:48-50` records that `PUT /api/v1/auth/me/password` with `user_id` sets any
user's password for `users.manage` holders. Today this is latent, because only seed `admin` holds
`users.manage` (`internal/auth/seed.go:47-59`). The spec makes roles editable data (05-rbac §4.7), so a
custom role with `core:users.manage` and nothing else can mint a temp-login for the admin and get a
delegated session with `core:admin`. It can also reset the admin's password, or strip `admin` from a
co-admin (removal is not covered by `CanAssign`). The escalation rules stop the role path but leave
this credential path open.

**Fix:** add an escalation rule 5 to 05-rbac §4.7, enforced in `auth.Service`: *"Any action that
targets another user's credentials, status or roles (temp-login, reset link, set password, disable,
enable, revoke sessions, create keys for, change roles, merge) requires that the actor's checker
dominates the target's effective set (`actor ⊇ target`), or that the actor holds `core:admin`. Removing
a role is checked like assigning one."* List `PUT /me/password` with a target user explicitly in
§4.13, or delete that path. Tests: `TestUsersManage_CannotTempLoginAdmin` and
`TestUsersManage_CannotResetDominatingUser`.

## SEC-5 — MAJOR — Facade-key confinement exists only in 08; 05-indexer requires the opposite scope

**Where:** 08 §8.5 ("`httpapi` refuses facade keys on every non-facade group", open item O9); 05-http-api
§4.2 stage 12 and §12 tests (no such rule); 05-indexer §3 (lines 270-271: "instances creatable by an API
caller holding `core:integrations.manage` via an API key").

**Evidence:** under 08 a facade key holds only `m:arr.read|request|indexers`, and a key holding any
other scope "is refused". 05-indexer requires the Prowlarr facade caller to hold
`core:integrations.manage` so that `Registry.Put` accepts it. Only two implementations are possible:
(a) the facade key carries `core:integrations.manage` and is therefore a general integrations-admin key
on every core route (the leak 08 says cannot happen), or (b) the facade calls `Registry.Put` with a
system principal, which bypasses rbac. Neither choice is written down. The refusal rule that 08
depends on is absent from the chain and the tests of the file that owns the chain.

**Fix:** move the rule into 05-http-api §4.2 stage 12 ("a `Confining` principal, SEC-3, is refused on
every route that does not name its scope; facade groups accept only facade-confined keys"). Add
`TestFacadeKey_RefusedOnCoreAndModuleGroups` to 05-http-api §12. In 05-indexer §3 replace the
`core:integrations.manage` requirement with this: *"the facade calls `Registry.Put` through
`integrations.FacadeWriter`, which accepts the principal when it holds `<m>:arr.indexers` and forces
`Origin=arrfacade` with the egress restrictions of SEC-15."* Make `KeySpec.Purpose`/`Module` (O9)
normative instead of optional.

## SEC-6 — MAJOR — Config keys for sign-in, proxies and the updater carry no `Protection`

**Where:** 05-config §1.3.1 (`Protection`: none/interactive/owner/operator), §1.3.4 (defaults:
`core.*` write = `core:settings.manage`); 05-authn §8, 05-http-api §10 and 05-updater §5U.8 key
tables (no protection column).

**Evidence:** ABO classifies 92 fields today (`internal/config/protected_fields.go:53`). The
`FieldSignIn` class includes `oauth_default_role` ("admin would mint admins", `:66`), the OAuth client ids
and redirect base, `enable_auth`, `cf_access_*` and `owner_email`. The spec keeps the mechanism but none
of the core key tables say which keys are protected. Worse, `ProtectInteractive` still only needs
`core:settings.manage`, which roles-as-data can now hand to non-admins. Concrete chains:
(1) add `core.auth.oidc.providers[]` with an issuer the actor controls → sign in through it with the
admin's email → linked to the admin by the verified-email rule (SEC-7).
(2) set `core.http.trusted_proxies` to a catch-all prefix + `client_ip_header` → spoof the client address
past throttles, rate limits and audit.
(3) set `core.updater.mode=apply` → the system-actor apply runs without the `core:admin` that
`POST /updater/apply` requires (05-updater §5U.5 row "Who").

**Fix:** add a `Protection` column to every key table in 05-authn §8, 05-http-api §10, 05-updater §5U.8,
05-ai, 05-download and 05-notify. Add a `ProtectAdmin` level (requires `core:admin` + an interactive
method) and assign it to every former `FieldSignIn` key, to `core.http.{trusted_proxies,client_ip_header,
peer_allowlist,cors.allowed_origins,basic_gate.*}` and to `core.updater.{mode,channel,restart_*}`.
Port `protected_fields_test.go` as `config.TestEveryURLPathOrSignInKeyClassified` (it fails when a key
whose name looks like a URL, path, executable or sign-in setting has no class).

## SEC-7 — MAJOR — OIDC linking by verified email across generic providers lets one account take over another

**Where:** 05-authn §4.7 ("generic OIDC … configured as a list"; "the existing verified-email takeover rule …
is kept").

**Evidence:** `internal/oauth/resolve.go:97` (`ResolveUser` step 4) links an unseen `(provider, subject)`
to *any* account holding the same verified email, trusting the IdP's `email_verified` claim. With two
fixed providers (Google, GitHub) that is a known trust set. With "generic OIDC configured as a list",
the trust set is every issuer an operator (or, via SEC-6, a settings holder) adds. Self-hosted IdPs
commonly let users set their own email with `email_verified=true`. One weak or hostile IdP then takes
over the admin account.

**Fix:** in 05-authn §4.7, give each provider `link_existing_by_email: bool` (default `false`;
`true` allowed only with `ProtectAdmin`), and never link by email to an account that holds `core:admin`
or has a password set unless the user proves the password in the same flow. Add
`TestOIDC_UntrustedProviderCannotLinkByEmail`.

## SEC-8 — MAJOR — Seeding `core:system.view` gives viewers the system logs and status

**Where:** 05-rbac §4.8 (lines 490-491: `editor` and `viewer` seeds hold `core:system.view`); 05-logging
(line 254: `GET /api/core/v1/system/logs` = `core:system.view`); 05-health §5H.3 (lines 165-166:
`/health` full snapshot, `/system/info`).

**Evidence:** today `GET /api/v1/system/status` and `GET /api/v1/system/logs` require `settings.manage`
(`internal/server/wire_system_routes.go:28,31`), which only admin holds. After S4 every viewer reads
the operational journal (file paths, user names, provider error text, and invite tokens, see SEC-21)
plus sysinfo and component detail. That breaks 09's own invariant (identical allow/deny for 572 routes
× 4 seed roles), and `settings.manage` does not expand to `core:system.view` (05-rbac §6.1), so the
spec is inconsistent with itself.

**Fix:** drop `core:system.view` from the editor and viewer seeds. If the shell needs a status banner,
add `core:system.status` (readiness tokens only) for them. Map `/system/logs`, `/system/info` and the
detailed health route to `core:system.manage` (or a new `core:system.logs`), held only through
`core:admin` by default. Add both routes to the `TestLegacyRoleReachUnchanged` sample.

## SEC-9 — MAJOR — Removing the task-start floor lets viewers start jobs

**Where:** 05-tasks §5T.3.7 table row "start" ("today's `scan.trigger` floor … is replaced by the per-def
check, not kept as a second atom").

**Evidence:** the floor exists on purpose: "without it a viewer could start any def that declares only
library.view" (`internal/server/handlers/opsv3/handler.go:236-246`). Such a def exists:
`bulk-apply-preview` declares `Permissions: []auth.Permission{auth.PermLibraryView}` with a 4 h timeout
(`internal/server/bulk_apply_preview.go:265-278`). Translated, it gets `Start = books:view`, so every
viewer, and every `books:view` key, can start a 4-hour library-wide CPU job. This is a deny-to-allow
change that breaks 09 §9.2.6.

**Fix:** in 05-tasks §5T.3.7, *"`Perms.Start` must not be satisfiable by a view-class scope; registration
fails if `Start` names a scope whose `PermissionSpec.Class` is `view`, unless the definition sets
`StartableByViewers: "<reviewed reason>"`."* In the legacy-def translation, map an op def's
`[library.view]` to `AllOf(books:scan, books:view)`, which preserves the floor. Add
`TestStart_ViewOnlyDefRefusedForViewer`.

## SEC-10 — MAJOR — No CSRF or cross-origin defence in the chain

**Where:** 05-http-api §4.2 (stages 0–15 contain no cross-origin check); 05-authn §4.3 (relies on
`SameSite=Strict`); 07-ui-shell O5 ("CSRF protection model … → 05-authn"), which 05-authn never answers.

**Evidence:** SameSite is decided per *site* (registrable domain), not per origin or port. The target
topology is several self-hosted apps side by side: ABO plus the TV and Movies apps and third-party *arr
tools on the same host or sibling subdomains. Any of them, or an XSS in any of them, is "same-site" and
receives no SameSite protection. Bodiless state changes are then forgeable with a plain form POST: user
disable, role reset, logout, task cancel/approve/decide, and task start when the body is empty. JSON
routes are protected only by the 415 rule. Today ABO adds per-handler custom-header checks for exactly this
reason (`internal/server/handlers/repairs/handler.go:27-28`); the design drops them.

**Fix:** add stage 11a to 05-http-api §4.2: *"For unsafe methods on a principal whose credential came from
a cookie, apply `net/http.CrossOriginProtection` (stdlib since Go 1.25; the toolchain pin is 1.27):
reject when `Sec-Fetch-Site` is not `same-origin`/`none`, or, without it, when `Origin` does not match the
host. Trusted origins come from `core.http.cors.allowed_origins`. Bearer and API-key requests are
exempt."* Close 07 O5 with a pointer to it. Test: `TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused`.

## SEC-11 — MAJOR — One default session-cookie name for every app on a host

**Where:** 05-authn §4.3 and §8 (`core.auth.session.cookie_name` default `mc_session`, `Path=/`, no
`Domain`), §4.2 step 4 and §6 row 2 (a presented invalid credential → 401, no fall-through).

**Evidence:** cookies are not isolated by port. A TV app at `host:8989` and a Movies app at `host:7878`,
both built on core, share `mc_session`. Each app (a) receives the other's bearer session token on every
request, and (b) overwrites the other's cookie on login. Because a presented invalid credential is a
401 with no fall-through, a stale cookie from the sibling also blocks that app's own login and Public
routes until the user clears cookies.

**Fix:** default the cookie name to `<AppID>_session`, prefixed `__Host-` whenever `Secure` is set, in
05-authn §4.3 and §8. Rule: an invalid *cookie* credential on a `Public` route is treated as anonymous
and the response clears the cookie. Invalid Bearer or API-key credentials stay a 401. Test:
`TestCookie_SiblingAppCookieDoesNotBlockLogin`.

## SEC-12 — MAJOR — Stage 11 is specified both as "never rejects" and as "invalid credential is a 401"

**Where:** 05-http-api §4.2 stage 11 ("**does not reject**", line 431); 05-authn §3.2 `Middleware` ("never
writes a response except 503", line 229) vs §3.3 (`Authenticator` error → "refuse the request (401, fail
closed …)") and §6 row 2.

**Evidence:** if stage 11 never rejects and leaves the request anonymous, an implementation will
naturally try the next authenticator after an error. That breaks "a presented-but-invalid credential
never falls through to a weaker one", and with it the key-wins rule, which today blocks recording a
key-plus-Access-cookie request as an interactive login (`middleware/auth.go:76-99`). If stage 11 does
not continue, a Public route with a bad credential either 401s or silently runs anonymous; the spec
does not say which.

**Fix:** state in both files that stage 11 stops at the first authenticator returning anything other
than `ErrNoCredential`, and records `auth.Failure{Code}` in the context without writing a response.
Stage 12 then answers 401 with that code on non-Public routes, and applies SEC-11's cookie rule on
Public routes. Test: `TestStage11_InvalidKeyPlusValidSessionIs401`.

## SEC-13 — MAJOR — During the transition the legacy mount is authorized only by gin

**Where:** 05-http-api §8.1 H0 (line 633), §8.2 (`LegacyAuthCore`: "stage 11 binds a Principal; gin bridge
enforces"); 09 S2 ("global gin middleware … stays inside it") vs H0 ("core chain stages 0–10 run outside
gin … removed from the gin engine"); 09 S4 ("Gin `s.perm(atom)` becomes a thin adapter over
`rbac.Require`").

**Evidence:**
- (a) The manifest (`LegacyRoute.Perm`) is documentation only. ServeMux matches the `/api/v1/` subtree,
  so stage 12 never sees a legacy route, and enforcement is entirely the gin bridge.
  `archtest.RoutesHavePermissions` then checks a list, not the running chain.
- (b) `legacyauth.FromCorePrincipal` appears once and has no contract. It does not say what it does
  for: zero users (`middleware/auth.go:165` passes everything), the worker confinement, the Cloudflare
  fail-open binder, `RequireAdmin` by role name (the key-scope bypass, 05-rbac out-of-scope defect 1),
  `credGuard`, `RequireOwner`, and the `adminRole` pass-through vs `RequireAdmin` 401 split when auth is off
  (`wire_admin_role.go:18-45`).
- (c) H0 needs `auth.Service` for stage 11, which exists only at S4. S2 and H0 cannot both be right.

**Fix:** in 05-http-api §8.2, register **each manifest route as its own ServeMux pattern** (gin path →
mux pattern), with its `Perm` and `Guards` enforced by stage 12 and the gin engine as the handler.
Requests matching no manifest route get a problem 404, not gin `NoRoute`. Add a bridge contract table
with one row per legacy behaviour listed in (b). In 09, add a column per stage saying which stack
authenticates and authorizes (S2–S3: gin only, core stages 11–12 off for the mount; S4+: core, with gin
bridge read-only). Extend `TestLegacyRoleReachUnchanged` to API keys with scopes and to auth-off mode.

## SEC-14 — MAJOR — The Basic-auth perimeter exemption becomes mount-wide

**Where:** 05-http-api §4.2 stage 7 ("exempts infra probes and compat mounts that declare own auth").

**Evidence:** today's exemption is decided per route from the handler chain, and the code explains why a
prefix cannot work: `/ping`, `/status`, `/login`, `/auth/refresh`, `/auth/openid*`, the cover routes and
`/public/session/...` carry no `ABSRequireAuth` and **stay behind Basic Auth**
(`internal/server/middleware/basicauth.go:51-70`). A mount-level exemption for the ABS `LegacyAuthOwn`
mount removes the perimeter from those routes for operators who rely on it.

**Fix:** add `LegacyRoute.OwnAuth bool` (true only when the route's chain runs the compat
authenticator, derived when the manifest is recorded). Stage 7 exempts per route, never per mount.
Test: `TestBasicGate_ExemptSetEqualsHEAD` (a golden test of today's exempt set).

## SEC-15 — MAJOR — SSRF and credential theft through integration instances

**Where:** 05-metadata M1 (`EgressAny` for "operator-configured targets incl. LAN"), M1 `Registry.Put/Test`;
05-indexer §3 (facade `Registry.Put` with `Origin=arrfacade`; fetches use `EgressSameHostOrPublic`, where
"own host" is the instance's host); 08 §8.5 (`<m>:arr.indexers` keys push indexers).

**Evidence:**
- (1) A facade key (one leaked Prowlarr key) can create an indexer whose `baseUrl` points at any LAN
  service or link-local metadata endpoint. Scheduled RSS and search then fetch it with operator trust,
  and `POST /indexer/test` (unsaved `Registry.Test`) returns the outcome: an SSRF oracle. "Same host"
  for download links is the attacker's chosen host.
- (2) Any `core:integrations.manage` holder (a grantable non-admin permission) can change the URL of an
  existing download-client, indexer or AI instance and keep the stored `secrets.Ref`. The adapter then
  sends the stored password or API key to the new host. Personal notify webhooks are restricted
  (05-notify, line 243), but no equivalent rule exists for other kinds or for facade-origin instances.

**Fix:** in 05-metadata M1, define *"An instance not created by an interactive principal holding
`core:integrations.manage` (Origin `arrfacade`, or created by an API key) gets `EgressPublicOnly`, unless
its host is in `core.integrations.egress.lan_allow` (operator, `ProtectAdmin`)."* On `Put`, when the
scheme, host or port of any URL field changes, every secret field of the instance is cleared unless it is
re-supplied in the same request, and an audit record is written (SEC-17). `Test` returns only the class
token and status, never response bodies or remote error text. Tests:
`TestRegistry_URLChangeDropsSecrets` and `TestFacadeIndexer_LANRefused`.

## SEC-16 — MAJOR — Restore trusts any age-encrypted archive; revoked API keys come back

**Where:** 05-backup B3, §5B.3.3 (age recipients), §5B.3.4 (`core:backup.restore`), `Import`, line 36
("05-authn implements the hook this file calls"), error table line 390 ("Session revocation hook");
05-rbac §6.1 permission list.

**Evidence:**
- (1) age gives confidentiality, not authenticity. Recipients are public, so anyone can produce an archive
  that "decrypts" with the install identity. An imported archive can therefore replace users, roles,
  API keys and `secrets/master.key`: account takeover.
- (2) `core:backup.restore` does not appear in the 05-rbac catalog (§6.1 new-permission list).
- (3) 05-authn has no restore hook at all (no section mentions restore except owner-guarded routes).
  The backup file names only *session* revocation, so API keys revoked after the backup was taken are
  live again after the restore.

**Fix:** in 05-backup, the manifest carries an HMAC with a key derived from the install keyring
(`DeriveKey("mediacore/backup/manifest/v1")`), or an install ed25519 signature. Archives without a valid
MAC are "foreign": restoring one requires `core:admin` + owner/interactive + `foreign: true` in the
request, and is audited. Add `core:backup.restore` (`Sensitive`, `AuthRequired`) to 05-rbac §6.1. Add
a 05-authn §4.14 "post-restore hook": revoke all sessions and one-time tokens, set every API key
created before the restore time to `needs_confirmation` (disabled until an admin re-enables it), and bump
every user's `CredentialEpoch`.

## SEC-17 — MAJOR — Audit gaps

**Where:** 05-rbac §7.1 action table, §7.3, §7.6; 05-backup restore; 05-http-api §8 (legacy mount).

**Evidence:**
- (1) The `core.system.backup.restored` record is written into the live DB at stage time, and that DB is
  then replaced at the next boot. The audit trail of the most powerful admin action erases itself,
  together with every record since the archive was taken. The hash chain cannot detect this (§7.4 rules
  tail truncation out of scope).
- (2) The table has no `core.integration.*` actions (instance create, update, URL change, secret write,
  delete, test), no `core.secret.rotated`, no `core.diagnostics.downloaded`, no `core.audit.exported`,
  no backup import/download, and no `core.oidc_provider.*` beyond the generic `settings.changed`.
- (3) Denials inside the gin bridge during H0–Hn are not routed to `audit` (only stage 12 and
  `rbac.Require` are named).

**Fix:**
- (1) After the restore swap, the kernel copies the pre-swap audit records newer than the archive's
  `CreatedAt` from the undo directory into the restored store, then appends `core.system.backup.restored`
  before listeners open. Fail boot if that copy fails.
- (2) Add the missing actions to §7.1 as sync.
- (3) The bridge calls `rbac.Require`, which audits.

## SEC-18 — MAJOR — Client-IP trust migration is missing; tunnels collapse throttles

**Where:** 05-http-api §3.5 (`TrustedProxies []netip.Prefix`, `ClientIPHeader`), §10 aliases (only
`allowed_peer_cidrs`); 05-authn §4.9.

**Evidence:** ABO's trust model is `TUNNEL_CONNECTOR_ADDRS` (the tunnel connector peers whose CDN
client-IP header is believed, and which are always admitted by the peer gate), with IPv6 link-local
entries **with zones** (`internal/server/client_trust.go:32,91-130,201`;
`internal/server/peer_allowlist.go:28-34,223`). No spec file mentions it. `netip.Prefix` cannot carry a
zone. With the default `trusted_proxies=[]`, every tunnelled request has the connector's (or loopback)
address. One attacker's 15 failures then trip the per-source login throttle for all users, the
per-client rate limit is shared, and audit `ClientAddr` is useless.

**Fix:** in 05-http-api §3.5 and §10, add `core.http.trusted_proxies` entries of type `netip.Addr`
with zone *or* `netip.Prefix`, an alias from `TUNNEL_CONNECTOR_ADDRS` (zone-resolution semantics kept from
`client_trust.go:133-201`) that also admits those peers in stage 0, and an ABO default
`client_ip_header=CF-Connecting-IP`. Test: `TestClientIP_ZonedConnectorTrusted`.

## SEC-19 — MAJOR — Session migration: hashed-only in 05-authn, raw mirror in 09

**Where:** 05-authn §4.3, §11 (lines 728-735, recommendation "no mirror", tests assume it); 09 S4
Data/keys (line 336: "mirrored raw to `sess:` … fall back to legacy `sess:` for 7 days"); 09 C4 ("drop
`sess:` fallback"); 14 Q-D.

**Evidence:** 09 states the raw mirror as the plan, not as an option. With the mirror on, every new
session also exists raw, so the "7-day" fallback actually lasts the whole window W (two minor releases,
until C4). Plaintext bearer tokens stay at rest the whole time, and the diagnostics command prints
`sess:` values. A native revoke that does not also delete the mirror row resurrects the session through
the fallback.

**Fix:** make 09 S4 match 05-authn: no raw mirror. Fallback lookup in `sess:` only for legacy tokens,
with epoch, status and revoked checks (09 risk row S4). On a fallback hit, issue a hashed `mcs_`
session, set the cookie, and delete the legacy row (upgrade on use). The fallback code is removed at
S4 + 7 days, independent of C4. Close Q-D in 14 accordingly.

## SEC-20 — MAJOR — Secrets key migration: three incompatible keyrings

**Where:** 05-config §2.4 step 1 and §2.10 step 1 (import `.encryption_key` as retired `k0-legacy`, generate
a new active key, re-encrypt with AAD); 09 S3 (adopt "with the **same** key file (path unchanged)";
mirror legacy rows); 14 Q-E ("make the previous release read the encrypted copy").

**Evidence:** under 05-config, secrets written on S3 use the new key with AAD, which the S2 binary cannot
decrypt. 09's mirror must therefore keep writing legacy-format rows under `.encryption_key`, which is
a separate cipher path that 05-config does not specify. Q-E's recommendation (scrub plaintext and let S2
read the encrypted copy) only works if that path exists. Otherwise the outcome is either plaintext kept
for W or secrets lost on rollback.

**Fix:** in 05-config §2.10, *"During W, `k0-legacy` remains readable and the mirror writer re-encrypts
each changed secret in the legacy format (no AAD, `.encryption_key`) into `setting:<legacy-name>` in the
same batch. Plaintext blob fields are zeroed at S3 (Q-E). `Rotate` is refused until C3."* Align 09 S3
wording ("same key file" → "legacy key imported as `k0-legacy`; legacy-format mirror").

## SEC-21 — MAJOR — Invite tokens sit in a core URL path that no redactor covers

**Where:** 05-authn §4.13 (`GET /auth/invites/{token}`); 05-logging (line 343: `RedactInvitePath` "stays
app-side, registered as a books redaction hook"); 05-http-api stage 2 (tracing redaction = invite path of
the old route).

**Evidence:** the new core route puts a bearer one-time token (`mci_…`, which creates an account with the
invite's role) in the path of a **core** URL. The only path redactor is the books hook for the legacy
path. The access log, the journal (readable by viewers under SEC-8) and span `url.path` therefore record
it.

**Fix:** move the token out of the path (`POST /auth/invites/lookup {token}`; the SPA reads it from the
URL fragment), or make core's redactor cover `/api/core/v1/auth/invites/*` and the `token` query
parameter on `/auth/temp-login`. Test: `TestAccessLog_NoInviteToken`.

## SEC-22 — MINOR — `apikey` and `token` query values can reach trace span attributes

**Where:** 05-http-api stage 2 (redaction covers the invite path only); 08 §8.5 ("never reaches access logs
or traces").

**Evidence:** `otelhttp` records the request target and query as span attributes. 05-logging's redactor
runs in the slog pipeline, not on spans.

**Fix:** stage 2 sets `otelhttp.WithSpanOptions` and a span-attribute filter that applies the same URL
redactor (`log.Redactor`). Add `TestTrace_NoApikeyInSpanAttributes` to 05-http-api §12.

## SEC-23 — MINOR — How `Sensitive` interacts with `core:admin` is undefined

**Where:** 05-rbac §3.2 (`Sensitive`: "a check of it fails for a non-interactive method"), §4.6
(`core:admin` satisfies every requirement); 08 §8.5 (`Dangerous: true`, `rbac.PermissionDef`,
`Manifest.Grants`).

**Evidence:** for the bootstrap key (`["core:admin"]`, method `api_key`) or an admin automation key, the
spec does not say whether `Has(core:roles.manage)` is true. 08 uses names (`Dangerous`,
`PermissionDef`, `Grants`) that 05-rbac does not define. An implementer mapping `Dangerous` to
`Sensitive` breaks the facade, and mapping it to nothing loses the UI warning.

**Fix:** in 05-rbac §4.4, *"`Sensitive` is evaluated before the admin flag: a non-interactive principal
never satisfies a Sensitive scope, `core:admin` included."* Rename 08's fields to
`PermissionSpec`/`DefaultGrants`, and add `Warn bool` (UI-only) to `PermissionSpec` for `arr.request`.

## SEC-24 — MINOR — Task approval: API key allowed in 05-tasks, interactive required in 05-rbac

**Where:** 05-tasks §5T.3.7 ("interactive session **or an issued API credential**", line 530); 05-rbac §4.10
("approvals need an interactive method"); code `internal/server/handlers/opsv3/runs.go:395-410` allows
API tokens.

**Fix:** pick one rule in both files. Recommended: keep today's rule (session, API key, or verified
Cloudflare Access), but require the key to name the `Approve` scope explicitly, refuse
`LegacyFullScope` keys, and refuse `MethodSessionDelegated`.

## SEC-25 — MINOR — Seed merge and `DefaultGrants` can silently push Sensitive or AuthRequired scopes into existing roles

**Where:** 05-rbac §4.8 ("a permission a new release grants to `editor` reaches every `editor` role the
operator did not explicitly strip"), §3.2 `DefaultGrants`.

**Fix:** §4.9 adds a validation rule: `DefaultGrants` may not name a `Sensitive` or `AuthRequired`
permission, or any `*:settings.manage` or `*:integrations.manage`. In §4.8, a seed delta that adds such a
permission to an existing role is held as `pending_seed` until an admin accepts it in the Roles page.
`core.role.seed_updated` already records it.

## SEC-26 — MINOR — `auth.System` and `rbac.All` let any module mint a superuser principal

**Where:** 05-authn §3.1 (`func System(perms rbac.Checker) *Principal`, public); 05-rbac §3.4
(`func All(c *Catalog) Checker`, public). Modules are trusted (14 K5), but a handler that builds a
background context this way silently escalates a request.

**Fix:** move both to `kernel/` (or make `System` accept only a `tasks`-issued `SystemGrant` built from the
definition's declared permissions), and add `archtest.NoSystemPrincipalOutsideKernel`.

## SEC-27 — MINOR — The updater signature does not bind app, version or channel

**Where:** 05-updater §5U.3 `TrustedKey`, §5U.4 steps 3-5, §5U.10 (one CI key; the TV and Movies apps are
expected to use the same pipeline).

**Evidence:** an ed25519 signature over `checksums.txt` alone lets any validly signed older release or
sibling-app release be served under a new tag. Only the self-test (step 5), which *executes* the
downloaded binary, detects the substitution.

**Fix:** sign a canonical manifest `{app, version, channel, assets: {name: sha256}}` and verify `app` and
`version` before step 4. Use one key per app. Ship a compiled-in revoked-key-id list. Self-test stays
as a second check.

## SEC-28 — MINOR — `DeriveKey` from the active master changes on rotation; AppID in the AAD makes a rename fatal

**Where:** 05-config §2.3 (`DeriveKey(info)` from "the active master"; AAD = `AppID + name`), §2.4 step 4.

**Evidence:** after `Rotate`, every derived key changes: OAuth state codec, cursor HMAC, ABS refresh
derivation (named as users). Every outstanding refresh token and cursor becomes invalid with no
spec'd handling. A change of `app.Options.AppID` makes every stored secret `ErrCorrupt`.

**Fix:** derive from a dedicated `derivation` key in the keyring that `Rotate` does not replace (separate
command `rotate-derivation`), or embed the kid in each derived artifact. Make AppID a compiled constant
checked against a stored `c/secrets/app_id` at open (boot error on mismatch).

## SEC-29 — MINOR — Secret naming and scoping disagree

**Where:** 06 §6.4.4 (`Secrets secrets.Scope // names under "<ID>/"`; `TestDepsScoping`), 05-config §2.2
(new names `modules.books.acoustid.api_key`, `core.download.clients[<id>].password`), §2.3 (no `Scope`
type defined).

**Fix:** define `secrets.Scope` in 05-config §2.3 with prefix `modules.<id>.`. Integration-instance
secrets are reachable only through `integrations.Env.Secrets` (declared fields), never through a
module's `Scope`. Fix 06's `<ID>/` text.

## SEC-30 — MINOR — Setup's loopback exception is defeated by a local reverse proxy or tunnel

**Where:** 05-authn §4.6 step 3 ("unless `core.auth.setup_requires_token=false` *and* the peer is
loopback"); §4.11 (auth-off non-loopback check is listener-based).

**Evidence:** with a local reverse proxy or tunnel connector (ABO's documented topology,
`peer_allowlist.go:34`: "anything on this host reaches the service as loopback"), every internet
request has a loopback peer.

**Fix:** "loopback" means a loopback peer **and** no forwarding header **and** the peer not in
`trusted_proxies`/connector set. Apply the same test to auth-off mode: refuse to start disabled when any
trusted proxy or connector is configured, unless `disabled_allow_remote=true`.
