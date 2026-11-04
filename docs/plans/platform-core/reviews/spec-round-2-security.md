<!-- file: docs/plans/platform-core/reviews/spec-round-2-security.md -->
<!-- version: 1.0.0 -->
<!-- guid: ca9f21e3-3af7-4e3d-8980-551f66adaec2 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 2: security and RBAC lens

Reviewed: `docs/specs/platform-core/` at the integration-pass head (03 v1.4.0, 05-http-api v1.2.0,
05-authn v1.2.0, 05-rbac v1.2.0, 09 v1.1.1). Code anchors are at `748fd787a`. Binding inputs:
round-1 dispositions (cross-cutting resolutions 2, 5, 8 and 12 are not attacked; findings below
test whether they work as written) and `spec-round-1-security.md`.
Fixed decisions D1–D9 are not attacked.

**Summary: 3 BLOCKER, 14 MAJOR, 8 MINOR (25 findings).**

## A. Regression check of round-1 SEC-1 … SEC-30

| Round-1 | Status in round 2 | Where checked | Finding |
|---|---|---|---|
| SEC-1 never widen (keys) | **Half-fixed**: the previous build can re-activate a mirror-disabled row | 05-rbac §6.3, 05-storage §5S.3.11, 09 §9.2.3 | SEC2-1 |
| SEC-1 never widen (roles) | **Not covered**: a role mirrored with an exact atom still gets wider in the legacy build | 05-rbac §6.1/§6.3 | SEC2-2 |
| SEC-2 auth-off principal | Fixed (05-rbac §4.11, 05-authn §4.2 step 0, §4.11) | — | (auth-off reach drift: SEC2-21) |
| SEC-3 confinement | Fixed for stage 13; **lost on the 75 `OwnAuth` ABS routes** | 05-http-api §8.3 | SEC2-4 |
| SEC-4 `users.manage` bound | Fixed natively (05-rbac §4.7 rule 5); legacy-alias retargeting and rollback reopen it | — | SEC2-2, SEC2-5 |
| SEC-5 facade keys | Fixed (05-http-api stage 13, 05-authn §4.4, 05-indexer §3) | — | — |
| SEC-6 Protection | Fixed for authn, http-api, updater, logging, metrics, realtime; **missing in 05-backup and the other 05-* key tables** | 05-backup §5B.7 | SEC2-8 |
| SEC-7 OIDC email linking | Fixed (05-authn §4.7) | — | — |
| SEC-8 system.view seed | Fixed (05-rbac §4.8, 05-health §5H.3.3, 05-logging §5) | — | — |
| SEC-9 task-start floor | Fixed (05-tasks §5T.3.7, 05-rbac §4.10) | — | — |
| SEC-10 CSRF | Fixed (05-http-api stage 7) | — | (S2 audit gap: SEC2-20) |
| SEC-11 cookie names | Fixed (05-authn §4.3) | — | — |
| SEC-12 stop rule | Fixed (05-authn §4.2, 05-http-api stages 12–13) | — | — |
| SEC-13 transition | Fixed for S2/S4 split; the generic `MountLegacy` API now exposes the bypass to every module | 05-http-api §8.2 | SEC2-16 |
| SEC-14 Basic per route | Fixed (stage 8, `LegacyRoute.OwnAuth`) | — | — |
| SEC-15 SSRF / URL change | URL-change rule fixed; **`Local` trust is weaker than `lan_allow`** and 05-config/05-metadata disagree | 05-metadata M1.3 | SEC2-7 |
| SEC-16 restore | Provenance MAC fixed; **revocations re-applied only to the rebuilt-from-legacy copy**, journal unauthenticated, `role.removed` unrepresentable, passwords restored | 05-authn §4.14, 05-backup §5B.3.6 | SEC2-3, SEC2-9, SEC2-10, SEC2-11 |
| SEC-17 audit | Actions added; **copy-forward collides with the restored sequence** | 05-rbac §7.1/§7.2 | SEC2-12 |
| SEC-18 client IP | Fixed (`TrustedPeer`); S2 gin-side users not covered | 05-http-api §8.4 | SEC2-19 |
| SEC-19 sessions | Fixed (no raw mirror; 05-authn §11, 09 §9.2.3, 14 Q-D) | — | — |
| SEC-20 keyring | Fixed (one keyring, 05-config §2.3/§2.4, 14 Q-E) | — | — |
| SEC-21 invite tokens | **Half-fixed**: 03/07/09 still put the token in the shell path | 07 §7.4.3, 09 S9 | SEC2-6 |
| SEC-22 span redaction | Fixed in principle; key list regressed vs HEAD | 05-logging §3 | SEC2-18 |
| SEC-23 Sensitive vs admin | Fixed (05-rbac §4.4 rule 1) | — | — |
| SEC-24 approval | Fixed and consistent (05-tasks §5T.3.7 = 05-rbac §4.10) | — | — |
| SEC-25 seed restriction | Fixed (05-rbac §4.8/§4.9 `PendingSeed`, `ErrRestrictedGrant`) | — | — |
| SEC-26 system principal | Fixed (`core/internal/authz`, `authsys`); module system actor undefined | 05-tasks §5T.3.10 | SEC2-15 |
| SEC-27 updater signature | Fixed (signed manifest, per-app key, revocation list); unsigned path is pure attack surface after S8 | 05-updater §5U.5 | SEC2-23 |
| SEC-28 derive key | Fixed for rotation stability; no rotation path for the derive key itself | 05-config §2.3 | SEC2-24 |
| SEC-29 secret naming | Fixed (05-config §2.3 `Scope`, 06 `Deps.Secrets`) | — | — |
| SEC-30 local setup | Fixed (05-authn §4.6 item 4, §4.11) | — | — |

## B. Findings

| ID | Sev | Title |
|---|---|---|
| SEC2-1 | BLOCKER | SEC-1 half-fixed: the previous build's owner can re-activate a mirror-disabled key, and re-upgrade then adopts it as `LegacyFullScope` |
| SEC2-2 | BLOCKER | Rollback turns a native, dominance-bounded `core:users.manage` (or `settings.manage`) custom role into legacy admin power |
| SEC2-3 | BLOCKER | Post-restore revocations are written to the native copy, which the next boot rebuilds from legacy: revoked keys and epochs come back |
| SEC2-4 | MAJOR | Worker/facade confinement is deleted from the ABS surface: `OwnAuth` routes skip stage 13 and the bridge drops `abortWorkerOnlyOutsideWorkerAPI` |
| SEC2-5 | MAJOR | Aliases cannot express legacy routes that multiplex on a body/query field; non-strict alias decoding retargets the action to the caller; 05-authn and 05-http-api disagree on `PUT /api/v1/auth/me/password` |
| SEC2-6 | MAJOR | SEC-21 half-fixed: 03, 07 and 09 keep `/invite/{token}` as a shell path while 05-authn moved the token to the URL fragment |
| SEC2-7 | MAJOR | SSRF trust inversion: `InstanceConfig.Local` (any private address) needs only interactive `core:integrations.manage`, while the narrower `lan_allow` needs `ProtectAdmin`; 05-config and 05-metadata contradict each other on who may write URL fields |
| SEC2-8 | MAJOR | SEC-6 incomplete: 05-backup (and five other 05-* key tables) have no Protection column; `core.backup.recipients`/`encryption` let a settings holder read or downgrade every future archive; backup routes have no permission table |
| SEC2-9 | MAJOR | The out-of-DB revocation journal has no integrity: deleting or truncating lines silently resurrects revoked credentials on restore |
| SEC2-10 | MAJOR | `backup.Revocation` cannot represent a role removal (one `Subject`); rbac's re-application is undefined, so a demoted admin is re-promoted by restore |
| SEC2-11 | MAJOR | Restore brings back the pre-compromise password hash: a post-archive password change is journaled only as an epoch bump |
| SEC2-12 | MAJOR | Audit copy-forward reuses `seq` values the restored store's `Sequence` will reissue: new records overwrite the copied trail |
| SEC2-13 | MAJOR | Diagnostics bundle download is gated on "the run's actor", not on the presenting credential: a narrow key or delegated session of an admin downloads logs, config and sysinfo |
| SEC2-14 | MAJOR | Infra routes are outside the chain's "routes" scope, so `/metrics`' `core:metrics.read` is not enforced by stages 12–13 |
| SEC2-15 | MAJOR | The module "system actor" that `Client.Start` stamps outside a request has no defined permission set |
| SEC2-16 | MAJOR | No registration path for authenticators/compat methods in 06; whoever adds one decides `Interactive`, `MayChangeCredentials`, `OwnerVerifier` and the returned `Perms` |
| SEC2-17 | MAJOR | `MountLegacy` is a permanent public API: any module can mount with `LegacyAuthGin` or mark routes `OwnAuth`+`Delegated` and skip stages 8, 12, 13 |
| SEC2-18 | MINOR | Core's URL redaction list is narrower than HEAD's (`code`, `code_verifier`, `refresh_token`, `secret` lost); 05-http-api stage 2 lists a third, shorter set |
| SEC2-19 | MINOR | S2 moves `client_trust.go` to core while gin-side bootstrap/invite/ABS limiters keep calling `trustedClientIP` until S4 |
| SEC2-20 | MINOR | Stage 7 and stage 13 refusals are "audited" from S2, but `audit` exists only from S4 |
| SEC2-21 | MINOR | Auth-off reach changes for logs/status/info (`core:system.manage` is `AuthRequired`) despite the "auth-off reach identical" invariant |
| SEC2-22 | MINOR | ServeMux (escaped segments) and gin (`UseRawPath=false`, decoded path) can pick different routes for one request; per-route Basic exemption and stage 13 follow the mux, gin executes its own |
| SEC2-23 | MINOR | The updater's unsigned legacy path is reachable only by an anomalous or malicious unsigned release once the S8 build (with compiled keys) runs |
| SEC2-24 | MINOR | The `derive` key can never be rotated; a leaked keyring export forges backup MACs and state codecs forever |
| SEC2-25 | MINOR | Stale or mis-stated cross-references: 09 §9.2.3 rule 2 parenthetical; 05-realtime "aliases" vs 06 `Projection`; 05-health `rbac.Scope` |

---

## SEC2-1 — BLOCKER — Mirror-disabled key is re-activatable in the previous build

**Where:** 05-rbac §6.3 "API keys never widen on rollback" (line 727 ff.), 05-storage §5S.3.11
`LegacyProjection.Disabled` and the `Side records` row (line 982), 05-authn §11 apikey row (line 936),
09 §9.2.3 rule 1, 09 S4 Rollback and Done-when ("create a scrape-only key on cur, roll back, key refused
on prev").

**Evidence:** "disabled" is not a terminal state in the legacy build.
`PATCH /api/v1/auth/api-keys/:id {"status":"active"}` (`internal/server/wire_auth_routes.go:73`,
credential-guarded, so any interactive session of the key **owner**) reaches
`handlers/apikeys.go:336-372`, which never looks at the current status, and
`pebble_store_auth.go:1226-1247` sets `Status="active"` from any state, `revoked` included
(`RevokeAPIKey` is just `SetAPIKeyStatus(id,"revoked")`, `:1222-1224`; the auth path checks only
`Status`, `middleware/auth.go:335-344`; `RevokedAt` is never consulted). The spec does not say which
`Scopes` the disabled legacy row carries. If it carries the empty list or a partial projection,
the re-activated key is served by the old build with `scopes: []` = every permission of the owner's
roles (`auth.go:405-408`): exactly SEC-1. Worse, the PATCH rewrites the row, so on the next upgrade
`Fresh=false`, "legacy wins", the side record is dropped and the adoption rule "legacy key with empty
scopes → `LegacyFullScope=true`" (05-rbac §6.3) makes it a full-scope key **in the new build too**.
The 05-authn integration pass changed "revoked" to "disabled" (line 1024), which maps naturally to the
legacy `inactive` state that the UI offers to re-enable.

**Fix:**
1. 05-storage §5S.3.11 / 05-rbac §6.3: a `Disabled` projection writes `status:"revoked"`, a
   **poisoned `token_hash`** (e.g. `"mirror-disabled:" + id`) and **no** `idx:apikey:hash:` row; the
   real hash lives only in the side record. The old build can then never authenticate the row whatever
   its status, and if it rewrites the row, the native copy that "follows legacy" is unusable rather
   than full-scope. `Scopes` in the disabled row = a sentinel atom absent from the legacy catalog
   (`"mirror.disabled"`), so `intersectPermissions` yields the empty set, never "all".
2. Adoption rule: a legacy row whose `token_hash` is poisoned or whose `mirror_reason` is set is never
   adopted as `LegacyFullScope`.
3. Hot-fix the live defect in a release **before S4** (S0 is the natural carrier, it already ships the
   Teach phase): `UpdateStatus` refuses any transition out of `revoked` (409) and `SetAPIKeyStatus`
   refuses `active` for a row with `revoked_at` or `mirror_reason`. Add it to "Out-of-scope defects".
4. Tests: `TestMirrorDisabled_PrevBuildReactivateStillRefused` (drive the real legacy handler against
   a mirrored store), extend `TestMirror_NarrowKeyNeverBecomesEmptyScope` with "after any legacy
   rewrite, re-adoption is not `LegacyFullScope`". Update 09 S4 Done-when to include the PATCH step.

## SEC2-2 — BLOCKER — Rollback widens a custom role holding `core:users.manage` or the `settings.manage` set

**Where:** 05-rbac §6.1 reverse rules (`users.manage` iff `core:users.manage`, line 652;
`settings.manage` iff all four), §6.3 "Roles … The mirrored legacy row holds the reverse-mapped atoms;
dropping an atom from a role only narrows it", §4.7 rule 5; 09 S4 Rollback ("Custom roles created on S4
appear with reverse-mapped (possibly narrower) atoms").

**Evidence:** "never widen" is checked per atom, but the legacy build enforces those atoms more weakly:
- `users.manage` in the legacy build has no dominance check and no `CanAssign`: it mints a temp-login
  for any user (`auth_temp_login.go:225-227`), sets any user's password through
  `PUT /api/v1/auth/me/password {user_id}` without the current password (`handlers/auth.go:581-616`),
  and assigns `admin`. 05-rbac §4.7 itself calls this "latent at HEAD" because only `admin` holds the
  atom there.
- `settings.manage` in the legacy build changes every `FieldSignIn` key with only an interactive
  session (`internal/config/update_service.go:779-783`), e.g. `oauth_default_role=admin`
  (`protected_fields.go`, "admin would mint admins").

S4 makes roles editable data, so an operator can legitimately create `custom.helpdesk =
{core:users.manage}` for a non-admin (dominance keeps it safe natively). On rollback to S3 the mirror
writes `users.manage` for that role, and the helpdesk user becomes an admin. Same for a custom role
holding the four `settings.manage` scopes.

**Fix:** 05-rbac §6.3 and 09 §9.2.3 rule 1, new rule "**legacy-admin-equivalent atoms**":
`users.manage` and `settings.manage` (and `integrations.manage`, whose legacy form keeps secrets across
URL changes, SEC-15) are mirrored into a legacy role row **only if the role also holds `core:admin`**;
otherwise they are omitted from the legacy row and kept in the side record (so S3 sees the role
narrower, never wider). Users whose only path to those atoms was such a role lose them on rollback;
09 S4 Rollback row says so. Property test `TestMirror_RoleNeverGainsLegacyAdminPower`: for random
native roles without `core:admin`, the legacy row contains none of the three atoms.

## SEC2-3 — BLOCKER — Post-restore revocations are undone by the next boot's rebuild from legacy

**Where:** 05-authn §4.14 steps 1–3 ("in one storage batch per family"), 05-rbac §7.1 rbac hook,
05-backup §5B.3.4 step h; versus 05-storage §5S.3.11 ("legacy rows are authoritative … rebuilds the
core copy from legacy at every boot"; Rebuild "takes legacy-changeable fields (status, password hash,
credential epoch, …) from the legacy row").

**Evidence:** during the whole window W the identity families are rebuilt from `u:`/`apikey:`/`role:`
at every boot, after the migration DAG and **before** step h runs. Nothing says the restore hooks write
through `Mirror.Stage`. If they write `c/auth/…` only (the natural reading of "one storage batch per
family"), every re-applied key revocation, user disable and `CredentialEpoch` bump exists only in the
native copy and is erased at the next restart; a rollback to the previous build sees none of them
either. Step 2's "revoke every session" also misses the legacy `sess:` rows that the 7-day fallback
still honours (05-authn §11). And `needs_confirmation` (step 3) has no legacy equivalent: if it is
projected as `inactive`, the owner re-enables it on the old build (SEC2-1).

**Fix:** 05-authn §4.14 and 05-rbac §7.1: "During the compatibility window, every write a
`RestoreHook` makes to a mirrored family goes through that family's `Mirror.Stage` (legacy primary and
index rows + native row + side record in one batch); the hook also deletes every legacy `sess:` and
`idx:sess:user:` row. `needs_confirmation` projects to the legacy row as the SEC2-1 poisoned
`revoked` form." 05-backup step h: hooks run **after** the rebuild and their writes are re-verified by a
second rebuild before listeners open. Test: `TestRestoreHook_RevocationSurvivesReboot` (restore, apply,
restart, key still refused; roll back to the previous build, key still refused).

## SEC2-4 — MAJOR — Confinement is removed from the ABS (`OwnAuth`) surface

**Where:** 05-http-api §4.2 stages 12–13 scope ("routes, except `OwnAuth` legacy routes"), §8.3 rows
"worker-only path confinement → `Confining` scopes at stage 13; `abortWorkerOnlyOutsideWorkerAPI`
deleted" (line 835) and "ABS `ABSRequireAuth` unchanged … resolves through `PrincipalFor`" (line 842);
05-authn §4.10.

**Evidence:** today the ABS group refuses a worker-only credential explicitly
(`internal/server/middleware/absauth.go:629-632`, "A worker-only credential never reaches the ABS
surface either"). From S4 the confinement lives only in stage 13, which never runs on `OwnAuth` routes,
and the bridge deletes the function. An `fp-worker` key (`abk_` via `ResolveAPIKey`) or a worker user's
ABS login then reaches every ABS route that does no in-handler `auth.Can` (library listing, items,
covers, progress). A `PurposeFacade` key presented to ABS (multi-app binary) is not refused either,
because the purpose rule is in stage 12's success path, which ABS bypasses by calling the API-key
authenticator "directly". 09 §9.2.6's invariant test covers `fp-worker` × 572 routes, so S4 would fail
its own gate; the design gives no way to pass it.

**Fix:** 05-http-api §8.3 ABS row: "After `PrincipalFor`, the adapter refuses (ABS JSON 403) a principal
with `Perms.Confined()` or a `PurposeFacade` key, unless the route's manifest `Perm` names one of its
scopes; `abortWorkerOnlyOutsideWorkerAPI` is replaced by this check, not deleted." 05-authn §4.10: the
"direct" API-key path applies the same status/purpose/confinement rules as stage 12 (expose them as
`auth.Service.CheckCredentialFor(route)`). Tests `TestConfining_WorkerKeyRefusedOnABS`,
`TestFacadeKey_RefusedOnABS`.

## SEC2-5 — MAJOR — Aliases retarget legacy actions that select their target by a body or query field

**Where:** 05-http-api §3.1 `Alias` ("same handler, same permission"), `LegacyPath` (output `Shape`
only, no request adapter), §12 `TestAlias_NonStrict` ("accepted on alias", line 960), §8.1 S4 (legacy
auth/users/keys paths become aliases, gin patterns deleted, line 775); 09 S4 Endpoints (22 + 15
aliases); versus 05-authn §4.13 (line 737: legacy `PUT /api/v1/auth/me/password` with `user_id` "is
served during the window by the legacy mount … calls the dominance-checked service").

**Evidence:** one legacy path, two native routes:
- `POST /api/v1/auth/api-keys` creates a key for the caller **or**, with `user_id`, for another user
  (`credential_routes.go:93`, UI `web/src/services/api.ts:7512-7539` sends `user_id`). Native splits
  it into `POST /me/api-keys` and `POST /users/{id}/api-keys`. An alias can point at one; with
  non-strict decoding `user_id` is silently dropped and the admin's UI action mints a key **owned by
  the admin**, bounded by the admin's roles instead of the target's, attributed to the admin.
- `GET /api/v1/auth/api-keys?all=true` (admin list of all keys) loses `all`.
- `PUT /api/v1/auth/me/password` with `user_id` sets another user's password with no current password
  (`handlers/auth.go:581-616`). 05-authn says it stays a gin route; 05-http-api/09 delete the gin
  pattern and alias the path to the self-only native route.

**Fix:** 05-http-api §3.1: `LegacyPath` gains `Request func(*http.Request) (target string, err error)`
that dispatches to one of several native routes, and an alias is **strict for every field of the
legacy request type** (a field the alias does not map → 400 `legacy_field_unsupported`, never dropped).
09 S4 lists, per aliased path, the native route(s) and the selector field; paths that cannot be mapped
(`me/password` with `user_id`) stay gin routes calling `auth.Service` (as 05-authn says) or become
`Gone`. Replace `TestAlias_NonStrict` with `TestAlias_UnknownLegacyFieldRefused` and add
`TestAlias_CreateKeyForOtherUserKeepsTarget`.

## SEC2-6 — MAJOR — SEC-21 half-fixed: the shell still carries the invite token in the path

**Where:** 05-authn §4.13 (tokens "travel in the request body, and the shell reads them from the URL
fragment … `/invite#token=…`"); versus 07 §7.4.3 shell routes (line 358 `/invite/:token`) and §7.9.1 (line 721 "renders
before auth gates"), 03 §3.6 UI-route example `/invite/{token}` (line 331), 09 S9 "UI routes …
`/invite/{token}`" (line 530).

**Evidence:** a full-page load of `/invite/mci_…` sends the one-time token to the server (stage 4 access
log, stage 2 span `url.path`, the `ui_route` usage recorder of 09 S9) and to every third party in
`Referer` unless the CSP/referrer policy blocks it. The core path redactor covers
`/api/core/v1/auth/invites/*` and the books legacy path, not a shell page path.

**Fix:** 07, 03 §3.6 and 09 S9: shell route `/invite` with the token in the fragment (`/invite#token=`),
`/login/temp#token=` likewise; the legacy `/invite/:token` link (aliased for W) is answered by a
server-side 302 to `/invite#token=<token>` with `Referrer-Policy: no-referrer` and is in
`DefaultPathRules`. Test: `TestShell_InvitePathCarriesNoToken`.

## SEC2-7 — MAJOR — SSRF trust inversion between `Local` and `lan_allow`; who may write URL fields is contradictory

**Where:** 05-metadata M1.3 `InstanceConfig.Local` ("Only an interactive principal holding
`core:integrations.manage` may set it", line 147), `Origin "api"` (API key with
`core:integrations.manage` writes instances, line 156), `Registry.Test` (unsaved config); 05-config
§1.3.7 (lines 484-494: `lan_allow` is `ProtectAdmin`; "instance URL and credential fields are
`ProtectAdmin`-equivalent under `core:integrations.manage`"; `lan_allow` applies only to instances
"not created by an interactive `core:integrations.manage` holder").

**Evidence:** `Local=true` admits **every** loopback/private/CGNAT address for that instance; `lan_allow`
admits a listed set. The wide trust needs only a grantable non-admin scope (the legacy
`integrations.manage` atom maps to it, 05-rbac §6.1), the narrow one needs `core:admin`. With
`Registry.Test` on an unsaved config returning `Class` and the HTTP status, an interactive custom-role
holder gets a LAN port/status oracle and can aim scheduled adapters at loopback-only services (the
app's own `core.metrics.listen`, a router UI). This is the SEC-6 pattern (settings holder becomes
network-trust admin) applied to instances. Separately, 05-config says URL fields are
ProtectAdmin-equivalent while 05-metadata lets an API key write them; 05-config says `lan_allow` applies
only to non-interactive-created instances while 05-metadata applies it to all.

**Fix:** 05-metadata M1.3: setting or keeping `Local` requires `core:admin` + interactive (the
`ProtectAdmin` check), as does `Test` with `Local=true`; non-admin writers get `lan_allow` only.
Pick one rule for URL fields and state it in both files: "`core:integrations.manage` writes URL fields;
an API-key writer's instance is `EgressConfigured` restricted to public + `lan_allow`". Align the
`lan_allow` sentence in 05-config with 05-metadata's "applies to every instance without `Local`".
Test: `TestRegistry_LocalNeedsAdmin`.

## SEC2-8 — MAJOR — SEC-6 incomplete: 05-backup and five other key tables carry no Protection

**Where:** 05-backup §5B.7 (no Protection column; `core.backup.recipients`, `core.backup.encryption`,
`core.backup.dir`), §5B.10 row "legacy paths aliased" (no route/permission table for list, create,
verify, delete, import, download); 05-notify §7, 05-ai §7, 05-download §7, 05-library §5L.7,
05-health §5H.7, 05-tasks §5T.10 (no Protection column). The SEC-6 fix as accepted names 05-ai,
05-download and 05-notify explicitly.

**Evidence:** at the default (`core:settings.manage`, grantable to non-admins) an actor adds an age
recipient they hold to `core.backup.recipients`; every later archive (DB with password hashes, secret
ciphertexts, `certs/` TLS material) is decryptable by them. `core.backup.encryption=none` silently
downgrades. `TestEveryURLPathOrSignInKeyClassified` catches neither (no URL/path/sign-in shape).
05-rbac §7.1 audits `backup.downloaded`/`backup.imported`, but no file defines those routes or their
permission; 05-rbac §4.10 says "core:system.manage" generically.

**Fix:** add a Protection column to every listed table. In 05-backup: `recipients`, `encryption`,
`dir`, `include_*` = `ProtectAdmin`; extend the classifier test with "key/recipient/encryption"
shapes. Add a 05-backup route table: list/verify `core:system.view`; create `core:system.manage`;
delete, import, download `core:system.manage` + `Guards.Interactive` (download and import audited);
restore as specified.

## SEC2-9 — MAJOR — The revocation journal has no integrity

**Where:** 05-backup §5B.3.6 (`<backup dir>/revocations.journal`, JSON lines, header
`{install_id, started_at}`, line 554; Coverage rule, line 557).

**Evidence:** the archives got an HMAC because the backup directory is untrusted storage (often a
network share, copied off-host for host-loss restore, §5B.3.3). The journal sits in the same directory
with no MAC, no sequence and no chain. Deleting or truncating the lines for one key revocation leaves
`install_id`, `started_at` and the gap rules satisfied, so `Revocations` is non-nil, keys are **not**
set `needs_confirmation`, and the revoked key is live after restore. Cross-cutting resolution 8 relies
on this file.

**Fix:** each line carries `seq` and `mac = HMAC(DeriveKey("core.backup/revocations/v1"),
prev_mac || line)`; the revoking batch also writes `c/backup/journal_head = {seq, mac}` to the DB (so
the pre-swap store, and the archive itself, know the last committed head). Coverage requires an
unbroken chain from the archive's recorded head to the pre-swap store's head; any break → nil
(`needs_confirmation`). Host-loss restore (no pre-swap store) without a verifiable head → nil. Test:
`TestRevocationJournal_TruncationDetected`.

## SEC2-10 — MAJOR — `backup.Revocation` cannot express what a role removal removed

**Where:** 05-backup §5B.3.2 `Revocation{At, Kind, Subject entity.Ref, Epoch}` (line 316-321); 05-rbac
§3.4 `Deps.Revocations` ("Append(role.removed) before DeleteRole or a permission removal commits",
line 240), §7.1 ("rbac's hook re-applies the `role.removed` entries", line 781); 05-backup §5B.3.6
Writers ("role removal from a user").

**Evidence:** removing role R from user U needs two refs; removing permission P from role R needs a role
and a permission list; deleting role R needs the role. One `Subject` holds one. The files also disagree
on which of the three events is journaled. The hook therefore cannot re-apply anything precise: after
restoring an archive taken before an admin was demoted, the demoted account is admin again, sessions
revoked but its password still valid.

**Fix:** `Revocation` gains `Object entity.Ref` and `Detail []string`; kinds
`role.unassigned{Subject: user, Object: role}`, `role.narrowed{Subject: role, Detail: removed
permissions}`, `role.deleted{Subject: role}`; 05-rbac §7.1 specifies the re-application of each (and
`ErrLastAdmin` handling when re-application would remove the last admin: keep, health `degraded`).
Test: `TestRestore_DemotedAdminStaysDemoted`.

## SEC2-11 — MAJOR — Restore re-installs the password a user changed after the archive

**Where:** 05-authn §4.12 (journaled kinds: `apikey.revoked`, `user.disabled`,
`credential_epoch.bumped`, `session.revoked`), §4.14 steps 1–2.

**Evidence:** a password change after a compromise bumps the epoch, and only the bump is journaled.
Restore puts back the old (compromised) hash and re-applies the bump, which revokes sessions but lets
the attacker sign in again with the old password. The same holds for OIDC identities unlinked after the
archive (`oauthid:` restored).

**Fix:** journal `password.changed{Subject: user}` and `identity.unlinked{Subject: user, Detail:
provider}`; §4.14 step 1 marks each such user `password_reset_required` (hash invalidated, sign-in
through a reset link only) and deletes the re-appeared identity links. Test:
`TestRestore_PostArchivePasswordChangeNotReverted`.

## SEC2-12 — MAJOR — Audit copy-forward collides with reissued sequence numbers

**Where:** 05-rbac §7.1 restore paragraph (copy "every record newer than `rc.Manifest.CreatedAt`
forward"), §7.2 (`rec/<seq:8B big-endian>`, `seq` from a 05-storage `Sequence` persisted in the store,
line 785).

**Evidence:** the restored store's `Sequence` high-water mark is the archive's. Copied records keep
pre-swap seqs above it; the next records written after boot get seqs from the archive's mark and
overwrite `rec/<seq>` rows of the copied trail (and their index rows point at the wrong record). The
SEC-17 guarantee ("restore does not erase its own trail") fails silently after the first few hundred
records.

**Fix:** the audit hook re-sequences copied records (new seqs from the restored `Sequence`, original seq
kept in `Details.orig_seq`) or advances the `Sequence` high-water mark past the maximum copied seq in
the same batch. Test: `TestAudit_CopyForwardThenWriteNoOverwrite`.

## SEC2-13 — MAJOR — Diagnostics download authorizes the user, not the credential

**Where:** 05-health §5H.3.3 (`GET /api/core/v1/diagnostics/{run}`: "the run's actor or `core:admin`",
line 180), §5H.3.5 `Viewer` resolved at start time (line 229-231), §5H.5 "Bundle download by another
actor → 404".

**Evidence:** "the run's actor" is a user identity, not an `rbac.Requirement`, so the route's stage-13
`Perm` must be something weaker (`AnyAuthenticated`) plus an in-handler identity check. Any credential of
that user then downloads the bundle (logs, effective config, sysinfo, task error text): a scrape key
scoped `core:metrics.read`, a delegated temp-login session, or an admin whose role was removed after
the run started (24 h retention).

**Fix:** route `Perm` = `core:system.manage`, `Guards.Interactive`; the handler additionally requires the
run's actor (or `core:admin`); the bundle's sections are re-filtered against the **download-time**
checker intersected with the start-time `Viewer`. Rename `rbac.Scope` → `rbac.Permission` in `Viewer`.
Tests: `TestDiagnostics_NarrowKeyOfActor403`, `TestDiagnostics_DemotedActor403`.

## SEC2-14 — MAJOR — Infra routes are outside the authenticated scope of the chain

**Where:** 05-http-api §4.2 scope definitions (line 484: "routes = registered routes (native, alias,
facade, legacy manifest routes)"; stages 10–14 apply to "routes"), §3.5
`Router.Infra(pattern, perm, h)` (line 433); 05-metrics M5/§6 (`/metrics` = `core:metrics.read`).

**Evidence:** infra is in "All" but not in "routes", so stages 12–13 never run for `/metrics`; the
`perm` argument of `Infra` is recorded but not enforced. Today `/metrics` needs an authenticated
credential (`server_lifecycle.go:1735-1747`); after S8 it would be public, leaking route names,
counts and per-user-ish labels. `TestModuleGroup_IsAuthenticated` covers module groups only.

**Fix:** define "routes" to include `Infra`; `Infra` with a non-Public perm runs stages 10–13 (probes
`/livez`, `/readyz` stay Public and `RateExempt`). Test `TestInfra_MetricsRequiresScope`.

## SEC2-15 — MAJOR — The module system actor has no permission set

**Where:** 05-tasks §5T.3.10 "Actor stamping" (line 793: outside a request the kernel stamps "the calling
module's system actor … `Perms.Start` is checked against the stamped actor"); 06 §6.4.4 `Tasks` row;
05-rbac §3.4 `authz.ForSystem` (built "from the task definition's declared permissions" — of a running
task, not of a module).

**Evidence:** a module's `Worker`, `Hook` or bus subscriber starts a run by `Kind` string, cross-module
included (R3). Either the system actor passes every `Start` check (any module can start
`core:system.manage` kinds such as diagnostics export, backup create, or another module's
data-destroying kinds) or it passes none (outbox-free automation breaks). Neither is written down.

**Fix:** 05-tasks: "A module's system actor holds exactly the union of `Perms.Start` of the module's own
kinds plus `Manifest.TaskStarts []tasks.Kind` (reviewed list of foreign/core kinds); `core:admin` and
`AuthRequired` scopes are never included." 06 adds the manifest field; kernel validates kinds exist.
Test `TestModuleSystemActor_CannotStartUndeclaredKind`.

## SEC2-16 — MAJOR — Authenticator and compat-method registration is unreachable and unconstrained

**Where:** 05-authn §3.3 (`Registrar.AddAuthenticator`, `AddMethod`, "Registered by app options
(extensions) or module registrars (compat methods)", line 358; ABO registers `internal/cfaccess`
this way, line 389; `books.abs` via `AddMethod`, line 663); 05-http-api §8.3 CF row; 06 §6.4.3
`module.Registrar` and §6.4.5 `app.Options` (neither has any auth registration).

**Evidence:** the only `OwnerVerifier` ABO has (`cfaccess`) and the ABS method cannot be wired at S4 as
specified. Whoever adds the missing hook will decide, per call, `MethodSpec.Interactive`,
`MayChangeCredentials` ("only extensions proving a person's own sign-in may set this" — unenforced),
`OwnerVerifier` (owner assurance for restore/factory reset/merge) and the `Perms` inside the returned
`*Principal` (an authenticator can return any `rbac.Checker`). The approval rule ("verified identity
extension", 05-tasks §5T.3.7) and `Guards.Owner` then depend on module code.

**Fix:** 06 §6.4.5: `app.Options.Authenticators []auth.Extension{Authenticator, MethodSpec, Order}` —
app-level, compile-time, the **only** place `Interactive`/`MayChangeCredentials`/`OwnerVerifier` may be
true. 06 §6.4.3: `Registrar.AuthMethod(spec)` for compat methods named `<ID>.<name>`, kernel-refused
unless non-interactive and `MayChangeCredentials=false`. 05-authn §4.2: stage 12 ignores the
authenticator-supplied `Perms` and recompiles via `rbac.Compile` from `{User, Method, scopes}`. Test
`TestModuleAuthMethod_CannotBeInteractive`.

## SEC2-17 — MAJOR — Any module can use `MountLegacy` to skip core authentication permanently

**Where:** 06 §6.4.3 `Registrar.MountLegacy` (line 320, any module with `LegacyPrefixes`); 05-http-api
§8.2 `LegacyMount.Auth` ("`LegacyAuthGin` (09 S2–S3)", line 792), `LegacyRoute.OwnAuth` (set by the
caller, line 804), 05-rbac §3.1 `Delegated(reason)` (line 114, accepted by any `MountLegacy`);
05-http-api §8.2 R6 rule ("or `OwnAuth` with `Delegated`").

**Evidence:** "S2–S3" is a comment, not a check. A TV/Movies module (or any future module) can mount a
handler with `Auth: LegacyAuthGin` (stages 12–13 off for the whole mount, forever) or mark routes
`OwnAuth` + `Delegated` (also skips the Basic gate). `OwnAuth` is "derived when the manifest is
recorded" only for ABO's gin recorder; for other mounts it is a self-declared flag. This reopens the
G8 failure mode ("a group without the chain") that 05-http-api's objection to 03 asks R6 to forbid.

**Fix:** 05-http-api §8.2: `LegacyAuthGin` requires `app.Options.LegacyAuthGinUntil` (compile-time,
names the module; ABO removes it in the S4 PR, archtest fails if present after); `OwnAuth=true` is
accepted only when the route names a compat method registered with `Registrar.AuthMethod` (SEC2-16) and
the kernel records it in a golden (`TestOwnAuthRoutesGolden`). 03 R6 gains the 05-http-api objection
text.

## SEC2-18 — MINOR — URL redaction lists regressed and disagree

**Where:** 05-logging §3 `DefaultRedactQuery` (line 214: `token, access_token, api_key, apikey, key, sig,
signature, password`); 05-http-api stage 2 (`apikey`, `token`, `access_token`).

**Evidence:** HEAD's access-log redactor also masks `refresh_token`, `secret`, `code`, `code_verifier`
(`internal/server/middleware/log_redact.go:26-37`). From S2 the access log is core stage 4, so OAuth
authorization codes (`/auth/oidc/.../callback?code=`) and any `refresh_token` query value reach the log
and spans.

**Fix:** `DefaultRedactQuery` ⊇ HEAD's `credentialQueryKeys`; 05-http-api stages 2/4 reference the 05-logging
list instead of restating it. Test `TestRedactQuery_SupersetOfHEAD`.

## SEC2-19 — MINOR — S2 moves client-address trust out from under gin's limiters

**Where:** 05-http-api §8.4 (`peer_allowlist.go`, `client_trust.go` → `core/internal/httpmw/peer.go`),
§8.1 S2 (auth unchanged inside gin until S4).

**Evidence:** gin keeps calling `trustedClientIP` in `bootstrap_rate_limit.go:207`,
`invite_rate_limit.go:405` and `client_trust.go:600` until S4, including the
"local peer + verified Access assertion" branch that `TrustedPeer` does not model.

**Fix:** S2: `client_trust.go` stays in ABO until S4, or core stage 0 publishes the resolved
`{ClientAddr, Attributed}` in the request context and the gin helpers read it; S4 moves the
CF-assertion branch into ABO's `internal/cfaccess` authenticator. Test: S2 done-check includes the
invite and bootstrap limiter tests unchanged.

## SEC2-20 — MINOR — "Audited" refusals before the audit log exists

**Where:** 05-http-api stage 7 ("refusal 403 `cross_origin_rejected`, audited"), §6; 09 S2 (stage 7 is
new at S2); 09 S4 ("New `c/audit/`").

**Fix:** stage 7 refusals before S4 go to the access log with a structured `security_event` attribute;
the audit write starts at S4. State it in 05-http-api §8.1's S2 row.

## SEC2-21 — MINOR — Auth-off reach changes for logs, status and info

**Where:** 05-health §5H.3.3 and 05-logging §5 (`core:system.manage`), 05-rbac §6.1 (`core:system.manage`
is `AuthRequired`), §4.11 ("read-only admin introspection stays reachable"); 09 §9.2.6 invariant
("auth-off mode … identical").

**Evidence:** today `s.perm` passes everything with auth off (`wire_admin_role.go:18-22`), so
`/api/v1/system/logs` and `/system/status` (`settings.manage`) are open; after S8's aliases they answer
401. Not a hole, but the invariant test will fail and the reach table in 05-rbac §6.2 omits them.

**Fix:** either split `core:system.logs` (`ClassView`, not `AuthRequired`) for logs/info/health detail and
keep `core:system.manage` for resets, or record the deliberate tightening in 09 §9.2.6 and exclude those
aliases from the auth-off half of `TestLegacyRoleReachUnchanged`.

## SEC2-22 — MINOR — ServeMux and gin can dispatch the same request to different routes

**Where:** 05-http-api §8.2 ("each manifest route is its own mux pattern … with the gin engine as the
handler"), §4.8.

**Evidence:** ServeMux matches escaped segments (`/a%2Fb` is one segment); gin with default
`UseRawPath=false` routes on the decoded `r.URL.Path` (`server.go:637-641`). Stage 8 (per-route Basic
exemption), stage 10 (rate class) and from S4 stage 13 use the mux pattern; gin runs whatever it
matches. The gin-side `s.perm` adapters limit the impact, but the bridge is described as "read-only".

**Fix:** the legacy handler wrapper passes `r.Pattern` to gin and returns 404 when `c.FullPath()` does not
map to that manifest entry; state that bridge adapters still call `rbac.Require` (enforcing, not
read-only). Test `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash`.

## SEC2-23 — MINOR — Unsigned updater path is attack surface only

**Where:** 05-updater §5U.5 "Legacy unsigned path" and its S8 precondition.

**Evidence:** installs reach S8 through the old updater; the S8 build has compiled keys and every later
release is signed (precondition). So in S8+ the unsigned path triggers only for a release that lacks a
`.sig`, i.e. a mispublished or attacker-published release, until the first signed apply clears the flag.

**Fix:** accept an unsigned release only if its version ≤ a compiled `LastUnsignedVersion` (the release
before signing began); otherwise `verify_failed`.

## SEC2-24 — MINOR — The derive key has no rotation path

**Where:** 05-config §2.3 keyring format ("the single `use: "derive"` key … is **never** replaced").

**Evidence:** the derive key backs the backup provenance MAC (05-backup §5B.3.7), OIDC state, ABS refresh
derivation and cursor HMACs. A leaked keyring export lets the holder forge `Own` archives indefinitely.

**Fix:** add `secrets rotate-derive` (CLI, offline): new `d2` active, `d1` retained verify-only for
`max(archive retention, refresh TTL)`, then retired; manifests record the derive key id.

## SEC2-25 — MINOR — Stale cross-references

- 09 §9.2.3 rule 2 (line 192): "(05-rbac §6.3 and 05-authn §11 still state the unconditional drop …)";
  both now carry the `MirrorDisabled`+`Fresh` exception. Delete the parenthetical.
- 05-realtime §4.9 says the legacy streams are "registered by the `books` module as aliases (05-http-api
  §4.6)"; `Alias` requires a same-handler native route in the caller's group, and 06 §6.4.3 says books
  attaches `Hub.Projection` through `Registrar.Realtime()`. State that projections are gin manifest
  routes (Perm `AnyAuthenticated`, per-event topic filtering) wrapping `Hub.Projection`.
- 05-health §5H.3.5 `Viewer.Can func(rbac.Scope)`: no such type; use `rbac.Permission` (see SEC2-13).
