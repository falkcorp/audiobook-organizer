<!-- file: docs/agent-tasks/platform-core/F4-08-identity-flip-legacyauth-native-routes.md -->
<!-- version: 1.1.3 -->
<!-- guid: 0ad91c06-04c3-47a1-bdf3-1ab090720204 -->
<!-- last-edited: 2026-11-05 -->

# F4-08 — F4 flip, one PR: `LegacyAuthCore` with `LegacyAuthGinUntil` deleted, the `legacyauth` bridge, native identity and settings routes with the gin routes deleted, the OAuth paths served by core, M3 registered, the events bus live, the identity UI and the SPA's reset control

| Field | Value |
|---|---|
| ID | F4-08 |
| Release / flip | R2, **the identity flip F4** (one atomic PR, 09 §9.2.1) |
| Tier | **T4** (Opus, xhigh–max): the identity flip replaces the auth chain of every gin route |
| Wave | W34 — the only PR of its wave touching `main.go`, `internal/server/` and books' registration, and the wave's one owner of the kernel and module wiring files; it **merges alone**; runs inside an announced **move window** |
| Depends on | F3-07, F4-03, F4-04, F4-05, F4-06, F4-07, F4-09, F4-10, F4-11 |
| Owns gate tests | `TestOAuthCallback_HEADPathServedByCore`; `TestOAuthStart_HEADPathServedByCore`; `TestIdentityCutover_ABSSurvives` (the ABS conformance suite on the migrated corpus); `TestCutover_NoRawTokenOnDisk`; `TestOIDC_AppHandoffMintsABSCode`; the route cells of `TestWorkerKey_Confined`; `TestAuthOff_AuthModeWriteRefused` (05-authn §10); `TestAuthOffDockerBoots` (image job, 05-authn §10); the `auth_disabled_remote` health token on the authenticated `degraded` field (05-authn §4.11) |
| Gate | Plan, then execute autonomously. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-08-identity-flip
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm every dependency is merged and the move window is announced. Read F4-01 … F4-07 and F4-09 … F4-11's PR
descriptions for the seams, coined names and deferred test cells they handed to this PR (F4-11's names its two boot
functions; F2-07's names the audit restore hook it deferred to F4).

## Goal

Switch identity to core for ABO in one PR, with every first-party caller (09 §9.2.9, 05-http-api §8.1 F4 row):

0. **Kernel and module wiring** (06 §6.4.10 F4 row, §6.5.1 P4d, P5, P7): inject F4-09's `authz.Compile` as
   `rbac.Deps.Compile` at P5 (05-rbac §5); build rbac, auth and audit serving; register `backup/journal`'s
   `StorageSpec` for `c/backup/` (F4-04) and pass the journal as `rbac.Deps.Revocations` and to auth; mount F4-05's route
   packages (for every app, hello included) and F4-10's `CoreFixedGroup`; P7 seeding, `auth.BootWrites` (P4d step 4),
   the bus and the stage 7/13 audit. **P4d steps 2 and 3** (06 §6.5.1; 05-backup §5B.3.6, §5B.3.4 step h): call F4-11's
   two kernel functions from boot, in that order and after the keyring opened at P4b — the step 2 re-apply (install
   check, replaced-store rule, chain verification, gap and `restore` lines, `c/backup/served_boot`,
   `backup.location.json`, from the effective `core.backup.dir`) and the step 3 restore-hook runner — then step 4's
   `auth.BootWrites`. Register every restore hook with F4-11's runner: auth's `PostRestore` (F4-02), rbac's (F4-01)
   and the audit hook F2-07 deferred to F4. F4-11 builds both functions and the journal format; this row only calls
   and registers them. Add the F4 module members to `core/module/` (`Permissions`, `Roles`,
   `ImportedRoleIDs`, `Topics`, `Subscribe`, `Deps.Auth`, `AuthAdmin`, `AuthCompat`, `Audit`, `Events`), `users
   reset-password` and `storage cutover-blockers` (F4-06's command) to `core/app/offline.go`'s list with their cobra
   wrappers under `cmd/`. Regenerate `core/openapi/core.json` and the generated client under
   `core/web/packages/api-client/src/generated/` for the newly mounted routes; regenerate
   `tools/cmd/sdkguard/internal-deps.txt` (`go run ./tools/cmd/sdkguard/main.go -update`) once `internal/auth` is gone.

1. **Auth engine for the mount:** `LegacyAuth` switches to `LegacyAuthCore`; delete `app.Options.LegacyAuthGinUntil`
   with an archtest that fails if it is present. Stage 12 binds the core principal; stage 13 enforces each manifest
   route's `Perm`/`Guards` (an `s.perm(atom)` route's atom-table requirement recorded since R1). The legacy `/metrics`
   entry carries `core:metrics.read`.
2. **The bridge** (05-http-api §8.3): create `internal/server/legacyauth` (a new root path; it enters `layerOf` here):
   `legacyauth.FromCorePrincipal` replaces `RequireAuth`, `legacyauth.Can(ctx, atom)` replaces every raw in-handler
   `auth.Can`, the gin adapters enforce through `rbac.Require`, `credGuard` → `Guards.Interactive`, `RequireOwner` →
   `Guards.Owner`, `RequireAdmin` → the §6.2 permission; the ABS `ABSRequireAuth` resolves through books' `auth.Compat`
   handle and then calls `Compat.CheckCredentialFor(p, route.Perm)`. An ABO archtest finds no raw `auth.Can` left in the
   mount.
3. **Native routes live:** mount F4-05's packages; delete the gin routes they replace (`/api/v1/auth/*` and the identity
   half of `/api/v1/users*`, `PUT /api/v1/auth/me/password` with `user_id` included, the gin settings handlers for every
   `k` and `s` key; `GET`/`PUT /api/v1/config` keep only the class-`i` fields until F7); `GET /auth/temp-login` and
   `/invite/<token>` replaced by the fragment link forms; one-time tokens minted before the flip are reset (no redirect
   route).
4. **OAuth paths:** HEAD's callback and start paths served by core on `CoreFixedGroup` with
   `app.Options.Auth.OIDCCallbackBase = "/api/v1/auth/oauth"`; books' gin start and callback handlers deleted; books
   registers `MethodSpec{Method: "books.abs", AppHandoff: <books' hand-off over HEAD's code store>}`.
5. **App options:** `app.Options.Auth` (`ImportedKeyPrefixes: ["abk_"]`, `DefaultDisabledAllowRemote: true`,
   `OIDCCallbackBase`), `app.Options.Authenticators` with `internal/cfaccess`'s `auth.Extension`; `client_trust.go`'s
   Access-assertion branch moves into that authenticator in `internal/cfaccess/`.
6. **M3 registered** in `internal/books/cutover/steps.go` in 03 §3.8's order (after M2, before M8), then the per-flip
   level mechanism (order M2, stated in F3-04 and F3-06): **add** a new level step for each owner whose level this
   flip raises (books' `books.level-<n>`; core's `core.level-<n>` when core's level changes), named for the raised
   target level, at that owner's next contiguous version, numbered immediately before M3 (which runs `After` it);
   F3-07's level steps stay registered unchanged (05-storage §5S.3.5: versions contiguous per owner, recorded by
   version number). **Replace** the M8 entry with a newly named one whose step ID names M3 as the step it compacts
   after, so exactly one M8 is registered and it runs last; bump the raised owners' declared `Spec.Level` in the same
   commit. F3-06's test checks the registered list: the levels are raised to the build's target before the first step
   of each flip, versions are contiguous per owner, one M8, last. On a store that F3-07's `main` migrated, F3-07's
   level steps are applied, and the new M8 ignores the previous M8's `done` record and `applied` journal (03 §3.8;
   05-storage §5S.3.13). So a store migrated by F3's `main` has its levels raised, runs M3 and is rewritten once more,
   and F3's `main` refuses it.
7. **Boot writers move:** HEAD's Start-time identity writers (`seedRolesAndTokens`: `internal/auth/seed.go`'s seed-role
   and `_system` writes, `internal/server/bootstrap.go`'s token, the key-expiry stamp, the read-only key) are deleted;
   P7 seeding and `auth.BootWrites` (P4d step 4) take over. Extend `TestBootCrashMatrix_ABO` with a cell at every
   durable write of each new boot writer (06 §6.5.1 table, P4d rows 2–4 and P7): the re-apply's revocation writes, its
   gap and `restore` lines with their head updates, `c/backup/served_boot`, `backup.location.json` and a journal move;
   the hooks' writes, the `marker_written` batch, journal `hooks_done` and `applied`, the `inflight.json` removal;
   `auth.BootWrites`' rows and its bootstrap-token file; the P7 seed rows.
8. **Events bus live:** `Registrar.Topics`, `Registrar.Subscribe`, `Deps.Events`; the kernel publishes
   `core.rbac.changed.v1`; `auth` publishes `core.principal.invalidated.v1`.
9. **Merges:** books' undo of a merge finished before R2 answers 409 `merge_permanent` (`internal/usermerge/`); HEAD's
   identity code for undo/resume of pre-R2 journals is deleted.
10. **UI and callers:** the SPA's auth, users, keys and roles calls, its five legacy permission constants, the settings
    page on `/api/core/v1/settings` and `/settings/schema`, the fragment-form `/invite` and `/login/temp` routes, and the
    factory-reset dialog on `POST /api/core/v1/system/factory-reset` (two-step count confirmation) — through the
    generated `@mediacore/api-client` (07 §7.19 step 3); the `server-bootstrap` skill's `POST /api/v1/auth/bootstrap` and
    every `scripts/` caller on the native routes. The ESLint permission-literal rule (07 §7.18, `no-restricted-syntax`:
    no string literal in a permission position unless it comes from `manifest.permissions` or the generated client)
    lands with the SPA's switch to the generated client; it belongs in `web/eslint.config.mjs`, which is not in this
    row's list (STOP 3: report it before editing).
11. **04 keys of F4 moved here:** `internal/auth` → core `auth`/`rbac` (delete `internal/auth/` once empty);
    `internal/server`, `internal/server/middleware` → core `auth`; `internal/server/handlers` → core `internal/authhttp`;
    core `audit` serving; `.mockery.yaml` entries of deleted interfaces.

## Files (the row's list)

| Action | Path |
|---|---|
| modify | `main.go`, `.mockery.yaml`, `cmd/` |
| modify | `core/kernel/boot.go`, `core/kernel/kernel.go` |
| modify | `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` |
| modify | `core/app/options.go`, `core/app/offline.go` |
| regenerate | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/`, `tools/cmd/sdkguard/internal-deps.txt` |
| modify / create | `internal/server/` (with `internal/server/legacyauth/` created), `internal/auth/` (emptied and deleted) |
| modify | `internal/cfaccess/`, `internal/usermerge/` |
| modify | `internal/books/module.go`, `internal/books/cutover/steps.go` |
| modify | `web/src/`, `.claude/skills/server-bootstrap/`, `scripts/` |
| create | `web/tests/e2e/` (the two fragment-link Playwright specs) |
| modify | `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json` |

This PR is its wave's only owner of the kernel and module wiring files.

## Spec

09 §9.4 R2 flips table F4 row, §9.6 rows 1, 4, 9–12, 20, 24, 26, §9.2.6; 03 §3.8 F4 and M3; 05-http-api §8.1 F4 row and
the paragraph after the table, §8.3 (the bridge table), §3.5, §4.9; 05-authn §3.2 (`Options`), §3.3, §4.7, §4.10, §4.13,
§5, §10 rows named below, §11 (release-note text); 05-rbac §6 (all), §12; 07 §7.19 step 3, §7.4.3, §7.9.1; 06 §6.4.10 F4
row, §6.5.1 P4d steps 2–4, P7, §6.10 rows named below; 05-backup §5B.3.4 step h, §5B.3.6 (what F4-11's functions do;
this row calls them), §5B.9 rows named below; 05-events §5E.13 "Order" item (1); 05-storage §5S.3.13 (a level step added and M8
replaced per flip, `TestCutover_NoRawTokenOnDisk`), §5S.10 (`TestBootCrashMatrix_ABO`); 03 §3.8 (M7 and M8 at each
flip); 05-http-api §12 rows named below; 05-authn §4.11 (`auth_disabled_remote`); 05-health §5H.3.1 (that token's
route before F8); 07 §7.4.3, §7.18 (`TestOldLinkFormsNotServed`, the fragment e2e rows, the ESLint rule).

## Re-verify at BASE (HEAD `f7ac5b11e` values shown; regenerate at BASE)

```bash
grep -rn 'LegacyAuthGinUntil' --include=*.go . | grep -v _test | wc -l          # > 0; 0 after (outside the archtest)
grep -c 'func RequireAuth' internal/server/middleware/auth.go                    # HEAD 1 (:152)
grep -rn 'auth.Can(' --include=*.go internal/server | grep -v _test | wc -l      # HEAD 14 sites to replace
ls internal/server/wire_auth_routes.go | wc -l                                   # 1
ls internal/auth/                                                                # HEAD: auth_test.go context.go method_credentials_test.go owner.go … (owner.go gone after F4-03)
grep -rn 'api/v1/auth' .claude/skills/server-bootstrap scripts | wc -l           # HEAD 22 caller lines
ls -d internal/server/legacyauth 2>/dev/null | wc -l                             # 0
grep -c 'cutover/m3\|m3\.' internal/books/cutover/steps.go                       # 0 (F3-07's list holds M2, M7, M8)
ls core/kernel/restore_reapply.go core/kernel/restore_hooks.go | wc -l            # 2 (F4-11)
ls web/tests/e2e/*.spec.ts | wc -l                                               # HEAD > 0 (Playwright specs dir)
ls -d core/internal/authhttp internal/cfaccess internal/books/rbacreach | wc -l  # 3
grep -rn 'func Compile\|func AuthDisabled\|func ForSystem' core/internal/authz/ | wc -l   # 3 (F4-09)
grep -rn 'func (r \*Router) CoreFixedGroup' core/httpapi/ | wc -l                # 1 (F4-10)
grep -rn 'authz\.Compile' core/kernel/ | wc -l                                   # 0 now; >= 1 after
grep -c 'internal/auth$' tools/cmd/sdkguard/internal-deps.txt                    # 1 now; 0 after
```

## STOP conditions

1. `TestIdentityCutover_ReachOracle` (F4-07) shows any difference outside its expected list on the registered path.
2. The ABS conformance suite fails on the migrated corpus, or an ABS phone-app session issued by the R0 binary stops
   working (the only frozen contract, D12 (c)).
3. The work needs a path outside the Files table above: report before editing it.
4. A first-party caller of a deleted route remains on `main` after this PR.

## Steps (one PR; every commit builds)

1. Write the owned gate tests and the deferred cells first (below).
2. Kernel/module/app (Goal item 0): inject `authz.Compile`, mount F4-05's routes on `CoreGroup` and the OIDC paths on
   `CoreFixedGroup`, start rbac/auth/audit serving, the bus, P7 seeding, F4-11's P4d step 2 and step 3 functions with
   the auth, rbac and audit restore hooks registered, `BootWrites`, the `c/backup/` journal spec, stage 7/13 audit; the F4 module members; the offline commands and their `cmd/` wrappers; `app.Options.Auth`,
   `Authenticators`; delete `LegacyAuthGinUntil`; archtest; regenerate `core.json` and the client.
3. Books: `books.abs` with `AppHandoff`; `Deps.AuthCompat` in the ABS adapters; M3 in `steps.go`; add this flip's
   level steps, replace the M8 entry with a newly named one and bump the raised owners' declared levels (Goal item 6); this flip's cell in
   F2-15's cutover-suite harness (the cell shape is F3-04's).
4. Bridge `internal/server/legacyauth`; replace the gin auth chain and the 14 raw `auth.Can` sites; delete `RequireAuth`
   and the gin identity, OAuth and settings (`k`/`s`) routes; move `client_trust.go`'s Access branch.
5. Delete HEAD's Start-time identity writers and HEAD's merge undo of pre-R2 journals (409 `merge_permanent`).
6. SPA, skill and scripts on the native routes; reset control back; the two fragment-link Playwright specs under
   `web/tests/e2e/`.
7. Move `internal/auth` per the 04 keys; `.mockery.yaml`; layering and shard weights; regenerate
   `tools/cmd/sdkguard/internal-deps.txt` and bump its `# version:` line.
8. Headers; one `changelog.d/` fragment; Acceptance.

## Tests first

- `TestOAuthCallback_HEADPathServedByCore`: a sign-in started at `GET /api/core/v1/auth/oidc/{provider}/start` completes at
  HEAD's `GET /api/v1/auth/oauth/{provider}/callback` with the redirect URI HEAD registered; no gin handler serves it.
- `TestOAuthStart_HEADPathServedByCore`: HEAD's start path reaches core's start handler through the whole core chain
  (`RouteInfo.Owner` `core`, Public, `RateAuth`), no gin handler serves it, one cell per `AppHandoff` rule (no app fields →
  web sign-in; listed `redirect_uri`, and in its own cell `callback`, of `audiobooth://oauth` with an S256
  `code_challenge` → `AppHandoff.Mint`, app-scheme redirect with code and state, no web session).
- `TestIdentityCutover_ABSSurvives`: the ABS conformance suite on the migrated fixture; an `abs_sess:` refresh session and
  an ABS JWT issued by the R0 binary keep working; ABS logins by password and by `abk_` key resolve the same user with the
  same reach.
- `TestCutover_NoRawTokenOnDisk`: a canary session token and invite token planted by the R0 binary are found in no file
  under the live DB directory (SSTs, WAL, MANIFEST, OPTIONS) after M3 and M8; the retained checkpoint is the exception.
- `TestOIDC_AppHandoffMintsABSCode` (gate test, owned here; 05-authn §10 "ABO, fake IdP, from F4"): with books' real
  `AppHandoff` over HEAD's code store and a fake IdP, one cell per control of 05-authn §4.7 (F4-05's
  `TestOIDCHandoff_StartAndCallbackControls` covers the core half: reuse its fixtures), and the redemption: the code
  redeems once at the ABS route `/auth/openid/callback` and is refused after the user's epoch is bumped.
- `TestWorkerKey_Confined`'s route cells (F4-01 created the test, F4-09 added the checker cells): an admin-owned key
  scoped `["books:fingerprint.worker"]`, an `fp-worker` user's key and an `fp-worker` session get 403 on
  `/api/core/v1/me`, own sessions, `/metrics` and a reading-state route, and 200 on the worker routes.
- Deferred cells finished here: `TestIdentityCutover_WorkerKeyStaysConfined` (live routes),
  `TestCutoverRefusedWhileMergeInProgress` (undo → 409 `merge_permanent`), `TestCutoverRefusedByOlderBuilds` (the mid-M3
  cell), `TestIdentityCutover_ReachOracle` on the registered path, `TestMetricsScrape_MigratedReach`,
  `TestCompat_ABSLoginHonoursRestrictions`, `TestBootWrites_RunInP4d`, `TestConfining_WorkerKeyRefusedOnPlaylists`,
  `TestAuthOff_BooksGatesAndInHandlerDenial`, `TestAuthOff_SettingsManageKeepsReach`, the core half of
  `TestUserMerge_ScopedAdminKeyRefused`.
- **Levels and M8 (order M2):** this flip's cell in F2-15's cutover-suite harness (the cell shape is F3-04's): a store
  migrated by F3-07's `main` and booted by this `main` has its levels raised to this build's target before M3, runs
  exactly one M8, and F3-07's binary then exits 78 on it. F3-06's registered-list test (levels raised before the first
  step of each flip, versions contiguous per owner, one M8, last) stays green on this build.
- **Boot wiring of F4-11's functions** (F4-11 owns the journal and re-apply tests; these prove the wiring):
  - `TestBootCrashMatrix_ABO` (05-storage §5S.10; 06 §6.10): the new cells of Goal item 7; a kill or error at each
    write leaves a store this build boots and finishes.
  - `TestBootPhaseOrder_NoWriteBeforeCheckpoint` (06 §6.10): with auth's boot writes, a revocation re-apply and a
    post-restore hook pending, a boot's first write follows the checkpoint, the reconcilers run in the P4d order
    (overrides before the journal), and no store constructor or `Register` writes.
  - `TestRestoreHooksWriteNativeRows` (05-backup §5B.9): the hooks run after the re-apply and write through the
    native stores; after a restart the re-revoked key, a restored session and a restored invite stay refused.
  - `TestCrashBetweenSwapAndHooksResumesHooks` (05-backup §5B.9): a crash at P2, P4a, P4b and each P4d step after step
    g leaves a restore boot whose hooks run once to completion, and the in-flight probe refuses until `applied`.
- **Routes** (05-http-api §12):
  - `TestStage13_StaleCookieOnPublicRouteIsAnonymous`: a stale cookie on a Public route is cleared and the request
    proceeds anonymous.
  - `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor`: every manifest route gated by `settings.manage` or
    `users.manage` carries the atom table's full-reach entry; a custom role holding the four `settings.manage`
    expansion scopes gets 403 on backup restore, backup create, logs, tool install and plugin settings.
  - `TestCoreRoutesReplaceGin` (created here, with the F4 cell): on the real engine no gin route remains at a path this
    flip's native routes replace (the flip's generated list). F5-18, F6-03, F7-09 and F8-04 each add their cell.
- **Link forms** (07 §7.18):
  - `TestOldLinkFormsNotServed`: `GET /auth/temp-login?token=` answers 404, an old `/invite/<token>` link opens no
    invite, a token minted before the flip is refused, and the access log and span show the redacted path.
  - `TestTempLoginLink_FragmentEndToEnd` and `TestInviteLink_FragmentEndToEnd` (ABO Playwright specs under
    `web/tests/e2e/`, this flip's done-check; F4-05 owns the server half): the temp-login link signs the user in and the
    invite link shows the invite signed out and signed in, and the SPA removes each fragment with
    `history.replaceState`.
- **Auth off** (05-authn §10):
  - `TestAuthOff_AuthModeWriteRefused`: with auth off, the auth-disabled principal's write of `core.auth.mode` or a
    `core.http.basic_gate.*` key gets 403 and nothing is stored, on hello and ABO.
  - `TestAuthOffDockerBoots` (image job): ABO with auth off in a container boots and reports `auth_disabled_remote`.
    Add the check to the image job's probe under `scripts/ci/`. The token travels the authenticated route, never
    `/health`'s unauthenticated list (05-health §5H.3.1). If the image job needs a workflow edit for this run, STOP 3
    applies.
  - The `auth_disabled_remote` token (05-authn §4.11, owned here): while `disabled` runs remote-reachable, health
    reports `degraded` with `auth_disabled_remote`, carried before F8 by the authenticated `degraded` field that R1 adds
    to HEAD's `GET /api/v1/system/status` (from F8 by core's authenticated endpoints), and never on the unauthenticated
    `/health`. `TestAuthOffDockerBoots` reads it there.
- **App SDK** (06 §6.10):
  - Extend `TestDepsScoping` (R1-20's) with the F4 cell: a publish to a topic outside hello's catalog entries is
    refused.
  - Delete `TestLegacyAuthGinOnlyNamedModule`'s `LegacyAuthGin` half with `LegacyAuthGinUntil`, and keep its `OwnAuth`
    cell (an `OwnAuth` route without a module method fails boot).

## Invariants

- Every intermediate `main` boots and passes e2e; the PR is atomic.
- API keys and ABS phone-app sessions survive; web sessions and one-time tokens reset (D12 (b)).
- The bridge never authenticates; it only reads the principal stage 12 bound.
- No route is redirected; no alias (D10).

## Acceptance

```bash
go build ./... && go vet ./...                                                                  # exit 0
go test -count=1 ./internal/server/... ./internal/books/... ./internal/usermerge/... ./internal/cfaccess/...   # ok
go test -count=1 -run 'TestOAuthCallback_HEADPathServedByCore|TestOAuthStart_HEADPathServedByCore|TestIdentityCutover_|TestCutover_NoRawTokenOnDisk|TestOIDC_AppHandoffMintsABSCode|TestWorkerKey_Confined|TestCutoverRefusedByOlderBuilds|TestBootCrashMatrix_ABO|TestRestoreHooksWriteNativeRows|TestCrashBetweenSwapAndHooksResumesHooks|TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor|TestCoreRoutesReplaceGin|TestOldLinkFormsNotServed|TestAuthOff_AuthModeWriteRefused' ./...   # PASS
(cd core && go test -count=1 -run 'TestWorkerKey_Confined|TestBootCrashMatrix|TestBootPhaseOrder_NoWriteBeforeCheckpoint|TestStage13_StaleCookieOnPublicRouteIsAnonymous|TestDepsScoping|TestAuthOff_AuthModeWriteRefused' ./...)   # PASS
make sdkguard                                                                                   # exit 0 (closure matches the regenerated list)
ls -d internal/auth 2>/dev/null | wc -l                                                         # 0
grep -rn 'LegacyAuthGinUntil' --include=*.go . | grep -v _test | wc -l                         # 0
grep -rn 'auth\.Can(' --include=*.go internal/server | grep -v _test | grep -v legacyauth | wc -l   # 0
grep -rn 'api/v1/auth/' .claude/skills/server-bootstrap scripts web/src | grep -v 'api/v1/auth/oauth' | wc -l   # 0
(cd web && npm run build && npm test -- --run)                                                  # exit 0
make -C core ci                                                                                 # exit 0
make ci                                                                                         # exit 0
make test-e2e                                                                                   # exit 0, the two fragment-link specs included
```

## Out of scope

Tasks and the log journal (F5); realtime (F6); the class-`i` config fields (F7); `/health` (F8).

## Rollback

As F3: `make rollback-restore` to the pre-freeze release for a deployed build; a revert on `main` is the lead's decision.

## Repo rules

Worktree only; file headers on every file (fragments headerless); move PR rules (old ==> new list, every commit builds,
`go fix`); conventional commit ending `Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer;
`gh pr merge <n> --rebase` alone in its wave with every required check green, never squash, never `--admin`.

```
feat(auth)!: identity flip: core auth for the legacy mount, native identity routes, M3 registered

Co-Authored-By: Claude <noreply@anthropic.com>
```
