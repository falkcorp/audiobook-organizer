<!-- file: docs/plans/platform-core/reviews/spec-round-2-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: ea867699-2dbf-4044-a247-06e303f7a81b -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 2 — dispositions

Inputs: `spec-round-2-{correctness,data-migration,frontend,modules-build,ops-rollback,security,simplicity}.md`.
Totals: **162 findings — 10 BLOCKER, 89 MAJOR, 63 MINOR.** All 162 accepted (some
through a resolution below); 0 rejected. Round-1 cross-cutting resolutions remain binding. Owner: the reviser of each
file the finding names (clusters as in round 1). Revisers must reject a finding with evidence if applying it shows it
to be wrong.

Lead verification: the revoked-key re-activation path behind SEC2-1/DATA2-3 was confirmed in code
(`internal/server/handlers/apikeys.go:336-372` → `internal/database/pebble_store_auth.go:1226-1247`: `SetAPIKeyStatus`
sets `Status` without checking `RevokedAt`); it is also filed as a live hotfix in
`todo.d/2026-11-04_platform_core_followups.md`.

## Round-2 resolutions (binding)

- **R2-1 Rollback floor = the Teach build.** Seven blockers share one root cause: a rollback may land on a build that
  predates the adoption logic and still has legacy behaviour that can widen access (re-activating a disabled key,
  unbounded `users.manage`, no revocation journal). The fix is structural: the first build of every adopt/expand
  sequence is a **Teach build** that (a) ships the legacy-side guards (refuse status changes on revoked or
  mirror-disabled keys; dominance bound on `users.manage`/`settings.manage` in legacy handlers; journal every
  revocation and restore out of DB), and (b) is the **minimum rollback target**: the per-owner level guard refuses to
  open the store with any older build once adoption has run. The live-defect hotfixes in the follow-ups fragment are
  the same changes and can ship earlier.
- **R2-2 Revocations always reach the authoritative copy.** Any revocation (including post-restore re-application)
  writes the legacy row in the same batch as the native one.
- **R2-3 Ownership is per stage.** Claim-mode changes are stamped level steps of the single guard.
- **R2-4 Legacy migrations before claims; honest stepping stone.** Books legacy migrations run before core claims are
  enforced; their code is kept until a declared minimum-upgrade-from release; older installs must pass through it.
- **R2-5 MOD-2 gate on real data.** Generated from `go list -deps -test -json`, per stage.
- **R2-6 Gates are real.** The extraction's gates become required checks (or block RC promotion); owner decision
  recorded in 14.
- **R2-7 One schedule-key rule** (owner-namespaced, declared, via `tasks.ScheduleFromConfig`).
- **R2-8 One boot-phase table** (06 §6.5.1); checkpoint before any write.

## Findings

| ID | Sev | Finding (reviewer's title) | Disposition | Note |
|---|---|---|---|---|
| COR2-1 | MAJOR | COR-1 not fully fixed: the books module cannot obtain or mount the realtime legacy projections | Accepted | Reviewer's concrete fix applied as written. |
| COR2-2 | MAJOR | The task-run redactor is placed on a core topic, which the registrar contract forbids (privacy regression versus HEAD) | Accepted | Reviewer's concrete fix applied as written. |
| COR2-3 | MAJOR | Modules have no handle to `notify`, and 05-events and 05-notify describe two incompatible delivery models | Accepted | Reviewer's concrete fix applied as written. |
| COR2-4 | MAJOR | Outbox retention counts only manifest-declared consumers, so durable topics consumed only by core are deletable at once | Accepted | Reviewer's concrete fix applied as written. |
| COR2-5 | MAJOR | The relay's task definition cannot deliver, and outbox-triggered writer runs default to Preview | Accepted | Reviewer's concrete fix applied as written. |
| COR2-6 | MAJOR | Seven files declare schedule keys that 05-tasks refuses at registration | Accepted (resolution R2-7) | Same as SIMP2-1. |
| COR2-7 | MAJOR | The boot-phase order disagrees across 06, 05-config and 05-storage: writes land before the checkpoint, and config reads its keyspace before  | Accepted (resolution R2-8) | One boot-phase table, owned by 06 §6.5.1; the pre-migration checkpoint precedes any write (config, storage, tasks); 05-config and 05-storage reference it. |
| COR2-8 | MAJOR | Three sources for the data and database paths; P2 as written cannot open ABO's database in the shipped Docker layout | Accepted | Reviewer's concrete fix applied as written. |
| COR2-9 | MAJOR | The landing order contradicts the import graph that the 05 files specify; S2's storage ownership and the `hello`/G6 landing are unspecified | Accepted | Reviewer's concrete fix applied as written. |
| COR2-10 | MAJOR | acquire consumes `item.completed` through two unordered consumers, and no consumer form can filter by owner | Accepted | Reviewer's concrete fix applied as written. |
| COR2-11 | MAJOR | Core task kinds execute module write callbacks, so the module's writes bypass G8 (COR-9's rule is not applied beyond the relay) | Accepted | Reviewer's concrete fix applied as written. |
| COR2-12 | MAJOR | Participant APIs take `storage.Batch` by value, so the `Bind` grant and the staged writes are made on a copy | Accepted | Reviewer's concrete fix applied as written. |
| COR2-13 | MAJOR | `app.Describe` runs only P1 and P6, which violates `Register`'s own precondition | Accepted | Reviewer's concrete fix applied as written. |
| COR2-14 | MAJOR | COR-16 half fixed: `hello` compiles but boot refuses it, and stale vocabulary remains | Accepted | Reviewer's concrete fix applied as written. |
| COR2-15 | MAJOR | Module callbacks are invoked outside the module's lifecycle: `DerivedIndex` hooks are manifest data that storage calls before `Register` | Accepted | Reviewer's concrete fix applied as written. |
| COR2-16 | MINOR | COR-7 residue: the grab-lock mechanism, `keyedmutex`'s home and the `Decide` pool are inconsistent | Accepted | Reviewer's concrete fix applied as written. |
| COR2-17 | MINOR | A Durable topic with Realtime metadata silently streams nothing | Accepted | Reviewer's concrete fix applied as written. |
| COR2-18 | MINOR | `ui.WriteManifestTS` takes an `app` type: an L6 → L9 import in a normative Go block | Accepted | Reviewer's concrete fix applied as written. |
| COR2-19 | MINOR | Naming and vocabulary contradictions across files | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-1 | BLOCKER | The "one ownership table" is static: S2b gives core families to owners that do not exist yet, and adopt/contract "claim flips" have no mecha | Accepted (resolution R2-3) | Ownership becomes a per-stage table; every claim-mode change (books → shared → core) is a stamped level step of the single per-owner guard, applied by the adopt/contract step that needs it. |
| DATA2-2 | BLOCKER | Skipped release: books legacy steps 59–66 write core-owned ops families and call code T8 deletes; "no stepping-stone release" is false | Accepted (resolution R2-4) | Books legacy migrations 59–66 run in a migration phase that precedes core claim enforcement, under an explicit, logged legacy-migration grant; T8 may not delete code they call until a declared minimum-upgrade-from release. 09 states the stepping-stone rule honestly: an install older than that release must pass through it (boot refuses with a clear message otherwise). |
| DATA2-3 | BLOCKER | The rollback build can re-activate a mirror-disabled or natively revoked API key: empty legacy scopes = full access, and "legacy wins" then  | Accepted (resolution R2-1) | Same as SEC2-1. |
| DATA2-4 | BLOCKER | Post-restore revocations are native-only: the next boot's rebuild from legacy undoes them; restored legacy sessions/invites stay valid | Accepted (resolution R2-2) | Same as SEC2-3. |
| DATA2-5 | BLOCKER | The revocation journal never sees rollback-era revocations, nor restores done by an older build | Accepted (resolution R2-1) | The Teach build writes the out-of-DB revocation journal for revocations and restores it performs; builds older than the Teach build cannot be rolled back to. |
| DATA2-6 | BLOCKER | S0 writes `c/` keys without `c/storage/install`, so S2b's first core boot halts with `ErrForeignKeys` | Accepted | S0 writes `c/storage/install` (the install marker) before any other `c/` key. |
| DATA2-7 | MAJOR | Restore boundary is a wall-clock `Manifest.CreatedAt` taken after the checkpoint; post-archive password changes are silently reverted | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-8 | MAJOR | Invites: mirrored (05-storage) vs read-in-place, not mirrored (05-authn, 09); the per-boot rebuild would wipe native invites | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-9 | MAJOR | `RequiresServed` and "the build that booted before" are not computable from the served ledger: no ≥-level rule, no reset on rollback, pre-gu | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-10 | MAJOR | Integration instances: one-shot import and no blob mirror; 09 S7's rollback claim is false and rollback-era edits are lost | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-11 | MAJOR | Secret edits on the rollback build: the T overlay shadows T's own blob writes, and a cleared secret is never re-imported | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-12 | MAJOR | DATA-15 repeated: `op_result:`, `opsummary:`, `operation:` have 20 live books writers but are core/tasks `AgingOut` | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-13 | MAJOR | C5 removes the tasks reconcile without a final reconcile; P11 is an operator check, not a store precondition | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-14 | MAJOR | A contract that resumes after a crash re-runs "one more rebuild" over half-deleted legacy rows and deletes identities | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-15 | MAJOR | Over-cap rebuild path never removes natives whose legacy row vanished; run-once data-migration state contradicts "every boot" | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-16 | MAJOR | Audit copy-forward after a restore collides with the restored `Sequence` high-water mark | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-17 | MAJOR | Unencrypted archives carry the operator config file, i.e. plaintext secrets | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-18 | MAJOR | Checkpoint consumption needs the upgrade boot's own process to live 7 days; checkpoints accumulate until the free-space check halts boot | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-19 | MAJOR | 06 contradicts 05-storage: storage opened under `DataDir` with `os.OpenRoot`, and an automatic checkpoint restore on failure | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-20 | MAJOR | A seed role narrowed on the new build is widened again on rollback by HEAD's per-boot `SeedRoles` overwrite | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-21 | MINOR | `Mirror.Stage` checks only the primary row; uniqueness of legacy index rows is unguarded | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-22 | MINOR | Guard mechanics: projection stamp needs a books `counter:` ID; raises not stated monotone | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-23 | MINOR | Row 7 "exact keys" cannot express the data-dependent `metadata_sources.<id>.credentials.<k>` rows | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-24 | MINOR | No single ordered list of every-boot writers relative to checkpoint, DAG and handle issue | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-25 | MINOR | Pre-boot restore resolves paths from flags/env only; revert/checkpoint restores have no revocation "since" | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-26 | MINOR | Offline dry run must checkpoint the live store read-only; `TestDryRunMatchesLiveRun` as written is unachievable | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-27 | MINOR | 05-backup mechanics: orphaned sidecars after old-build pruning; journal name not app-scoped; derived "changed during the capture" | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-28 | MINOR | Fresh installs have no initial-level rule: a month of mirror phase and inactive encryption | Accepted | Reviewer's concrete fix applied as written. |
| DATA2-29 | MINOR | Stale or contradictory cross-references left by the revision | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-1 | BLOCKER | SEC-1 half-fixed: the previous build's owner can re-activate a mirror-disabled key, and re-upgrade then adopts it as `LegacyFullScope` | Accepted (resolution R2-1) | Rollback floor = the Teach build, which ships the legacy-side guards (refuse status changes on revoked or mirror-disabled keys). |
| SEC2-2 | BLOCKER | Rollback turns a native, dominance-bounded `core:users.manage` (or `settings.manage`) custom role into legacy admin power | Accepted (resolution R2-1) | The Teach build adds the `users.manage`/`settings.manage` dominance bound to the legacy handlers, so a rollback to it cannot turn a bounded custom role into admin power. |
| SEC2-3 | BLOCKER | Post-restore revocations are written to the native copy, which the next boot rebuilds from legacy: revoked keys and epochs come back | Accepted (resolution R2-2) | Post-restore revocations (and restored-session/invite invalidation) are written to the authoritative legacy rows in the same batch as the native copy, so the next boot's rebuild keeps them. |
| SEC2-4 | MAJOR | Worker/facade confinement is deleted from the ABS surface: `OwnAuth` routes skip stage 13 and the bridge drops `abortWorkerOnlyOutsideWorker | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-5 | MAJOR | Aliases cannot express legacy routes that multiplex on a body/query field; non-strict alias decoding retargets the action to the caller; 05- | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-6 | MAJOR | SEC-21 half-fixed: 03, 07 and 09 keep `/invite/{token}` as a shell path while 05-authn moved the token to the URL fragment | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-7 | MAJOR | SSRF trust inversion: `InstanceConfig.Local` (any private address) needs only interactive `core:integrations.manage`, while the narrower `la | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-8 | MAJOR | SEC-6 incomplete: 05-backup (and five other 05-* key tables) have no Protection column; `core.backup.recipients`/`encryption` let a settings | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-9 | MAJOR | The out-of-DB revocation journal has no integrity: deleting or truncating lines silently resurrects revoked credentials on restore | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-10 | MAJOR | `backup.Revocation` cannot represent a role removal (one `Subject`); rbac's re-application is undefined, so a demoted admin is re-promoted b | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-11 | MAJOR | Restore brings back the pre-compromise password hash: a post-archive password change is journaled only as an epoch bump | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-12 | MAJOR | Audit copy-forward reuses `seq` values the restored store's `Sequence` will reissue: new records overwrite the copied trail | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-13 | MAJOR | Diagnostics bundle download is gated on "the run's actor", not on the presenting credential: a narrow key or delegated session of an admin d | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-14 | MAJOR | Infra routes are outside the chain's "routes" scope, so `/metrics`' `core:metrics.read` is not enforced by stages 12–13 | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-15 | MAJOR | The module "system actor" that `Client.Start` stamps outside a request has no defined permission set | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-16 | MAJOR | No registration path for authenticators/compat methods in 06; whoever adds one decides `Interactive`, `MayChangeCredentials`, `OwnerVerifier | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-17 | MAJOR | `MountLegacy` is a permanent public API: any module can mount with `LegacyAuthGin` or mark routes `OwnAuth`+`Delegated` and skip stages 8, 1 | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-18 | MINOR | Core's URL redaction list is narrower than HEAD's (`code`, `code_verifier`, `refresh_token`, `secret` lost); 05-http-api stage 2 lists a thi | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-19 | MINOR | S2 moves `client_trust.go` to core while gin-side bootstrap/invite/ABS limiters keep calling `trustedClientIP` until S4 | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-20 | MINOR | Stage 7 and stage 13 refusals are "audited" from S2, but `audit` exists only from S4 | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-21 | MINOR | Auth-off reach changes for logs/status/info (`core:system.manage` is `AuthRequired`) despite the "auth-off reach identical" invariant | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-22 | MINOR | ServeMux (escaped segments) and gin (`UseRawPath=false`, decoded path) can pick different routes for one request; per-route Basic exemption  | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-23 | MINOR | The updater's unsigned legacy path is reachable only by an anomalous or malicious unsigned release once the S8 build (with compiled keys) ru | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-24 | MINOR | The `derive` key can never be rotated; a leaked keyring export forges backup MACs and state codecs forever | Accepted | Reviewer's concrete fix applied as written. |
| SEC2-25 | MINOR | Stale or mis-stated cross-references: 09 §9.2.3 rule 2 parenthetical; 05-realtime "aliases" vs 06 `Projection`; 05-health `rbac.Scope` | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-1 | MAJOR | — Release gating: RCs and production minors are cut from main before CI runs, so the flip-PR, contract and ratchet gates gate nothing | Accepted (resolution R2-6) | 09 §9.1 corrected: main merges without CI gating (`auto-revert.yml`) and `prerelease.yml` auto-promotes a production minor every 10 RCs. The extraction makes its gates real: S0 adds the extraction gates (flip PR, contract, ratchet, MOD-2 gate, Docker build) as required checks via branch protection, or — if the owner keeps ungated merges — they run in the RC pipeline and block promotion. Owner choice recorded in 14. |
| OPS2-2 | MAJOR | — A contract can run on the first boot of an auto-applied release, which disables the updater's own rollback | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-3 | MAJOR | — The updater's `Boot` has no place in the normative boot sequence, so config-stage failures never trigger an automatic rollback | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-4 | MAJOR | — The updater (the S0 hotfix and core `updater`) needs to write to the executable's directory, but the committed install layout does not all | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-5 | MAJOR | — From S0 to S7 the hotfixed HEAD updater applies adoption releases unattended, with no guard, no backup and no automatic rollback | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-6 | MAJOR | — After an automatic rollback, the updater re-applies the same release on the next window, every night | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-7 | MAJOR | — The persisted usage gate can read "unused" while a surface is in use | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-8 | MAJOR | — The plaintext-blanking rule cannot see a pre-guard build that booted in between | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-9 | MAJOR | — S5 deletes the legacy `Requires` stamps inside the window; a rollback to S4 re-runs prerequisites across the whole library | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-10 | MAJOR | — The legacy projection's hard-coded value 2 collides with the storage-efficiency program's real format 2 | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-11 | MAJOR | — S10 deletes adopt steps, yet 09 and 05-updater still promise that no stepping-stone release is needed | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-12 | MAJOR | — S3's "canonical names in addition to legacy names" in the deploy files can point ABO at an empty database | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-13 | MAJOR | — The S0 reload check (`X-App-Build`) has no owner after S2 and conflicts with 07's build-ID design | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-14 | MAJOR | — The Docker PR job still leaves the round-1 failure class open | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-15 | MAJOR | — Three stages remain big-bang releases | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-16 | MAJOR | — Per-stage done-checks do not deliver what §9.3 promises (boot budget, R-test, UI and ABS cells) | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-17 | MAJOR | — 05-health forbids the startup `EXTEND_TIMEOUT_USEC` that HEAD and 05-storage rely on | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-18 | MINOR | — The `Boot` attempt budget has no margin under `StartLimitBurst` | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-19 | MINOR | — The removal gate measures only the reference production store | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-20 | MINOR | — The S6 rollback leaves undelivered webhooks in `c/outbox/`, contradicting "nothing correctness-critical is carried only by the outbox" | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-21 | MINOR | — `TestShippedDeployEnvNamesArePermanent` fails on HEAD's own unit, and the permanent lists differ | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-22 | MINOR | — 05-tasks' batch table still says T0+T1 "raises the level" | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-23 | MINOR | — Shutdown-budget details | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-24 | MINOR | — Boot-phase and level metrics have two names each, and the phase set cannot attribute the steps S5 asks for | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-25 | MINOR | — The R-test's `prev` cannot be determined from CI, and the job is wired nowhere | Accepted | Reviewer's concrete fix applied as written. |
| OPS2-26 | MINOR | — A deploy while a restore is staged silently discards the restore | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-1 | BLOCKER | MOD-2 not fixed for `sdnotify`: ABO stops compiling at S2. S2 moves `internal/sdnotify` to `mediacore/internal/sdnotify`. Only `.` and `inte | Accepted (resolution R2-5) | The MOD-2 gate is rebuilt to parse real `go list -deps -test -json` output for every stage; `internal/sdnotify` (and any package with remaining ABO importers after its stage) moves to a public core path or stays in ABO; 04 shows one importer count per package, generated. |
| MOD2-2 | MAJOR | A superseded wave table survives in the "authoritative" file and contradicts §8.1 and §3. It puts `internal/sdnotify` at wave 1 (§8.1: S2).  | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-3 | MAJOR | Split cells that cannot compile, because the generator checks only exported cross-package references. (1) `fence.go`, `intent.go` and `evide | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-4 | MAJOR | 04 moves the whole cobra root (`cmd/root.go`, 792 LOC) into `mediacore/app`. That contradicts 13 (cobra: "none in core; apps choose their CL | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-5 | MAJOR | `check_genproto_exclude.py` cannot pass as specified, so MOD-4(c) is only half fixed. (1) The script is supposed to fail when `go mod graph` | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-6 | MAJOR | Every core dependency change also requires a root `go.mod`/`go.sum` change, and the spec never says so. With `replace => ./core`, the root's | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-7 | MAJOR | The redesigned ABO guard makes every core import layer −1, "legal from every ABO layer". That erases the domain/transport/entry split for co | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-8 | MAJOR | Readiness criteria are wired as merge gates, which makes them self-contradictory. `make -C core ci` includes `apidiff-gate` (S1), `coverage` | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-9 | MAJOR | The scripts behind core's own checks live outside `core/`, so core CI cannot run "from an export of `core/` alone", and the scripts never re | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-10 | MAJOR | `examples/hello` has no stage, and its dependencies contradict the stage it would need. Neither 04 nor 09 places it. 06 lands it with `modul | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-11 | MINOR | `internal/diag/flight` moves to `mediacore/health` at S1, but it imports ABO's `internal/logger` (`flight.go:108`, `var flightLog = logger.N | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-12 | MAJOR | History-path generation is unsound, and cutover step 3 would abort. (1) Split files, one source file divided between destinations (e.g. `int | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-13 | MINOR | The 12 commands hard-code `go1.27.3`, while the repo pin is `go1.27.2` (`Makefile:43`) until 13's bump PR lands, and no stage owns that bump | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-14 | MINOR | The checkpoint is recorded only if `version.Version == "0.Y.Z"` on the release commit, but the spec never says who bumps the constant, in wh | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-15 | MINOR | At S0, core contains only the `archtest` engine. Today that engine is stdlib-only (`internal/arch/layering_test.go:20-31`), so `go mod tidy` | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-16 | MINOR | Docker and web entry-point gaps. (a) `build_web.sh` runs in a frontend stage built on `node:26-alpine` (`Dockerfile:13`, `Dockerfile.build-c | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-17 | MINOR | `npm run build --workspaces` runs in the order of the workspaces array, which here is glob order: `api-client`, `shell`, `ui-kit`, then `../ | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-18 | MINOR | The table is incomplete in places. `test-all-short` is `test-short web-test` (Makefile), so running it "per module" runs ABO's vitest twice  | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-19 | MINOR | A per-module deadcode ratchet means nothing for a library. deadcode reports functions unreachable from `main`s, and core's only mains are `e | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-20 | MINOR | "Pinned exactly" has no mechanism in Go modules. A `require` is a minimum: any module in either build list that requires a higher version wi | Accepted | Reviewer's concrete fix applied as written. |
| MOD2-21 | MINOR | The two numbering schemes collide. "S5" means Tasks in 09 and Coverage in 12. 09's own table says "core gate added at S2b (12 S5 thresholds) | Accepted | Reviewer's concrete fix applied as written. |
| FE2-1 | MAJOR | From S2 to S9 the SPA is served with `cspBase` alone: the inline theme script is blocked, the Access-host `connect-src` is lost, and `Report | Accepted | Reviewer's concrete fix applied as written. |
| FE2-2 | MAJOR | One-stream-per-browser relies on the Web Locks API, which browsers expose only in secure contexts, so it is missing on exactly the plain-HTT | Accepted | Reviewer's concrete fix applied as written. |
| FE2-3 | MAJOR | From S5 to S9 the Activity page reads native task REST but legacy `op.*` stream frames, and nothing says who feeds the legacy streams betwee | Accepted | Reviewer's concrete fix applied as written. |
| FE2-4 | MAJOR | Step 4 of 07 §7.19 (`renderWithProviders` onto `renderInShell`) is not shippable before step 5 | Accepted | Reviewer's concrete fix applied as written. |
| FE2-5 | MAJOR | S0 introduces a second stale-build mechanism (`X-App-Build`) that 07 does not know, with a different ID source, no dev exemption, and a head | Accepted | Reviewer's concrete fix applied as written. |
| FE2-6 | MAJOR | FE-3 is half-fixed: `NonSPA` drops today's `health` and `metrics` segments and never covers core's own probe paths, so `/health/` and `/Metr | Accepted | Reviewer's concrete fix applied as written. |
| FE2-7 | MAJOR | The shell route `/logout` collides with ABO's ABS `/logout` segment: a full page load of `/logout` gets a 404 problem+json after S9 | Accepted | Reviewer's concrete fix applied as written. |
| FE2-8 | MAJOR | Invite and temp-login links: 05-authn moved tokens to the URL fragment (SEC-21), but 07, 09 and 03 still route `/invite/:token`; invites cre | Accepted | Reviewer's concrete fix applied as written. |
| FE2-9 | MAJOR | `core.json` has a single producer, `core/examples/hello`, but no stage creates hello; the S4–S6 generated-client switch has nothing to gener | Accepted | Reviewer's concrete fix applied as written. |
| FE2-10 | MAJOR | At S9, `ui.MustFromFS(WebFS, "web/dist")` panics on every API-only build, because ABO's non-embed `WebFS` is an empty `embed.FS` | Accepted | Reviewer's concrete fix applied as written. |
| FE2-11 | MAJOR | FE-23 is half-fixed: the union that `module.ReservedIDs()` computes does not equal 03's reserved row, and two different mechanisms supply th | Accepted | Reviewer's concrete fix applied as written. |
| FE2-12 | MINOR | The build ID is defined circularly: a compile-time `define` cannot hold a hash of the chunk names that the define's own value changes | Accepted | Reviewer's concrete fix applied as written. |
| FE2-13 | MINOR | The core-version row's semver rule is wrong for v0, and in-repo nothing ties `@mediacore/shell`'s package version to `version.Version` | Accepted | Reviewer's concrete fix applied as written. |
| FE2-14 | MAJOR | `make web-deps` runs `npm run build --workspaces`, which npm runs alphabetically, so `@mediacore/shell` builds before `@mediacore/ui-kit` on | Accepted | Reviewer's concrete fix applied as written. |
| FE2-15 | MINOR | Every `web-*` target now depends on `make web-deps` (two `npm ci` runs plus a full core build), which slows the dev loop and races under `ma | Accepted | Reviewer's concrete fix applied as written. |
| FE2-16 | MINOR | 09 S9's legacy SPA redirect list misses ABO's current home and three other pages, and `ui.Handler` has no hook for the promised `ui_route` u | Accepted | Reviewer's concrete fix applied as written. |
| FE2-17 | MINOR | The S4 permission-literal ban cannot be implemented as worded | Accepted | Reviewer's concrete fix applied as written. |
| FE2-18 | MINOR | Route names in 07 and 09 do not match the owning specs | Accepted | Reviewer's concrete fix applied as written. |
| FE2-19 | MINOR | FE-17's count is still split, and 13 E4 describes a different UI-0 scope than 07 and 09 | Accepted | Reviewer's concrete fix applied as written. |
| FE2-20 | MINOR | 07 and 05-realtime describe the realtime client differently: redirect detection and the hook signature | Accepted | Reviewer's concrete fix applied as written. |
| FE2-21 | MINOR | The leader's single stream has no defined subscription set; with `*` it carries every visible task log line to every tab | Accepted | Reviewer's concrete fix applied as written. |
| FE2-22 | MINOR | 12 still offers GitHub Packages as the private-repo path, which 07 rejects (FE-14 residue) | Accepted | Reviewer's concrete fix applied as written. |
| FE2-23 | MINOR | CSRF and cookie wording disagrees across 07, 05-http-api, 05-authn and 09; one claim would open a hole if implemented literally | Accepted | Reviewer's concrete fix applied as written. |
| FE2-24 | MINOR | Stale and contradictory text left in 05-http-api and 07 by the revision (FE-20 and FE-28 half-applied) | Accepted | Reviewer's concrete fix applied as written. |
| FE2-25 | MINOR | S9's preconditions name S4–S6 only, but shell-owned pages need S3, S7 and S8 native routes, and `/api/core/v1/modules` is not scheduled in a | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-1 | MAJOR | SIMP-6 not fixed: 8 files bind schedule keys that `ScheduleFromConfig` refuses at registration, so S7/S8 builds fail boot as written. | Accepted (resolution R2-7) | One schedule-key rule: every schedule key is declared under its owner's namespace and registered through `tasks.ScheduleFromConfig`; 05-tasks accepts any declared key of the registering owner (drops the `core.schedules.*`-only rule); all 8 files use it. |
| SIMP2-2 | MAJOR | Two key artifacts (keyring + install backup identity), two exports, two health tokens. 09 confuses them. A derived identity removes one. | Accepted | Backup age identity derived from the single keyring (`DeriveKey`); one export covers both; 09 corrected. |
| SIMP2-3 | MAJOR | The "one reflector" override table is config-owned (L3) but names `rbac.Permission` (L4). Two access paths. 13 is stale. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-4 | MAJOR | S6 writes webhook instances into the instance store and sends through providerhttp, both of which S7 introduces. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-5 | MAJOR | `Origin=pinned` persists env values into stored rows: a second lock model, a regression of a deliberate HEAD behaviour, and contradicted by  | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-6 | MAJOR | Facade-only key machinery (`PurposeFacade`, `Module`, query-key extraction, facade groups) is built in S2/S4 for the deferred `arrfacade`. | Accepted | Facade-only key machinery (`PurposeFacade`, query-key extraction, `FacadeGroup`) moves to the deferred `arrfacade` build; S2/S4 do not build it. |
| SIMP2-7 | MAJOR | A library-scale books migration (`TorrentHash`/`DelugeHash` → `ItemRef`) serves a deferred multi-instance need and has no stage, rollback or | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-8 | MINOR | 05-http-api §4.5; 05-tasks §5T.3.10 `StartRequest.IdempotencyKey`, §5T.6 `c/tasks/idem/` | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-9 | MINOR | 08 moves the conformance engine into core during the extraction, justified by a deferred consumer. 04 keeps it in ABO. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-10 | MINOR | Phase-2 download topics are listed as core topics, with no deferred marker, and only 3 of the 6. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-11 | MINOR | The two-stage dispatch outlived its reason (per-user delivery is deferred). 07's "notifications" is undefined. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-12 | MINOR | Every subsystem keeps its own alias classes and dates beside `compat.Entry`. One duration key is "the compatibility window". | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-13 | MINOR | The metric segment depends on the *enabled* module list, so toggling a module renames another module's series. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-14 | MINOR | "raises the level" contradicts the one-guard policy that only contracts raise levels. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-15 | MINOR | The operational dial has two names: `…parallelism` and `…parallelism_cap`. | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-16 | MINOR | 09 §9.2.3 rule 2; 13 §13.5.2; 05-config §2.3; review-history lines | Accepted | Reviewer's concrete fix applied as written. |
| SIMP2-17 | MINOR | Download residue: `Remove` is listed as an ABO need (it has 0 callers), and books Deluge routes are rewritten to native without a reason of  | Accepted | Reviewer's concrete fix applied as written. |
