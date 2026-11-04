<!-- file: docs/plans/platform-core/reviews/spec-round-9-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92bd6807-b89a-4d43-9af9-cfe9492da670 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 9: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR9`). Inputs: `docs/specs/platform-core/` after the advisor pass
(03 v1.12.1 normative; 05-storage, 05-http-api, 05-rbac, 05-authn, 05-tasks, 05-config, 05-ai, 06 and 14 at v1.10.1;
05-backup, 05-metrics and 09 at v1.9.1; 04 v1.9.0). Binding: A-1..A-17 (they supersede earlier resolutions where
they conflict), R3-1..R8-6. Code anchors are `path:line @748fd787a`. Spec anchors are `file:line` in
`docs/specs/platform-core/`.

Method. I traced the eight flows the brief names through the Go blocks, phase tables, stage tables and test rows,
looked up every cross-file name in its owner file, and checked layers against 03 R4. For legacy-backed ABO data the
bar is HEAD parity, so every lock or check the A-2/A-3 deletions removed was compared against what HEAD's code
actually does.

**Verdict.** The deletions are clean: no body text still uses the extension family, `legacy_ref`, `NarrowStored`,
`operator-names.json`, `DeleteLegacySignIns`, `["none"]`, the sentinels, `reset_*` tokens, `c/compat/`, `stagecheck`
or `CheckedWrites` on legacy rows (only native-row `OperatorSecretOrigin` remains, which A-2 keeps). The rollback table
was re-derived to nine rows and every cross-file row citation matches it. `hello` still compiles. What breaks:

- A-3 kept HEAD's identity mutexes, but `auth.Store` stages into the caller's batch, so those mutexes no longer span
  the commit. Early fix 6 and the single-use invite and bootstrap guarantees reopen from S4 r2 (COR9-1).
- A-1's rule 1 refuses the very cross-owner component that its rule 7 needs at S5 (COR9-2).
- `LegacyAmbientScopes` is injected at role read, and the key intersection removes it again (COR9-3).
- The A-17 confirmation contract differs across four files, and the stated signatures cannot carry it (COR9-4).

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 2 | COR9-1, COR9-2 |
| MAJOR | 4 | COR9-3, COR9-4, COR9-5, COR9-6 |
| MINOR | 9 | COR9-7 … COR9-15 |

## A. Regression check

| ID | Round-8 severity | Status | Where |
|---|---|---|---|
| COR8-1 (`POST /system/reset` has no path) | MAJOR | **Fixed.** Kind `ResetStore`, books' gin handler through `legacy.Maintenance`, the S4 r2 alias with `reset_kind=store`, and `TestSystemResetStagesStoreOnlyReset_S2b` (`05-storage.md:1670-1671,1682`). | — |
| COR8-2 (hook-argument capture; S4 r2 bypass) | MAJOR | **Fixed in mechanism.** `kernel.stageReset`, `storagehttp.Stager` and `ConfigResetArgs` are now in 03's one list of four. A-17 then re-opened the request contract, and `Stager` still has no method set. | COR9-4, COR9-9 |
| COR8-3 (cross-filesystem set-aside) | MAJOR | **Fixed.** Each target is set aside beside itself, rename pre-validation runs at staging, a `Recreatable` auxiliary DB is deleted, and `TestFactoryResetCrossDeviceTargets` covers it. One destination rule differs between 05-storage and 06. | COR9-12 |
| COR8-4 (`hook_done` owner; failure rule) | MAJOR | **Fixed.** The kernel records `hook_done` when there is no hook, the hook never fails boot, the dangerous root is refused at staging, kept paths are skipped and runtime is bounded (`05-storage.md:1677-1678`). | — |
| COR8-5 … COR8-9 | MINOR | Fixed. The one residue is 03 R4's "first for a `ResetFactory`". | COR9-9 |
| ADV-1 (routes cannot all be mux patterns) | BLOCKER | **Half-fixed.** S2 composes. At S5 rule 1 fails `Build`. | COR9-2 |
| ADV-2 (legacy decode parity) | MAJOR | **Fixed** (`05-config.md:767-790`, `TestLegacyEnvDecodeMatchesHEAD` with a frozen golden). The env case-sensitivity correction is right (`cmd/root.go:625`; viper upper-cases the key). | — |
| ADV-4 (whole-boot crash model) | MAJOR | **Half-fixed.** The diagrams are good. The harness cannot reach ABO's boot writers, and the writer table is incomplete. | COR9-5 |
| ADV-6 (`staging-scrub` interlock) | MAJOR | **Half-fixed.** The marker is specified, but from S2b ABO has no API that can write or read `c/storage/staging`. | COR9-6 |
| ADV-8 (`config.yaml` second copy) | MINOR | Fixed. The clause is gone, and `TestCoreWritesOnlyHEADGapFillKeys` exists. | — |

## B. Flow traces

| Flow | Result |
|---|---|
| Legacy stores in HEAD representation | Interfaces, `legacy` list and mirror pattern compose; `Handles` lost `Ext` everywhere. Breaks: identity lock discipline across the caller's commit (COR9-1); `legacy.AuthAtomWriter` missing from the one list (COR9-10); config's model (COR9-11). |
| `rbac.LegacyAtom`, `LegacyAmbientScopes` | `LegacyAtom` reduces to HEAD's `s.perm`/`auth.Can` on ABO (`05-rbac.md:1180-1189`) and auth-off keeps HEAD's open `/metrics` (`AuthDisabled` holds every non-`AuthRequired` scope). Ambient scopes fail for scoped keys (COR9-3). |
| A-1 collision rule | S2 r1 composes (both sets same-requirement; kept routes strictly more specific; the generalised patterns conflict with no other HEAD route, checked against `raw/09-http-api.md` rows). S5 contradicts itself (COR9-2). |
| `TasksWriteLock` / `WriteRuns` | Sound for the run families; capability target and the non-run stores are underspecified (COR9-8). |
| Boot phases, P−1 deferred | 06's table and the matrix compose for core writers; ABO writers uncovered (COR9-5); several files still run P−1 (COR9-7). |
| Swap and reset, `delete_library_files` | One slot/journal/token pair consistent in 03, 05-storage, 05-backup, 06, 09, 14. Breaks: A-17 contract (COR9-4); minor drift (COR9-12, COR9-13). |
| `kernel.OpenOffline` | Verdicts, close batch, refusal and users list consistent. `app.Offline` too narrow for what ABO must do offline (COR9-6); checkpoint finish vs refusal (COR9-15). |
| `MetricNamespace`, `ai.WithObserver` | Names consistent in 03, 05-metrics, 06, 09; `ErrFamilyTaken` guards a shared namespace/prefix. 04 and 05-ai disagree on who emits HEAD's unprefixed AI families (COR9-14). |
| `hello` | **Compiles.** `app.Options` gained only optional fields (`MetricNamespace`, `LegacyAmbientScopes`); `storage.Spec`, `Migration{…Seed, Up}`, `MigrationTx.{Keyspace,Set}`, `Keyspace.{Sub,Get,Scan}`, `ScanOptions`, `KV.Clone`, `tasks.Task/TaskSpec/Common/Permissions/ReadOnly/Res/RunItems/ItemsOptions/CPU`, `module.Setting`, `Registrar.Resources`, `coretest.New/WithModules/WithUser/AssertPermission/As/GetJSON/RunTask` match their owner files; `Maintenance` is nil for hello (no claims). |

## C. Findings

### COR9-1: BLOCKER. HEAD's identity mutexes cannot span the caller's commit, so A-3 reopens early fix 6 and breaks single-use invites and bootstrap from S4 r2

**Where:**
- 05-authn §3.4 (`05-authn.md:537-542`): "a legacy store keeps its app's own lock discipline … `inviteMu`, `bootstrapMu`
  and `apiKeyMu`".
- §4.4 (`:821-829`): every `apikey:` writer "runs under `apiKeyMu` … so a revoke and a touch never interleave".
- §4.6 step 2 (`:862-864`): bootstrap is spent "under HEAD's `bootstrapMu`".
- §7 (`:1373-1383`): invites "keep HEAD's own mutexes", and the touch runs "under `apiKeyMu` in ABO's".
- Against these, 05-storage §5S.9.1 row 3 (`05-storage.md:2325`): "books' identity store stages an `Expect` on the
  row it read where it writes into auth's batch (05-authn §3.4)". Rows 4a and 6 (`:2326,2329`) say "under HEAD's
  `inviteMu`/`sessionWriteMu`". §5S.3.2 (`:379-388`) defers to the owner spec on who compare-and-sets.

**Evidence:**
- HEAD holds each mutex **across the Pebble write**. `TouchAPIKeyLastUsed` takes `apiKeyMu`, reads the row and
  `p.db.Set`s it inside the lock (`internal/database/pebble_store_auth.go:1284-1302` @748fd787a). `inviteMu`
  "serializes every invite write … so an accept is single-use" and every email check-then-write
  (`internal/database/pebble_store.go:232`).
- In core every `auth.Store` write **stages into the caller's `*storage.Batch`** and never commits
  (`05-authn.md:535-537`: `PutAPIKey(b,…)`, `TouchAPIKey(b,…)`, `TakeOneTime(b,…)`, `PutUser(b,…)`). The auth
  service commits later, with the audit record and the journal line. A mutex taken inside the method is therefore
  released before the commit. For the run store the spec saw this and added `legacy.TasksWriteLock`
  (`05-tasks.md:1363-1377`). The identity store has no equivalent, and 05-authn names no `Expect`.
- With only the 05-authn text, three interleavings commit. Touch reads the active row, then a revoke reads, stages
  and commits, then the touch commits the pre-revoke row: the revocation is undone. This is SEC6-3, which early
  fix 6 closed at HEAD, and a stolen key widens the window by looping requests. Two `TakeOneTime` calls on one
  invite or bootstrap token both stage a blind delete and both commit, so two accounts come from one invite, or
  bootstrap is redeemed twice. Two `PutUser` calls with one e-mail both pass HEAD's uniqueness check. HEAD prevents
  all three today. The tests that claim otherwise (`TestLegacyRevokeRacesTouch_NeverUnrevokes`,
  `TestLegacyInviteDoubleAcceptOneWins`, `TestBootstrap_SingleUseConcurrent`) are specified against ABO's current
  code or the native store, not against core's service over books' store.

**Fix (pick one, state it in 05-authn §3.4, §4.4, §4.6 and §7, and make 05-storage rows 1, 3, 4a, 6 and 8 cite it):**
- (a) **Compare-and-set.** Books' identity store stages `GetForUpdate`/`Expect` on every legacy row it read for a
  write: the `apikey:` row for touch, status and expiry; the `invite:` row and the bootstrap hash row for
  `TakeOneTime`; the `u:` row and its e-mail index row for `PutUser`. Those claims are already `Serialized`. A
  conflicting touch is dropped, and a conflicting take fails, so exactly one wins.
- (b) **A lock that spans the commit.** Add `legacy.AuthWriteLock{LockIdentity(ctx) (held, unlock, err)}`, the
  analogue of `TasksWriteLock`, and one auth build-and-commit path, `auth.writeIdentity(ctx, fn)`, that holds
  `inviteMu`/`apiKeyMu`/`bootstrapMu` from the first read until the commit returns.

Then:
- Add both rows to 05-storage's one list.
- Move the three tests into `legacytest.AuthStoreSuite`, run through `auth.Service` over books' store.
- Delete "keeps HEAD's own mutexes" wherever the method only stages.

### COR9-2: BLOCKER. ADV-1 half-fixed: rule 1 fails the cross-owner component that rule 7 relies on at S5

**Where:**
- 05-http-api §4.8, rule 1 (`05-http-api.md:779-782`): routes are registered "from S4, every native route and alias
  of the same build … A component that holds routes of more than one owner fails `Build` with
  `ErrPatternConflict`".
- Rule 7 (`:806-810`): the S5 core alias "registers beside" books' generalised pattern.
- Error row (`:946`): "a cross-owner component" is a boot error.
- `TestCollisionRule_RefusesUnmergeableSets` (`:1369`, "a component spanning two owners → `ErrPatternConflict`")
  and `TestS5_CoreAliasesRegisterBesideLegacy` (`:1371`).
- 03 R6 (`03-architecture.md:563`) and 09 risk row (`09-abo-on-core.md:1388`).

**Evidence:**
- At T0+T1 books' gin route `GET /api/v1/operations/v2/:id` is gone (`wire_operations_routes.go:27`). It is now
  core's alias.
- The pairwise relation of rule 1 links the alias with each of books' `…/{id}/activity`, `/changes`, `/result` and
  `/results`. They conflict exactly as in appendix A. So the component holds two owners, and rule 1 fails S5's
  `Build`.
- Computing components per owner does not help either. Without the kept partner, books' four routes do not
  conflict with each other, so nothing folds and the alias then panics against each of them.
- The two required tests contradict each other, and the S5 flip build does not boot as written. This is the
  advisor's "S5: no at T0+T1" case.

**Fix:** In rule 1, replace the blanket refusal:

> A component may contain routes of a second owner only as **kept** routes (rule 2), each a ledger-listed core
> alias (SEC3-8). Every folded member must share one owner. Kept routes of any owner count as partners when the
> generalised pattern is computed (rule 3).

Then:
- Mirror the rule in the error row, in 03 R6 and in 09's risk row.
- Change the refusal-test fixture to "folded members spanning two owners".
- Add to `TestS5_CoreAliasesRegisterBesideLegacy` that books' four routes are still folded once their partner is
  core's.

### COR9-3: MAJOR. `LegacyAmbientScopes` is added to roles, and the key intersection removes it, so scoped `abk_` keys lose HEAD's authenticated-only routes

**Where:**
- 05-rbac §6.3 read mapping (`05-rbac.md:1128-1130`): "Each role read from the store also gets the app's ambient
  scopes … when it is not `Confined()`", and writes subtract them.
- §6.3 rule (`:1139-1146`): "every principal that satisfies `AnyAuthenticated` … holds those scopes".
- §4.4 (`:616`): with key scopes "the checker is the intersection".
- `CheckerInput` (`:353-365`) has no ambient input.
- Tests: `TestLegacyAmbientScopes` (`:1515`, "a key scoped `["library.view"]` included") and
  `TestLegacyMetrics_AnyAuthenticated` (`05-http-api.md:1331`: that key → 200).
- 06 `app.Options.LegacyAmbientScopes` (`06-app-sdk.md:957-970`).

**Evidence:**
- An ABO key's scopes are its atoms' expansion (`05-rbac.md:628-640`; `["library.view"]` gives
  `books:view`). The intersection with the role set, ambient scopes included, drops `core:self`,
  `core:metrics.read` and `books:reading`.
- At HEAD such a key passes every authenticated-only route:
  - `/metrics`: any credential (`internal/server/server_lifecycle.go:1736-1747`);
  - `GET /api/v1/auth/me`;
  - its own keys;
  - reading state.
- In core these become `core:self` (`05-authn.md:1116-1119`, with the `/api/v1/auth/me` alias at
  `05-rbac.md:1233`), `books:reading` among the 36 permission-less routes, and from S8 `core:metrics.read`.
- So from S4 r2 a scraper or automation key gets 403 where HEAD answers 200. This contradicts the spec's own tests
  and `TestLegacyRoleReachUnchanged`'s scoped-key cells.

**Fix:**
- Add `Ambient []Permission` to `rbac.CheckerInput`. `rbac` fills it from the option when the role store is
  legacy-backed.
- `Compile` decides `Confined()` on the set **before** ambient. When the principal is not confined, it adds
  `Ambient` **after** the key intersection.
- Delete the role-read injection and the "minus the ambient scopes" write rule, so role editors and `/me`'s
  `Concrete()` stay honest. Ambient scopes come only from the principal, never from a role.
- State this in §6.3 and in 06's option comment.
- Add a `LegacyFullScope` key and a key scoped `["library.view"]` to `TestLegacyAmbientScopes`, on `/api/v1/auth/me`,
  `/me/api-keys` and `/metrics`.

### COR9-4: MAJOR. The A-17 confirmation contract differs across four files, and `stageReset`/`Stager`/`ResetArgs` cannot carry it

**Where:**
- 05-storage §5S.3.15 "Who stages" (`05-storage.md:1671`): the request is `confirm` plus `delete_library_files`; a
  missing confirm gets 422; with the flag, the 422 and the 200 "name the `RootDir` and the number of files";
  success is `{reset_id, message}`.
- 05-rbac §6.2 (`05-rbac.md:1012-1034`) and 05-http-api §5 (`05-http-api.md:902-912`): the request must also carry
  `library_root` and `library_file_count`; a mismatch gets 422 `library_files_confirm_required`; success is
  `{reset_id, message, library_root, library_file_count}`; the alias sets `delete_library_files: true` and the path
  value `library_confirm=legacy`.
- 06 §6.4.6 (`06-app-sdk.md:1179-1186`): "require `{"confirm":"RESET"}` and answer 422 otherwise"; the alias "passes
  `delete_library_files: true` through its `LegacyPath.Request`".
- `kernel.stageReset(ctx, requestedBy, kind, deleteLibraryFiles) (string, error)` (`05-storage.md:1671`).
- `ConfigResetArgs.ResetArgs(ctx, kind, deleteLibraryFiles)` (`:1509-1511`), and `ConfigResetPlan.Hook` "the
  library_root and library_file_count the route confirmed" (`:1515-1519`).
- `LegacyPath.Request` "may set path values" only, and names only `reset_kind=store` (`05-http-api.md:262-268`).
- 06's `ConfigResetRun` (`06-app-sdk.md:1645-1650`) lacks 05-storage's `DeleteLibraryFiles` (`05-storage.md:1546-1548`).

**Evidence:**
- The route must:
  1. compare the client's `library_root` and `library_file_count` with the current values before anything is
     staged;
  2. answer 422 with the current values;
  3. answer 200 with them.
- `stageReset` takes no confirmed values and returns only an ID. `ResetArgs` never sees the client's values. The
  Go type of `storagehttp.Stager` is defined nowhere. So the route either stages without binding the wipe to what
  the user confirmed (a `RootDir` changed between the two requests is wiped unseen), or the implementer invents
  the contract.
- The alias cannot inject a body field through `Request`, so it either loses HEAD's wipe or the mechanism is
  stretched unreviewed.
- Counting every file under a NAS library inside the request, on the legacy path too, where HEAD counted nothing,
  is a latency cost nobody states. A live import also changes the count between the two requests.

**Fix:**
- Make 05-rbac §6.2 the one contract, and cite it from 05-storage and 06.
- Define:

  ```go
  type storagehttp.Stager interface {
      StageReset(ctx, requestedBy string, kind restorejournal.ResetKind, lib LibraryConfirm) (StageResult, error)
  }
  // LibraryConfirm{Delete, Legacy bool; Root string; Files int}
  // StageResult{ID, Root string; Files int}
  ```

  with `*storage.ResetConfirmError{Root, Files}` for the 422.
- `kernel.stageReset` takes the same arguments. It calls `ResetArgs`, compares when `!Legacy`, and only then stages.
- `ResetArgs` counts files only when `Delete && !Legacy`.
- The factory-reset alias sets the path values `library_confirm=legacy` and `delete_library_files=true` (add the
  latter to `LegacyPath.Request`'s comment beside `reset_kind`), never a body field.
- Add `DeleteLibraryFiles` to 06's `ConfigResetRun`.
- State whether the count is exact or a bound, so a live import cannot loop the confirmation.

### COR9-5: MAJOR. ADV-4 half-fixed: the crash matrix lives in core and cannot exercise ABO's boot writers, and the writer table is incomplete

**Where:**
- 05-storage §5S.3.16 (`05-storage.md:1894-1930`): "`TestBootCrashMatrix` is a required S2b test in core
  (`core/internal/kernel`) … boots fixture stores in-process", and "runs `prev` (… built from `PREV_REF` as the
  R-test does)".
- 06 boot-writer table (`06-app-sdk.md:1802-1828`): rows "Run 2 | books' start hooks … the post-reset hook";
  "P4d 1 | `secrets.Open` (from S3)".
- 09 places the test in ABO's `extraction-gates` (`09-abo-on-core.md:220,1030`).
- 05-storage row 7 (`05-storage.md:2330`): "The legacy `Open` writes nothing".

**Evidence:**
- A test in `core/internal/kernel` cannot import `books` (03 R1), so it never runs books' P4d step 1 `Open`, books'
  start hooks, the post-reset hook or `auth.BootWrites` over books' store. Those are the writers its own table
  lists, and the ones in which rounds 6–8 found the joint defects.
- `prev` "from `PREV_REF`" is an ABO release binary, which a core package test cannot build.
- The table also omits writes inside the range:
  - Books' `Open` runs HEAD's `InitEncryption` (06 P4d (d)1(i)), which writes the key file on a store that has none
    (`internal/database/settings.go:149`, a plain `os.WriteFile`). So row 7's "writes nothing" is false for a
    fresh install or the first boot after a reset with no key.
  - The kernel journals `applied` and, for `ResetStore` or a store with no hook, `hook_done` after removing the
    `delete` entries (`05-storage.md:1677`). Neither has a row.
- ABO code does not write through `core/internal/statefs`, so `TestBootCrashMatrixCoversEveryWriter` cannot see
  books' unlabelled writes, and it passes while they go uncovered.

**Fix:**
- Export the harness as test support, for example `coretest/crashmatrix.Run(t, Scenario{Build, Prev, Fixtures})`,
  with `Prev` a binary path.
- Keep a core instance over hello.
- Add an ABO instance, `TestBootCrashMatrix_ABO` in the root module (`extraction-gates`), that boots books with
  HEAD fixtures and `PREV_REF`.
- Route books' boot-time non-Pebble writes (the key file, the gap-fill file, the post-reset deletions) through an
  injected `statefs`-compatible FS, so they carry labels.
- Add rows for P4d 1 (books' `Open`: the key-file write), the kernel's `applied` and `hook_done` writes, and the
  `delete`-entry removals.
- Correct row 7's sentence.
- Make 05-storage, 09 and 14 agree on the range ("P0 to Run (3b)"; P−1 is deferred).

### COR9-6: MAJOR. `app.Offline` exposes only module handles, so ABO cannot write or read the staging marker (A-10) or reach core's offline commands

**Where:**
- `app.Offline` has `Storage(id) (module.StorageHandles, error)` and `Close()` only (`06-app-sdk.md:1079-1088`). The
  `app` package has no other offline entry (`:786-1100`).
- The staging marker row (`05-storage.md:1026`): `staging-restore` commits `c/storage/staging` "through
  `kernel.OpenOffline` from S2b"; `staging-scrub` reads it "through a read-only open (§5S.3.10)".
- §5S.3.10's read-only open is `storage.Admin.OpenReadOnly`, kernel-only (`:1356-1363`).
- Core's offline commands: `<app> storage restore-checkpoint` (`:1570`), `<app> users reset-password`
  (`05-authn.md:1230`) and `<app> secrets set-install-id` (`05-config.md:2156`).
- ABO keeps its cobra tree, and "no file of `cmd/` moves into `app`" (`06-app-sdk.md:1130-1131`).

**Evidence:**
- `c/storage/` is a core keyspace. A module's handles refuse it (R8, `ErrOutsideKeyspace`; strict claims from
  S2b r2). So from S2b, A-10's interlock cannot be written by `staging-restore` or checked by `staging-scrub`.
- Neither `kernel` nor `core/internal/restoreswap` is importable by ABO. So the checkpoint restore, which is "a
  failed migration['s] recovery from the first release on core storage", cannot be invoked from ABO's CLI. Neither
  can the reset-password or set-install-id commands. Nothing in `app` exposes them.

**Fix:**
- Add an `app` surface:

  ```go
  func (o *Offline) StagingMarker() (Marker, bool, error)
  func (o *Offline) WriteStagingMarker(sha256 string) error // staged into the close batch
  func ReadStagingMarker(opts Options) (Marker, bool, error) // read-only, no OPTIONS write
  ```

- Add one `app.OfflineCommands(opts, mods...) []app.Command` that returns name, help and `Run(ctx, args)` for
  `storage restore-checkpoint`, `users reset-password` and `secrets set-install-id`. ABO wraps each in cobra.
- Cite it from 05-storage, 05-authn and 05-config, and add `TestCLIExposesCoreOfflineCommands` (ABO).

### COR9-7: MINOR. P−1 is deferred, but four files still run it

**Where:** 05-backup B2 (`05-backup.md:81`, "after the updater's own pre-boot check (P−1)"); the P0a paragraph
(`:711-714`: "P−1 runs first … `CheckReader` ignores a pending intent staged by the failing build"); `:955`; the test
`TestUpdaterRollbackRejectsFailingBuildsIntent` (`:1088`). Also 05-storage's lifecycle block (`05-storage.md:1941`,
"06 P−1 updater.Boot … may restore .prev"), 05-config (`05-config.md:318,919`, "passes the result to P−1"), and the
matrix range "from P−1" (`05-storage.md:1904`, 14 `:89`) against 09's "from P0" (`09-abo-on-core.md:838,1030`).

**Evidence:** P−1 is deferred-build, and "no S0–S10 build runs this phase" (`06-app-sdk.md:1787`). The
`CheckReader` exemption is the deferred `ReaderCheckOptions.IgnoreRestoreStagedBy` (`05-updater.md:267`). As listed,
the 05-backup test cannot be written at S8.

**Fix:** Prefix each sentence with "once the deferred P−1 is built (A-6)". Move the test into 05-updater's
deferred-build table. Use "P0 to Run (3b)" in 05-storage and 14.

### COR9-8: MINOR. The `legacy.TasksWriteLock` contract names a struct as the capability holder and leaves the other three stores open

**Where:** `05-tasks.md:1363-1377` ("an OPTIONAL capability of a legacy `tasks.Stores`"); `tasks.Stores` is a struct
(`:1138-1143`). The build-and-commit path lists only `RunStore` and `ClockStore` writers (`:1783-1789`). 05-storage
row 9 (`05-storage.md:2332`) says the pause and stand-down rows are "written under HEAD's `opsMu`". 06 §6.4.10's S5 row
(`06-app-sdk.md:1468`) omits the capability.

**Evidence:**
- A struct value cannot carry ABO's method. The executor has to assert the capability on `Stores.Runs`, as it does
  for `TasksRecoverer` (`05-tasks.md:1413`).
- `Pause` and the stand-down writes are not on the `WriteRuns` path. If books' pause store refuses writes outside
  a hold (`ErrRunsNotLocked`), `Executor.Pause` fails. If it does not, row 9 is wrong.
- HEAD writes these rows with no `opsMu`: `store.SetSetting` (`internal/scheduler/maintenance.go:71`), and the
  pause gate under its own `g.mu` (`internal/operations/registry/pause_gate.go:93-121`).
- Re-entrance is unspecified for a `WriteRuns` called with a ctx that already holds the lock, and `opsMu` is not
  re-entrant.

**Fix:**
- Say "asserted on `Stores.Runs`".
- State that `Clocks`, `Pause` and `StandDown` share its hold, and that `Pause`/`Unpause` and the stand-down writes
  build and commit inside `WriteRuns`.
- Make `LockRuns` return the incoming ctx unchanged when the ctx already holds the lock.
- Correct row 9's description of HEAD.
- Add `legacy.TasksWriteLock` to 06's S5 row.

### COR9-9: MINOR. 03 contradicts itself on two A-era rules

**Where and evidence:**
1. R4 (`03-architecture.md:561`): `kernel.stageReset` "calls `legacy.ConfigResetArgs` first for a `ResetFactory`".
   §3.3 (`:422-425`), 05-storage and 06 say both kinds, because the plan carries `fresh_rows` for `ResetStore` too.
   Read literally, R4 leaves a `ResetStore` with no factory-default blob, which reopens SEC8-5.
2. R6 (`:563`) and 09 (`09-abo-on-core.md:1313`) list the fields that members of a merged set must share as `Perm`,
   `Guards`, `RateClass`, `OwnAuth`, `BasicExempt` and `LegacyAtom`. 05-http-api rule 4 (`05-http-api.md:794-796`)
   also requires `Public`, `AuthMethod` and `NoCompress`. 03 wins on conflict, so the weaker list governs.

**Fix:** In R4 write "for both reset kinds". Copy rule 4's full field list into R6 and 09.

### COR9-10: MINOR. `legacy.AuthAtomWriter` is missing from the one list of legacy identifiers

**Where:** Defined in 05-authn §3.4 (`05-authn.md:633-640`) and removed at H1 (`:1658`). It is absent from 05-storage's
"one list" (`05-storage.md:1408-1423`), which names `AuthReconciler` and `RBACAtomWriter`. Its absence falls back to
`IssueAPIKey` with canonical scopes, which would put namespaced strings into an `abk_` row. **Fix:** Add a row with
owner 05-authn §3.4, landing S4. Books asserts it at compile time (`var _ legacy.AuthAtomWriter`), as it does for
`TasksRecoverer`.

### COR9-11: MINOR. Config is called "exclusive", yet A-2 makes it routed on ABO

**Where:** 03 §3.3 "Two selection models" and the store table (`03-architecture.md:367-371,476`, Model "exclusive": "a
supplied legacy implementation replaces the native one"). 05-config's routing store (`05-config.md:385-388`) sends
legacy-named keys to books' store and every other key to native `c/config/`. 05-storage's "Native-only data" row
(`05-storage.md:1396`) says a legacy-backed exclusive subsystem does not offer native-only data.

**Evidence:** On ABO `c/config/` rows exist (09 rollback row 2). The model, the "native-only" rule and the `backends`
ledger value (`legacy` for config) all describe something else. **Fix:** Give config its own line in 03's models: an
exclusive store keyed per key by `Alias.Legacy`, with ledger value `legacy+native` while a legacy store is supplied.
Except config in 05-storage's "Native-only data" row.

### COR9-12: MINOR. The reset set-aside destination and the failure rule differ between 05-storage and 06

**Where and evidence:**
- **Destination.** 05-storage (`05-storage.md:1672`) sends "an item on the DB's filesystem" to
  `<db parent>/.mediacore-restore/<id>/previous/`. 06 P0a (`06-app-sdk.md:1788`) sends an auxiliary DB at a
  `LegacyPath`, such as books' Open Library store under `RootDir`, to `<its parent>/.mediacore-restore/…`, even when
  it is on the DB's filesystem. The hook's kept-path rule (4) then skips `openlibrary-dumps` under one rule and not
  the other.
- **Failure.** 05-storage (`:1674`) renames `inflight.json` to `rejected.json`; 06 deletes it.

**Fix:** Use one rule ("every target not under `<db parent>` or the state directory is set aside beside itself") and
the rename, and keep 05-storage as the owner.

### COR9-13: MINOR. `restore/pending.json` has two writers and no shared type

**Where:** ABO's pre-S8 `internal/backup` writes the intent (`05-backup.md:34-37`, step 8 at `:680-688`). Core's
`restoreswap` writes reset intents and reads both at P0a. `storage/restorejournal` defines only `Kind`, `ResetKind`,
`Record` and `SidecarIdentity` (`05-storage.md:1708-1769`).

**Evidence:** The intent fields that P0a's one rejection rule reads (`expires_at`, `staged_by_build`,
`pebble_options`) and the atomic `link(2)` create are implemented twice, once in ABO and once in core, with no common
type. The two copies can drift into a rejected or misread intent. **Fix:** Add `restorejournal.Intent`, and
`restorejournal.CreateIntent(stateDir, Intent) error`, which performs the temp + fsync + `link(2)` +
`ErrSwapPending` sequence. Both writers call it. (The leaf stays stdlib-only; `ErrSwapPending` can move into the leaf,
with `storage` re-exporting it.)

### COR9-14: MINOR. 04 and 05-ai disagree on who emits HEAD's unprefixed AI families, and `Observer.Attempt` cannot reproduce their semantics

**Where:** 04 (`04-package-mapping.md:416`) moves `telemetry.go` and `ai_metrics.go`, the HEAD-named emitters, into
core's `integrations/ai/openai`. 05-ai §8 (`05-ai.md:786-806`) and 05-metrics rule (b) say core records only
`<ns>_ai_*`, while books records `ai.*` and `ai_dispatch.*` from `WithObserver`.

**Evidence:**
- If both are followed, `ai.requests` has two emitters, and the second registration fails with `ErrFamilyTaken` at
  S7.
- If only 05-ai is followed, HEAD counts one request per HTTP attempt, including the SDK's own retries
  (`internal/ai/telemetry.go:256-268`), with `reason` values `quota`, `rate_limit`, `timeout`, `other`,
  `reply_parse` and `count_mismatch` (`:189-201,375-391`).
- `Observer.Attempt` fires once per router attempt with an `integrations.Class`, so the names survive but the counts
  and the `reason` values change without anyone saying so.

**Fix:**
- Strip HEAD's names from the moved files in 04's cell (core emits only `<ns>_ai_*`).
- Add `Observer.Transport func(c Capability, t Target, status int, quota bool, elapsed time.Duration)`, fired by the
  adapter per HTTP attempt, and have books' recorder map it to HEAD's outcome and reason. Alternatively, record the
  semantic change in the S7 release note.
- Extend `TestUnprefixedAIFamiliesRecordedByBooks` with a retried call.

### COR9-15: MINOR. The open-checkpoint offline finish must commit `OpenOffline`'s close batch, which `OpenOffline` refuses

**Where:** `OpenOffline` refuses while `CheckReader` would report `restore_inflight` (`05-storage.md:2105-2106`;
`06-app-sdk.md:1193-1196`). Rule (1) of the checkpoint restore commits "`kernel.OpenOffline`'s close batch … before it
journals `applied_offline`", while its own `inflight.json` exists (`05-storage.md:1570`; `06-app-sdk.md:1866-1869`).

**Fix:** State that the checkpoint tool calls the internal close-batch function with the step-e identities (for
example `kernel.offlineCloseBatch(store, identities)`), not the public open. Or exempt from the refusal a
`KindCheckpoint` journal whose ID the caller names.
