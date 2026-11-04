<!-- file: docs/plans/platform-core/reviews/spec-round-6-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 122c3554-ef86-4681-81e7-ddaf71055099 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 6: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR6`). Inputs: the revised spec in
`docs/specs/platform-core/` (03 v1.8.1 normative; 06 v1.6.1, 05-storage v1.6.1, 05-config v1.6.0,
05-tasks v1.6.1, 05-authn v1.6.1, 05-rbac v1.6.1, 05-http-api v1.6.0, 05-backup v1.5.1, 05-realtime v1.5.0,
05-events v1.6.0, 05-health v1.6.1, 09 v1.5.1, 04 v1.5.1, 10 v1.5.0). Binding: R3-1…R3-5 as refined by
R4-1…R4-6 and R5-1…R5-7. Code anchors are at `748fd787a`; spec anchors are `file:line` in
`docs/specs/platform-core/`.

Method: the four flows of round 5 were traced again through the current Go blocks and stage tables, with
every cross-file name on each path looked up in its owning file:

1. ABO scan → `RunItems` → books' legacy `tasks.Stores` (and its optional `tasks.Recoverer`) → outbox →
   realtime projection → UI.
2. Route → `httpapi` stages 0–13 → `rbac` (`LegacyAtom`/`LegacyCheck`, auth-off) → `audit`, per stage
   S2 → S4 r2.
3. `app.New(books, hello)` through P−1 … P8 and Run (1) … (8) of 06 §6.5.1/§6.5.2, per stage, including
   the served-ledger `owed` entries, the books start-hook repairs and HEAD's Start-time writers.
4. `hello` against core's native stores (config S2b, auth/rbac S4, tasks/journal S5), symbol by symbol,
   plus what its test asserts on a fresh store.

**Verdict.** Every round-5 BLOCKER and MAJOR of this lens landed in substance; the store-interface and
optional-capability model is consistent at the type level (every interface and capability named in
05-storage §5S.3.11 "Legacy capabilities" exists in its owner file), and `hello` compiles against the owner
files as written. What breaks now is again **stage timing and mechanism**: the `LegacyAtom` requirement
that round 5 put on every legacy `s.perm` route lands two stages after the manifest that must carry it
(COR6-1); the `owed` duty ledger has no API through which its consumers can read or clear it (COR6-2);
HEAD's Start-time identity writers have no home in the dark S4 r1 release (COR6-3); the seed-step rule
runs a seed after the later steps that reshape its rows were stamped (COR6-4); and HEAD's settings load,
which the legacy `Open` runs, writes, while the contract says it never does (COR6-5).

## Regression check (round-5 BLOCKER and MAJOR, correctness lens)

| ID | Sev | Status in round 6 | Where |
|---|---|---|---|
| COR5-1 | BLOCKER | **Fixed**: row 7 `CheckedWrites` from S3, row 19 `import_path:` from S7 (`05-storage.md:1727,1740`); `Claim.CheckedWrites` comment staged (`:476-484`); 09 §9.2.3 sentence amended (`09-abo-on-core.md:372-377`); `TestS2bBooksWritesEveryFamily` includes a settings save and a root update (`05-storage.md:568-571`; `09-abo-on-core.md:833`). Residue: row 7 and 09 S2b still list the corrupt-secret repair as an S2b writer inside the legacy `Open`, which 05-config removed (COR6-5). | COR6-5 |
| COR5-2 | MAJOR | **Fixed**: one order in 06 P4d step 1 (i)–(iv) (`06-app-sdk.md:1423`), 05-storage step 11 (a) (`05-storage.md:1507-1513`), 05-authn §5 step 2 (`05-authn.md:1221-1225`), 05-config §1.4 step 5 (`05-config.md:776-786`); 06 §6.4.3 comment rewritten (`06-app-sdk.md:615-626`); `TestP4dStep1_LegacyOpenBeforeSecrets`. | — |
| COR5-3 | MAJOR | **Half-fixed**: `last` is replaced only at Run (3b) and owed duties persist in `c/storage/served/<owner>/owed` (`05-storage.md:978,1497-1498,1542-1547`; `06-app-sdk.md:1483-1492`). But no consumer has a way to read or delete an `owed` entry: `auth.BootWrites(ctx, RestoreDeps)` takes none, and the consumers hold no handle on `c/storage/`. | COR6-2 |
| COR5-4 | MAJOR | **Fixed from S4**: `rbac.LegacyAtom`/`LegacyCheck` (`05-rbac.md:136-166`), stage 13 and the bridge use them (`05-http-api.md:634,1020,1088-1089`). New defect: the constructor lands at S4 while the S2 manifest must already carry it. | COR6-1 |
| COR5-5 | MAJOR | **Fixed**: `app.Options.LegacyPermissionAliases` (`06-app-sdk.md:970-979`), `Manifest.LegacyRoleIDs` (`:242-248`), `ProblemLegacyRole`, no `core/rbac/legacy_abo.go` (`05-rbac.md:765-778`). | — |
| COR5-6 | MAJOR | **Fixed**: the kernel registers `journal.StorageSpec()` and declares `core.backup.dir` from S4 (`05-backup.md:36-45`; `06-app-sdk.md:1339`); `TestRevocationStagesJournalHeadAtS4`. Residue: 06 lists one `Serialized` key, 05-backup two. | COR6-6 |
| COR5-7 | MAJOR | **Fixed** for the two named writers (`06-app-sdk.md:1373-1382,1450-1455`; `05-storage.md:1821-1822`; `09-abo-on-core.md:361,829`). The same class of gap remains for HEAD's Start-time writers at S4 r1. | COR6-3 |
| COR5-8 | MAJOR | **Fixed in mechanism**: `Migration.Seed` (`05-storage.md:658-663,727`), fresh-install stamp skips seeds (`:1480-1486`; `06-app-sdk.md:1423`), hello's step is `Seed: true` (`06-app-sdk.md:1984`), 10 §10.5 seeds profiles at acquire start (`10-tv-movies-proof.md:707-716`). The rule itself is unsound once a later step reshapes seeded rows. | COR6-4 |

Round-5 MINORs: COR5-9, 10, 11, 13, 15, 16 and 17 are fixed. COR5-12 is fixed in 05-storage but 06 still
states two exceptions (COR6-7). COR5-14 is fixed except for `LegacyOpener`'s landing stage (COR6-8).

## Flow traces (where each one breaks now)

| Flow | Breaks at |
|---|---|
| 1 scan → UI | Composes: `RunStore` stages into the executor's batch by participant binding, `Recover` runs inside `Executor.Run` from S5 and in books' start hook before that (`05-tasks.md:1252-1260`), the S5 bridge subscribes before `Bus.Open` delivers (`06-app-sdk.md:1492`), projection A's topics agree across 05-realtime, 05-health and 09 S6. Settings read by the scanner: a stored `embed_cover_art=false` is dropped on every boot of a store without HEAD's one-shot marker (COR6-5). |
| 2 route → audit | S2: every `s.perm(atom)` manifest entry must carry `rbac.LegacyAtom(atom)`, which does not exist until S4 (COR6-1). S4 r2 on: stage 13 → `LegacyAtom` (roles via `LegacyReach`, pre-core keys by intersection, auth-off by expansion) → `rbac.Require` audits; consistent across 05-rbac, 05-http-api, 05-tasks, 09. |
| 3 boot | P4b stages `owed`; P4d steps 2 and 4, step 5 and Run (3a) must read and clear it, with no API (COR6-2). S4 r1: no home for role seeding, `_system`, the API-key expiry stamp and the bootstrap mint (COR6-3). P4a/P4b: seed steps (COR6-4). P4d step 1: the legacy `Open` writes through HEAD's load (COR6-5). |
| 4 hello | Compiles: `config.Define/Docs/Path/Section/Handle`, `module.ID/Manifest/UIDescriptor/Settings/Setting/SettingsSection/Registrar`, `rbac.Permission/PermissionSpec/ClassView/RoleViewer/RoleName/Need/CoreAdmin`, `storage.Spec/Migration{Seed}/Additive/MigrationTx.Set/Keyspace.Sub/Get/Scan/KV.Clone/ErrNotFound/ScanOptions`, `httpapi.Group/Handle/Route/In/Invalid/Field`, `tasks.Kind/Task/TaskSpec/Common/ReadOnly/Res/Permissions/ResumeFromZero/RC.Context/RunItems/CPU/ItemRC.Count/ItemsOptions/ItemsResult.Counters`, `coretest.New/WithModules/WithUser/AssertPermission/As/GetJSON/RunTask/RunResult`, `ui.Bundle/MustFromFS`, `app.Options/New/Run` all resolve. `TestHello`'s `Seen == 1` now holds on a fresh store. Two contract nits remain: hello's seed overwrites an existing row (COR6-4) and its `main` calls `signal.NotifyContext` under a `TestNoGlobals` that scans `core/examples/...` (COR6-11). |

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 0 | — |
| MAJOR | 5 | COR6-1 … COR6-5 |
| MINOR | 7 | COR6-6 … COR6-12 |

## Findings

### COR6-1: MAJOR. `rbac.LegacyAtom` lands at S4, but every `s.perm` entry of the S2 legacy manifest must already carry it

**Where:** 05-http-api §8.2 `LegacyRoute.Perm` (`05-http-api.md:1020`: "s.perm atom: rbac.LegacyAtom(atom)");
§8.1 S2 row (`:980`: the manifest exists from S2 and only the 36 permission-less routes get
`AnyAuthenticated`); 03 R6 (`03-architecture.md:457`: every legacy route names its permission,
`archtest.RoutesHavePermissions` permanent); 05-rbac "Landing" (`05-rbac.md:79-86`: "the legacy
requirements `LegacyAtom`/`LegacyCheck` (§3.1; they need the catalog) … land at S4"); 06 §6.4.10
(`06-app-sdk.md:1326-1329`: the S2 rbac leaf list has no `LegacyAtom`); 09 S2 HTTP row
(`09-abo-on-core.md:815`); 09 §9.2.6 (`09-abo-on-core.md:518`: "`LegacyRoute.Atom`, or `rbac.LegacyAtom(atom)`
— 05-http-api decides").

**Evidence:**
- From S2 `books` calls `MountLegacy` with the whole gin engine and a manifest in which every route has a
  `Perm`; R6 and `RoutesHavePermissions` hold from S2. Round 5 made the `s.perm` entries' `Perm` the
  value `rbac.LegacyAtom(atom)`, and nothing re-annotates them at S4 (the S4 r2 PR re-annotates only
  `/metrics`, `05-http-api.md:982`).
- `LegacyAtom` is not an S2 leaf. The other S2 leaves cannot express an un-namespaced atom
  (`Permission` is `<ns>:<action>`, `05-rbac.md:100`), so the ~480 `s.perm` entries of the S2–S4 r1
  manifests have no constructible value: the S2 PR either does not compile as specified or records
  `AnyAuthenticated`, which turns the reviewed manifest into documentation and must then be rewritten
  silently in the S4 r2 flip.
- `LegacyAtom`'s constructor needs no catalog: only its evaluation does (`Catalog.LegacyReach`,
  `Catalog.Legacy`), and evaluation happens only from S4 r2, when stage 13 runs for the mount.

**Fix:** Move `LegacyAtom(atom string) Requirement` and `LegacyCheck(atom string, readSide Permission)
Requirement` into the S2 leaf set as values that only carry the atom; keep their resolution in the S4
checker, and let `Freeze` (S4) reject an unknown atom. Edit 05-rbac "Landing" (S2 list gains both;
"they need the catalog" applies to evaluation), 06 §6.4.10 S2 row, 04 §8.1 S2 rbac leaves and 05-http-api
§3. State in 05-http-api §8.1 S2 that the S2 manifest records `rbac.LegacyAtom(atom)` for every `s.perm`
route and that `httpapi` at S2 accepts it only in `MountLegacy` manifests under `LegacyAuthGin`. Replace
09 §9.2.6's "`LegacyRoute.Atom`, or `rbac.LegacyAtom(atom)` — 05-http-api decides" with `rbac.LegacyAtom(atom)`.
Add `TestS2Manifest_SPermRoutesCarryLegacyAtom`.

### COR6-2: MAJOR. COR5-3 half-fixed: the `owed` duty ledger has no API, so none of its consumers can read it or delete its entry "in the batch that performs it"

**Where:** 05-storage §5S.3.7 served-ledger row (`05-storage.md:978`: "Each consumer reads `owed`, performs
its fallback, and deletes its own entry **in the batch that performs it**"), step 11 (b) and (d)
(`:1519-1520,1524-1526`); 05-authn `RestoreDeps`/`BootWrites` (`05-authn.md:395-417`) and §4.3
(`:747-752`: revoke sessions "created before **this boot's start**"); 05-backup "Boots without the
journal duty" (`05-backup.md:730`); 09 §9.2.1 rule W (`09-abo-on-core.md:123-126`); 05-storage §5S.3.9
derived loader (`05-storage.md:1199-1202`); 06 P4 (b) (`06-app-sdk.md:1423`: consumers "read the previous
boot's record").

**Evidence:**
- `c/storage/served/<owner>/owed` lives in storage's own keyspace. Handles are capabilities and a write
  outside the bound handles fails commit with `ErrOutsideKeyspace` (`05-storage.md:325-327`); auth,
  `backup/journal` and compat hold only their own keyspaces, and no storage member reads or stages a
  delete of an `owed` entry (the Go blocks of §5S.3.1–§5S.3.14 define none; `storage.Admin` is
  kernel-only and its surface is an open item, `05-storage.md:1959-1961`).
- `auth.BootWrites(ctx, RestoreDeps)` has no input that carries "05-storage reports that a boot …
  lacked it" nor the `detected` time, and no way to clear the entry; 05-authn §4.3 still uses "this
  boot's start" where 05-storage specifies `detected` ("the same `t` on a retry").
- The key is per owner, but the duties are store-wide. Nothing says which owner's entry a consumer
  clears. If each owner's entry is recorded and a consumer clears one, the rest re-fire on every boot:
  for `gen` that means "the loader rejects every artifact while `gen` is owed", so every boot rebuilds
  every derived index (HNSW included) for as long as one owner's entry survives.
- 06 P4 (b) still says the consumers read "the previous boot's record", not `owed`.

**Fix:** Add to 05-storage §5S.3.7 a kernel-built duty handle, handed to each consumer at P4c/P4d:
```go
type Owed struct{ Since, Detected time.Time }
type Duties interface {
	Owed(duty string) (Owed, bool)                    // store-wide, read at step 2
	StageDone(b *Batch, duty string) error           // binds storage's own handle to b (participant binding)
}
```
Make `owed` store-wide (`c/storage/served/owed/<duty>`), or state that `StageDone` deletes the duty's entry
under every owner. Add `Duties storage.Duties` to `auth.RestoreDeps` (and to `backup/journal`'s boot
re-apply input, compat's usage recorder and the derived loader); rewrite 05-authn §4.3 and the
`BootWrites` comment to "created before `Owed.Detected`". Change 06 P4 (b) to "every consumer reads the
`owed` entries this boot staged". Extend `TestServedDutiesFallBack` with a two-owner store: one clear
removes the duty for the store.

### COR6-3: MAJOR. HEAD's Start-time identity writers (`seedRolesAndTokens`) have no home in the dark S4 r1 release

**Where:** 09 S4 Releases (`09-abo-on-core.md:866`: at r1 "`auth.BootWrites` and P7's default-grant
seeding are skipped and logged, **nothing is written**"); 06 §6.5.1 Stages (`06-app-sdk.md:1463-1467`);
05-rbac §5 item 1 (`05-rbac.md:845-847`: r1 "skips … P7's seed commit"); 05-authn §5 (`05-authn.md:1234-1238`)
and `TestDarkReleaseWritesNoIdentityRow` (`:1428`); 04 S4 rewires `internal/auth`'s `seed.go`
(`04-package-mapping.md:1051`); 05-authn §11 moves `internal/server/bootstrap.go` to `core/auth`
(`05-authn.md:1448`); code: `internal/server/server_lifecycle.go:355,1098-1140` @748fd787a.

**Evidence:**
- HEAD's `Server.Start` calls `seedRolesAndTokens` on every boot: `auth.SeedRoles` (upserts the four
  seed roles), `auth.SeedSystemUser`, `stampNeverExpiringAPIKeys`, `InitBootstrapToken` (a new 10-minute
  token file and hash on every start, `05-authn.md:53`) and the optional startup read-only key.
- Their successors are P7's seed-role reconcile and `auth.BootWrites` (`06-app-sdk.md:1423` P4d step 4,
  P7 row), both skipped at r1, and the code they replace moves to core at S4. Round 5 gave the two
  open-time writers of the same kind (`ReconcileOpsV3WithV2`, `ReconcileVerifiedEmails`) a books
  start-hook home until their successor runs (COR5-7); these five got none.
- Read literally ("nothing is written"), S4 r1 mints no bootstrap token, so ABO's bootstrap runbook
  (restart, read the token file, exchange it) fails after any r1 restart once the previous token's
  10 minutes pass. A fresh install at r1 has no seed role rows, and gin's HEAD authorizer
  (`internal/server/middleware/auth.go:430-455`, exact match over the stored role list) gives the first
  admin no permission. r1 is a production soak release, so this is reachable.

**Fix:** In 09 S4 Releases, 06 §6.5.1 "Stages" and 05-authn §5, state that through S4 r1 books keeps
HEAD's `seedRolesAndTokens` sequence (roles, `_system`, expiry stamp, bootstrap token, read-only key) in
its start hook (Run step 2) over books' own code, exactly as for `ReconcileVerifiedEmails`, and that the
r2 flip PR deletes it as P7 seeding and `BootWrites` take over; or schedule the `seed.go` and
`bootstrap.go` moves in the r2 PR. Narrow `TestDarkReleaseWritesNoIdentityRow` to "core writes no
identity row at r1", and add an r1 done-check: a fresh store booted on r1 has the four seed roles and
`_system`, and every r1 restart writes a new bootstrap token file.

### COR6-4: MAJOR. The seed-step rule runs a seed after the later steps that reshape its rows have been stamped applied

**Where:** 05-storage `Migration.Seed` (`05-storage.md:658-663`), runner rule "Seed steps"
(`:727`: "a seed step below an applied non-seed step of the same owner still runs"), step 6
(`:1480-1486`); 06 P4 (a) (`06-app-sdk.md:1423`); hello's seed (`06-app-sdk.md:1981-1985,2025-2030`);
`TestFreshInstallRunsSeedSteps` (`05-storage.md:1877`).

**Evidence:**
- On a fresh install the stamp batch records every non-seed step applied; the seed then runs alone in
  P4b, writing rows in the shape of the seed's own version. A module whose v1 seeds rows (step 1, seed)
  and whose v2 reshapes them (step 2, non-seed) therefore gets v1-shaped rows on every fresh install
  from v2 on, and step 2, stamped applied, never converts them. Migrations are forward-only and
  frozen, so the seed cannot be kept current; the rule is wrong exactly when a module's data evolves.
- The bookkeeping is unspecified for this case: a seed step's batch carries "version bump + applied
  record", but the owner's `version` counter is already above it.
- hello, the worked example, violates the stated contract ("it must leave rows that already exist
  unchanged"): `seedNames` writes `name/world` = `1` unconditionally, so on a store holding the row it
  resets a count.

**Fix:** In 05-storage §5S.3.5 and §5S.4 step 6, and 06 P4 (a): on a fresh install stamp only the
versioned steps **below the owner's lowest `Seed` step**; that seed and every later step of the same owner
then run in P4b in version order (cheap, since the keyspace holds only the seed's rows), and the counter
is bumped normally. Alternatively keep seeds out of migrations (`storage.Spec.Seed func(ctx,
*MigrationTx) error`, run only on a fresh install and always written against the current shape) and
drop `Migration.Seed`. Make hello's seed conditional (`tx.Keyspace().Get` → `ErrNotFound` → `tx.Set`), and
extend `TestFreshInstallRunsSeedSteps` with an owner whose step 2 reshapes step 1's seeded row.

### COR6-5: MAJOR. HEAD's settings load writes, but the legacy `Open` that runs it is specified to write nothing; read as "read transforms", a stored `embed_cover_art=false` is dropped on every boot of a store without HEAD's marker

**Where:** 05-config `LegacyOpener` (`05-config.md:397-403`: "It never writes the store"), suite
(`:1087`: "`LegacyOpener.Open` writes nothing"), §1.4 step 5 (a) (`:776-779`: "never a write"), §1.12.2
(`:1211-1219`: `Open` runs the load "without that repair"; `:1154`: "the ten `migrate*Blob` read
transforms"); 04 `internal/books/configstore` row (`04-package-mapping.md:276`); against 05-storage row 7
(`05-storage.md:1727`: "the corrupt-secret repair that runs inside the legacy `Open`") and 09 S2b Data/keys
(`09-abo-on-core.md:831`). Code: `internal/config/persistence.go:1043-1058` (reshaping migrations
persisted by `saveRawBlob`), `:1088-1129` (one-shot migrations and their markers `log_retention_zero_migrated`,
`embed_cover_art_default_on_migrated`, `:649`), `:659-674` (`migrateEmbedCoverArtBlob` drops a stored
`false`), `:1274,1314-1338` (`MigrateMaintenanceWindow` and its marker) @748fd787a.

**Evidence:**
- From S2b to the S3 façade, the legacy `Open` runs HEAD's `loadConfigFromDB`. That load does more than
  the corrupt-secret repair DATA5-11 removed: it persists every reshaping blob migration
  (`saveRawBlob`) and writes the one-shot markers. The contract ("writes nothing") and the suite
  therefore fail books' implementation if it keeps HEAD's code, and silently change behaviour if it
  drops the writes.
- If the writes are dropped (the spec's "read transforms"), the marker-guarded one-shots run on every
  read of every store whose marker is absent: every fresh S2b+ install and every store upgraded from a
  pre-marker build. No core-era path writes the markers (`Load` is read-only, `Store.Apply` renders only
  changed keys). `migrateEmbedCoverArtBlob` drops any stored `embed_cover_art=false`, so an operator who
  turns cover-art embedding off sees it on again after each restart, and the organizer writes cover art
  into their audio files. `MigrateMaintenanceWindow` likewise resets an explicit 0–0 window to 1–4 on
  every boot.
- 05-storage row 7 and 09 S2b still describe the corrupt-secret repair as an S2b writer inside `Open`
  that the S3 PR converts, which contradicts 05-config §1.12.2 (DATA5-11 residue).

**Fix:** Give HEAD's load-time writes the home §5S.9.3 gave HEAD's other init-phase writers. Convert the
marker-guarded one-shots (`log_retention_zero_migrated`, `embed_cover_art_default_on_migrated`,
`maintenance_window_migrated`) into books versioned `Migration`s (`Additive`, P4b, over books'
`LegacyRange`, each writing HEAD's marker so an older build also skips it). A fresh install records them
applied, which is correct because it holds no stale value. Make the read transform consult the marker
**or** that step's applied record. Keep the idempotent reshaping `migrate*Blob` functions as pure read
transforms. Then `Open` truly writes nothing. Say so in 05-config §1.12.2 and 04 §2.4, delete the repair
from 05-storage row 7's S2b writer list and from 09 S2b Data/keys, and add
`TestFreshInstall_EmbedCoverArtFalseSurvivesRestart`.

### COR6-6: MINOR. `journal.StorageSpec()`'s `Serialized` list differs between 06 and 05-backup

**Where:** 06 §6.4.10 S4 row (`06-app-sdk.md:1339`: `Serialized: ["journal_head"]`) and changelog
(`:2539`); 05-backup "Stages" (`05-backup.md:39`: `["journal_head", "served_boot"]`) and "Content
marker" (`:728`: `c/backup/served_boot` is `Serialized`).

**Fix:** Add `served_boot` to 06's S4 row and changelog line. State in 05-backup that `served_boot` is
written by the journaling boot with `GetForUpdate` (it is the only reason it is `Serialized`).

### COR6-7: MINOR. COR5-12 residue: 06 names two batch-rule exceptions, 05-storage and 05-metadata three

**Where:** 06 `LegacyStores` comment (`06-app-sdk.md:372-377`: "with two stated exceptions … `log.JournalStore`
… and the maintenance methods `Recover` and `Prune`"); 05-storage "Store interfaces" (`05-storage.md:1251`:
also `providerhttp.GateStore`); 05-metadata `GateStore` (`05-metadata.md:978-988`).

**Fix:** In 06 write "three stated exceptions" and add `providerhttp.GateStore` (commits its own batch
under the Registry's mutex, no commit-time check, 05-metadata M2).

### COR6-8: MINOR. `config.LegacyOpener` lands "from S2" in 05-config but at S2b in 06

**Where:** 05-config (`05-config.md:397-400`: "Open runs once per boot … (06 P4d step 1; at S2, P3) … From
S2"; `:766-769`: at S2 a constructor option makes `Open` run none of ABO's steps); 06 §6.4.10 S2 row
(`06-app-sdk.md:1336`: only `ExternalWriter`) and S2b row (`:1337`: `LegacyOpener` listed); 06 P3 row
(`:1422`: attach at S2 with no `Open`).

**Fix:** Pick one. Either list `config.LegacyOpener` in 06's S2 row and add "(i) the store's `Open`
(no-op by constructor option)" to P3's S2 sentence, or say in 05-config that `LegacyOpener` lands at S2b
and that at S2 config does not call it.

### COR6-9: MINOR. Public SDK sub-packages used by owner files are missing from 03's package tree and R4 layer list

**Where:** 03 §3.3 table and R4 (`03-architecture.md:258-305,455`); `auth/oidc`, `auth/password`
(`05-authn.md:77`; `04-package-mapping.md:865`), `health/diagnostics` (`05-health.md:10-12,304`),
`archtest/tasklint` (`04-package-mapping.md:868`), `coretest/fakes` (`06-app-sdk.md:1273`),
`coretest/acquiretest` (`10-tv-movies-proof.md:912`); 06 §6.4.8's suite list omits `storage/index/indextest`,
which 03 lists (`06-app-sdk.md:1266-1267` vs `03-architecture.md:300`).

**Evidence:** 03 is normative for paths, and the core layer guard runs the R4 table with its own
`layerOf` (`03-architecture.md:547-548`). A public package with no R4 layer is either unclassifiable
there or classified by an unstated rule. `health/diagnostics` importing `tasks`
is legal only because 05-health asserts a same-layer edge that R4 does not record.

**Fix:** Add the six packages to 03 §3.3 (or one rule: "a public sub-package `p/q` takes the layer of `p`
unless listed; test-support sub-packages of `coretest`/`archtest` are test-support") and list them in R4.
Add `indextest` to 06 §6.4.8's list.

### COR6-10: MINOR. 03 §3.7's boot diagram and 05-authn use stale phase and step names

**Where:** 03 §3.7 diagram (`03-architecture.md:657`: "phases P0a–P8 of 06 §6.5.1", omits P−1 and the
served-ledger commit at Run (3b)); 05-authn (`05-authn.md:1138`: "`secrets.Open` and `AttachOverrides`")
against 05-config `Store.Attach` (`05-config.md:324`) and 06 P4d step 1 (iv).

**Fix:** 03: "phases P−1–P8 of 06 §6.5.1 and the start order of §6.5.2 (served-ledger commit at Run
(3b))". 05-authn: replace `AttachOverrides` by `Store.Attach`.

### COR6-11: MINOR. hello's `main` calls `signal.NotifyContext`, but R5 forbids `signal.Notify` in core and `TestNoGlobals` scans `core/examples/...`

**Where:** 03 R5 (`03-architecture.md:456`); 06 §6.10 (`06-app-sdk.md:1665`: "`archtest.TestNoGlobals`,
`archtest.NoInit` over `core/...` and `core/examples/...`"); hello `cmd/hello/main.go`
(`06-app-sdk.md:2211`).

**Evidence:** `signal.NotifyContext` installs a process-wide handler through `signal.Notify`. An AST scan
keyed on `signal.Notify` misses it, which silently weakens R5. A scan that catches it fails hello.

**Fix:** In R5, exempt `package main` (a process owner, like `app.InstallProcessDefaults`), so that
`TestNoGlobals` skips main packages under `core/examples/*/cmd/`, and list `signal.NotifyContext` beside
`signal.Notify` for every other core package.

### COR6-12: MINOR. 09 S2b says books' legacy migrations run "before claims are enforced", but 05-storage runs them on claim-checked handles

**Where:** 09 S2b Data/keys (`09-abo-on-core.md:831`: "Books migrations 1..66 … run before claims are
enforced (§9.2.2)"); 05-storage §5S.3.4 "Books' legacy steps" (`05-storage.md:577-580`: they run "on books'
own claims through books' own `LegacyRange`"), "Reads are checked too" (`:630-632`) and §5S.4 step 8
(`:1493`).

**Evidence:** Every `LegacyRange` read and write is claim-checked (shadow or strict). Exempting P4b would
hide exactly the HEAD-era writers the S2b shadow release exists to find (steps 62/65/66 write the ops
families blind, `05-storage.md:1731`).

**Fix:** Replace the 09 clause with "run first in the DAG, on books' own claims, through books'
`LegacyRange` (claim-checked like every other access; 05-storage §5S.3.4)".
