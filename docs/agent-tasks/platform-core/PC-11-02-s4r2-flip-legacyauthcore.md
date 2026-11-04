<!-- file: docs/agent-tasks/platform-core/PC-11-02-s4r2-flip-legacyauthcore.md -->
<!-- version: 1.0.0 -->
<!-- guid: f779952f-8689-4105-a3d4-84b6ae1db33d -->
<!-- last-edited: 2026-11-04 -->

# PC-11-02 — S4 r2 flip: LegacyAuthCore decides every request; native routes and aliases mount; P4d identity steps, P7 seeding and BootWrites start; role-name checks become permission checks; the identity UI switches in the same build

**ID:** PC-11-02 · **Wave:** W11 (S4 r2) · **Priority:** P1 · **Effort:** XL
**Tier:** T4 Opus/xhigh-max — the one stage in which the auth engine changes (09 S4 Goal); reach, HEAD's identity locks
and single-use tokens must hold across the switch, and 09 S4's UI row puts the UI switch in the same build.
**Depends on:** PC-11-01 (native routes and aliases), PC-11-04 (the SPA link readers). This is the stage's **flip PR**.
PC-11-03 follows it.
**Stage-Release trailer:** `Stage-Release: S4 flip` on every commit.
**Marker:** `ci/stage.txt` → `shipped_stage: S4`, `release: S4r2`. Merges only with a current `S4r1` soak row of at least
14 days (09 §9.2.1 check (a)).
**Required checks beyond `make ci`:** `compat-rehearsal` (cells a, b, c and e at r2; 09 §9.10), `unit-lifecycle`,
`extraction-gates`, `docker-image` (`TestS4_AuthOffDockerBoots`), `core-ci`, `make test-e2e`.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Behaviour rules 4-24; Rollback rows 3, 4, 11; Done-when).
**Owning spec:** 09 S4 (Releases r2, Moves, Permissions, UI, Invite and temp-login links, Done when); 07 §7.19 step 3,
§7.4.3, §7.9.1; 03 §3.5.1 (`legacyauth` in `layerOf`); 05-config §1.3.3 (the `core.settings.update` audit record from
S4); 05-http-api §8.1 S4 row, §8.3 (bridge contract); 05-authn §4.2 (stage 12 order), §4.6 (bootstrap), §4.11 (auth-off),
§4.14, §5; 05-rbac §4.6, §6.3; 09 §9.2.6.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-11-02" -b agent/pc-11-02-s4-flip origin/main
cd "$REPO/../audiobook-organizer-pc-11-02"
git rebase origin/main
git worktree list
npm ci --prefix web
```

`gh pr list --state open` must show no other open S4 r2 content PR (PC-11-01 and PC-11-04 merged). Use a
code-exploration pass first (map every gin auth element of 05-http-api §8.3 at your base), then implementation in the
commit order below, then a code-auditing pass (reach, locks, tokens) before the PR.

## Goal (one PR; every commit builds)

1. **Bridge.** New ABO package `internal/server/legacyauth` (imports only core `auth`, `rbac` and gin; enters
   `layerOf` as the floor, 03 §3.5.1): `FromCorePrincipal` (fills HEAD's context values from the stage-12 principal,
   never authenticates), the `s.perm` adapter over `rbac.Require(ctx, rbac.LegacyAtom(atom))`, `legacyauth.Can(ctx,
   rbac.LegacyCheck(atom, readSide))` replacing every raw in-handler `auth.Can` per the reach snapshot's in-handler list,
   and the adapters of 05-http-api §8.3 (`RequireAdmin`/`adminRole` → §6.2's permission per admin kind; `credGuard` →
   `Guards.Interactive`; `RequireOwner` → `Guards.Owner`; ABS `ABSRequireAuth` → `PrincipalFor` then
   `CheckCredentialFor`). Worker confinement moves to stage 13 for non-`OwnAuth` routes; the ABS adapter keeps it.
2. **Switch.** `MountLegacy(Auth: LegacyAuthCore)`; remove `app.Options.LegacyAuthGinUntil` (an archtest fails if it is
   set after S4); register `internal/cfaccess`'s extension through `app.Options.Authenticators`; pass
   `app.Options.Auth` (`InviteLinkForm: InviteLinkPath`, `DefaultDisabledAllowRemote: true`),
   `LegacyPermissionAliases` (the 13 atoms with their frozen `Reach` from the snapshot), `LegacyAmbientScopes`
   (`core:self`, `core:metrics.read`, `books:reading`).
3. **Boot.** P4d steps 2 (revocation re-apply), 3 (post-restore hooks), 4 (`auth.BootWrites`: `_system`, expiry stamp,
   bootstrap token and file, read-only key, `AuthReconciler.Reconcile`) and P7 seeding start; the `journal` duty is
   listed (the first r2 boot appends a `gap` line for r1's interval). Books' start hook stops running
   `seedRolesAndTokens` and `ReconcileVerifiedEmails`; `internal/auth/seed.go` and `internal/server/bootstrap.go` move to
   core (`rbac` and `auth`); `apikey_expiry_stamp.go` becomes a boot write of books' legacy auth store over HEAD's one-shot
   done flag. Seed roles are read-only (`ErrSeedRoleLocked`; `accept-seed` allowed); P7 writes HEAD's seed atoms.
4. **Routes.** The kernel mounts PC-11-01's native routes and aliases; the gin routes they replace are deleted except the
   unmapped ones and `PUT /api/v1/auth/me/password` (stays gin, calls the dominance-checked service). Books' manifest
   rewrites `GET /auth/temp-login?token=` to a 302 to `/login/temp#token=<token>` with `Referrer-Policy: no-referrer`;
   core issues `/login/temp#token=` links and `/invite/<token>` links (`InviteLinkPath`).
5. **Role-name checks → permission checks** at every site of 05-rbac §4.6 (`RequireAdmin`, `adminRole`,
   `handlers/user.go` ×2, `mergeActor` on PC-11-01's merge handler, repairs owner apply → `Can(core:admin)` key-scoped;
   `usermerge/plan.go` survivor and `ActorIsAdmin` → roles compiled role-only). Merge POSTs keep `Guards.Interactive`.
   `internal/usermerge` is rewired onto `auth` for its identity writes (`StageUserMerge` under `CommitLocked`).
6. **Config.** From S4 the committer stages the `core.settings.update` audit record in the same batch (05-config
   §1.3.3); the S4 actor rule replaces S3's (auth-off principal never holds `core:admin`; `HEADAuthOffWritable` keys keep
   HEAD's auth-off write rule; every other protected key refused with auth off through `/api/v1/config`).
7. **Auth-off.** The synthetic principal holds no `AuthRequired` or `Sensitive` scope; every `Guards.Interactive` route
   refuses it; health Degraded `auth_disabled_remote` with a Settings banner (HEAD's degraded token list until S8).
8. **UI, same build** (09 S4 UI): auth, users and config calls use generated client operations (`core/web` api-client);
   the five hard-coded permission constants (`THROTTLE_RESET_PERMISSION`, `LIBRARY_ORGANIZE_PERMISSION` ×2 files,
   `COLLECTIONS_MANAGE_PERMISSION`, `LIBRARY_DELETE_PERMISSION`) and the `APIKeysTab` scope list come from
   `GET /api/core/v1/permissions`; an ESLint `no-restricted-syntax` rule fails any string literal passed to
   `usePermission`/`useAction`/`hasPermission`, assigned to a `permission` or `*_PERMISSION` property or constant, or
   used in the key-scope list, unless it comes from `manifest.permissions` or the generated client; the Users page gains
   role management; `AcceptInvite` calls `POST /api/core/v1/auth/invites/lookup|accept` with the token in the body.
9. **Marker** `shipped_stage: S4`, `release: S4r2`; **release notes** rows 3, 4, 11 (texts in the digest), the auth-off
   tightening line, the 422 confirmation line (with PC-11-03), the `auth_disabled_remote` note, row 12's 401/403 line.

## Exact files

Create: `internal/server/legacyauth/`.
Modify: `internal/server/middleware/auth.go`, `internal/server/server_lifecycle.go`, `internal/server/wire_auth_routes.go`,
`internal/server/handlers/`, `internal/server/client_trust.go` (the rest of it is deleted; 05-http-api §8.4),
`internal/server/auth_temp_login.go`, `internal/server/apikey_expiry_stamp.go`, `internal/auth/seed.go` (moves),
`internal/server/bootstrap.go` (moves), `internal/usermerge/`, `internal/books/module.go`, `core/app/options.go`,
`main.go`, `internal/arch/layering_test.go`, `web/src/services/api.ts`, `web/src/hooks/useLibraryPermission.ts`,
`web/src/hooks/useCanOrganizeLibrary.ts`, `web/src/hooks/useCollections.ts`, `web/src/components/system/ThrottlePanel.tsx`,
`web/src/components/settings/APIKeysTab.tsx`, `web/src/pages/`, `web/eslint.config.js` (see Planner notes: the file is
`web/eslint.config.mjs`), `ci/stage.txt`.

## Re-verify (expected at the plan checkout `f07f516be`; rerun at base)

```bash
grep -n 's.seedRolesAndTokens()' internal/server/server_lifecycle.go             # 1 hit (:355; deleted here)
grep -n '^func (s \*Server) seedRolesAndTokens()' internal/server/server_lifecycle.go   # 1 hit (:1098)
grep -rnE 'slices\.Contains\([a-zA-Z_.]*Roles, *("admin"|adminRole|auth\.SeedRoleAdmin)\)' --include=*.go internal cmd | grep -v _test | wc -l   # 7 at the plan checkout; ratchet ceiling at base from PC-10-06; 0 after this PR
grep -rn 'auth\.Can(' --include=*.go internal | grep -v _test | wc -l           # N (equals the snapshot's in-handler list); 0 raw calls after this PR
grep -rnE "export const [A-Z_]*_PERMISSION = '" web/src --include=*.ts --include=*.tsx | grep -v test | wc -l   # 5
ls web/eslint.config.*                                                            # web/eslint.config.mjs at the plan checkout
grep -n 'LegacyAuthGinUntil' -r core/app/ main.go internal/books/ | head          # >= 1 hit (removed here)
grep -n 'shipped_stage:\|release:' ci/stage.txt                                   # shipped_stage: S3, release: S4r1
ls internal/books/rbacreach/                                                      # the frozen snapshot (PC-10-06)
test -d internal/server/legacyauth && echo EXISTS-STOP || echo absent             # absent
```

## Commit order (each commit builds and passes its package tests)

1. `legacyauth` package and its tests (unused). 2. Kernel/app options and books module wiring behind the switch, with
`LegacyAuthGinUntil` still set (inert). 3. Boot steps (P4d 2-4, P7, `BootWrites`) with the crash-matrix "staged restore
with hooks" scenario. 4. The switch: `LegacyAuthCore`, `LegacyAuthGinUntil` removed, gin auth middleware replaced by the
bridge, native routes mounted, replaced gin routes deleted, temp-login 302. 5. Role-name sites. 6. UI switch and ESLint
rule. 7. Marker. The switch (4) and the UI (6) are in the same PR, so no release ships one without the other.

## Done-check tests (names fixed by 09 S4, 05-authn §10, 05-rbac §12; all green in this PR)

`TestLegacyRoleReachUnchanged` (now through `LegacyAuthCore`, custom-role half and native cells enabled; golden unchanged
except 09 §9.2.6's recorded changes), `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor` (custom-role cells enabled),
`TestWorkerKey_NoAmbientScopes`, `TestLegacyAmbientScopes`, `TestLegacyRevokeRacesTouch_NeverUnrevokes`,
`TestInviteRedeemedOnce`, `TestBootstrapRedeemedOnce` (each through core auth and `storage.CommitLocked` over books' one
store), `TestLegacyStoresAreSingletons`, `TestRoleNameChecksUseCoreAdmin` and `TestNoRoleNameAuthz` (ceiling 0),
`TestRollbackR2ToR1Boots`, `TestAPIKey_StoredScopesNeverEmpty`, `TestOneFormat_LegacyRowsReadByPrevBuild` (the previous
release's real binary), `TestConfining_WorkerKeyRefusedOnABS`, `TestUserMerge_ScopedAdminKeyRefused`,
`TestS4_AuthOffDockerBoots` (image job), `TestLegacyTempLoginLink_EndToEnd`, `TestLegacyInviteLink_EndToEnd` (e2e,
including the fragment cell `TestLegacySPA_InviteFragmentReader` end to end), the crash-matrix "staged restore with
hooks" scenario, `TestLegacyFamiliesWrittenOnlyByLegacyStores` at its identity target, `TestLegacySeedAtomsEqualHEAD`,
`TestReseedFromHEADRows_AdminKeepsCoreAdmin`, `TestLegacyAtomEqualsHEADCheck`, `TestRowRuleDominatesLegacyReach`,
`TestVerifiedEmailReconcileInBootWrites`, `TestExpiryStamp_OneShot`, `TestBootWrites_RunInP4d`,
`TestLegacyInviteAcceptRechecksCanAssign`, `TestRevocationStagesJournalHeadAtS4`, `TestAuthOff_IntegrationFieldsWritableAsHEAD`,
`TestPreS7Facade_IntegrationURLIsProtectAdmin` (S4 variant), `TestLegacyScopedKey_KeepsLegacyRouteReach`,
`TestAuthEnable_MatchesHEAD`. Audit: `GET /api/core/v1/audit?kind=auth.login` shows the rehearsal login. New tests named
here: `TestSettingsChangeStagesAuditRecord` (name coined here; the row's "a settings change stages a
`core.settings.update` audit record": one `Apply` batch holds the change and the record, a failed batch holds neither),
and an ESLint fixture test `eslint-no-literal-permission` (name coined here) proving the rule fails a string literal
passed to `usePermission`. R-test (09 S4 Done when): every identity cell listed there, plus cells a, b, c and e.

## Invariants (STOP conditions)

- `TestLegacyRoleReachUnchanged`'s golden changes only in 09 §9.2.6's recorded cells; any other diff = STOP.
- No identity batch is committed outside `storage.CommitLocked` with the store's locks.
- No `mck_` key, hashed session or grant is written on ABO; rows stay HEAD's (09 §9.2.3).
- `TestRollbackR2ToR1Boots` red, or any R-test cell red = STOP (never mark flaky).
- The bridge never authenticates.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                   # exit 0
grep -rn 'LegacyAuthGinUntil' --include=*.go . | grep -v _test                  # no output
grep -rnE 'slices\.Contains\([a-zA-Z_.]*Roles, *("admin"|adminRole|auth\.SeedRoleAdmin)\)' --include=*.go internal cmd | grep -v _test   # no output
grep -rn 'auth\.Can(' --include=*.go internal/server | grep -v _test            # no output (only legacyauth.Can)
grep -n 'shipped_stage: S4' ci/stage.txt && grep -n 'release: S4r2' ci/stage.txt   # 2 hits
(cd web && npx eslint --no-warn-ignored src && npm run build && npx vitest run) # exit 0
make test-all                                                                   # exit 0
make test-e2e                                                                   # exit 0
make compat-rehearsal                                                           # exit 0
make compat-ledger                                                              # exit 0
make -C core ci                                                                 # exit 0
make ci                                                                         # exit 0
```

## Out of scope

The core factory-reset route and deleting `legacy.Maintenance` (PC-11-03); the invite fragment form and its 302 (S9);
`/metrics` permission (S8); identity hardening (H1).

## Rollback

Binary swap at no identity cost (09 S4 Rollback): sessions, keys, roles and invites are HEAD's rows; rows 3, 4 and 11 of
09 §9.2.1 apply, and r2's release notes carry row 11's text. r2 → r1 is a binary swap (`TestRollbackR2ToR1Boots`).

## Repo rules

Headers on every changed file (TS files included); `changelog.d/` fragment. Conventional commits with
`Stage-Release: S4 flip` and `Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase` after the
gate; never `--admin`. Boot writers extend `TestBootCrashMatrix` and `TestBootCrashMatrix_ABO` in this PR. CLAUDE.md
concurrency: `BootWrites` and journal re-apply loops over keys and sessions use the batch-per-family pattern of 05-authn
§4.14; no unbounded fan-out.

## Planner notes

- **Wrong path:** the row lists `web/eslint.config.js`; the repo's ESLint config at the plan checkout is
  `web/eslint.config.mjs`. Edit that file (re-check after UI-0, which may rename it).
- **Missing file:** `web/src/pages/AcceptInvite.tsx` switching to the core invite calls is covered by the row's
  `web/src/pages/` entry; PC-11-04 also edits `AcceptInvite.tsx` (its fragment reader) and lands first.
- `internal/server/handlers/` is shared with PC-11-01 and PC-11-03 (collision matrix: PC-11-01 → PC-11-02 → PC-11-03).
- The row's tier is right, but the PR is very large; the commit order above keeps every commit buildable for
  `check_commits_build.py`. The coordinator may split commits 1-3 into a preceding dark PR (`S4 2`) if review demands it,
  as long as the switch and the UI stay in this flip PR.
