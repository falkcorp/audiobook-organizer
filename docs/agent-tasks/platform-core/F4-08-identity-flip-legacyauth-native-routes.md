<!-- file: docs/agent-tasks/platform-core/F4-08-identity-flip-legacyauth-native-routes.md -->
<!-- version: 1.3.0 -->
<!-- guid: 0ad91c06-04c3-47a1-bdf3-1ab090720204 -->
<!-- last-edited: 2026-11-19 -->

# F4-08 — F4 flip, one PR: `LegacyAuthCore` with `LegacyAuthGinUntil` deleted, the `legacyauth` bridge, native identity and settings routes with the gin routes deleted, the OAuth paths served by core, M3 registered, the events bus live, the identity UI and the SPA's reset control

| Field | Value |
|---|---|
| ID | F4-08 |
| Release / flip | R2, **the identity flip F4** (one atomic PR, 09 §9.2.1) |
| Tier | **T4** (Opus, xhigh–max): the identity flip replaces the auth chain of every gin route |
| Wave | W34 — the only PR of its wave touching `main.go`, `internal/server/` and books' registration, and the wave's one owner of the kernel and module wiring files; it **merges alone**; runs inside an announced **move window** |
| Depends on | F3-07, F4-03, F4-04, F4-05, F4-06, F4-07, F4-09, F4-10, F4-11 |
| Owns gate tests | `TestOAuthCallback_HEADPathServedByCore`; `TestOAuthStart_HEADPathServedByCore`; `TestIdentityCutover_ABSSurvives` (the ABS conformance suite on the migrated corpus); `TestCutover_NoRawTokenOnDisk`; `TestOIDC_AppHandoffMintsABSCode`; the route cells of `TestWorkerKey_Confined`; `TestAuthOff_AuthModeWriteRefused` (05-authn §10); `TestAuthOffDockerBoots` (image job, 05-authn §10); the `auth_disabled_remote` health token on the authenticated `degraded` field (05-authn §4.11) |
| Gate | Plan, then execute autonomously. **Not before the Preconditions below hold** (amendment 2026-11-19, owner-reviewable). |

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
functions, its health tokens and the rbac and audit restore hooks it built).

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
   `auth.BootWrites`. Register every restore hook with F4-11's runner: auth's `PostRestore` (F4-02), and rbac's and
   audit's (F4-11). F4-11 builds both functions, the journal format and those two hook bodies; this row only calls and
   registers them. **Health tokens:** register F4-11's `revocation_journal_unwritable`, `revocation_journal_broken`,
   `revocation_journal_reset` and `revocation_journal_colocated` on the kernel-side health component on F2-10's
   registry (05-health §5H.3.1 names `core.backup` for the colocation token), on authenticated endpoints only: before
   F8 that is the authenticated `degraded` field of HEAD's `GET /api/v1/system/status`, never `/health`
   (05-backup §5B.8; 05-health §5H.3.1). Add the F4 module members to `core/module/` (`Permissions`, `Roles`,
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
   version number). M8 stays exactly as F3-07 registered it, last: M3 is inserted before it, and M8 is not renamed or
   replaced (F3-04 rule 3; D15, advisor S2). Bump the raised owners' declared `Spec.Level` in the same commit.
   F3-06's test checks the registered list: the levels are raised to the build's target before the first step of each
   flip, versions are contiguous per owner, one M8, last. In `internal/books/cutover/stepids.go` add this flip's level
   constant and level-step ID beside F3-06's (never editing an earlier level's or the M8 ID). F4-06's M3 constructor takes
   `RaisesLevel` and the `After` level-step ref from its caller: pass this flip's constant and level step here.
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
    lands with the SPA's switch to the generated client, in `web/eslint.config.mjs` (in this row's list).
11. **04 keys of F4 moved here:** `internal/auth` → core `auth`/`rbac` (delete `internal/auth/` once empty);
    `internal/server`, `internal/server/middleware` → core `auth`; `internal/server/handlers` → core `internal/authhttp`;
    core `audit` serving; `.mockery.yaml` entries of deleted interfaces.
12. **coretest's authentication half** (06 §6.4.8): complete R1-37's `WithUser`, `As`, `Anonymous` and
    `AssertPermission` over core auth's principal at stage 13, in `core/coretest/harness.go` and `assert.go`, keeping
    R1-37's exact signatures: `WithUser` seeds a principal holding exactly its permissions, `As` returns a `Client` whose
    requests reach stage 13 as that principal, and R1-37's "needs core authentication" failure path is deleted.

## Pre-scope amendments (2026-11-19) — owner-reviewable decisions, not settled facts

A read-only pre-scope against `origin/main` `5a24c5b2c` found that this brief, written against HEAD `ac4368bdb`, omits
blockers, paths and live effects. Everything in this section is a **proposed decision** that the coordinator took from the
pre-scope's Recommended option; the owner may overturn any of it. Anchors below are `origin/main` values at that SHA and
shift as F3-07's slices land: regenerate them at BASE.

### Preconditions (five blockers the first draft did not list)

1. **F4-02-ROLE-GUARD must land first**, as its own pre-PR (**P-A**, S/M, no F3-07 dependency). This brief has the builder
   implement `rbac.Holders`; without the role-definition last-admin guard, an rbac role edit and an auth user write can
   commit in the wrong order and leave no active admin. The fix touches `core/rbac`, `core/internal/rbacstore` and
   `core/auth` (all outside the Files table) and needs a concurrent last-admin test.
2. **The reach obligations block `LegacyAuthCore`.** Six `reachObligations` rows plus the handler-admin pin
   (`internal/books/cutover/reach_flip_obligations_test.go`, `reach_pin_source_test.go`) make
   `TestReachFlip_FailsWhileThePinOrTheOwnerlessModelStands` fail the moment `internal/books/module.go` says
   `LegacyAuthCore`. This PR must also: write `TestReachRegisteredPath_{AbsReplay,HandlerRefusals,HiddenHandlerChecks,OwnerSet,OwnerAllow}`
   (`internal/books/cutover`) with their declared bars; write `TestLegacyStage13_RandomRolesAdmitOnlyWhatTheSuccessorAdmits`
   (`core/httpapi`); discharge the 11 pinned cells and delete the pin files; replace the test-local replica
   `reach_eval_test.go` with the registered path; build the core-route half of the oracle (auth-off against every core
   route, 09 §9.2.6); port `TestReachHeadHandlers_RefuseTheAuthDisabledPrincipal` (`internal/server/reach_head_refusals_test.go`)
   to the registered path. Size L+, the largest unlisted item.
3. **F3-07 must merge with ABO's `CoreConfigBoot` on** (slice (e): `flipDeclared` on, `CoreConfigBoot` set in ABO's
   options, `R1Values` and the gate deleted). P4d only exists behind that gate, F4-11's `reapplyRevocations` refuses before
   P4d step 1 (`errReapplyNotReady`) and needs the keyring only the gated boot opens. If F3-07 merges with ABO gate-off,
   F4-08 cannot run P4d steps 2-3 and would absorb F3-07's live switch, which is out of scope (STOP 3).
4. **The F4-03 owner-link gate.** `seams.d/F4-03.md`: F4-08 must not register `cfaccess.Extension` until an owner-link path
   exists, yet `F4-07-OWNER-SET` and `F4-07-OWNER-ALLOW` need `internal/cfaccess` configured on the registered path. The
   code match was checked end to end (subject, provider `cfaccess`, `auth.CheckImportIdentity`), so the gate is met if
   prod already holds the owner account's `oauthid:` row with provider `cfaccess` (M3 imports it). **Before dispatch the
   coordinator runs a read-only prod count of those rows.** If absent, build F4-05's deferred prove-the-password link as a
   pre-PR; never seed the row by hand. Not verified: that `oidc.Resolver`'s linked fast path still applies admission
   (`Policy.AllowedDomains`, allowlist, `OwnerEmail`); `TestReachRegisteredPath_OwnerAllow` must prove the `ExtensionConfig.Policy`
   this PR builds admits the owner's address.
5. **The crash matrix needs a labelled-FS seam the revocation journal lacks** (**P-B**, M/L, own pre-PR, F3-07-independent).
   F4-11's file writes go through raw `os` (`core/backup/journal/file.go` `OpenFile`, `location.go` temp file and
   `Rename`, `move.go` `MkdirAll` and `Rename`), which the crash kit cannot see or break. P-B adds the seam (mirror F3-07's
   `internal/statefs`) plus writer-table rows in `core/coretest/crashmatrix/rows.go`. Separately, every new boot batch
   needs a `bootseam.Region(ctx, label)` at its commit site (`auth.BootWrites`, `rbac.CommitSeeds`, the re-apply's tail,
   gap, restore-line and head commits, `served_boot`, the hooks' writes, and TODO F4-11-F4-08-QUEUE's writers: queue
   batches, dequeue, `MarkUnappliedClosed`, `restore_run` write/clear, the kept admin's restriction commit, the withheld
   head commit), which needs edits inside `core/auth`, `core/rbac` and `core/kernel/restore_*`.

### Pre-PRs and sequencing (decisions Q1, Q7)

| Pre-PR | Scope | Size | Lands |
|---|---|---|---|
| P-A | F4-02-ROLE-GUARD (optional role-guard interface on `rbacstore` plus an `rbac.Service` method auth calls in its batch, concurrent test) | S/M | now, own PR |
| P-B | labelled-FS seam for `core/backup/journal` plus crashmatrix rows | M/L | now, own PR |
| P-C | F4-03-OWNER: **not a pre-PR.** Done inside this PR **after the switch commit**, when only the config and repairs readers remain (a rename, TODO F4-03-OWNER). Acceptance needs `internal/auth` gone, so it cannot be a follow-up | S | inside F4-08 |
| P-D | this docs amendment | S | now |

Decision Q1: one draft branch of dark commits, each of which builds, steps 1-7 dark and the switch (Goal items 1 and 3, as
the last behavioural commit) the only live one, merged as one PR under coordinator gate. Only the deadcode-baseline
prune, the `.mockery.yaml` cleanup and shard weights may defer to a follow-up. Do not split the SPA, skill or scripts out
(STOP 4), and do not mount core routes on ABO as an earlier dark step (reasoning, not verified: core's identity store is
empty before M3).

### Size and builder split (proposed)

Well beyond the plan's 6.5-day flip estimate. Goal items 0 (kernel P5/P7 and P4d steps 2-4), 1-3 (routes), 6 (books), 2
(bridge), 10 (SPA, skill, scripts) and the reach-obligation work are each L. Plan **2-3 builders on the one stacked
branch**: (1) kernel/core, (2) books/server/bridge/reach obligations, (3) SPA, skill and scripts; exactly one of them owns
`core/kernel/boot.go` and `kernel.go`. The builder bases on post-F3-07 main and does not start before F3-07 merges.

### Live-behaviour switches (16)

There is no off-switch: the PR is atomic by design (09 §9.2.1) and deleting gin routes cannot be dark. The gate is merge
and deploy: keep the branch draft, merge only with coordinator and owner sign-off, hold the next deploy for the owner's OK.

| # | Switch | What prod sees after deploy | Gate |
|---|---|---|---|
| 1 | books mount `LegacyAuthGin` to `LegacyAuthCore` | every gin route authenticates through core stage 12/13 | merge and deploy hold |
| 2 | gin identity, OAuth and settings routes deleted | old bundles, scripts, external callers of `/api/v1/auth/*`, identity `/api/v1/users*`, settings `k`/`s`, `/auth/temp-login` get 404 (no alias, D10) | SPA, skill, scripts switch in the same PR |
| 3 | M3 at P4b plus the new level step(s) | identity rows move to `c/auth/` and `c/rbac/`; store level raised; **no binary rollback** (`make rollback-restore` plus checkpoint) | run `storage cutover-blockers list` on prod data first |
| 4 | web sessions and one-time tokens reset (D12 b) | every browser user signed out; pending invite, temp-login and reset links die | owner comms; ABS app sessions and `abk_` keys survive (`TestIdentityCutover_ABSSurvives`) |
| 5 | OAuth start/callback served by core (PKCE and nonce new, same callback path) | redirect URI unchanged; flow differs slightly | `TestOAuthCallback_HEADPathServedByCore` |
| 6 | `cfaccess.Extension` registered | Access sign-in goes through `oidc.Resolver` link rules; leaving it unregistered loses the owner's Access path | precondition 4 |
| 7 | `/metrics` needs `core:metrics.read` | the prod scrape key keeps working unless it is a Confined worker key | `TestMetricsScrape_MigratedReach`; check the scrape key |
| 8 | P4d steps 2-4 | new refuse-to-serve boot failures on journal errors; new journal file in the backup dir and `backup.location.json` in the state dir | health tokens; crash-matrix cells |
| 9 | bootstrap token written by `BootWrites` | token file name or location may change from `.bootstrap-token`, breaking the `server-bootstrap` skill | skill updated in this PR; verify the path it reads |
| 10 | auth-off posture (`DefaultDisabledAllowRemote: true`) | only if a deployment runs auth-off | `auth_disabled_remote` token |
| 11 | events bus live (`core.rbac.changed.v1`, `core.principal.invalidated.v1`) | internal only | none needed |
| 12 | factory-reset control back in the SPA | visible owner-only destructive control | owner guard plus two-step count confirmation |
| 13 | `CoreConfigBoot` on for ABO (F3-07's switch) | config/secrets boot, M2, M7 | must already be live (precondition 3) |
| 14 | SSO link rule moves to core `oidc` | an unlinked SSO identity whose address belongs to an admin with a password is refused `email_in_use`; non-ASCII addresses refused | owner comms; precondition 4's check covers the owner |
| 15 | audit visibility default-deny | a non-manager no longer sees own sign-in rows or module actions in `/audit`; `credential_id` and `client_addr` stripped | owner may loosen (one-line change) |
| 16 | an M3 preflight blocker refuses the boot | a prod blocker (two usernames folding to one, an undecodable merge journal, a rejected user row) fails the upgrade boot | `storage cutover-blockers list` on a prod copy (Q9) |

### Decisions recorded (the pre-scope's §6, Recommended answer taken; all owner-reviewable)

| Q | Decision (proposed) |
|---|---|
| Q1 | One atomic PR from one draft branch of dark commits; switch is the last live commit; see Pre-PRs above |
| Q2 | F4-08 waits for F3-07 to merge with slice (e) complete (precondition 3) |
| Q3 | Coordinator runs the read-only prod `cfaccess` `oauthid:` count first; if present, register the extension (tests use fixtures); if absent, build the prove-the-password link as a pre-PR; never seed by hand |
| Q4 | The Files table gains the paths below; the coordinator pre-registers them in `inventory.json` and re-runs `check_waves.py` against F7-08 before dispatch |
| Q5 | The `api/v1/auth/` acceptance grep excludes `scripts/r2corpus/`, `scripts/tests/test_record_r0_*.py` and `scripts/tests/test_write_r0_corpus.py` (they record the R0 binary and keep R0's routes), and adds `web/src/services/api.ts` to the checked set |
| Q6a | A non-empty unapplied queue raises an info-severity authenticated `degraded` token (name coined here, added to 05-backup §5B.5 and the "never on `/health`" golden) plus offline `storage journal-queue list\|clear <seq> --confirm` |
| Q6b | Re-enable is journalled as a new `user.enabled` kind in `KnownKind`, with a spec amendment (no age rule) |
| Q6c | The health endpoint shows token and severity only; file paths go to the log |
| Q6d | Compare `Header.App` at journal bind (mismatch reports `revocation_journal_broken`); the shared-`db_path` case stays a documented limit unless the owner wants a `StoreIdentity()` check |
| Q7 | P-A and P-B are separate pre-PRs now; P-C stays inside F4-08 after the switch |
| Q8 | `cutover.Flip` changes from a single `Level` to an ordered `Levels` list (F5-18 needs the same); F3-07's builder is asked only for a one-line note in `seams.d/F3-07.md` |
| Q9 | Hold the deploy for explicit owner OK; owner told beforehand that browser sessions end, pending invites and temp links die, phone apps and API keys keep working, rollback is a restore not a binary swap; `storage cutover-blockers list` on a prod copy must report zero blockers first |
| Q10 | Pre-approve a minimal edit to the image job's probe step for `TestAuthOffDockerBoots`: pinned SHAs only, pushed with git, never the MCP contents API (resolves STOP 3 for `.github/workflows/` and `scripts/ci/` for that one edit) |
| Q11 | The builder reads 03 §3.8's F4 row and 05-storage §5S.3.13 first and records which owners' levels F4 raises in `seams.d/F4-08.md`; if silent, raise books only (`BooksIdentityLevel = 2`, `"books.level-2"` are proposed names); the coordinator confirms |

### Corrected anchors

`LegacyAuthGinUntil` has 15 non-test lines across files outside the Files table (below). The raw `auth.Can(` count is
**11 calls and 3 comments** under `internal/server`, plus one raw call at `internal/cfaccess/owner_guard.go:74`.
`internal/auth/owner.go` and `owner_test.go` still exist until F4-03-OWNER, whose callers are in `internal/config`,
`internal/repairs/owner.go`, `internal/plugins/maintenance/repairs_ops.go`, `internal/server/handlers/repairs/handler.go`
and `internal/server/middleware/auth.go`. `internal/books/cutover/steps.go` does not exist on main until F3-07 slice (d);
its `Flip.Level` is hard-wired to `BooksCutoverLevel`, hence Q8. Real `api/v1/auth` callers also include
`web/src/services/api.ts` and `web/src/pages/AcceptInvite.tsx`.

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
| modify | `internal/books/cutover/stepids.go` (add this flip's level constant and level-step ID) |
| modify | `core/coretest/harness.go`, `core/coretest/assert.go` (Goal item 12) |
| modify | `web/src/`, `.claude/skills/server-bootstrap/`, `scripts/` |
| create | `web/tests/e2e/` (the two fragment-link Playwright specs) |
| modify | `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json` |
| modify | `web/eslint.config.mjs` (the permission-literal rule, Goal item 10) |

**Added by the 2026-11-19 pre-scope (Q4, owner-reviewable; each was a STOP 3 trigger):**

| Action | Path | Why |
|---|---|---|
| modify | `internal/books/options.go`, `internal/books/cutover.go` (F3-07's), `internal/books/crash_matrix_test.go` | `LegacyAuthGinUntil: ID`; the `app.Options.Auth` and `Authenticators` values are built here; flip wiring; ABO crash cells and allowlist |
| modify / create | `core/httpapi/` (`router.go`, `problem.go`, `legacy.go`, new `legacy_stage13_random_roles_test.go`) | delete `LegacyAuthGin` and `ErrLegacyAuthGin`; stage 13 honours `core.auth.owner.required=false`; the random-roles discharge test |
| modify | `core/module/validation.go`, `core/module/settings.go`, `core/module/module.go` | `ProblemLegacyAuth` text; `Importers.Auth/Roles/Audit` for M3 (same instances as the kernel's) |
| modify / create | `core/kernel/` (beyond `boot.go`, `kernel.go`; several new files expected) | P4d journal wiring, reapply and hooks, health registration, `restoreAborted` from swap-back, the Holders adapter, the taskhttp `VerifiedMethod` closure, `confighttp.Deps.ReadPerm`, the reset `Stager` |
| modify | `core/app/app.go` | passes `LegacyAuthGinUntil`; new options |
| modify | `core/backup/journal/`, `core/coretest/crashmatrix/rows.go` | labelled-FS seam and writer rows, only if P-B has not landed |
| modify | `core/rbac/`, `core/internal/rbacstore/`, `core/auth/` | `bootseam.Region` labels at each new boot batch; `auth.Service` exports the e-mail lock for `PATCH /me`; F4-02-ROLE-GUARD only if P-A has not landed |
| modify | `core/internal/authhttp/` | the `Profiles` implementation for `PATCH /me` |
| delete | `internal/legacyoauth/` | F4-03: this PR deletes it and the gin binder |
| modify | `internal/config/`, `internal/repairs/owner.go`, `internal/plugins/maintenance/repairs_ops.go` | F4-03-OWNER callers (after the switch commit) |
| modify / delete | `internal/books/cutover/reach_*_test.go` only (`reach_eval_test.go`, `reach_flip_obligations_test.go`, `reach_pin_test.go`, `reach_pin_source_test.go`, new `reach_registered_path_test.go` for the five `TestReachRegisteredPath_*`); never the directory, because F7-08 owns `internal/books/cutover/m6/` in the same wave. The `m3.Config` wiring and `Flip` shape live in `internal/books/cutover.go`, `steps.go` and `stepids.go` | precondition 2 |
| modify | `internal/books/store/booksclaims.go` (or wherever `Flip.Spec` retires claims) | retire the 13 `m3.SourcePrefixes()` and the 2 exact bootstrap keys (`seams.d/F4-06.md`) |
| modify | `docs/agent-tasks/platform-core/seams.d/F4-07.md`, `TODO.md` | clear the obligations rows both ways; check off F4-07-*, F4-02-ROLE-GUARD, F4-11-F4-08-*, F4-03-OWNER |
| modify | `scripts/ci/` and, for `TestAuthOffDockerBoots` only, the image job in `.github/workflows/` (Q10) | the auth-off image probe |

This PR is its wave's only owner of the kernel and module wiring files.

## Spec

09 §9.4 R2 flips table F4 row, §9.6 rows 1, 4, 9–12, 20, 24, 26, §9.2.6; 03 §3.8 F4 and M3; 05-http-api §8.1 F4 row and
the paragraph after the table, §8.3 (the bridge table), §3.5, §4.9; 05-authn §3.2 (`Options`), §3.3, §4.7, §4.10, §4.13,
§5, §10 rows named below, §11 (release-note text); 05-rbac §6 (all), §12; 07 §7.19 step 3, §7.4.3, §7.9.1; 06 §6.4.10 F4
row, §6.5.1 P4d steps 2–4, P7, §6.10 rows named below; 05-backup §5B.3.4 step h, §5B.3.6 (what F4-11's functions do;
this row calls them), §5B.9 rows named below; 05-events §5E.13 "Order" item (1); 05-storage §5S.3.13 (a level step added per flip,
M8 registered once, `TestCutover_NoRawTokenOnDisk`), §5S.10 (`TestBootCrashMatrix_ABO`); 03 §3.8 (M7 and M8 at each
flip); 05-http-api §12 rows named below; 05-authn §4.11 (`auth_disabled_remote`); 05-health §5H.3.1 (that token's
route before F8); 07 §7.4.3, §7.18 (`TestOldLinkFormsNotServed`, the fragment e2e rows, the ESLint rule).

## Re-verify at BASE (HEAD `ac4368bdb` values shown; regenerate at BASE)

```bash
grep -rn 'LegacyAuthGinUntil' --include=*.go . | grep -v _test | wc -l          # > 0; 0 after (outside the archtest)
grep -c 'func RequireAuth' internal/server/middleware/auth.go                    # HEAD 1 (:152)
grep -rn 'auth.Can(' --include=*.go internal/server | grep -v _test | wc -l      # 14 grep lines = 11 calls + 3 comments (plus internal/cfaccess/owner_guard.go:74)
ls internal/server/wire_auth_routes.go | wc -l                                   # 1
ls internal/auth/                                                                # HEAD: auth_test.go context.go method_credentials_test.go owner.go … (owner.go gone after F4-03)
grep -rn 'api/v1/auth' .claude/skills/server-bootstrap scripts | wc -l           # HEAD 22 caller lines
ls -d internal/server/legacyauth 2>/dev/null | wc -l                             # 0
grep -c 'cutover/m3\|m3\.' internal/books/cutover/steps.go                       # 0 (F3-07's list holds M2, M7, M8)
ls core/kernel/restore_reapply.go core/kernel/restore_hooks.go | wc -l            # 2 (F4-11)
ls core/rbac/restore_hook.go core/audit/restore_hook.go | wc -l                  # 2 (F4-11)
git grep -n 'revocation_journal_' -- core/kernel/boot.go core/kernel/kernel.go | wc -l   # 0 now; >= 1 after
grep -n 'needs core authentication' core/coretest/*.go | wc -l                  # >= 1 (R1-37's stub); 0 after
grep -n 'level-\|books.rewrite' internal/books/cutover/stepids.go                # F3-06's F3 level constant and M8's fixed ID (unchanged here)
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
3. The work needs a path outside the Files table above, including its 2026-11-19 additions: report before editing it.
4. A first-party caller of a deleted route remains on `main` after this PR.

## Steps (one PR; every commit builds)

1. Write the owned gate tests and the deferred cells first (below).
2. Kernel/module/app (Goal item 0): inject `authz.Compile`, mount F4-05's routes on `CoreGroup` and the OIDC paths on
   `CoreFixedGroup`, start rbac/auth/audit serving, the bus, P7 seeding, F4-11's P4d step 2 and step 3 functions with
   the auth, rbac and audit restore hooks registered, F4-11's `revocation_journal_*` tokens on the health registry,
   `BootWrites`, the `c/backup/` journal spec, stage 7/13 audit; the F4 module members; the offline commands and their `cmd/` wrappers; `app.Options.Auth`,
   `Authenticators`; delete `LegacyAuthGinUntil`; archtest; regenerate `core.json` and the client.
3. Books: `books.abs` with `AppHandoff`; `Deps.AuthCompat` in the ABS adapters; M3 in `steps.go`; add this flip's
   level constant and level-step ID to `stepids.go`, add this flip's level steps before M8 (left unchanged) and bump
   the raised owners' declared levels (Goal item 6).
4. Bridge `internal/server/legacyauth`; replace the gin auth chain and the 14 raw `auth.Can` sites; delete `RequireAuth`
   and the gin identity, OAuth and settings (`k`/`s`) routes; move `client_trust.go`'s Access branch.
5. Delete HEAD's Start-time identity writers and HEAD's merge undo of pre-R2 journals (409 `merge_permanent`).
6. SPA, skill and scripts on the native routes; reset control back; the two fragment-link Playwright specs under
   `web/tests/e2e/`.
7. coretest's authentication half (Goal item 12). Move `internal/auth` per the 04 keys; `.mockery.yaml`; layering and shard weights; regenerate
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
  `TestCutoverRefusedWhileMergeInProgress` (undo → 409 `merge_permanent`), the mid-M3 cell (not in
  `TestCutoverRefusedByOlderBuilds`, whose corpus boots run M3 to completion or fail before it and never stop
  inside it: its own file, `internal/books/midm3_test.go`, over a HEAD store killed between M3's chunks and
  failed between its families: `TestMidM3_KillLeavesEachRecordInOnePlace`,
  `TestMidM3_OlderBuildRefusesBeforeAnyWrite`, `TestMidM3_ThisBuildResumesEveryRecordOnce`,
  `TestMidM3_HalfStoreIsNeverServed`; run by `boot-crash-matrix-abo`), `TestIdentityCutover_ReachOracle` on the
  registered path, `TestMetricsScrape_MigratedReach`,
  `TestCompat_ABSLoginHonoursRestrictions`, `TestBootWrites_RunInP4d`, `TestConfining_WorkerKeyRefusedOnPlaylists`,
  `TestAuthOff_BooksGatesAndInHandlerDenial`, `TestAuthOff_SettingsManageKeepsReach`, the core half of
  `TestUserMerge_ScopedAdminKeyRefused`.
- **Levels and M8 (order M2):** F3-06's registered-list test (levels raised before the first step of each flip,
  versions contiguous per owner, one M8, last) stays green on this build. No previous `main` is built (F2-15 rule 9;
  D15).
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
  - `TestPostDAGReconcilerOrder`'s real-boot cell (05-storage §5S.10; F4-11 owns the dark cell): on the booted App the
    reconcilers run in 06's order after the DAG, before any listener, and the journal re-apply sees the overrides and
    the keyring.
- **Health tokens** (05-health §5H.3.1, §5H.9; 05-backup §5B.8):
  - `TestHealth_UnauthenticatedTokenSetGolden`'s F4 cell: the four `revocation_journal_*` tokens are in its
    "never on `/health`" set; raised at once, none appears on `/health`.
  - A booted cell (name coined here: `TestHealth_RevocationJournalBrokenRaised`): a journal whose chain is broken
    before boot raises `revocation_journal_broken` (degraded) on the authenticated `degraded` field of
    `GET /api/v1/system/status`, and not on `/health`.
- **coretest** (06 §6.4.8): `TestAssertPermission_Hello`'s authenticated cells (R1-37 coined the test): over hello, a
  route with a real permission answers 403 to a user lacking it and neither 401 nor 403 to a user holding exactly it;
  `As` no longer fails with R1-37's "needs core authentication" message.
- **ESLint permission-literal rule** (07 §7.18): `web/eslint.config.mjs`'s `no-restricted-syntax` selector passes over
  `web/src/` after the switch, and fails on a fixture passing a string literal to `usePermission`.
- **Routes** (05-http-api §12):
  - `TestStage13_StaleCookieOnPublicRouteIsAnonymous`: a stale cookie on a Public route is cleared and the request
    proceeds anonymous.
  - `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor`: every manifest route gated by `settings.manage` or
    `users.manage` carries the atom table's full-reach entry; a custom role holding the four `settings.manage`
    expansion scopes gets 403 on backup restore, backup create, logs, tool install and plugin settings.
  - `TestCoreRoutesReplaceGin` (created here, with the F4 cell): on the real engine no gin route remains at a path this
    flip's native routes replace (the flip's generated list). F5-18, F6-03, F7-09 and F8-04a/b each add their cell.
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
(cd core && go test -count=1 -run 'TestWorkerKey_Confined|TestBootCrashMatrix|TestBootPhaseOrder_NoWriteBeforeCheckpoint|TestStage13_StaleCookieOnPublicRouteIsAnonymous|TestDepsScoping|TestAuthOff_AuthModeWriteRefused|TestPostDAGReconcilerOrder|TestAssertPermission_Hello' ./...)   # PASS
go test -count=1 -run 'TestHealth_UnauthenticatedTokenSetGolden|TestHealth_RevocationJournalBrokenRaised' ./...   # PASS
grep -n 'needs core authentication' core/coretest/*.go | wc -l                                 # 0
(cd web && npx eslint src)                                                                      # exit 0 (the permission-literal rule on)
make sdkguard                                                                                   # exit 0 (closure matches the regenerated list)
ls -d internal/auth 2>/dev/null | wc -l                                                         # 0
grep -rn 'LegacyAuthGinUntil' --include=*.go . | grep -v _test | wc -l                         # 0
grep -rn 'auth\.Can(' --include=*.go internal/server | grep -v _test | grep -v legacyauth | wc -l   # 0
grep -rn 'api/v1/auth/' .claude/skills/server-bootstrap scripts web/src | grep -v 'api/v1/auth/oauth' | grep -v -e '^scripts/r2corpus/' -e '^scripts/tests/test_record_r0_' -e '^scripts/tests/test_write_r0_corpus.py' | wc -l   # 0 (Q5: R0 recorders excluded, web/src/services/api.ts included)
(go test -count=1 -run 'TestReachFlip_FailsWhileThePinOrTheOwnerlessModelStands|TestReachRegisteredPath_|TestLegacyStage13_RandomRolesAdmitOnlyWhatTheSuccessorAdmits' ./internal/books/... ./core/httpapi/...)   # ok (precondition 2)
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
