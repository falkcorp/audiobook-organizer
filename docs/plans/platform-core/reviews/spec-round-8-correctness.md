<!-- file: docs/plans/platform-core/reviews/spec-round-8-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: b7983a1f-adb1-4f56-aacb-aad9cd3b98b3 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 8: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR8`). Inputs: `docs/specs/platform-core/` at the round-7
integration head (03 v1.10.1 normative; 06 v1.8.1, 05-storage v1.8.1, 05-config v1.8.1, 05-tasks v1.8.1,
05-backup v1.7.1, 05-authn v1.8.1, 05-rbac v1.8.1, 05-updater v1.8.1, 04 v1.7.1, 09 v1.7.0, 14 v1.8.1).
Binding: R3-1 … R7-8 and the round-7 dispositions. Code anchors are `path:line @748fd787a`. Spec anchors are
`file:line` in `docs/specs/platform-core/`.

Method. I traced each flow below through the current Go blocks, phase tables and stage tables. On each path I
looked up every cross-file name in its owner file and checked its layer against 03 R4:

1. Factory reset at S2b → S4 r1 → S4 r2 → S8. This covers books' gin handler, `legacy.Maintenance`,
   `storage.Admin.StageFactoryReset`, `legacy.ConfigResetArgs`, the P0a set-aside, the reset boot, books'
   post-reset hook, `ResetRun.Done`, `CheckReader`, Q-K and `internal/storagehttp`. Each step was checked
   against HEAD's handlers.
2. `kernel.OpenOffline` / `app.OpenOffline` against HEAD's offline subcommands.
3. P−1 … P8 and Run (1) … (8): owed duties and `Duties`, `NarrowStored` at P4d step 1(v), R7-5's cut-off rule,
   the restore boot's second constructor call, and `storage/restorejournal` at L0.
4. The `legacy` package after the SIMP7-8 rename sweep, the mirror tests, and the `any`-typed options with
   `storage.ErrLegacyStoreType`.
5. The run store's `Insert`/`Transition` retry (R7-3).
6. `hello` against the owner files.

**Verdict.** COR7-1 landed in mechanism. The rename sweep is clean: every body-text spelling matches 05-storage's
one list, and the old names survive only in review-history lines. The mirror test and the `any`-typed options are
consistent in every owner file. R7-5's cut-off rule composes with `Duties`. `NarrowStored` is placed consistently
in 03, 05-config, 05-authn, 05-http-api, 06 and 09. `hello` still compiles.

What breaks is the factory-reset mechanism that round 7 added:

- It has no specified path for HEAD's other whole-store wipe, `POST /system/reset` (COR8-1).
- Its hook arguments cannot be captured by any layer-legal implementation of the stated signatures, and the S4 r2
  route bypasses them (COR8-2).
- Its set-aside renames targets across filesystems (COR8-3).
- Its completion record has no owner when there is no app hook, and no rule for when the hook fails (COR8-4).

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 0 | — |
| MAJOR | 4 | COR8-1, COR8-2, COR8-3, COR8-4 |
| MINOR | 5 | COR8-5 … COR8-9 |

## A. Regression check (round-7 BLOCKER and MAJOR, correctness lens)

| ID | Sev | Status in round 8 | Where |
|---|---|---|---|
| COR7-1 | MAJOR | **Fixed in mechanism**, with new defects in the mechanism. Each of the fix's items landed. (1) `legacy.Maintenance{UpgradeFloors, StageFactoryReset}` is handed as `module.StorageHandles.Maintenance` from S2b (`05-storage.md:1560-1572`, `06-app-sdk.md:719-733,1517-1532`). Books' gin handler stages behind HEAD's chain (`05-storage.md:1736`, `05-rbac.md:995-1003`). (2) `internal/storagehttp` (L6, S4 r2) is in 03's list (`03-architecture.md:325`), 04 §8.1 S4 (`04-package-mapping.md:883`) and 09 S4 r2 (`09-abo-on-core.md:1035`); the legacy path becomes its `Alias`. (3) Auxiliary DBs are in the P0a set-aside (`05-storage.md:1737-1738`). (4) `TestFactoryResetFromLegacyRoute_S2b` exists (`05-storage.md:1746`). (5) The handle is deleted in the S8 PR (`06-app-sdk.md:1405`, `04-package-mapping.md:887`). | COR8-1 … COR8-4 |

Round-7 MINORs:

- **Fixed:** COR7-2 (`03-architecture.md:404-415`, every owner file), COR7-3 (`03-architecture.md:392-402`,
  `06-app-sdk.md:1567-1577`), COR7-4 (`05-storage.md:1384`, `06-app-sdk.md:1678,1680`), COR7-5
  (`12-repo-split.md:369`, `03-architecture.md:386-390`), COR7-6 (`03-architecture.md:290,468`), COR7-7
  (`05-storage.md:1602-1613`, `06-app-sdk.md:1448-1454`), COR7-9 (`05-storage.md:1739`), COR7-11
  (`04-package-mapping.md:1109,143`), COR7-12 (`04-package-mapping.md:882`) and COR7-13
  (`05-storage.md:2314`).
- **Fixed, with one signature nit (COR8-9):** COR7-8.
- **Half-fixed:** COR7-10. The L0 leaf removes the `backup/journal` edge, but restoreswap still imports `storage`
  while sitting "behind `storage.Admin`" (COR8-5).

## B. Flow traces

| Flow | Result |
|---|---|
| 1 Factory reset | **S2b–S4 r1** composes for `POST /system/factory-reset`, with three exceptions: the hook-argument capture (COR8-2), the set-aside destination (COR8-3) and the hook's completion and failure rules (COR8-4). **`POST /system/reset`**, which also calls `store.Reset()`, has no path from S2b (COR8-1). **S4 r2**: the route is specified as "over the kernel's `storage.Admin`", which cannot call `ConfigResetArgs`. It has no request, confirmation or response contract for its legacy `Alias` (COR8-2). **S8**: the deletion composes. |
| 2 Offline open | Composes. `app.Offline` exposes only `Storage(id)`, and that is enough: HEAD's `scan`, `organize`, `tag` and `playlist` read `AppConfig` from file, env and flags, never from the DB (`cmd/root.go:103-250` @748fd787a), so skipping P4d's config attach changes nothing. The naming is consistent in every file: `app.OpenOffline` in 09 and 14, `kernel.OpenOffline` for core's `users reset-password` in 05-authn and 05-rbac. The refusal for in-flight restores and resets uses the four tokens. Through COR8-4, a hookless app's reset leaves offline open refused forever. |
| 3 Boot | P0a precedence, the floor exemption for an empty store, P2/P4c backend split, P4c's three asserted capabilities, `NarrowStored` (S3 Basic gate, S4 r1 auth) and the dark-r1 skip all compose. So do the owed-duty consumers: journal (step 2), auth (step 4, with R7-5's cut-off-before-`Since` rule), usage (step 5) and gen (Run 3a). `kernel.Open` receives the restore identity from the kernel, and `storage/restorejournal` is at L0 in 03 R4 and 04 S2b. Breaks: the restoreswap ↔ `storage` import cycle (COR8-5); `ConfigResetArgs` as an unlisted fourth kernel-asserted capability (COR8-2); the reset sentinels' layer (COR8-6). |
| 4 `legacy` package | Clean. Every capability, helper and suite in 05-storage's one list (`05-storage.md:1388-1412`) has the same spelling in its owner file. `legacy` imports only L0–L4 and is imported only by `module`, `kernel` and apps. `module.StorageHandles.Maintenance legacy.Maintenance` is a legal L7 → L5 edge. hello imports no `legacy` identifier, and `archtest.SDKOnly` checks direct imports, so the transitive `module` → `legacy` edge is fine. |
| 5 Mirror tests / `any` options | Clean. `secrets.Options.LegacyStore any` (`05-config.md:1933-1938`) and `library.Options.LegacySource any` (`05-library.md:256-262`) both assert at construction. `storage.ErrLegacyStoreType` is defined once (`05-storage.md:1445-1449`). `secrets` is L2 and `library` is L4, so both may import `storage` (L1). |
| 6 Run-store retry | The mechanism is sound. The callers' `RetryOnConflict` loop re-stages the whole batch, and the drop predicate follows HEAD's closed-ledger rule. The contract text disagrees about who retries (COR8-7). |
| 7 hello | **Compiles.** I re-checked `app.Options{AppID, DisplayName, Version, EnvPrefix, StateDir, UI}` (`06-app-sdk.md:782-847`), `storage.Migration{Version, Name, Compat, Seed, Up func(ctx, *MigrationTx) error}` (`05-storage.md:693-708`), `tasks.Task`/`TaskSpec`/`Common`/`Permissions`/`Res`/`ReadOnly`/`RunItems`/`ItemsOptions`/`ItemRC.Count`/`ItemsResult.Counters`/`RC.Context` (`05-tasks.md:134-212,263,303-362,410,458,470,591`) and `module.StorageHandles.Keyspace`. hello's `Maintenance` is nil because it declares no claims. Since round 7, no symbol hello uses has changed. `TestHello`'s `Seen == 1` still holds on a fresh store. |

## C. Findings

### COR8-1: MAJOR. HEAD's second whole-store wipe, `POST /system/reset`, has no path from S2b, but the S2b PR deletes `PebbleStore.Reset`

**Where:** 05-storage §5S.9.3 row "HEAD's factory reset" (`05-storage.md:2251`: "`PebbleStore.Reset` … called by
`handlers/system/handler.go:538-560` … and `Reset` is deleted"); §5S.3.15 (`05-storage.md:1721-1736`); 05-rbac
§6.2 table (`05-rbac.md:992`: `POST /system/reset` → `core:system.manage`, auth off 401); 09 S2b; 06 P0a.

**Evidence:**

- HEAD has two callers of `store.Reset()`, not one. `ResetSystem` (`internal/server/handlers/system/handler.go:463-491`
  @748fd787a) calls `store.Reset()` at `:479` and then `config.ResetToDefaults()` at `:485`. It is wired with the
  same `credRoute` + always-authenticated admin group + `ownerGateWhenOwnerSet` chain as factory reset
  (`internal/server/wire_system_routes.go:45-46`). HEAD's admin test pins it (`internal/server/admin_routes_test.go:46`,
  "wipes the store, users included").
- The spec places only the factory-reset caller (`:551`). After the S2b PR deletes `PebbleStore.Reset`,
  `ResetSystem` either does not compile or keeps a whole-keyspace `DeleteRange`. 05-storage's own analysis says
  that range cannot run under claims (`05-storage.md:1724-1728`): strict mode answers 500, and the shadow release
  wipes `c/` under a live kernel, so the next boot halts with `ErrForeignKeys`.
- 05-rbac keeps the route and maps it to `core:system.manage` from S4 r2. It does not carry the owner and
  interactive guards that 05-rbac gives the factory-reset route, which wipes the same data (`05-rbac.md:993`).
- Routing it naively through `StageFactoryReset` is also wrong. Books' `ResetArgs` would record `RootDir`, and the
  post-reset hook would delete the organized library and the Open Library data. HEAD's `ResetSystem` deletes
  neither (`handler.go:463-491` has no file deletion).

**Fix:**

1. In 05-storage §5S.3.15 "Who stages", add a second legacy caller: from S2b, books' gin handler for
   `POST /system/reset` (HEAD's chain unchanged) stages the same reset with no file side effects.
2. Give the staging call a kind, e.g. `StageFactoryReset(ctx, requestedBy string, kind ResetKind)` with
   `ResetStore` (store + settings only, HEAD's `ResetSystem`) and `ResetFactory` (plus the post-reset hook's file
   wipes). Record `kind` in `reset/pending.json`. The kernel calls `ConfigResetArgs` only for `ResetFactory`, and
   books' hook deletes files only for that kind.
3. In 05-rbac §6.2, map `POST /system/reset` from S4 r2 to the same guards as factory reset (`core:admin` +
   `Guards.Interactive` + `Guards.Owner`), as an `Alias` of the S4 r2 route with `kind=store`.
4. Add the `handler.go:463-491` caller to the §5S.9.3 row.
5. Add `TestSystemResetStagesStoreOnlyReset_S2b`: no file under `RootDir` or the OL dump directory is deleted, the
   next boot is fresh, and HEAD's guards apply.

### COR8-2: MAJOR. The staging contract that captures the hook arguments has no layer-legal implementation, and the S4 r2 route bypasses it

**Where:** 05-storage §5S.3.15 "Who stages" and "Stage" (`05-storage.md:1736-1737`: "The kernel-only
`storage.Admin.StageFactoryReset(ctx, requestedBy) (string, error)`", and "call the app's
`legacy.ConfigResetArgs.ResetArgs` … then write `<state_dir>/reset/pending.json` = `{…, hook_args, …}`");
`legacy.ConfigResetArgs` (`05-storage.md:1575-1583`, `06-app-sdk.md:1535-1548`: "The kernel calls it inside every
StageFactoryReset"); 03 §3.3 (`03-architecture.md:394`: "The kernel asserts only three capabilities"); 06 §6.4.11
Detection (`06-app-sdk.md:1567-1577`) and P4c (`06-app-sdk.md:1680`); the S4 r2 route "over the kernel's
`storage.Admin`" (`03-architecture.md:325`, `04-package-mapping.md:883`, `09-abo-on-core.md:1035`: "staging through
the kernel's `storage.Admin`").

**Evidence:**

- `storage.Admin` is in `storage` (L1). It cannot name `legacy.ConfigResetArgs` (L5), and its stated signature
  `(ctx, requestedBy)` has no parameter for the bytes. So the intent's `hook_args` can only come from a kernel-level
  wrapper, and no file names one.
- The S4 r2 route is specified as calling `storage.Admin` directly. A literal implementation therefore writes an
  intent with no `hook_args` from S4 r2 on. Books' hook acts only on recorded paths, "never on what the fresh
  settings say" (`05-config.md:555-558`). With nil arguments it cannot rewrite the gap-fill file, delete the OL dump
  or wipe `RootDir`.
- If the hook then calls `Done`, `ResetUnfinished` clears, and the old install's gap-fill copies of the OpenAI,
  Google Books and Hardcover keys become effective in the fresh install (`05-config.md:1579-1586`). If it does not
  call `Done`, `reset_inflight` never clears (COR8-4).
- The kernel's call of `ResetArgs` makes `ConfigResetArgs` a fourth kernel-asserted capability, asserted at
  staging, not at P4c. 03, 06 Detection and 06 P4c each say there are exactly three. 06's capability paragraph
  (`06-app-sdk.md:1556`) contradicts its own Detection bullet eleven lines later.
- Nothing states the S4 r2 route's request or response. HEAD requires `{"confirm":"RESET"}` (`handler.go:540-547`).
  ABO's UI sends it and reads `body.data.message` (`web/src/services/api.ts:3260-3270` @748fd787a). The legacy path
  becomes an `Alias` of a route with no stated confirmation token and no `LegacyPath.Shape`. The alias can
  therefore drop HEAD's interlock or break the UI's response parsing.

**Fix:**

1. In 05-storage §5S.3.15, change the Admin method to
   `storage.Admin.StageFactoryReset(ctx, requestedBy string, kind ResetKind, hookArgs []byte) (string, error)`.
2. Add one kernel function, `kernel.stageReset(ctx, requestedBy, kind)`. It takes the staging mutex, calls
   `legacy.ConfigResetArgs.ResetArgs` when the built config store has it (and the kind needs it, COR8-1), then calls
   the Admin method. `legacy.Maintenance.StageFactoryReset` is this function.
3. `internal/storagehttp` receives this function from the kernel as a plain `func` or a storagehttp-local
   interface (`storagehttp.Stager`), never `*storage.Admin`, the same way `modulehttp.ModulesView` is handed
   (`06-app-sdk.md:1136-1142`). Change "over the kernel's `storage.Admin`" in 03, 04, 06 and 09 accordingly.
4. In 03 §3.3 "Layer and detection", 05-storage "Detection" and 06 Detection/P4c, list `legacy.ConfigResetArgs` as
   a kernel-driven capability, asserted at staging.
5. In 05-rbac §4.10 / 05-http-api, state the route's request `{confirm: "RESET"}` (422 otherwise) and a
   `LegacyPath.Shape` that keeps HEAD's `{data: {message}}`. Add `TestFactoryResetCoreRouteRecordsHookArgs` (S4 r2:
   the intent from the core route and from the alias carries `hook_args`) and `TestFactoryResetAliasKeepsConfirmAndShape`.

### COR8-3: MAJOR. The P0a set-aside renames every target into `<db parent>/.mediacore-reset/<id>/previous/`, but two kinds of target can sit on another filesystem

**Where:** 05-storage §5S.3.15 "Stage" and "Set aside" (`05-storage.md:1737-1738`: targets include "every
auxiliary DB the kernel opened (`<db parent>/aux/<module>/<name>` or a module's `LegacyPath`, such as books' Open
Library store)", "`<state_dir>/config.boot.json` … and `<state_dir>/storage.boot.json`"; "moves every target, by
idempotent renames … into `<db parent>/.mediacore-reset/<id>/previous/`"); 06 P0a (`06-app-sdk.md:1675`);
05-backup factory-reset paragraph (`05-backup.md:703`).

**Evidence:**

- **The Open Library store.** HEAD keeps it at `<GetOLDumpDir()>/oldb`, and the dump directory defaults to
  `<RootDir>/openlibrary-dumps` (`internal/metafetch/openlibrary.go:54-61,73` @748fd787a). Books opens it from
  S2b through `OpenAuxiliary` with that `LegacyPath` (`09-abo-on-core.md:993`), so by default it lives on the
  library volume.
- **The state-directory files.** The state directory is the compiled default or an explicit path, falling back to
  the DB's directory only when the default cannot be created (05-config C15, `05-config.md:39`). The spec states
  that the DB parent need not be under the state directory (DATA2-19, `03`/`06` P2).
- `rename(2)` across filesystems fails with `EXDEV`. On a host whose library or state directory is on another
  volume than the DB, the reset therefore cannot complete its set-aside. Per 05-backup step x, a failed rename
  swaps back and exits non-zero (`05-backup.md:701`), so the admin's audited reset silently never happens. If the
  swap-back itself fails, boot halts.
- The S2b done-check does not catch this, because a single temp filesystem never returns `EXDEV`:
  `TestFactoryResetFromLegacyRoute_S2b` asserts "the Open Library store moved" (`05-storage.md:1746`).

**Fix:**

1. In 05-storage §5S.3.15, set each target aside **beside itself**:
   - DB-parent items to `<db parent>/.mediacore-reset/<id>/previous/`;
   - state-directory files to `<state_dir>/reset/<id>/previous/`;
   - an auxiliary DB at a `LegacyPath` to `<its parent>/.mediacore-reset/<id>/`.
2. Record each target's destination in the journal's `moved` records, so resume and manual undo know where each
   item went.
3. Alternatively, do not move `Recreatable` auxiliary DBs outside `<db parent>` at all. Books' post-reset hook
   `RemoveAll`s the recorded OL dump directory, as HEAD's `deleteOLDataLocked` does (`handler.go:507-536`).
4. Mirror the change in 06 P0a and 05-backup.
5. Add `TestFactoryResetCrossDeviceTargets`, using two filesystems or an injected `EXDEV` rename.

### COR8-4: MAJOR. `hook_done` has no owner when an app has no post-reset hook, and the hook's failure rule contradicts the start-hook contract

**Where:** 05-storage §5S.3.15 "Side effects" and "Rollback tooling" (`05-storage.md:1742-1743`); error row
(`05-storage.md:1999`: "Books' post-reset hook fails … | open: the next boot reruns"); `legacy.ResetRun`
(`05-storage.md:1585-1597`); `legacy.ConfigOpenEnv.Reset` (`05-config.md:545-551`); 06 P0a (`06-app-sdk.md:1675`)
and §6.5.2 (`06-app-sdk.md:1770-1772`, `core.lifecycle.start_timeout` 60 s at `:1878`); offline open
(`05-storage.md:1956`); 14 Q-K (`14-risks-open-questions.md:92`).

**Evidence:**

- **Nobody completes a hookless reset.**
  - `reset/<id>/inflight.json` is "kept until `hook_done`", and only `ResetRun.Done` journals `hook_done`.
  - The kernel hands a `ResetRun` only through `legacy.ConfigOpenEnv`, that is, only to an app whose legacy config
    store implements `legacy.ConfigOpener`.
  - hello, every TV or Movies app and ABO after its settings cutover have no such store. Their `POST
    /api/core/v1/system/factory-reset` (S4 r2 on) leaves `reset_inflight` forever. `make rollback`, `deploy` and
    the updater refuse permanently (the updater has no override, `05-updater.md:608`), and `kernel.OpenOffline`
    refuses permanently.
- **The failure rule is self-contradictory.**
  - The hook runs inside books' start hook at Run step 2, and §6.5.2 fails `Run` when a hook's `Start` errors or
    exceeds 60 s. The error row says a failing hook is "open".
  - Per-entry deletion failures are not classified. HEAD only warns on them (`handler.go:574-577`). A
    non-removable entry is common: a `lost+found` in a `RootDir` that is a mount point, or a read-only NAS
    export. Read as "fail Run", such an entry crash-loops the install. Read as "open", `Done` is never called and
    the blocking above follows.
  - Wiping a large library on a NAS in one start hook can exceed 60 s.
- **The dangerous-root refusal is placed in two different steps.** 05-storage puts HEAD's `IsDangerousRoot`
  refusal in the hook (`05-storage.md:1742`), which runs after the store was already set aside. 14 Q-K says "the
  intent records it at staging, where HEAD's dangerous-root refusal also moves" (`14-risks-open-questions.md:92`).
  The two disagree on whether a dangerous root refuses the reset or only skips the wipe.
- **The wipe can reach what it must keep.** When `<db parent>` or the state directory lies under `RootDir`, the
  hook's `RootDir` wipe would delete the live fresh store, `previous/` (the only undo) and the kept keyring.

**Fix:**

1. **Kernel-owned completion.** In 05-storage §5S.3.15, the kernel journals `hook_done` itself, right after
   `applied`, when the built config store does not implement `ConfigResetArgs`/`ConfigOpener` (no app hook). 06 P0a
   and 05-config `ConfigOpenEnv.Reset` say so.
2. **The refusal moves to staging.** Books' `ResetArgs` returns an error for a dangerous `RootDir`, so the request
   answers 400 as HEAD does and nothing is staged. The hook re-checks only to skip the wipe, never to fail. Q-K and
   §5S.3.15 then agree.
3. **The hook never fails `Run`.** A per-entry failure is logged, as at HEAD, and counted in the boot log and the
   health token `reset_hook_incomplete`; the hook still calls `Done`. Only a crash before `Done` reruns the hook.
4. **The hook's runtime is bounded.** It calls `Deps.ExtendStart` per batch of entries and is exempt from the 60 s
   per-hook `start_timeout`, or it runs the wipe as a one-shot `BootBlocking` step with persisted progress.
5. **Kept paths survive the wipe.** The hook skips any entry that contains `<db parent>`, the state directory or a
   set-aside destination.
6. **Tests:** `TestFactoryResetNativeAppCompletes` (hello: `reset_inflight` is gone after the reset boot),
   `TestPostResetHookUnremovableEntryCompletes` and `TestPostResetHookSkipsKeptPaths`.

### COR8-5: MINOR. COR7-10 half-fixed: `internal/restoreswap` imports `storage`, while `storage.Admin` delegates to it

**Where:** 03 R4 (`03-architecture.md:567`: "`internal/restoreswap` (the restore and factory-reset directory swap
behind `storage.Admin`) is **L1** and imports only `storage` and its L0 leaves"); 05-backup Stages
(`05-backup.md:28-29`: "`core/internal/restoreswap` behind `storage.Admin` … imports only `storage`");
05-storage §5S.3.15 (`05-storage.md:1732`: the reset "is core code … in `core/internal/restoreswap`", staged
through `storage.Admin.StageFactoryReset`).

**Evidence:** Suppose `storage.Admin`'s staging and swap methods (package `storage`) call `restoreswap`, and
`restoreswap` imports `storage`. Then `storage` → `internal/restoreswap` → `storage` is an import cycle. The L0 leaf
removed the `backup/journal` edge that COR7-10 named. It did not remove this one.

**Fix:** In 03 R4 and 05-backup, state that `restoreswap` imports only the L0 leaves (`storage/restorejournal`,
`storage/keys`) and the standard library, never `storage`. Its sentinels move to the leaf (COR8-6). The kernel
calls `restoreswap` directly at P0a and from `kernel.stageReset` (COR8-2), so "behind `storage.Admin`" becomes
"run by the kernel". Alternatively, move the staging and swap methods off `storage.Admin` onto a kernel-held
`restoreswap.Swapper`. `core/internal/archtest_layers_test.go` then checks the edge.

### COR8-6: MINOR. The reset/restore refusal sentinels live in L4 `backup` (S8) but are returned by S2b and L1 code, and the S2b–S7 mutual exclusion has no shared lock

**Where:** `ErrRestorePending` and `ErrResetPending` are defined in package `backup` (`05-backup.md:363-364`).
`legacy.Maintenance.StageFactoryReset` and `storage.Admin.StageFactoryReset` return `ErrRestorePending` from S2b
(`05-storage.md:1568,1737,1997`; `06-app-sdk.md:1527`). The "Precedence" row (`05-storage.md:1739`: "they exclude
each other at staging, so only a crash between two processes or a hand-made file leaves both") and "Stage" (`:1737`:
"Under the kernel's one staging mutex") are the claims at issue.

**Evidence:**

- `backup` lands at S8 and is L4. `storage` (L1) and the kernel's S2b staging code cannot return its sentinel, and
  books' gin handler cannot `errors.Is` against it before S8.
- From S2b to S7, ABO's own `internal/backup` stages restores (`05-backup.md:33-37`) outside the kernel's staging
  mutex. It checks `reset/` files itself, while `StageFactoryReset` checks `restore/` files under the kernel mutex.
- Two concurrent requests in one process can therefore both pass their checks and both write intents. The
  "precedence" claim is false before S8. P0a still resolves the case by rejecting the restore (`restore_rejected`).

**Fix:**

1. Define `ErrRestorePending` and `ErrResetPending` in `storage` (or `storage/restorejournal`), and make
   `backup`'s names aliases.
2. Add `Maintenance.WithStagingLock(func() error) error` for ABO's pre-S8 restore staging, or state the S2b–S7
   race and its P0a outcome in "Precedence".
3. Extend `TestRestoreAndResetBothStaged` with two concurrent stagings at S2b.

### COR8-7: MINOR. Who retries `Insert`/`Transition`: 05-storage and the `RunStore` contract say the methods do, 5T.7 says the callers do

**Where:** 05-storage §5S.3.2 (`05-storage.md:357-360`: "`Transition` and `Insert` re-read the run on a conflict
and recommit under `RetryOnConflict` …, returning `ErrRunConflict` only when it does not"); 05-tasks `RunStore`
comment (`05-tasks.md:1171-1172`: "Insert and Transition then retry while their want still holds") and
`Transition` doc (`:1182-1183`); against 5T.7 (`05-tasks.md:1756-1765`: "whoever builds such a batch … builds
**and** commits it inside `storage.RetryOnConflict`").

**Evidence:** `Insert` and `Transition` stage into the caller's `*storage.Batch` and never commit, so they never see
the commit's `storage.ErrConflict`. Only the caller can rebuild every participant: clock advance, outbox cursor,
idempotency record, `OnTerminal`. If books' `taskstore` follows the interface text and adds an internal retry, it
cannot re-stage the other participants. If the executor follows the interface text and omits the loop, unrelated
runs' census-row writes fail its transitions spuriously.

**Fix:** In 05-storage §5S.3.2 and the `RunStore`/`Transition` comments, write: "`Transition` and `Insert` return
`ErrRunConflict` at staging when their `GetForUpdate` read no longer matches `want`. The caller that builds the batch
re-runs build-and-commit under `RetryOnConflict` (5T.7), which re-stages every participant." Name the callers once,
as 5T.7 does.

### COR8-8: MINOR. Three statements about the reset contradict the definitions they cite

**Where and evidence:**

1. `TestRestoreAndResetBothStaged` (`06-app-sdk.md:1917`) says `CheckReader` reports `reset_pending` "while
   `reset/pending.json` or a reset journal exists". The definitions use `reset_inflight` for
   `reset/<id>/inflight.json` (`05-storage.md:1104-1107,1743`). Every reset leaves its `journal.json` behind, so
   read literally, this row refuses every later deploy.
2. 06's write rule (`06-app-sdk.md:1625`) says "the deletion of `storage.boot.json` by an accepted factory reset".
   P0a moves it to `previous/` (`06-app-sdk.md:1675`, history line `:2948`: "moved, not deleted").
3. When `ResetRun` is handed differs between files. 05-storage step 11(a) and "Side effects" (`05-storage.md:1742,1890-1891`)
   and 06 P0a ("runs only on the boot whose reset journal reads `applied`") say the kernel hands it on boots whose
   journal reads `applied`. 05-config states the precise rule (`05-config.md:545-549`): on the reset boot,
   `Reset` is handed at `Open` while the journal still reads `set_aside`. 05-config's rule is the only one under
   which the hook runs on the reset boot itself.

**Fix:** In 06 `:1917`, write "`reset_pending` while `reset/pending.json` exists, `reset_inflight` while a
`reset/<id>/inflight.json` exists". In 06 `:1625`, write "the set-aside of `storage.boot.json`". In 05-storage and
06 P0a, copy 05-config's sentence: handed at `Open` on the reset boot (journal at `set_aside`) and on every later
boot whose journal reads `applied` without `hook_done`; the hook runs at Run step 2, after `applied`.

### COR8-9: MINOR. The reset text has a stale landing row, a signature mismatch and an ambiguous path

**Where and evidence:**

1. 06 §6.4.10's S2b row (`06-app-sdk.md:1399`) lists `legacy.Maintenance`. It omits `legacy.ConfigResetArgs`,
   `legacy.ResetRun` and the `legacy.ConfigOpenEnv.Reset`/`ResetUnfinished` fields, which land at S2b per
   05-storage's one list (`05-storage.md:1401`). 04 §8.1 S2b says only "the S2b capabilities".
2. 05-storage defines `ReadOnlyDB.Keyspace(owner Owner)` (`05-storage.md:1357`). 06 P4d step 3 says the second
   `legacy.Handles` come from "`Keyspace(prefix)`" (`06-app-sdk.md:1680`). Neither says how `Handles.Ext`
   (`m/<owner>/ext/<store>/`, the fixed segment) is cut from it.
3. The reset rows write the gap-fill file as `<db dir>/config.yaml` (`05-storage.md:1742`), next to rows that move
   "the DB directory". HEAD's file is `filepath.Dir(DatabasePath)/config.yaml` (`internal/config/persistence.go:31-32`),
   i.e. beside the moved directory. 05-backup and 09 already write `<db parent>/config.yaml` (`05-backup.md:131`,
   `09-abo-on-core.md:844`). Read as "inside the DB directory", the hook rewrites a file the set-aside already
   moved, and the real gap-fill copy keeps the old install's three plaintext keys.

**Fix:**

1. Add the three S2b members to 06's S2b row and name them in 04's S2b row.
2. In 06, write `Keyspace(owner)` and "`Ext` = `Keyspace(owner).Sub("ext/<segment>")` per 05-storage's segment
   table".
3. In 05-storage §5S.3.15 and 05-config §1.12.2's reset paragraph, write `<db parent>/config.yaml`.
