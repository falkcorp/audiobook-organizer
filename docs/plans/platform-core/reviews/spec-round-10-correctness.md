<!-- file: docs/plans/platform-core/reviews/spec-round-10-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 22ff89f3-1ae9-4ceb-bf35-a3eb50cb882d -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 10: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR10`). Inputs: `docs/specs/platform-core/` after the round-9
revision (03 v1.13.1 normative; 05-storage, 05-authn, 05-rbac, 05-http-api, 05-tasks, 05-config, 05-ai,
05-metadata, 05-notify, 06 and 14 at v1.11.1; 05-backup, 05-metrics and 09 at v1.10.1; 04 and 05-events at
v1.10.0). Binding: R9-1..R9-11, A-1..A-17, R3-1..R8-6. Code anchors are `path:line @748fd787a`. Spec anchors are
`file:line` in `docs/specs/platform-core/`.

Method. I traced every item of the brief through its Go blocks, prose, stage rows and tests, looked up each
cross-file name in its owner file, and checked layers against 03 R4. For legacy-backed ABO data the bar is HEAD
parity, so each R9 mechanism was compared with what HEAD's code does at the same moment, not with the deleted
design.

**Verdict.** R9-1's mechanism composes: `storage.CommitLocked` spans the commit, every lock is taken once in
`LockRank` order, a nested `WriteRuns` fails with `ErrWriteRunsNested`, and the kernel builds one instance per
family. The HEAD lock order the spec cites matches the code (`user_merge.go:507-529`,
`pebble_store_auth.go:509-525,587-594`). R9-4's ambient-after-intersection rule is right. R9-6's one request
struct flows unchanged through `Stager.StageReset`, `kernel.stageReset`, `ResetArgs` and
`Maintenance.StageFactoryReset` in every file. `coretest/crashmatrix` and its "P0a to Run (3b)" range agree in
05-storage, 06, 09 and 14. `app.Offline` and `app.OfflineCommands` close COR9-6. `hello` still compiles. What
breaks:

- **R9-2 cannot be built.** Four of the five operations routes it moves to core have no core target. A core
  `Alias` can only point at a core native route, and those handlers are books' code (COR10-1).
- **R9-1's refusal of writes staged outside the hold reads the hold from a `ctx`.** The identity store's staging
  methods and `legacy.SecretsStore.Write` take no `ctx` (COR10-2).
- **R9-3 keeps HEAD's `http_route` values only for `MountLegacy` requests.** Every HEAD route that becomes an
  alias changes its label value (COR10-3).
- **R9-7's labelled state filesystem cannot reach the post-reset hook's targets.** Its confinement leaves out the
  library root and the Open Library dump directory, so the legacy alias loses HEAD's wipe (COR10-4).

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | COR10-1 |
| MAJOR | 3 | COR10-2, COR10-3, COR10-4 |
| MINOR | 6 | COR10-5 … COR10-10 |

## A. Regression check (round-9 BLOCKER and MAJOR findings of this lens)

| ID | Round-9 severity | Status | Where |
|---|---|---|---|
| COR9-1 (identity mutexes do not span the commit) | BLOCKER | **Fixed in mechanism.** `storage.CommitLocked` holds the locks from staging through the commit (`05-storage.md:388-464`). `legacy.WriteLock` covers the identity, run, config, instance and rule stores. The kernel builds one instance per family (`05-storage.md:417-421,1493`; `03-architecture.md:405-420`). The HEAD order matches the code. **Half-fixed in contract:** the store-side refusal cannot be evaluated by the identity store (COR10-2). The lock names sit above the layer of the code that must pass them (COR10-5), and one boot writer has no lock name (COR10-6). | COR10-2, COR10-5, COR10-6 |
| COR9-2 (cross-owner component at S5) | BLOCKER | **Half-fixed.** Rule 1 now holds at every stage, and the cross-owner set is gone. But the move puts four books-domain handlers behind core aliases that have no target (`05-tasks.md:1051`). | COR10-1 |
| COR9-3 (ambient scopes removed by the key intersection) | MAJOR | **Fixed.** `CheckerInput.Ambient`. `Compile` decides `Confined()` first and adds the ambient scopes after the intersection (`05-rbac.md:365-370,623-630,1177-1186`). The role-read injection is gone, and 06 matches (`06-app-sdk.md:965-987`). One residue on `/metrics` (COR10-8). | COR10-8 |
| COR9-4 (reset contract in four shapes) | MAJOR | **Fixed.** One `storage.ResetRequest` (`05-storage.md:1840-1885`) with `Stager.StageReset` (`:1907`) and the same signatures in 05-config, 06, 05-rbac and 05-http-api. The comparison happens in `kernel.stageReset`, the count is exact and one directory read, and `LegacyConfirm` is `json:"-"`. **New residue:** the hook's labelled filesystem cannot touch the confirmed root (COR10-4). Two edge contracts are open (COR10-9). | COR10-4, COR10-9 |
| COR9-5 (crash matrix cannot reach ABO's writers) | MAJOR | **Fixed.** A public kit with `Injector`/`Fixture`/`Scenario`/`Run`, two instances, `Prev` as a binary path, and books' non-Pebble writes routed through `legacy.ConfigOpenEnv.Files` (`05-storage.md:2163-2232`; `06-app-sdk.md:1494-1530,2016-2034`; `05-config.md:589-603`). Coverage gaps remain (COR10-7). | COR10-7 |
| COR9-6 (`app.Offline` too narrow) | MAJOR | **Fixed.** `Offline.StagingMarker`/`WriteStagingMarker`, `app.ReadStagingMarker`, `app.OfflineCommands`/`Command` (`06-app-sdk.md:1106-1144`). The command list agrees with 05-authn (`:1352-1355`), 05-config (`:2343-2347`) and 05-storage (`:1707`). One citation drift (COR10-10). | COR10-10 |
| COR9-7 … COR9-15 | MINOR | Fixed. P−1 is gated everywhere. `Observer.Transport` and 04's AI cell agree (`04-package-mapping.md:426`; `05-ai.md:337,886-891`). There is one set-aside rule. `restorejournal.Intent`/`CreateIntent` exist. `kernel.offlineCloseBatch` is used. The config model is "exclusive per key". | — |

## B. Flow traces

| Flow | Result |
|---|---|
| `legacy.WriteLock.WriteLocks` → `storage.Lock`/`LockRank` → `CommitLocked` | Composes. The equal-value dedup, the ascending rank, `ErrLockOrder` for a lower rank or a shared rank, the release after `Commit` and the re-take per retry attempt are all consistent in 03, 05-storage, 05-tasks, 05-authn, 05-config, 05-metadata, 05-notify, 05-ai, 05-events and 06. HEAD's partial order (`userMergeApplyMu` < `inviteMu` < `userWriteMu` < {`sessionWriteMu`, `apiKeyMu`}) is a valid prefix of a total order. The bootstrap redeem, merge and revoke sets in 05-authn's table are ascending in it. Breaks: the hold check (COR10-2), the layering of names (COR10-5), `Reconcile` (COR10-6). |
| `WriteRuns` / `ErrWriteRunsNested` | Sound. `tasks.ErrWriteRunsNested` is declared once (`05-tasks.md:1011-1013`). The relay, the AI poll and `Pause` build inside it. The `RunStore`, `ClockStore`, `PauseStore` and `StandDownStore` staging methods all take `ctx`, so `LocksHeld` works for the run store. `ReplaceRows` is lock-free, as at HEAD (`pebble_store_op_rows.go:107-166`). |
| Singletons | Sound for the instance-held mutexes (`apiKeyMu`, `inviteMu`, `opsMu`, `counterMu`; `pebble_store.go:226-244`). `userWriteMu`, `sessionWriteMu`, `userMergeApplyMu` (`pebble_store_auth.go:515,525`, `user_merge.go:403`) and `configBlobSaveMu` (`internal/config/persistence.go:1897`) are package-level, so they are shared anyway. |
| R9-2 route ownership at S5 | Rule 1 is satisfiable, and no other HEAD route conflicts with `GET /api/v1/operations/{p4}/{p5}` (the `reconcile/preview` and `{id}/class-lists/{class}` routes are more specific or have another segment count, checked against `raw/09-http-api.md` rows 584-621). The move itself is unimplementable (COR10-1). |
| `storage.ResetRequest` → `stageReset` → `ResetArgs` → hook | Request, validation, preflight, comparison, `ResetStaged` and `LibraryConfirmError` are consistent. The hook's file effects break at `StateFiles` (COR10-4). |
| `coretest/crashmatrix` (P0a → Run 3b) | Types identical in 05-storage and 06, and the range is identical in 05-storage, 06, 09 and 14. Coverage gaps (COR10-7). |
| `app.Offline` / `OfflineCommands` | Consistent. The staging-marker order (key in the close batch, then the sidecar) matches 05-storage `:1104`. |
| `httpapi.ReportLegacyRoute` | Correct for `MountLegacy` requests: the books middleware calls it after gin routed, and stage 2 records the reported value. Aliases and the S5 dispatcher are not covered (COR10-3). |
| `LegacyAmbientScopes` after key intersection | Correct. The `/metrics` requirement is stated two ways (COR10-8). |
| `hello` | **Compiles.** Since round 9 no signature that hello uses changed. `app.Options.PromRegisterer` became `*metrics.Shared` (defined in `05-metrics.md:150-152`), and hello does not set it. `coretest.New`/`RunTask`, `storage.Spec`/`Migration`, `tasks.RunItems`/`TaskSpec` and `httpapi.Get`/`Handle` are unchanged in their owner files. |

## C. Findings

### COR10-1: BLOCKER. R9-2 half-fixed: four of the five operations routes it moves to core have no core target, and a core alias cannot run books' handlers

**Where:**
- 05-tasks 5T.3.10 (`05-tasks.md:1051`): "(core aliases, no native path: the five routes of the T0+T1 PR) … each is a
  ledger-listed core alias … over the handler that serves it at HEAD, `…/v2/{id}` rendering `GET /runs/{id}`
  through `LegacyPath.Shape` and the other four reading books' families through books' handlers".
- 05-http-api §4.8 "The operations component at S5" (`05-http-api.md:816-826`) and rule 5's core-owned dispatcher
  (`:807-810`, "runs that member's alias").
- 03 R6 (`03-architecture.md:596`), 04 `internal/server/handlers/operations` row (`04-package-mapping.md:542`), 09
  S5 "Endpoints" (`09-abo-on-core.md:1184`, which also counts `/api/v1/operations/*` (38) as aliases), and
  `TestS5_OperationsComponentMovesWhole` (`05-http-api.md:1414`).

**Evidence:**
- An alias can only point at a native route of the same group. `httpapi.Alias` "registers a legacy path for one
  or more already-registered native routes of g (each target keeps its own handler and permission)"
  (`05-http-api.md:247-254`). `LegacyPath.Targets` is the "Route.Name of each native route this path may reach"
  (`:259`).
- No such route exists for four of the five. 05-tasks says outright that they have "no native path". So `Alias`
  has nothing to name for `…/{id}/activity`, `/changes`, `/result` and `/results`, and `Build` fails at the S5
  flip PR.
- The handlers are books' domain code:
  - `activity` is books' activity feed: `activityH.ListOperationActivity`, `internal/server/wire_library_routes.go:48`.
    `internal/activity` stays ABO (`04-package-mapping.md:425`).
  - `changes` and `result` are the undo and result handlers: `operationsH.GetOperationChanges` and
    `GetOperationResult`, `internal/server/wire_operations_routes.go:103,110`. Their package imports `audiobooks`
    and `undo` (04 row).
  - `results` is the metadata batch-candidate page: `s.handleGetOperationResults`,
    `internal/server/server_lifecycle.go:1952` → `internal/server/metadata_batch_candidates.go:581`.
- Core cannot call any of them (03 R1), and no file defines a way for a module to supply the handler of a
  core-owned route.
- "With HEAD's permission (`library.view`)" also cannot hold: an alias carries its target's permission. For
  `…/v2/{id}` that is `GET /runs/{id}`'s per-definition `View` (`05-tasks.md:1044`), not `library.view`
  (`wire_operations_routes.go:27`).
- So the S5 flip build either does not boot, or it drops four routes that the legacy SPA calls.

**Fix:** keep the whole component **books'** at every stage, which satisfies rule 1 with no core route in it:
- At T0+T1 books keeps the five gin manifest routes. They fold as at S2–S4: the four members become one
  generalised pattern, `…/v2/{id}` stays kept, and post-match runs behind the legacy mount.
- Books' `GET /api/v1/operations/v2/:id` handler reads the run through `Deps.Tasks` (`tasks.Client.Get`, which
  `05-tasks.md:847-851` gives modules) and renders HEAD's v2 shape. It must do so by T8, when opsbridge goes.
  `library.view` stays enforced by stage 13 through `rbac.LegacyAtom`.
- Delete rule 5's "A component owned by core … dispatcher" sentences. Rewrite "The operations component at S5" to
  say: "the component stays books' until the `/api/v1/operations` compat window closes; core registers no route
  or alias in it".
- Delete 05-tasks' "(core aliases, no native path…)" row. Correct 03 R6, the 04 row, 09 S5 (and the alias count
  there), 14's route-collision hazard and the R9-2 history lines.
- Replace the test with `TestS5_OperationsComponentStaysBooks`: on the real engine with ABS on and off, the gin
  engine holds all five routes; no core route or alias is in the component; `/api/v1/operations/v2/x` reaches
  books' v2 handler, `/api/v1/operations/x/activity` reaches the activity handler, and `/api/v1/operations/x/y`
  answers 404 in the legacy shape. `TestLegacyMount_GeneralisedPatternAcceptsAnyMember` then runs unchanged at
  every stage.

### COR10-2: MAJOR. R9-1's refusal of a write staged outside the hold reads the hold from `ctx`, which the identity store's staging methods and `legacy.SecretsStore.Write` never receive

**Where:**
- `storage.LocksHeld(ctx context.Context, l Lock) bool`, "a legacy store refuses a write staged outside its hold
  with it" (`05-storage.md:459-462`), and the `WriteLock` contract (`:1602-1603`).
- 05-authn §3.4: "A staged identity write whose ctx does not carry the hold of its lock (`storage.LocksHeld`) is
  refused" (`05-authn.md:714-717`), with its error row (`:1468`) and
  `TestLegacyStagedIdentityWriteOutsideLockRefused` (`:1592`).
- 05-config: `configBlobSaveMu` guards "the blob and the four secret rows" (`05-config.md:1159-1162`).

**Evidence:**
- Every `auth.Store` staging method takes only the batch: `PutUser(b, u)`, `IssueSession(b, s)`,
  `DeleteSession(b, s)`, `TouchSession(b, …)`, `RevokeSignInsBefore(b, t)`, `IssueAPIKey`/`PutAPIKey`/
  `TouchAPIKey(b, …)`, `IssueOneTime`/`TakeOneTime(b, …)`, `PutIdentity`/`DeleteIdentity(b, …)` and
  `StageMerge(b, plan)` (`05-authn.md:553-600`). So does `legacy.AuthAtomWriter.IssueAPIKeyAtoms(b, k, atoms)`
  (`:647`).
- So does `legacy.SecretsStore.Write(b, n, v, clear)` (`05-config.md:690`), which writes HEAD's secret rows inside
  the config `Committer`'s `LockConfigSettings` hold.
- With no `ctx` these methods cannot call `LocksHeld`. The refusal, its error row and its test cannot be written,
  so a path that forgets to wrap its commit in `CommitLocked` is not caught. That is exactly the COR9-1 race
  class (revoke versus touch, double invite accept).
- The tasks stores do take `ctx` (`05-tasks.md:1241-1342`), so the gap is specific to the identity and secrets
  stores.
- A ctx-keyed check is also weaker than it looks. Code inside a hold can stage into a *different* batch and commit
  it later outside the hold, and the ctx still says "held".

**Fix (the correct one, and it changes no frozen interface):**
- Make the hold a property of the batch the hold commits. `CommitLocked` records on `b` the locks it holds,
  from before `stage` until `Commit` returns.
- Replace `LocksHeld(ctx, l)` with `func LocksHeld(b *Batch, l Lock) bool`. `CommitLocked` keeps using `ctx`
  internally to detect a nested call and join its locks.
- Every legacy staging method checks the batch it was handed: books' identity store, `SecretsStore.Write`, and the
  run store's `LocksHeld(b, opsMu)` (`05-tasks.md:1460`).
- Update 05-storage §5S.3.2's Go block and `TestCommitLockedHoldsThroughCommit` (add a case: a write staged into a
  second batch inside the hold is refused). Update 05-authn §3.4's bullet, error row and test, 05-tasks 5T.3.13,
  05-config §1.6 (name `SecretsStore.Write` and `OverrideStore.Stage` as checked) and 06 §6.4.11's comment.

### COR10-3: MAJOR. R9-3/MOD9-2 half-applied: every HEAD route that becomes an alias, and the S5 dispatcher, changes HEAD's `http_route` label value

**Where:**
- 05-http-api §4.2 stage 2 (`05-http-api.md:628`): "native, alias and `Infra` routes record their `r.Pattern`",
  and "A-1's generalised patterns therefore never become a label value".
- 05-metrics §3 (`05-metrics.md:195-210`), 09 §9.2.7 (`09-abo-on-core.md:749-753`, "Two label values are kept on
  purpose"), and 14's metric-family hazard (`14-risks-open-questions.md:93`), which names changed `http_route`
  values as the hazard.
- `TestLegacySeriesLabelValuesMatchHEAD` (`05-http-api.md:1415`) samples only a legacy route and a
  generalised-pattern member.

**Evidence:**
- HEAD's `http_server_*` rows carry `http_route` = gin's `c.FullPath()`, for example `/api/v1/auth/me`
  (`internal/telemetry/contract/testdata/series.golden:85-87`, otelgin v0.72.0, `go.mod:31`).
- `ReportLegacyRoute` keeps that value only for requests served through `MountLegacy` (`05-http-api.md:1159-1167`).
- Every HEAD route that moves becomes a core alias: the S4 r2 auth, user, role and key routes, the S4 r2 reset
  aliases, the S5 ops aliases, S7, S8 and the rest. Each then records `r.Pattern`.
- `r.Pattern` is the registered ServeMux pattern, method included (`GET /api/v1/auth/me`, `GET
  /api/v1/users/{id}`), so the series value changes at each move. These are the dashboard breaks that A-5 meant
  to remove, and R9-3 makes label values part of the contract.
- Under R9-2 as written, the S5 dispatcher records the generalised pattern itself
  (`GET /api/v1/operations/{p4}/{p5}`). That contradicts the "never" clause, and the "generalised-pattern member"
  cell of `TestLegacySeriesLabelValuesMatchHEAD` fails from S5. Under COR10-1's fix this sub-case disappears.
- An empty `c.FullPath()` (gin `NoRoute`, the SPA fallback) has no stated value either. HEAD records it without a
  route.

**Fix:**
- In stage 2, an alias records its `LegacyPath.Pattern` in HEAD's gin form: method and host stripped,
  `{name}` → `:name`, `{name...}` → `*name`. This is a pure function, so `LegacyPath` needs no new field.
- A native or `Infra` route records its pattern with the method and host stripped (semconv `http.route` is a path
  template).
- A reported empty `FullPath` is recorded as HEAD records it, never replaced by `r.Pattern`.
- Extend `TestLegacySeriesLabelValuesMatchHEAD` from S4 r2 with one alias route per family that moves, compared
  with HEAD's golden. State the rule once in 05-http-api §4.2 and cite it from 05-metrics §3 and 09 §9.2.7.

### COR10-4: MAJOR. The post-reset hook's labelled filesystem is confined to paths that exclude the library root and the Open Library dump directory, so the legacy alias loses HEAD's wipe from S2b

**Where:**
- `legacy.StateFiles`: "It never follows a path outside the directories the kernel resolved for this boot (the
  state directory, `<db parent>`, and the paths the reset intent recorded)" (`05-config.md:595-597`).
  `ConfigOpenEnv.Files` routes "the post-reset hook's deletions (the delete entries, the Open Library dump files
  and, only with DeleteLibraryFiles, RootDir's entries)" through it (`:584-590`).
- The hook's arguments, "the Open Library dump directory, the gap-fill file's path and … the confirmed library
  root", travel in `ConfigResetPlan.Hook` (`05-config.md:620-624`; `05-storage.md:1651-1657`).
- `restorejournal.Intent` (`05-storage.md:1934-1955`) records `DBPath`, `Targets`, `Delete`, opaque `HookArgs` and
  `DeleteLibraryFiles`, and **no** library root or dump directory.
- A-17: "The legacy `POST /system/factory-reset` alias keeps HEAD's wipe."

**Evidence:**
- HEAD deletes each top-level entry of `RootDir` (`internal/server/handlers/system/handler.go:571-577`) and the
  Open Library data directory (`:507-534`, `os.RemoveAll(targetDir)` at `:530`).
- The dump directory defaults to `<RootDir>/openlibrary-dumps` (`05-storage.md:1825`, citing
  `internal/metafetch/openlibrary.go:54-61,73`), and a library root normally lies outside the state directory and
  `<db parent>`.
- The kernel cannot read `hook_args`, which is app bytes. So, as written, the kernel-built `StateFiles` refuses
  every `RemoveAll` under `RootDir` and under the dump directory.
- The hook never fails boot. It records the refusals as `factory_reset_cleanup_incomplete`, so every factory reset
  through the S2b gin handler or the S4 r2 alias silently keeps the library and the dump files.
- That contradicts A-17, `TestFactoryResetFromLegacyRoute_S2b`, `TestPostResetHookRerunsUntilDone` and the native
  route's confirmed wipe. The only other reading, that the confinement does not exist, contradicts 05-config.

**Fix:**
- Have `ConfigResetPlan` return the hook's directories explicitly: `Paths []string`, the Open Library dump
  directory and, only with `DeleteLibraryFiles`, `LibraryRoot`.
- Record them in the intent as `restorejournal.Intent.HookPaths` (`hook_paths`), so the kernel can bound
  `StateFiles` without parsing `hook_args`.
- Define the confinement once in 05-storage §5S.3.15: the state directory, `<db parent>`, the intent's `Delete`
  entries and `HookPaths`. Have `StateFiles` itself apply rule (4)'s kept paths, so a library root that holds the
  store cannot reach it.
- Add a cell to `TestFactoryResetFromLegacyRoute_S2b`: the `RootDir` entries and the dump files are gone, and
  `hook_done` lists no failure.

### COR10-5: MINOR. The lock names live in `legacy` (L5), but the L3/L4 code that must pass them cannot import it, and the files disagree on who calls `WriteLocks`

**Where:**
- The constants `legacy.LockUsers` … `legacy.LockMerge` and `legacy.LockConfigSettings` are declared in package
  `legacy` (`05-authn.md:649-666`, "these are the names auth passes"; `05-storage.md:1611-1618`;
  `05-config.md:662-666`). So is `legacy.LockRuns` (`05-tasks.md:1428-1434`).
- The consumers that pass them:
  - the executor (`05-tasks.md:1001`, `WriteLocks([]string{legacy.LockRuns})`);
  - config's `Committer` (`05-config.md:438-442`);
  - integrations' Registry (`05-metadata.md:421-424`);
  - `notify` (`05-notify.md:208-213`);
  - `ai` (`05-ai.md:727-729`);
  - `auth` (`05-authn.md:684-688`).
- Against these, 03 §3.3 says the kernel "hands the store's locks to that store's owner together with the store"
  (`03-architecture.md:452-455`), and so does 05-storage §5S.4 step 10 (`05-storage.md:2339-2340`).

**Evidence:** 03 R4 places `legacy` at L5, "imported only by `module`, `kernel` and apps", above `config` (L3) and
`auth`, `tasks`, `integrations`, `notify` and `ai` (L4) (`03-architecture.md:594`). As written, those packages
either import `legacy`, which `archtest_layers_test.go` refuses, or they spell the strings themselves, which
nothing checks. "Hands the store's locks" is also undefined for `auth`, which needs a different subset per write.

**Fix:** The kernel resolves the locks at P4c by calling `WriteLocks` with the `legacy` constants. It hands each
owner an owner-typed value that names no lock string:
- `auth.Options.Locks auth.StoreLocks{Users, Sessions, APIKeys, Invites, Bootstrap, Merge []storage.Lock}`;
- the executor's `RunLocks []storage.Lock`;
- config, integrations and `notify` get `SettingsLocks []storage.Lock`.

Owners never call `WriteLocks`. State this in 03 §3.3 "Write locks" and in 05-storage step 10, and change the
seven owner passages above to "the locks the kernel resolved for …".

### COR10-6: MINOR. 05-authn's lock-name table has no row for the identity store's own boot writes

**Where:** `05-authn.md:690-699` (the table). `legacy.AuthReconciler.Reconcile(ctx, b)` runs inside `BootWrites`
(`:635-640`), and 06's P4d 4 row says "each committed through `storage.CommitLocked` on books' store"
(`06-app-sdk.md:2028`).

**Evidence:** `Reconcile` is not an `auth.Store` method, so no row names its locks. Books' staging refuses any
write outside the hold of its lock (`05-authn.md:714-717`). If `BootWrites` passes no names, the verified-email
reconcile and the one-shot expiry stamp are refused at every S4 r2 boot. HEAD runs the reconcile under `inviteMu`
then `userWriteMu` (`internal/database/pebble_store_verified_email_backfill.go:320-324`).

**Fix:** Add a row "`legacy.AuthReconciler.Reconcile` → `LockUsers`, `LockAPIKeys` (the expiry stamp rewrites key
rows)". Cover it in `TestVerifiedEmailReconcileInBootWrites` (`05-authn.md:1646`).

### COR10-7: MINOR. The crash matrix cannot observe some boot writes that its own rules require it to fail on, and the core instance has no `Prev`

**Where:** `05-storage.md:2176-2178` (statefs is "the … seam that every core non-Pebble write goes through"),
`:2196,2211-2213` (`Prev`), `:2823` (`TestBootCrashMatrixCoversEveryWriter`: "an app write outside the labelled
state FS fails ABO's instance"), and 06's Run 2 and P4d 4 rows (`06-app-sdk.md:2028,2030`).

**Evidence:**
- Core's own bootstrap-token write goes through `RestoreDeps.StateDir *os.Root` (`05-authn.md:410,953`). An
  `os.Root` is not the statefs seam, so the kit cannot see or kill that write.
- From S2b to S4 r1, books' start hook runs HEAD's `InitBootstrapToken` and the start-up read-only key, which
  write `.bootstrap-token` and `.readonly-key` with `os.WriteFile` (`internal/server/bootstrap.go:121,166`),
  outside `ConfigOpenEnv.Files`. The read-only key file is missing from the Run 2 row.
- The kit can fail only on writes it sees. No mechanism detects an app write that bypasses both seams.
- `TestBootCrashMatrix` over hello must pass a `Prev`, and nothing says which binary that is. At S2b there is no
  earlier hello that uses storage.

**Fix:**
- Give `auth.RestoreDeps` the labelled state filesystem (the type behind `legacy.StateFiles`) instead of
  `*os.Root`.
- Add the `.readonly-key` write to the Run 2 row, and route both HEAD token-file writes through
  `ConfigOpenEnv.Files` until S4 r2.
- Specify detection: after each boot the kit diffs the DB parent and state directory trees against its label log,
  and an unlabelled new or changed file fails the run.
- Define core's `Prev`: hello built from the PR's merge base. With an empty `Prev`, I6 checks `cur` only.

### COR10-8: MINOR. ABO's `/metrics` requirement from S8 is stated two ways

**Where:**
- **HEAD's any-credential check:** 05-metrics M5, §6 and §13 item 5 (`05-metrics.md:27,269-273,560-565`: "the
  kernel mounts it with HEAD's requirement instead … no ABO role or key carries `core:metrics.read`") and 09
  §9.2.6 (`09-abo-on-core.md:731-736`).
- **`core:metrics.read`, held through the ambient scopes:** 05-http-api §3.5 (`05-http-api.md:602`),
  `TestLegacyMetrics_AnyAuthenticated` (`:1374`, "from S8 the `Infra` route requires `core:metrics.read`"),
  05-rbac §6.3 (`05-rbac.md:1177-1186`) and 06 (`06-app-sdk.md:965-972`).

**Evidence:** On ABO the two admit the same principals, because every non-confined principal holds the ambient
scope. But they register different `Infra` permissions, and with an `AnyAuthenticated` mount the
`core:metrics.read` entry in `LegacyAmbientScopes` would be dead. R9-4 chose the ambient model.

**Fix:** Use `Router.Infra(path, "core:metrics.read", h)` on every app. Change 05-metrics M5, §6 and §13 item 5
and 09 §9.2.6 to "held by every non-`Confined()` ABO principal through `LegacyAmbientScopes`". Keep HEAD's
worker-only confinement, which `Confined()` gives.

### COR10-9: MINOR. Two edge contracts of `storage.ResetRequest` are open

**Where:** `05-storage.md:1824` (steps 2–3, the route "decodes the body into `storage.ResetRequest`", the aliases'
path values) and `05-http-api.md:262-273` (`Request`, `Selector`, "removed before the target decodes strictly").

**Evidence and fix:**
- **(a) An app with no hook.** When the built config store does not implement `legacy.ConfigResetArgs` (hello,
  TV, Movies), step 2 runs no preflight. Step 3 then compares the request with nothing, and an intent with
  `delete_library_files: true` is staged and answered 200, although no hook will ever wipe a file (the kernel
  journals `hook_done` itself). **Fix:** refuse `DeleteLibraryFiles` with `storage.ErrResetRequestInvalid`
  (422) when no `ConfigResetArgs` is present, and add the cell to `TestFactoryResetNativeAppCompletes`.
- **(b) Body against path value on an alias.** The precedence between the body's `kind` and
  `delete_library_files` and the aliases' path values (`reset_kind`, `delete_library_files`, `library_confirm`)
  is unstated. HEAD's `ShouldBindJSON` ignored every field but `confirm`. **Fix:** each reset alias lists `kind`,
  `delete_library_files`, `library_root` and `library_file_count` in its `Selector`, so they are dropped before
  the strict decode, and the path values alone set `Kind`, `DeleteLibraryFiles` and `LegacyConfirm`.
  `TestResetRequestFlowsUnchanged` covers `/system/reset` with a body `kind: "factory"`, which stays a `ResetStore`.

### COR10-10: MINOR. Name, landing and citation drift

- **06 §6.4.10's landing table** (`06-app-sdk.md:1591-1593`):
  - The S3 row lacks `legacy.WriteLock` and `legacy.LockConfigSettings` on books' config store (05-storage's list:
    S3).
  - The S2b row lacks `coretest/crashmatrix` and `legacy.ConfigOpenEnv.Files`/`legacy.StateFiles`.
- **05-storage §5S.3.11's one list** (`05-storage.md:1506-1524`):
  - It lacks `legacy.StateFiles`.
  - It lacks the six identity lock names, which `05-authn.md:663-670` says it names. `legacy.LockRuns` has its own
    entry.
- **03 §3.3's store table** (`03-architecture.md:507,515-516`): the `config`, `integrations` and `notify` rows
  omit `configBlobSaveMu` as their `legacy.WriteLock`. The `auth` and `tasks` rows name theirs.
- **05-storage citations** (`:1104`, `:1707`): they place `app.Offline.WriteStagingMarker`, `app.ReadStagingMarker`
  and `app.OfflineCommands` in "06 §6.4.6". They are defined in §6.4.5 (`06-app-sdk.md:1106-1144`).
- **05-storage's "lower-layer type" row** (`:1544`): it calls `config` an L4 owner. It is L3.
- **Singleton scope:** 05-storage builds the legacy stores "once per process" (§5S.3.11, step 10), while 06's
  `TestLegacyStoresAreSingletons` row says "once per App" (`06-app-sdk.md:2338`). Pick "once per App". Two apps
  in one binary (D9) are two kernels.
