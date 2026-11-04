<!-- file: docs/plans/platform-core/reviews/spec-round-7-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: e15badc1-1447-4e17-bd6a-edbcc99eab6f -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 7: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR7`). Inputs: the revised spec in
`docs/specs/platform-core/` (03 v1.9.1 normative; 06 v1.7.1, 05-storage v1.7.1, 05-config v1.7.1,
05-authn v1.7.1, 05-rbac v1.7.1, 05-tasks v1.7.0, 05-http-api v1.7.0, 05-backup v1.6.0, 05-library v1.6.1,
05-metadata v1.7.1, 04 v1.6.1, 09 v1.6.1, 12 v1.6.0). Binding: R3-1…R3-5 as refined by R4-1…R4-6,
R5-1…R5-7 and R6-1…R6-7 (R6-3: the one `legacy` package outside the API promise). Code anchors are at
`748fd787a`; spec anchors are `file:line` in `docs/specs/platform-core/`.

Method: the round-6 flows were traced again through the current Go blocks and stage tables, now with
the `legacy` package in the path, and every cross-file name on each path was looked up in its owner file:

1. ABO scan → `RunItems` → books' `legacy.Stores.Tasks` (`tasks.Stores` with `StandDown`) and its optional
   `legacy.TaskRecoverer` → outbox relay → realtime projection.
2. Route → `httpapi` stages → `rbac.LegacyAtom`/`LegacyCheck` (recorded from S2, evaluated from S4 r2) →
   `rbac.Require` → `audit`; plus the one new route the revision added, factory reset.
3. `app.New(books, hello)` through P−1 … P8 and Run (1) … (8): the `storage.Duties` handle and its four
   consumers, the seed-step rule, P4c legacy-store construction and capability detection, P4d steps 1–5,
   books' start-hook writers through S4 r1, the served-ledger commit at (3b).
4. `hello` against core's native stores (config S2b, auth/rbac S4, tasks S5), symbol by symbol.

**Verdict.** All five round-6 MAJORs landed and work as written (table below). The `legacy` package is
consistent at the type level: every member listed in 05-storage §5S.3.11's one list exists in its owner
file with that spelling, every legacy suite name agrees across files, `legacy` imports only L0–L4 types,
and `module` (L7) is its only SDK importer; `hello` compiles against the owner files and `TestHello`'s
`Seen == 1` holds on a fresh store under the new seed rule. What breaks now is one **feature path** the
revision added without a stage-consistent mechanism (factory reset, COR7-1) and a set of contract-wording
defects in the new `legacy` mechanism: the prescribed `export_test.go` assertion cannot compile as an
in-package test (COR7-2), two config capabilities cannot follow the "owner calls through an unexported
mirror" rule they are stated under (COR7-3), and P2 cannot decide a routed store's backend before P4c
detects `Covering` (COR7-4).

## Regression check (round-6 BLOCKER and MAJOR, correctness lens)

| ID | Sev | Status in round 7 | Where |
|---|---|---|---|
| COR6-1 | MAJOR | **Fixed.** `LegacyAtom`/`LegacyCheck` are S2 record-only leaves (`05-rbac.md:80-91,143-180`; `06-app-sdk.md:1323-1331`; `04-package-mapping.md:845`; `05-http-api.md:145-152`); the S2 manifest records them (`05-http-api.md:997`, `:1037`); `Handle` refuses them; `Freeze` rejects an unaliased atom from S4; 09 §9.2.6 names the one value (`09-abo-on-core.md:550`); `TestS2Manifest_SPermRoutesCarryLegacyAtom` (`05-http-api.md:1258`). | — |
| COR6-2 | MAJOR | **Fixed.** `storage.Duty`/`Owed`/`Duties`/`GenDuty` (`05-storage.md:1017-1050`), store-wide keys `c/storage/owed/<duty>` and `owed/gen/<owner>/<index>` (`:1008`; `06-app-sdk.md:1541`), decided from the `core` record; `auth.RestoreDeps.Duties` and `Owed.Detected` (`05-authn.md:400-432,797-806`); journal (`05-backup.md:777,779`), usage (`06-app-sdk.md:1541` step 5; `09-abo-on-core.md:125`) and derived loader (`05-storage.md:1268-1275`) consume it. Only a review-history line still says `served/<owner>/owed` (`05-storage.md:2340`), which is history. | — |
| COR6-3 | MAJOR | **Fixed.** Books' start hook keeps HEAD's `seedRolesAndTokens` through S4 r1 (`06-app-sdk.md:1568-1576,1587-1593`; `05-authn.md:1354-1366`; `09-abo-on-core.md:938`); `seed.go`/`bootstrap.go` move in the r2 PR (`04-package-mapping.md:849`); `TestDarkReleaseWritesNoIdentityRow` narrowed and `TestDarkRelease_HEADSeedWritersKeepRunning` added (`05-authn.md:1569-1570`). Residue: 04's move table still dates `seed.go` "S4" with no release (COR7-11). | COR7-11 |
| COR6-4 | MAJOR | **Fixed in mechanism.** Fresh install stamps only the steps below each owner's lowest `Seed` step (`05-storage.md:683-691,755,1632-1637`; `06-app-sdk.md:1541` P4a); hello's seed reads first (`06-app-sdk.md:2160-2183`). Residue: the test table row still describes the round-5 test (COR7-13). | COR7-13 |
| COR6-5 | MAJOR | **Fixed.** "`Open` writes nothing" is stated once (`05-config.md:1268-1291`): reshaping `migrate*Blob` are pure read transforms, the three marker one-shots become books versioned `Migration`s writing HEAD's markers, the corrupt-secret repair does not run on a core build; 05-storage row 7 and §5S.9.3 and 09 S2b follow (`05-storage.md:1888,1990`; `09-abo-on-core.md:900`). Checked against HEAD: `RunMigrations` treats a stored version above its last step as "up to date" (`internal/database/migrations.go:512-535` @748fd787a), so the new books steps 67+ do not break a rollback to S2 or earlier. | — |

Round-6 MINORs: COR6-6 (`06-app-sdk.md:1341`), COR6-7 (`:1404-1408`), COR6-8 (`:1339`, P3), COR6-9
(`03-architecture.md:306,513`), COR6-10 (`03-architecture.md:726,741`), COR6-11 (`03-architecture.md:514`)
and COR6-12 (`09-abo-on-core.md:900`) are fixed.

## Flow traces (where each one breaks now)

| Flow | Breaks at |
|---|---|
| 1 scan → UI | Composes. `legacy.Stores.Tasks func(legacy.Handles) (tasks.Stores, error)` (`06-app-sdk.md:1424`) returns the four-member `tasks.Stores` (`05-tasks.md:1105-1110`); `legacy.TaskRecoverer` is detected at P4c and run inside `Executor.Run`; every run write stages `GetForUpdate` (R6-1, `05-tasks.md:1152-1170,1690-1705`). 03's one-list table omits the fourth member (COR7-6); 05-tasks puts the mirror's `export_test.go` "in tasks" although the consumer is `internal/executor` (COR7-2). |
| 2 route → audit | S2–S4 r1 legacy routes: consistent. Factory reset: from S2b books' handler must call kernel-only `storage.Admin`, or the legacy path must be an alias of a core route that cannot exist or be authorised before S4 r2, and that route has no owning package or stage (COR7-1). |
| 3 boot | P2's backend comparison for a routed store needs a fact only P4c knows (COR7-4). P4c's "each constructor called once per boot" contradicts P4d step 3's second construction over a read-only pre-swap store, for which no handle type exists (COR7-8). P0a has no precedence between a staged restore and a staged factory reset, and no refusal for a reset journal in flight (COR7-9). Storage's P2 step 2 reads restoreswap's records, whose type lives in L4 `backup/journal` (COR7-10). The extension sub-keyspace name the kernel builds into `legacy.Handles.Ext` is not normative (COR7-7). Duties, seed rule, P4d order and the S4 r1 start hook compose. |
| 4 hello | **Compiles.** Checked: `config.Define(Path, T, ...SectionOption)`, `config.Docs(map[Path]string)`, `Handle[T].Get() *T` (`05-config.md:97,150-157,252-255`); `httpapi.Handle[Req, Resp]`, `Route{Method, Path, Name, Perm, Summary}`, `In[Req].Req`, `Invalid(...FieldError) *Error`, `Field(pointer, code, message)` (`05-http-api.md:181-200,238,303-308,400-401`); `rbac.CoreAdmin`, `PermissionSpec.Class`, `ClassView`, `RoleViewer` (`05-rbac.md:121,191,205,255`); `tasks.Task`, `Common` (required `Title`, `Effects`, `Perms`, `Resume`; `Timeout` 0 = shape default), `Res`, `ReadOnly`, `ResumeFromZero`, `CPU`, `RunItems`, `ItemsOptions.Label`, `ItemRC.Count`, `ItemsResult.Counters` (`05-tasks.md:134-166,212,263,303-343,362,458,470,564`); `storage.Spec{Keyspace, Migrations}`, `Migration{Seed, Up}`, `MigrationTx.Keyspace/Set`, `Keyspace.Sub/Get/Scan`, `KV.Clone`, `ScanOptions`, `ErrNotFound` (`05-storage.md:169-238,559-565,676-725`); `module.Deps.Log`, `StorageHandles.Keyspace` (`06-app-sdk.md:664-726`); `coretest.New/WithModules/WithUser/AssertPermission/As/GetJSON/RunTask/RunResult` (`06-app-sdk.md:1196-1250`); `ui.Bundle`, `ui.MustFromFS` (`07-ui-shell.md:695,712`). Every import is used. hello imports no `legacy` identifier and leaves `Manifest.LegacyStores` zero, so it runs on the native stores. |

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 0 | — |
| MAJOR | 1 | COR7-1 |
| MINOR | 12 | COR7-2 … COR7-13 |

## Findings

### COR7-1: MAJOR. Factory reset has no working path from S2b to S4 r2, and its core route has no owner or stage

**Where:** 05-config §1.12.2 "A core build's factory reset" (`05-config.md:1454-1469`: "Books' handler calls it
from the S2b PR on"); 09 S2b Data/keys (`09-abo-on-core.md:900`: "books' system handler calls storage's staged
reset (05-storage `Admin.StageFactoryReset`) … from this stage's PR, and `PebbleStore.Reset` … is deleted");
05-storage §5S.3.15 Stage row (`05-storage.md:1573`: exposed as `POST /api/core/v1/system/factory-reset`,
`core:admin` + `Guards.Interactive` + `Guards.Owner`, "from S2b ABO's legacy route is mapped onto it") and
§5S.9.3 (`:1989`); 05-rbac §6.2 (`05-rbac.md:986`: the legacy path is "from S2b an alias of" the core route)
and §4.10 (`:836`).

**Evidence:**
- `storage.Admin` is kernel-only (`05-storage.md:660`: "The census and backup use `storage.Admin`
  (kernel-only)"; its surface is open item 5, `:2132-2134`). No handle a module receives carries it:
  `module.StorageHandles` is `Keyspace`, `Legacy`, `Indexes`, `Aux`, `Derived` (`06-app-sdk.md:719-725`), and
  `legacy.Handles` is `Owner`, `Legacy`, `Ext` (`06-app-sdk.md:1381-1385`). So "books' handler calls it" has no
  call path. The same gap already sits under 05-backup's S2b–S7 staging, where ABO's `internal/backup` "takes
  the build's upgrade floors … from the kernel (`storage.Admin.UpgradeFloors()`)" (`05-backup.md:29-31`).
- The alternative, an alias of a core route, cannot hold either before S4 r2. "No alias exists before S4"
  (`05-http-api.md:159`); at S4 r1 "no native route exists" and the mount is still `LegacyAuthGin`
  (`05-http-api.md:999`); `core:admin`, `Guards.Owner` (05-authn §4.8) and stage 13 need the S4 r2 auth and
  rbac services. Through S2b–S4 r1 the core route either does not exist or answers 401 to everyone.
- No file gives the route a home or a stage. 03's `internal/<capability>http` list has no storage or system
  package (`03-architecture.md:316`); `internal/backuphttp` lands with `backup` at S8; 04 §8.1 and 05-http-api
  never mention the route.
- HEAD's `FactoryReset` also deletes the Open Library store and dump files after `store.Reset()`
  (`internal/server/handlers/system/handler.go:538-560`, `h.deleteOLDataLocked()` @748fd787a). From S2b that
  store is an auxiliary DB under `<db parent>/aux/<ID>/<name>` (05-storage §5S.3.10; 09 S2b gate), and the P0a
  set-aside list (`05-storage.md:1574`) moves the DB directory, sidecars, derived and index directories,
  checkpoints and the boot record, but no auxiliary DB.
- Net: the S2b PR deletes `PebbleStore.Reset` and leaves the admin's factory reset either uncompilable (direct
  call) or permanently refused (alias) for at least three stages. HEAD's admin test pins the route
  (`internal/server/admin_routes_test.go:47` @748fd787a).

**Fix:**
1. Add a narrow kernel-built handle for pre-core maintenance code, in `legacy` (it exists only for an app's
   legacy backup and reset paths until `backup` lands): `type legacy.Maintenance interface { UpgradeFloors()
   []storage.UpgradeFloor; StageFactoryReset(ctx context.Context, requestedBy string) (string, error) }`, handed
   to the module that declares legacy claims as `module.StorageHandles.Maintenance` from S2b (06 §6.4.4,
   §6.4.10 S2b row; 05-storage §5S.3.11 list; 05-backup Stages). State in 05-config §1.12.2 and 09 S2b that
   books' gin handler calls it under HEAD's own admin and owner guards (gin's chain decides until S4 r2).
2. Give the core route an owner and a stage: a new `internal/storagehttp` (L6, imported only by `kernel`) at
   S4 r2, or `internal/healthhttp` if the owner prefers; add it to 03 §3.3's list and 04 §8.1 S4. Change
   05-storage §5S.3.15 and 05-rbac §6.2 from "from S2b an alias" to "from S4 r2 an alias; before that the gin
   route calls `Maintenance.StageFactoryReset`". Delete `Deps.Storage.Maintenance` in the S8 PR, when
   `backup` and the route own both callers.
3. Add the module's auxiliary DBs (`<db parent>/aux/<ID>/`) to the P0a set-aside list, or state that books'
   handler keeps calling `deleteOLDataLocked` before staging; add a case to `TestFactoryResetUnderClaims`.
4. Done-check at S2b: `TestFactoryResetFromLegacyRoute_S2b` (gin admin + owner, intent written, next boot
   fresh, OL store gone or moved).

### COR7-2: MINOR. The prescribed in-package `export_test.go` assertion against `legacy` is an import cycle

**Where:** 03 §3.3 "Legacy-only identifiers" (`03-architecture.md:371-373`: "an `export_test.go` assertion in
the owner checks, in both directions"); 06 §6.4.11 Detection (`06-app-sdk.md:1453-1458`); 05-storage §5S.3.11
(`05-storage.md:1331`); 05-config (`:486-496`, `:1798-1803`); 05-tasks (`05-tasks.md:1297-1300`: "an
export_test.go assertion **in tasks**"); 05-library (`05-library.md:258-262`); 05-metadata (`:76`, `:401`,
`:603`, `:1058`, `:1275`); 05-notify (`:194`, `:331`). Against: 05-authn (`05-authn.md:661`) and 05-rbac
(`05-rbac.md:471`), which say "from auth's / rbac's **external** test package".

**Evidence:** `export_test.go` is by convention `package <owner>` (it compiles only with the owner's own
tests). An in-package test file of `auth` that imports `legacy`, which imports `auth`, fails with "import cycle
not allowed in test". The assertion can only live in an external `<owner>_test` file, and that file cannot see
the unexported mirror unless `export_test.go` exports an alias of it. Two files already say this; nine say the
opposite. 05-tasks also names the wrong package: the mirror's consumer is `Executor.Run` in
`core/internal/executor` (`06-app-sdk.md:1541` P4c; 04 §8.1 S5), not `tasks`.

**Fix:** One sentence in 03 §3.3, repeated by reference everywhere else: "the owner's `export_test.go`
(`package <owner>`) only exports a test alias of the unexported mirror (`type SignInsMirror = signIns`); the
two-way assignability assertion lives in an external test file of the owner (`package <owner>_test`), which may
import `legacy`." Change 05-tasks' "in tasks" to "in `internal/executor`", and align 06, 05-storage, 05-config,
05-library, 05-metadata and 05-notify with 05-authn's wording.

### COR7-3: MINOR. `legacy.ConfigOpener` and `legacy.ConfigSecrets` cannot follow the mirror rule they are stated under

**Where:** 05-storage §5S.3.11 (`05-storage.md:1331`: "so a capability's signatures name only types of its
owner's layer or lower"); 06 §6.4.11 Detection (`06-app-sdk.md:1453-1457`: "the owner calls the capability
through an unexported interface with the identical method set"); 06 §6.4.3 `Health` comment
(`06-app-sdk.md:575-578`: "whose Open (legacy.ConfigOpener) **core config** calls first in P4d step 1"); 06
§6.4.11 (`:1453`: "the kernel detects each capability at P4c") against S2, where P4 is skipped (`:1552-1553`).
Against 05-config (`05-config.md:486-496`).

**Evidence:** `ConfigOpener.Open(ctx, env ConfigOpenEnv)` names `legacy.ConfigOpenEnv`, and
`ConfigSecrets.Secrets() SecretsStore` returns `legacy.SecretsStore` (`05-config.md:508-535`). `config` (L3)
cannot name either type, so it cannot hold a mirror with "the identical method set". 05-config resolves this
correctly (the kernel calls `Open` and passes `Secrets()` to `secrets.Open`), but 05-storage's rule and 06's
Detection bullet state the opposite for every capability, and 06's `Health` comment says config calls `Open`.
At S2 `ConfigExternalWriter` is detected by config's own mirror at P3, not by the kernel at P4c.

**Fix:** In 03 §3.3 and 05-storage §5S.3.11 add: "A capability whose signature names a `legacy` type
(`ConfigOpener`, `ConfigSecrets`) is driven by the kernel, never by its owner; every other capability is
called by its owner through a mirror." In 06 §6.4.11 Detection, name that exception and say that at S2 config
detects `ConfigExternalWriter` itself when it attaches `Options.LegacyConfigStore` at P3. In 06 §6.4.3 replace
"core config calls" by "the kernel calls".

### COR7-4: MINOR. P2 cannot decide a routed store's `legacy` vs `legacy+native` backend: `Covering` is detected only at P4c

**Where:** 06 P2 (`06-app-sdk.md:1539`: the build's choice, "`legacy+native` or native for a routed store", is
compared at P2); 05-storage §5S.4 step 4 (`05-storage.md:1615-1619`) and "Adapter selection"
(`:1330`: "a legacy half without it covers every record … the ledger then records `legacy`", SIMP6-5); P4c
detection (`06-app-sdk.md:1541` (c)); `legacy.Stores` constructors need `legacy.Handles`, which storage issues at
step 10 (`05-storage.md:1655-1657`).

**Evidence:** After SIMP6-5 the ledger value of a routed store depends on whether the built legacy half
implements `legacy.Covering`, which only a type assertion on the constructed value at P4c can tell. At P2 the
kernel knows only whether a constructor is set. So the P2 check either cannot distinguish `legacy` from
`legacy+native` or must call constructors before storage has issued handles.

**Fix:** Split the check: P2 compares "legacy supplied or not" per subsystem (that alone catches
`ErrLegacyStoreMissing`); P4c, after detection, compares the full value (`legacy` / `legacy+native` / `native`)
and fails boot before P4d on a difference. State in 05-storage "Adapter selection" that adding or removing
`Covering` on a recorded routed store is a backend switch (allowed only by a cutover). Add the case to
`TestLegacyStores`.

### COR7-5: MINOR. 12 deletes a family's `legacy` identifiers in the cutover release; 06 and 05-storage keep them through it

**Where:** 12 §12.4.2 (`12-repo-split.md:363-367`: "a family's legacy-only identifiers are deleted in the release
that ships that family's cutover"); against 06 §6.4.11 "Landing and lifetime" (`06-app-sdk.md:1471-1475`: "A
family's cutover build still sets its member, because it reads the legacy data through it. A later build stops
setting it"), 06 §6.11.6 (`:2009-2014`) and 05-storage §5S.3.13 "After it" (`05-storage.md:1508`).

**Evidence:** A cutover copies the family out of the legacy rows through the legacy store; if the member is
deleted in the cutover release, that build cannot construct the store it needs.

**Fix:** In 12 §12.4.2 write "deleted in the first release after the family's cutover that no longer sets the
member (behind the cutover's `UpgradeFloor`, 05-storage §5S.3.13), a minor".

### COR7-6: MINOR. 03's one list of store interfaces omits the fourth `tasks.Stores` member (stand-down markers)

**Where:** 03 §3.3 store table `tasks` row (`03-architecture.md:416`: "runs, queues, checkpoints, schedule clocks,
the pause marker"), §3.3 `tasks` row (`:281`) and §3.7 family list (`:694`); against 05-tasks
(`05-tasks.md:1105-1110,1238-1256`: `StandDown StandDownStore`), 06 (`06-app-sdk.md:1424`: "scan stand-down
marker"), 05-storage row 9 (`05-storage.md:1890`: `setting:registry.scan_standdown`), 09 (`09-abo-on-core.md:956`)
and 04 (`04-package-mapping.md:788-789,1328`).

**Fix:** Add "the stand-down markers (`StandDownStore`)" to the three places in 03, and "HEAD's
`registry.scan_standdown` setting row" to the ABO-implementation cell (03 §3.3 is "the one list", SIMP5-8).

### COR7-7: MINOR. The extension sub-keyspace segment `<store>` is a stored format with no normative mapping

**Where:** 06 §6.4.11 `Handles.Ext` (`06-app-sdk.md:1384`: `m/<owner>/ext/<store>/`); 05-storage §5S.3.11
(`05-storage.md:1328`, `:1394-1397`). Segments named anywhere: `config`, `integrations`, `logs` only
(`05-config`, `05-metadata`, `05-logging.md:397`).

**Evidence:** The kernel builds `Handles.Ext` for each `legacy.Stores` field at P4c, and the extension records
under it (hashed sessions, native scopes, suspension state) persist. Nothing says whether `AIJobs` maps to
`aijobs` or `ai`, `NotifyRules` to `notifyrules` or `notify`, `Roles` to `roles` or `rbac`. Two builds that
derive it differently, or a later rename, orphan every extension record of that store; a suspended role's
record would read as absent (`ExtOrphan` is ignored and swept), which drops its suspension.

**Fix:** Add a table to 05-storage §5S.3.11 mapping each `legacy.Stores` field to its fixed segment (`Config`
`config`, `Auth` `auth`, `Roles` `roles`, `Tasks` `tasks`, `Logs` `logs`, `Roots` `roots`, `AIJobs` `aijobs`,
`Gates` `gates`, `Integrations` `integrations`, `NotifyRules` `notifyrules`), state that a segment never changes
before that family's cutover, and pin it with a golden (`TestLegacyHandlesExtSegmentsGolden`).

### COR7-8: MINOR. A restore boot calls the auth and role constructors a second time, over a store for which no handle type exists

**Where:** 06 §6.4.11 `Stores` comment (`06-app-sdk.md:1397-1399`: "Each constructor is called once per boot at
P4c"); 06 P4d step 3 (`06-app-sdk.md:1541`: "the kernel also builds, with the same constructor as the live store
… a read-only `auth.Store` and a read-only `rbac.Store` over `rc.Previous`"); 05-backup `RestoreContext.Previous
func(owner storage.Owner) (storage.Reader, error)` (`05-backup.md:451-475`); 05-storage open item 5
(`05-storage.md:2132-2134`: "read-only open of a directory for 05-backup's restore hooks").

**Evidence:** A `legacy.Stores` constructor takes `legacy.Handles{Owner, Legacy storage.LegacyRange, Ext
storage.Keyspace}`. Over the pre-swap store the kernel holds only a `storage.Reader`, and no storage API issues
a `LegacyRange` or `Keyspace` over a second, read-only DB. The second call also contradicts "once per boot".

**Fix:** In 05-storage §5S.3.10 or §5S.3.11 add `storage.Admin.OpenReadOnly(dir) (ReadOnlyDB, error)` whose
`Legacy(owner)` and `Keyspace(prefix)` return handles that fail every write with `ErrReadOnly`; have the kernel
build the second `legacy.Handles` from it; change 06's comment to "once per boot at P4c, and on a staged restore
once more over the read-only pre-swap store (P4d step 3)". Close open item 5's read-only half.

### COR7-9: MINOR. P0a has no precedence between a staged restore and a staged factory reset, and nothing refuses while a reset journal is in flight

**Where:** 06 P0a (`06-app-sdk.md:1536`); 05-storage §5S.3.15 (`05-storage.md:1574-1577`: "A crash during the
renames resumes from the reset journal at the next boot"); 05-backup (`05-backup.md:681`).

**Evidence:** `restore/pending.json` and `reset/pending.json` can both exist (an admin stages a restore, then a
factory reset, before restarting); P0a states both procedures and no order. DATA3-6's rule that `CheckReader`,
`make rollback` and `deploy` refuse while a restore journal exists is not extended to `reset/<id>/journal.json`,
so a rollback taken mid-reset lands an older build on a half-moved store directory.

**Fix:** In 06 P0a and 05-storage §5S.3.15: a staged factory reset supersedes a staged restore (the restore
intent is rejected and logged), and staging either refuses while the other is pending; extend the
`CheckReader`/`make rollback`/`deploy`/updater refusal to an in-flight reset journal. Add
`TestRestoreAndResetBothStaged`.

### COR7-10: MINOR. `core/internal/restoreswap` and storage's P2 read a record type that lives in L4 `backup/journal`

**Where:** 05-backup Stages (`05-backup.md:26-29`: restoreswap "behind `storage.Admin`"); `journal.JournalRecord`
with R6-4's `Sidecar`/`Options` fields (`05-backup.md:490-500`, package `journal`, L4, which imports `storage`);
05-storage §5S.4 step 2 (`05-storage.md:1605-1612`: storage's open reads "the sidecar identity and the OPTIONS
number restoreswap recorded at step e"); 05-storage §5S.3.15 (`:1569`: factory reset is "core code from S2b, in
`core/internal/restoreswap`"); 03 R4 bridge rule (`03-architecture.md:513`).

**Evidence:** No file states restoreswap's layer. If `storage.Admin` (L1) delegates to restoreswap and
restoreswap uses `journal.JournalRecord`, the edges are `storage` → `internal/restoreswap` → `backup/journal` →
`storage`, an import cycle; if storage's open parses the restore journal itself, `storage` reaches an L4 type.

**Fix:** Move the restore-journal file types (`JournalRecord` and its step constants) to `storage` (or an L0 leaf
`storage/restorejournal`) and let `backup/journal` re-export them as aliases; state in 03 R4 that
`internal/restoreswap` is L1 and imports only storage-level packages; state in 06 P0a/P2 that the kernel passes
the recorded identity to storage's open (`kernel.Open(storage, restoreIdentity)`), so storage never reads the
journal file.

### COR7-11: MINOR. 04's move table dates `seed.go` "S4" with no release, while the stage plan moves it in the r2 PR

**Where:** 04 §4 move table (`04-package-mapping.md:1071`: "S4 | `internal/auth`: `context.go`, `seed.go`,
`user_active.go`"); against 04 §8.1 S4 (`:849`), 09 S4 Releases (`09-abo-on-core.md:938`) and 05-authn §5
(`05-authn.md:1361-1363`): `seed.go` and `bootstrap.go` move "with that r2 PR, so r1 still has HEAD's code to
call" (COR6-3).

**Evidence:** The stage-visibility gate evaluates stage points from the mapping input (03 §3.5.1 item 4). A row
dated "S4" makes the r1 PR responsible for the move that COR6-3 put in r2, which would remove the code the r1
start hook calls.

**Fix:** Date the row "S4 r2", add `internal/server/bootstrap.go` with the same date, and state in 03 §3.5.1
item 4 that S4 r1 and S4 r2 are separate stage points of the mapping input.

### COR7-12: MINOR. 04's S3 row omits `legacy`'s S3 members

**Where:** 04 §8.1 S3 row (`04-package-mapping.md:848`) and review history (`:1774`: "created at S2, grows at
S2b, S4, S5 and S7"); against 06 §6.4.10 S3 row (`06-app-sdk.md:1340`) and 05-storage's list
(`05-storage.md:1348`: `legacy.ConfigSecrets`, `legacy.ConfigCommitObserver`, `legacy.SecretsStore` land at S3).

**Fix:** Add "`legacy` grows (`ConfigSecrets`, `ConfigCommitObserver`, `SecretsStore`; `ConfigExternalWriter`
deleted)" to 04 §8.1's S3 row and S3 to the history line.

### COR7-13: MINOR. COR6-4 residue: the seed test's row still describes the round-5 test

**Where:** 05-storage §5S.10 (`05-storage.md:2047`: `TestFreshInstallRunsSeedSteps` "a fresh install runs `Seed`
steps in P4b … `Seed` without `Additive` fails registration (COR5-8)"); against the runner-rule row
(`05-storage.md:755`), which adds "an owner whose step 2 reshapes step 1's seeded row ends a fresh install with
step-2-shaped rows and `version` 2" (COR6-4).

**Fix:** Copy the reshape case and "COR6-4" into the test-table row, which is what the plan's task list is
generated from.
