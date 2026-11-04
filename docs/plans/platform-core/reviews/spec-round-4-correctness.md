<!-- file: docs/plans/platform-core/reviews/spec-round-4-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: d241c64d-48c8-41f8-83bb-bd27fa426e61 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 4: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR4`). Inputs: the revised spec in
`docs/specs/platform-core/` (03 v1.6.1 normative, 06 v1.4.1, 05-tasks v1.4.1, 05-storage v1.4.1,
05-config v1.4.1, 05-events v1.4.0, 05-realtime v1.3.0, 05-http-api v1.4.1, 05-rbac v1.4.0, 09 v1.3.1,
04 v1.3.0, 05-download, 05-backup, 05-authn, 05-health, 10). Binding inputs: round-3 dispositions
(R3-1…R3-5) first, then the round-1 cross-cutting resolutions and R2-4…R2-8 where R3 does not
supersede them. Code anchors are at `748fd787a`; spec anchors are `file:line` in
`docs/specs/platform-core/`.

Method: the four flows were traced again through the current Go blocks and stage tables, every
cross-file type, name and signature on each path was looked up in its owning file, each package's
imports were checked against R4 (L0–L9) and against the landing tables (04 §8.1, 06 §6.4.10), and the
`hello` blocks of 06 §6.12 were checked symbol by symbol:

1. ABO scan → `RunItems` → legacy-format adapters and grants → outbox → realtime → UI.
2. Route → `httpapi` → `rbac` → `audit`.
3. `app.New(books, hello)` through P−1…P8 (06 §6.5.1) and Run (1)…(8), per stage.
4. TV acquisition at proof depth: grab → `core.download.event.v1` → acquire consumer → import run.

**Verdict.** R3-1 removed the machinery that produced most round-3 defects, and the round-3
correctness fixes landed in 6 of 7 cases: the P4d order (COR3-2), the S2 surface (COR3-3, one residue),
the single download lifecycle topic (COR3-4), compression exclusions (COR3-6) and the derived loader
(COR3-7) all hold across files. `hello` compiles and boots as written. What breaks now is again
**stage-specific**, concentrated where R3-1 moved work without saying where it runs:

- From S2b the store is opened by the kernel, but nothing says where ABO's own blob load into
  `config.AppConfig`, its settings-encryption init and its key-regeneration guard run; both readings of
  the spec fail (a second Pebble open, or a server on defaults whose first settings save overwrites the
  stored blob) (COR4-1, BLOCKER).
- S2b loses the live-reload source for UI edits (COR4-2), and 09 S3 removes the very option config's
  adapter needs (COR4-3).
- The S4 "dark" release reads and writes identity rows without a grant and claims a journal duty it
  does not perform (COR4-4).
- COR3-5 is half-fixed: the per-shard `Subjects` never overlap ID-scoped writers (COR4-5), and the
  kernel-synthesised migration kind has no specified timeout or resume shape (COR4-6).
- Three landing-order breaks: projection A names a topic that exists only at S8 (COR4-7), task
  readiness waits on a health registry that exists only at S8 (COR4-8), and the journal is located by a
  config key that exists only at S8 (COR4-10).

## Regression check (round-3 BLOCKER and MAJOR, correctness lens)

| ID | Sev | Status in round 4 | Where |
|---|---|---|---|
| COR3-1 | BLOCKER | **Resolved for boot reads** (S2: `Options.LegacyConfig` at P3; S2b+: config's adapter at P4d step 1). Three residues: no live-reload source at S2b; 09 S3 removes `LegacyConfig`; ABO's own blob load has no home from S2b. | COR4-1, COR4-2, COR4-3 |
| COR3-2 | MAJOR | **Fixed.** 06 P4d (`06-app-sdk.md:1199`), 05-storage step 11 (`05-storage.md:1303-1319`), 05-config §1.4 (`05-config.md:635-678`), 05-backup §5B.3.6/§5B.4 and 05-authn §5 all give keyring + overrides → journal → hooks → `BootWrites` → compat; `RetireUnused` moved to P5. New: the key that locates the journal does not exist S4–S7. | COR4-10 |
| COR3-3 | MAJOR | **Fixed except one residue.** `auth` leaf at S2, `Deps.Compat` S2b, `Manifest.PublicRoutes`, `AuthMethod` recording at S2, `ui.ReservedSegments` at S2, Basic gate at S3, `Deps.Notify` S7, six packages added to 04 §8.1. The S2 `auth.Principal`/`User` leaf names `rbac` types that land at S4. | COR4-11 |
| COR3-4 | MAJOR | **Fixed** (one durable topic keyed by `GrabID`, one consumer = one topic, stale transitions acked, `tracked/` `Serialized` with `GetForUpdate`). Residue: order inside one batch depends on an unstated ULID property. | COR4-14 |
| COR3-5 | MAJOR | **Half-fixed.** Module kind `<ID>.datamigrate.<name>` exists in 06, 05-storage and 05-tasks, but its exclusion does not work and its definition fields are unspecified. | COR4-5, COR4-6 |
| COR3-6 | MAJOR | **Fixed** (`LegacyRoute.NoCompress`, `text/event-stream` skip, `Unwrap`/`Flush`, `SkipPaths`, S2 golden; `05-http-api.md:617,995`). | — |
| COR3-7 | MAJOR | **Fixed** (Load at Run (3a), `Describe` skips it, `datamigrate.DerivedBuildTask`, module-relative names). Residue: the derived record key is spelled two ways, and the build start is a write the write rule does not list. | COR4-16 |
| COR3-8…COR3-15 | MINOR | Fixed (starter names, bridge wording, `TaskStarts` rule, `core.tasks.kinds` map key, `me/password` one gin route, tie-break list, P7 write column), except two COR3-13/COR3-15 residues. | COR4-21 |

## Flow traces (where each one breaks now)

| Flow | Path | Breaks at |
|---|---|---|
| 1 ABO scan → UI | `opsbridge` wraps the v2 def as a core Task (`LegacyDefID`) → `tasks.RunItems(rc, Fixed/Sequential, …)` → books writes its own families through `internal/books/store` on its claims; run rows through `legacystore` on books' `Exclusive` `core/tasks` grant (S5) → executor publishes `core.task.run-changed.v1` after commit → S5: books bridge subscriber (`Registrar.Subscribe`) → streams A/B/C; S6: hub raw subscription → coalesce → `Permission`/`Visible`/`Redact` per subscriber → projection A/B/C → unchanged UI | S2b–S3: ABO's own config load has no home (COR4-1). S5–S7: dispatch waits on readiness signals with no source (COR4-8). S6–S7: projection A names `core.system.status-changed.v1`, P7 aborts boot (COR4-7). Otherwise it composes. |
| 2 Route → RBAC → audit | `httpapi.Handle(r.HTTP(), Route{Perm})` → stages 0–11 → stage 12 `auth.Service.Middleware` (binds `Principal`, `rbac.WithChecker`, `audit.WithActor`) → stage 13 `Principal.Perms.Satisfies(Perm)` + `Guards` → handler → `d.Audit.RecordIn(ctx, b, e)` | Composes from S4. S3: stage 8's exempt rule drops three HEAD exemptions, so its own done-check cannot pass (COR4-9). `rbac` ↔ `internal/authz` is an import cycle as specified (COR4-12). |
| 3 `app.New(books, hello)` | P−1 → P0a → P0 → P1 → P2 (grants validated, logged) → P3 → P4a–d → P5 → P6 → P7 → P8 → Run (1)…(8) | S2b: COR4-1, COR4-2. S4 r1: identity adapters, `BootWrites`, journal re-apply, hooks and P7 seeding all need a grant that comes into force only at r2 (COR4-4). S4–S7: P4d step 2 has no `core.backup.dir` (COR4-10). Online data migrations: COR4-5, COR4-6. |
| 4 TV acquire | `Engine.Grab(ctx, rc, d)` (ref locks) → `Pool.Grab(…, rec)` (TD + pending grab record, one batch) → `added` CAS stages `event.v1{grab_added}` → poll stages `event.v1{completed}` (same key) → `core.acquire.download-events` `ConsumeTx` + `Where(Owner)` → TD `Downloading → ImportPending` + `tx.StartRun(<module>.acquire.import, …)` in the relay batch → `ImportTask` (`Subjects` = TD refs) → `Importer.Import(…, ImportTx)` → `MarkImported` | Composes. One residue: the in-batch order of `grab_added` and `completed` rests on stage-time ULIDs, and a reorder loses the completion (COR4-14). |

`hello` (06 §6.12, S9 state) was checked against the owning definitions: `config.Define`/`Docs`/
`Handle[T].Get` (05-config §1.3.1–§1.3.2), `Keyspace.Sub`/`Get`/`Scan`, `KV.Clone`, `ErrNotFound`,
`Migration`/`MigrationTx.Set` (05-storage §5S.3.1, §5S.3.5), `httpapi.Handle`/`Route`/`In`/`Invalid`/
`Field` (05-http-api §3.1–§3.3), `tasks.Task`/`TaskSpec`/`Common`/`Permissions`/`ResumeFromZero`/
`RunItems`/`ItemsOptions`/`ItemRC.Count` (05-tasks §5T.3.1–§5T.3.7), `coretest.New`/`WithModules`/
`WithUser`/`AssertPermission`/`As`/`GetJSON`/`RunTask`/`RunResult` (06 §6.4.8), `ui.Bundle`/
`MustFromFS` (07). Every identifier and import resolves, no import is unused, the migration key
`name/world` equals `Sub("name")` + `world`, and the registration rules pass (declared
`hello:settings.manage`, `TaskLedger`, `Resources("name")`, a ClassAction `Perms.Start`).

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | COR4-1 |
| MAJOR | 9 | COR4-2 … COR4-10 |
| MINOR | 12 | COR4-11 … COR4-22 |

## Findings

### COR4-1: BLOCKER. From S2b nothing loads ABO's settings blob into `config.AppConfig`, initialises ABO's settings encryption or runs the key-regeneration guard; both readings of the spec fail

**Where:** 06 §6.4.3 `Registrar.Health`/`StartupSteps` comment (`06-app-sdk.md:521-528`); 06 §6.5.1
"Stages" (`06-app-sdk.md:1216-1219`); 06 §6.11.1 step 2 (`06-app-sdk.md:1426-1435`); 09 S2b
(`09-abo-on-core.md:631,633,635`); 05-config §1.12.1 (the façade is S3, `05-config.md:949-960`).

**Evidence:**
- HEAD's `serve` runs, in order, `initializeStore`, `guardAgainstKeyRegeneration`, `initEncryption`
  and `loadConfigFromDB` (`cmd/root.go:280,345-352,364-369` @748fd787a). HEAD's own comment on the last
  one says why it is Fatal: "SaveConfigToDatabase persists the whole config struct, so a server running
  on defaults would write those defaults over the stored config on its first settings save"
  (`cmd/root.go:360-363`).
- 06 §6.4.3 says that until S8 `serve` "keeps running its books steps (initializeStore,
  guardAgainstKeyRegeneration, initEncryption, loadConfigFromDB, ...) through startup.Steps before
  app.New, as HEAD does".
- 06 §6.5.1, §6.11.1 step 2 and 09 S2b say that from S2b the kernel opens the one store at P2 and "those
  pre-server steps leave `serve`"; 06 also says "there is never a second Pebble open".
- Neither file gives the four steps a home from S2b. Until the S3 façade, ABO's code reads
  `config.AppConfig` (viper-backed) and ABO's legacy secret rows through ABO's own settings encryption.
- Reading 1 (06 §6.4.3): `initializeStore` opens Pebble before `app.New`, and P2 then fails on the
  store lock, so S2b cannot boot.
- Reading 2 (06 §6.11.1): `AppConfig` runs on defaults, every UI-saved books setting is ignored, the
  legacy OpenAI/Google/Hardcover/Basic secret rows are unreadable, and the first `PUT /api/v1/config`
  writes defaults over the stored blob, which is the data loss HEAD's comment describes.
- No S2b done-check would catch either outcome: it covers claims, the census, checkpoints and the
  restore rehearsal, never config.

**Fix:**
- State in 06 §6.5.1 "Stages", §6.11.1 step 2 and 09 S2b that from S2b to S3 these steps run in
  `books.Register`, before it constructs `*server.Server`. That is the only point after the store is
  open and before ABO code reads `AppConfig`:
  - `guardAgainstKeyRegeneration` and `initEncryption` (file reads of `.encryption_key` under the state
    directory);
  - `loadConfigFromDB` through `Deps.Storage.Legacy` (books owns `setting:`);
  - `syncConfigFromEnv`, then `config.Validate`.
- Each step fails `Register` (and so boot) exactly where HEAD's step class is Fatal. List these file
  reads as part of the recorded exception to `Register`'s side-effect rule (06 §6.4.1, §6.4.5), beside
  server construction.
- At S3 `loadConfigFromDB` is replaced by the façade, and `guardAgainstKeyRegeneration`/
  `initEncryption` by `secrets.Open`'s `ErrKeyMissing` and `k0-legacy` import (05-config §2.4).
- Rewrite the 06 §6.4.3 comment to say: "until S2b `serve` runs them before `app.New`; from S2b, in
  `books.Register`; at S3 the config and encryption steps are replaced; the remaining books steps become
  `StartupSteps` at S8".
- Add S2b done-check `TestS2bAppConfigFromBlob`: a value set only in the blob is effective in
  `AppConfig` after boot, and a `PUT /api/v1/config` changing one field leaves every other non-default
  blob field byte-identical.

### COR4-2: MAJOR. COR3-1 residue: from S2b to S3 a UI edit of a core-owned key (rate limiter, CORS origins, peer allow-list, log level) no longer reaches the running perimeter; there is no `Changed` source

**Where:** 06 §6.4.10 S2b row "`LegacyConfig.Rows` unused" (`06-app-sdk.md:1137`); 06 §6.4.5
`LegacyConfig` comment (`:795-808`); 05-config `Legacy.Rows` (`05-config.md:383-385`), §1.4 step 8
(`:676-678`), §1.12.2 "Edits made outside core config" (`:1018-1022`); 06 `TestLegacyConfigHonoured`
(`:1382`); 09 S2 Config row (`09-abo-on-core.md:621`) and S2b grant row (`:295`).

**Evidence:**
- 05-config promises that `OverrideStore.Changed` fires "until the S3 façade" whenever ABO's legacy
  config path writes the blob, so a perimeter edit applies without a restart "as at HEAD".
- The only change source the spec defines is `LegacyRows.Changed()`, and both 06 and 05-config declare
  `Rows` unused from S2b.
- At S2b–S3 the blob is written by ABO's `PUT /api/v1/config` through books' own claim (config's grant
  is read-only until S3). The kernel's `configstore` reads through the grant only at P4d. Nothing tells
  `configstore` that the row changed.
- `TestLegacyConfigHonoured` checks only the boot path from S2b, and 09's `TestPerimeterHonoursBlobOnlyValues`
  runs only at S2.
- Result: for the whole of S2b (two production releases, at least 14 days of shadow mode), an admin who
  tightens `allowed_peer_cidrs` or turns the rate limiter back on in the UI sees it saved, but stages 0
  and 10 keep the old value until a restart. HEAD applies both live.

**Fix:**
- Keep `Legacy.Rows.Changed()` as the change source from S2 until the S3 façade. ABO's legacy config
  path signals it after every blob write at S2 and S2b.
- `configstore.Changed()` forwards that signal, and the S2b layer re-reads the blob through the grant.
- Change 06 §6.4.10 S2b to "`Rows.Get` unused; `Rows.Changed` still the change source until S3", and
  amend 06 §6.4.5 and 05-config §1.3.3 to match.
- Alternatively, let storage notify subscribers of a granted exact key after commit
  (`LegacyRange.Watch(key)`). That is more general, but it is new storage API.
- Extend `TestPerimeterHonoursBlobOnlyValues` (runtime half) to S2b. Fix 09 S2's name for the signal
  (`Changes` → `Changed`).

### COR4-3: MAJOR. 09 S3 removes `app.Options.LegacyConfig`, which config's legacy-format adapter needs at every later stage

**Where:** 09 S3 Release row (`09-abo-on-core.md:652`: "`app.Options.LegacyConfig` is removed"); 06
§6.4.5 (`06-app-sdk.md:795-808`); 05-config §1.3.3 `Options.Legacy`/`Legacy` (`05-config.md:277-279,
373-386`) and §1.12.2 (`:1000-1053`).

**Evidence:**
- `config.Legacy` carries `BlobKey`, the `LegacyBlob` codec (`Decode`/`Encode`), `Secrets` (where
  each legacy secret lives), the gap-fill `File`/`Fill` and the `Aliases` of core keys.
- §1.12.2 reads the blob with `Legacy.Blob.Decode`, writes it with `Legacy.Blob.Encode` (from S3) and
  blanks secrets through `Legacy.Secrets`, until each family's cutover. 06 says "nil = an app with no
  legacy settings".
- With the field removed at S3, `configstore` has no codec and no row names. From S3 every blob-named
  key falls back to file/env/defaults, legacy secrets are unread, and `Store.Apply` cannot re-render the
  blob. That reverses COR3-1's fix at the stage that makes core config the writer.
- 06 §6.4.10 lists no removal, so 09 contradicts the owning file.

**Fix:** In 09 S3 replace "`app.Options.LegacyConfig` is removed" with "`app.Options.LegacyConfig.Rows`
is dropped (unused since S2b, or since S3 under COR4-2's fix); `LegacyConfig` itself stays until the
settings family's cutover (§9.13)". Add a 06 §6.4.10 S3 cell that records the `Rows` removal.

### COR4-4: MAJOR. The S4 r1 "dark" release reads identity rows with no grant, at a phase with no storage, while P4d and P7 write identity and role rows through that same missing grant, and r1 claims a journal duty its revocation paths do not perform

**Where:** 09 S4 Releases (`09-abo-on-core.md:667`: r1 "read every identity row through the
legacy-format adapters at boot (decode census, P1) ... nothing is written through them yet"; r2 "the S4
grants come into force") and the grant table (`:297`); 06 P1 (`06-app-sdk.md:1196`, "Static checks
only"); 06 "Stages" (`:1221-1223`: P4d steps 2–4 and P7 seeding exist "from S4"); 05-storage §5S.3.4
(`05-storage.md:613-616`: "A core subsystem never reads a family it holds no grant on") and
"Adapter selection" (`:1207`); 05-backup Writers (`05-backup.md:726`: every build from S4 lists the
`journal` duty).

**Evidence:**
- P1 opens no storage, so a decode census cannot run there.
- Without a grant, 05-storage forbids the read, and its adapter-selection rule makes `auth`/`rbac` use
  their **native** keyspaces at r1.
- 06 runs, from S4 and with no r1/r2 distinction, writes that all go through auth's or rbac's grant:
  P4d step 2 (journal re-apply), step 3 (`auth.PostRestore`), step 4 (`auth.BootWrites`: `_system` and
  expiry stamps) and P7's default-grant seeding into the legacy role rows. At r1 they either fail or land
  in `c/auth/`, against r1's "nothing is written".
- At r1 gin's handlers still perform every revocation (09: "gin's auth still decides every request").
  Those HEAD code paths do not append to the revocation journal. 05-backup nevertheless makes the r1
  build list the `journal` duty, so the next boot writes no `gap` line. A later restore whose boundary
  precedes an r1-era revocation gets `Revocations` that omit it and re-activates a credential revoked on
  r1.

**Fix:**
- r1 declares books' identity grants in `GrantRead` mode, not exclusive. The census runs at P4d (after
  step 1) through that range; `backends` records `legacy`, so r2 is not `ErrGrantMissing`.
- r2 upgrades the same grants to ReadWrite/ReadDelete `Exclusive`.
- State in 06 "Stages" that P4d steps 2–4 and P7 seeding start at S4 r2. Under a read-only grant they
  are skipped, logged and counted.
- In 05-backup Writers, the `journal` duty is listed only by builds in which every revocation path is
  core's (from S4 r2, `LegacyAuthCore`). r1 does not list it, so its interval becomes a `gap` line.
- Add `TestDarkReleaseWritesNoIdentityRow` and `TestDarkReleaseRecordsJournalGap` (S4 r1).

### COR4-5: MAJOR. COR3-5 half-fixed: the module kind's per-shard `Subjects` never overlap an ID-scoped writer, whole-kind `Writes` would block every write task, and non-task writers are not covered at all

**Where:** 05-storage §5S.3.6 (`05-storage.md:737-742`, `Writes` at `:763`, resource rule `:807-809`,
"Concurrent owner writes" `:824-828`); 05-tasks §5T.3.4 overlap rule and `SubjectsFunc`
(`05-tasks.md:469-472,480-482`), 5T.4 step 0 (`:1024-1028`); 06 `Manifest.DataMigrations`
(`06-app-sdk.md:273-282`) and `TestOnlineDataMigrationIsModuleKind` (`:1399`); `Batch.Expect` is legal
only on `Serialized` ranges (`05-storage.md:252-255`).

**Evidence:**
- `Writes` are whole kinds (`ID` empty) "plus one `Subjects` entry per shard". `SubjectsFunc` narrows
  the write set to concrete refs, and the overlap rule matches `{M,K,id}` only with `{M,K,id}` and
  `{M,K,""}`.
- A shard is a key range, not an entity ID, so its subject (`{books, books, "<shard>"}`) never overlaps a
  books run writing `{books, books, "<book id>"}`. With `Subjects` the dispatch protection is nil.
- Without `Subjects`, `{books, books, ""}` overlaps every books writer, and an Online rewrite blocks
  every book-writing task for its whole duration. That contradicts `Online` ("a background task while
  serving").
- Either way, writers that are not task runs (gin handlers such as a book PATCH, hooks, workers) are
  outside dispatch. On a range that is not `Serialized`, a rewrite and an HTTP edit of the same row still
  race last-writer-wins. That race is exactly what COR3-5 raised.
- `TestOnlineDataMigrationIsModuleKind` asserts serialisation that the rules cannot deliver.

**Fix:** Adopt COR3-5's stated alternative as the contract, and keep the module kind for attribution:
- An `Online` migration may rewrite only families that its owner declares `Serialized`. `Register`
  refuses one whose `Source` is not `Serialized`.
- Every `Step` write is `SetIfUnchanged` (against the bytes the step read) or `SetIfAbsent`. A failed
  precondition re-reads the item and retries, so an owner write is never overwritten by any writer,
  task or not.
- The kernel registers `<ID>.datamigrate.<name>` with `Effects = Writes(...)` (G8 attribution, audit,
  UI). It does not narrow by shard and does not take part in the dispatch conflict rule: give it its own
  `ConcurrencyKey` `<ID>.datamigrate`, so two of the module's migrations never overlap.
- Update 05-storage §5S.3.6 "Concurrent owner writes", 05-tasks 5T.4 step 0, 06 §6.4.2, and the test
  (a concurrent HTTP-path write to a row under rewrite survives; a concurrent task writer is not
  blocked).

### COR4-6: MAJOR. The kernel-synthesised `<ID>.datamigrate.<name>` (and `core.storage.migrate`) definition has no specified `Title`, `Perms.Start`, `Resume` or `Timeout`; under 05-tasks defaults a library-scale online rewrite ends `timed_out` after 30 minutes

**Where:** 05-storage §5S.3.6 (`05-storage.md:737-742,805-849`); 05-tasks §5T.3.1 required fields
(`05-tasks.md:129-135,226-241`), §5T.3.6 Timeout (`:573`); 06 P7 (`06-app-sdk.md:1202`); compare
COR3-7's fix for derived builds (`05-storage.md:787-802`).

**Evidence:**
- 05-tasks refuses a definition without `Title`, `Effects`, `Perms.Start` and `Resume`, and its Task
  default `Timeout` of 30 minutes is **terminal**, never re-queued.
- §5S.3.6 says only "the kernel registers [the kind] on the module's behalf". Nothing gives the shape,
  resume policy, timeout, yield behaviour, `Perms.Start` or the `TaskLedger` membership of the kind
  (P7 refuses a registered kind that is missing from `TaskLedger`).
- The 24-hour cap also means a multi-day rewrite must yield (`rc.ShouldYield` → `ErrContinueInPlace`),
  which the runner contract never mentions.
- A timed-out run leaves `c/storage/dm/<id>` short of `done`. With a `Barrier` the owner "is not
  started in this process" until a later boot restarts the run, which in turn times out after 30
  minutes.
- `DataMigration.ID` is `"<owner>.<name>"` with no grammar, so `<name>` can contain `_` and make the
  kind fail `ParseKind`.
- COR3-7 fixed exactly this for derived builds (`DerivedBuildSpec`: 24-hour timeout, resume, perms);
  migrations got no equivalent.

**Fix:**
- Specify the synthesised definition in 05-storage §5S.3.6 and 05-tasks §5T.3.12:
  - Shape Task, `Title` = `Description`, `Resume = ResumeContinue` (the persisted shard plan and cursors
    are the checkpoint);
  - `Timeout` 24 hours, and the runner yields with `ErrContinueInPlace` at a chunk boundary when
    `rc.ShouldYield()`;
  - `Perms.Start` = `core:system.manage` (manual retry only; never in a system actor's set);
  - parallelism = `DataMigration.Parallelism`.
- State that kernel-registered kinds are implicitly in the owner's `TaskLedger` and stay there after the
  migration is removed, so held and history classification keep working.
- Constrain `<name>` to the task-kind segment grammar `[a-z][a-z0-9-]*`. Add
  `TestOnlineMigrationYieldsPastTimeout`.

### COR4-7: MAJOR. At S6 and S7 projection A subscribes to `core.system.status-changed.v1`, which is declared only by `health`'s service at S8, so P7 aborts boot

**Where:** 05-realtime §4.9 table (`05-realtime.md:375`), Registrar errors (`:96-98`), lifecycle P7
(`:406-407`); 05-health "Status topic" (`05-health.md:182-188`); 04 §8.1 S8 "the rest of `health`"
(`04-package-mapping.md:727`); 09 S6 (`09-abo-on-core.md:700-706`).

**Evidence:**
- 09 S6 ships the hub with projection A carrying `core.task.*`, `core.system.status-changed.v1` and
  `books.book.changed.v1`.
- 05-realtime: an unknown topic in a projection "abort[s] boot" when the hub binds projections at P7.
- The status topic and its payload type belong to `health`'s component registry, which lands at S8 (at
  S1 `health` is the flight recorder only).
- So the S6 and S7 builds, as written, fail P7. With the topic simply dropped, stream A loses HEAD's
  5-second `system.status` frame from S6 (`internal/server/server_lifecycle.go:404-441` sends `{books,
  folders, memory_alloc, goroutines, timestamp}`). From S8 the frame's payload becomes `{ready,
  not_ready}`. The S6 done-check's "legacy-stream goldens per event type" would fail on that.
- 09 S6 also does not list the rewrite of books' `BookChangeCoalescer` into publishing
  `books.book.changed.v1`, which projection A needs (05-realtime §11 does list it).

**Fix:**
- Make projection A's topic list stage-dependent:
  - S6–S7: `core.task.*` and `books.book.changed.v1`, with `system.status` frames produced by a books
    bus topic that keeps HEAD's payload (`books.system.status-ticked.v1`, `Realtime`, the 5-second
    ticker moved into a books worker);
  - from S8: add `core.system.status-changed.v1` as a separate legacy frame type, or state the payload
    change in the compat ledger.
- Add the coalescer rewrite to 09 S6 Moves. Add `TestProjectionTopicsExistAtStage` (P7 with the S6
  catalog).

### COR4-8: MAJOR. From S5 to S7 task dispatch waits on readiness signals from a health registry that lands at S8, so HEAD's warm-up gating (D44) has no source

**Where:** 05-tasks 5T.4 step 2 (`05-tasks.md:1040-1042`: "Dispatch waits for each def's `Ready`
signals from the health registry"); `Common.Ready` (`:145`); 05-health (`05-health.md:129`:
"`tasks.ReadySignal` is `health.ComponentID`", books warm-up = component `books.memdb`); 06 §6.4.10 S8
(`Health` members); 04 §8.1 S5 depends on S4 only, and S8 "the rest of `health`".

**Evidence:**
- `tasks` lands at S5. Its `ReadySignal` type is `health.ComponentID`, and its dispatcher reads
  readiness from `health`'s component registry. Both land at S8.
- HEAD holds runs until the store and the memdb indexes are warm (`pkg/ops/effects.go:43-55`, D44),
  and book-reading ops depend on it.
- At S5–S7, as written, either `tasks` does not compile (the type is absent), or dispatch has no signal.
  It then either never dispatches (every run waits) or dispatches immediately, and `OnStart` jobs read a
  cold memdb (a regression against D44).

**Fix:** Land the readiness core of `health` with `tasks` at S5: `ComponentID`, `Reporter` and the
`Ready(id)` view, with `core.storage` reported by storage and `books.memdb` by books' warm-up. Add it to
04 §8.1 S5 and to 06 §6.4.10 S5 (`Deps.Health` limited to `Reporter`). Alternatively, make
`tasks.ReadySignal` a tasks-local string type that the S8 registry adopts. Add an S5 done-check: a
`books.*` run that reads books is not dispatched before `books.memdb` reports Up.

### COR4-9: MAJOR. At S3 stage 8's exemption rule drops three of HEAD's Basic-gate exemptions, so `TestBasicGate_ExemptSetEqualsHEAD` cannot pass and `/api/health` monitors start getting 401

**Where:** 05-http-api §4.2 stage 8 (`05-http-api.md:616`), §4.10 (`:797-803`: the `/` owner runs stages
0–9, Basic gate included), §8.1 S3 row (`:955`) and test (`:1160`); 09 S3 done-check
(`09-abo-on-core.md:660`).

**Evidence:**
- HEAD's gate exempts `/api/health` and `/api/v1/health` (`internal/server/middleware/basicauth.go:34`),
  static assets (`/assets/*`, `/favicon.ico` and the `.js`/`.css`/`.png`/`.svg`/`.woff2` suffixes,
  `:40-46`), and ABS routes that run `ABSRequireAuth` (`:69`).
- Core stage 8 exempts only "Public infra probes" (`/livez`, `/readyz`) and `OwnAuth` legacy routes.
  `/api/health` is a Public legacy manifest route, not `Router.Infra`, and the SPA's assets are served by
  the `ServesSPA` fallback, which runs stage 8.
- The S3 golden "today's per-route exempt set" therefore differs from what stage 8 implements. A Basic-
  enabled install's monitor on `/api/health` turns red at S3.

**Fix:**
- Add `LegacyRoute.BasicExempt bool`, generated from HEAD's two health paths and checked by the S3
  golden.
- Give stage 8 an explicit exemption for the `/` owner's static-asset paths with HEAD's prefix/suffix
  rule, as a `LegacyMount.BasicExemptAssets` predicate, or `ui.Options` later. Do not add a per-mount
  exemption.
- Alternatively, keep the set strictly smaller, record the change in the compat ledger and the release
  note, and rename the test `TestBasicGate_ExemptSetIsHEADMinus`.

### COR4-10: MAJOR. COR3-2 residue: from S4 to S7 P4d step 2 locates the revocation journal by `core.backup.dir`, a key that only `backup` (S8) declares

**Where:** 06 P4d step 2 (`06-app-sdk.md:1199`) and "Stages" (step 2 from S4, `:1222`); 05-backup "Where"
(`05-backup.md:723`), §5B.4 (`:770-772`), §5B.7 `core.backup.dir` (`:848`) and the staging paragraph
(`:17,26-30`); 05-config §1.7 (sections are owned by their specs, `05-config.md:758-762`);
`TestJournalReapplyUsesOverriddenBackupDir` (`06-app-sdk.md:1380`).

**Evidence:**
- The journal file is `<backup dir>/<prefix>revocations.journal`, found from "the effective
  `core.backup.dir`".
- That key belongs to the `core.backup` section of package `backup`, which lands at S8. At S4–S7 no
  core section declares it. ABO's `internal/backup`, which still writes archives, reads `backup_dir`
  from `AppConfig`.
- As written, the kernel cannot resolve the directory from S4: P4d step 2 has no input, and the two
  done-check tests cannot pass at S4. If the kernel defaults to "`backups` beside the DB", the journal
  sits away from the archives of an admin who moved them. That breaks "an off-host copy of the backup
  directory carries it", and S8 then has to move it.

**Fix:**
- Declare the restart-bound key `core.backup.dir` (alias `backup_dir`, `ProtectAdmin`) from S4 in the
  leaf `backup/journal`, or in the kernel's core-section list, with the default of §5B.7. `backup`
  adopts the declaration at S8.
- State it in 05-backup "Stages" and 06 §6.4.10 S4. Run `TestJournalReapplyUsesOverriddenBackupDir` from
  S4.

### COR4-11: MINOR. COR3-3 residue: the S2 `auth` leaf names `rbac` types that land at S4

**Where:** 05-authn §3.1 `User.Roles []rbac.RoleName` (`05-authn.md:116`), `Principal.Perms rbac.Checker`
(`:163`); 05-rbac landing (`05-rbac.md:75-78`: only `Permission`, `Requirement`, `Need`, `AllOf`,
`AnyOf`, `Public`, `AnyAuthenticated`, `Delegated` at S2); 04 §8.1 S2 (`04-package-mapping.md:719`);
05-http-api §3 (`05-http-api.md:147-151`).

**Evidence / Fix:**
- `auth.Principal` (S2 leaf) has `User *User`, `Assurance` and `Perms rbac.Checker`. `User` uses
  `rbac.RoleName`, `UserID` and `UserStatus`. `Checker` (§3.4) and `RoleName` (§3.3) are S4 members. Under
  06's rule ("absent, not stubbed") the S2 `auth` leaf does not compile.
- Add `rbac.Checker` (the interface only; it names nothing beyond the leaf types) and `rbac.RoleName`
  to the S2 rbac leaf list in 05-rbac, 04 §8.1 and 05-http-api §3.
- Add `User`, `UserID`, `UserStatus` and `Assurance` to the S2 auth leaf.

### COR4-12: MINOR. `rbac` and `core/internal/authz` import each other as specified

**Where:** 05-rbac §3 (`05-rbac.md:69-70`: "The compiled checker and the two privileged checker
constructors live in `core/internal/authz`"), §3.4 (`rbac.Service.Compile(in CheckerInput) Checker`,
`authz.AuthDisabled(cat *rbac.Catalog) rbac.Checker`, `authz.ForSystem(perms []rbac.Permission)
rbac.Checker`, `:287-289,337-343`).

**Evidence / Fix:**
- `Compile` (package `rbac`) must return the checker implemented in `authz`, so `rbac` imports `authz`.
  `authz`'s constructors take and return `rbac` types, so `authz` imports `rbac`. Go refuses the cycle.
- Keep the types in `rbac` and the implementation in `authz` (which imports `rbac`), and inject it:
  `rbac.Deps.Compile func(CheckerInput, *Catalog) Checker`, wired by the kernel to `authz.Compile`.
  `rbac` then imports nothing from `authz`, and SEC-26's "apps cannot construct a privileged checker"
  still holds.

### COR4-13: MINOR. 05-authn's lifecycle disagrees with the boot table and the landing table

**Where:** 05-authn §5 (`05-authn.md:977-986`), §4.3 (`:631-633`), §4.6 (`:678-681`); 06 write rule
(`06-app-sdk.md:1165-1174`), P5 (`:1200`), P7 (`:1202`); 04 §8.1 S5 (`tasks` lands at S5, depends on S4).

**Evidence / Fix:**
- **Prune tasks at S4.** §5 step 4 registers `core.auth.sessions.prune` and `core.auth.onetime.prune` on
  `tasks.ScheduleFromConfig`, but `tasks` lands at S5. The same holds for audit's kernel-registered
  prune job. State that both register from S5. Until then `c/auth/session/` and one-time tokens are
  pruned on read (expired rows are refused and deleted), so S4 has no unbounded growth.
- **Bootstrap mint.** "At every start … store hash + expiry in `c/auth/bootstrap`" (and the optional
  start-up read-only key, an `apikey:` row) is an every-boot store write at "before listeners open".
  06's write rule allows every-boot writes only in P4d and in `Executor.Run`, and 06 says P5 writes
  nothing. Move both into `auth.BootWrites` (P4d step 4), or name their phase in 06's write rule.
- **Compat methods at P5.** §5 step 2 attaches "module compat methods" in P5, but modules record them in
  P6 (`Registrar.AuthMethod`) and 06 hands them to `auth` at P7. Change 05-authn to P7.

### COR4-14: MINOR. COR3-4 residue: the order of two download events staged in one batch rests on an unstated ULID property, and a reorder silently loses the completion

**Where:** 05-events "Partitioning / order" (`05-events.md:450`: "rows taken in one scan are ordered by
their stage-time ULID"); 05-download §4.3 (`05-download.md:467-474`: recovery "stages `grab_added` and
then `completed` in that order in one batch"); 05-storage "key order says nothing about commit order"
(`05-storage.md:354-357`); 10 transition table and refusal rule (`10-tv-movies-proof.md:725-727,750,778`).

**Evidence / Fix:**
- Both intake rows of one batch commit together, so their order is their key order, which is their
  ULIDs.
- A ULID generator that is not monotonic orders two `Stage` calls within one millisecond by random bits.
  `completed` can then precede `grab_added`; the consumer refuses it on a `Grabbing` TD and acks it
  (10), and the TD stays `Downloading` with no import queued.
- State in 05-events 5E.4 that `Stage` takes IDs from one monotonic per-`Outbox` generator, so intake
  keys preserve `Stage` call order within a process. Add `TestStageOrderWithinBatch`.
- As defence in depth (COR3-4's alternative), let a `completed` on `Grabbing` apply
  `Grabbing → Downloading → ImportPending` in one transition.

### COR4-15: MINOR. Two built or deferred topics break 03's topic grammar

**Where:** 03 §3.6 Event topic row (`03-architecture.md:448`: `<moduleID>.<noun>[.<qualifier>].<verb-past>.v<N>`);
05-events (`05-events.md:156`), 05-download (`05-download.md:350,383`): `core.download.event.v1`;
05-notify (`05-notify.md:122,196`): `core.notify.deliver.v1`.

**Evidence / Fix:**
- `event` and `deliver` fill the `<verb-past>` slot. The kernel can only check the segment regex, so
  both boot, but they contradict the normative row that every other topic follows.
- Rename to `core.download.item.transitioned.v1` (noun `download`, qualifier `item`) and
  `core.notify.delivery.requested.v1` in 05-events, 05-download, 05-notify and 10. Alternatively, amend
  03's row to allow a noun-only command topic for durable work queues.

### COR4-16: MINOR. Derived-index residues: two spellings of the record key, and an unlisted write at Run (3a)

**Where:** 06 `Registrar.DerivedIndex` comment and §6.5.2 (`06-app-sdk.md:549,1173-1174,1235`:
`c/storage/derived/<name>`); 05-storage §5S.3.9 (`05-storage.md:1133`) and step 14 (`:1325`):
`c/storage/derived/<module>/<name>`.

**Evidence / Fix:**
- With module-relative names (`"hnsw"`), 06's spelling collides between two modules that both declare
  `hnsw`. Use 05-storage's `c/storage/derived/<module>/<name>` in 06.
- 05-storage says "the kernel starts the build kind on a version change, on a `Load` refusal and after
  every restore" at Run (3a). That enqueue writes a run row (for ABO, in the ops families through
  `legacystore`) before `Executor.Run` (step 5). 06's write rule lists only the derived records at (3a).
  Add "and the build run it enqueues", and state that `Executor.Start` before `Executor.Run` only queues.

### COR4-17: MINOR. A grant to a grantee absent from the build is inert in 05-storage but a boot failure in 06

**Where:** 05-storage §5S.3.11 Declaration (`05-storage.md:1202`: "a grantee absent from the build makes
the grant inert, logged"); 06 §6.6 (`06-app-sdk.md:1303`) and `TestStorageGrants` (`:1381`): "to an
absent owner … fail-closed at P2 (`ProblemGrant`)".

**Evidence / Fix:** One of the two rules must go. "Inert" is the one consistent with grants staying in
force for a disabled module and with optional core subsystems (`core/ai`, `core/providerhttp`). Make 06
say "a grant to an absent grantee is inert and logged". Keep `ProblemGrant` for a module grantee, an
unclaimed family, or a present grantee without an adapter.

### COR4-18: MINOR. The build ID has two sources

**Where:** 05-http-api stage 5 (`05-http-api.md:613`: "there is no ldflags value. Stage 5 writes, from S9,
`app.Options.UI.BuildID()` … before S9 the mount's `LegacyMount.BuildID`"); 06 `Options.BuildID`
(`06-app-sdk.md:722-737`: passed "to the Go link step (`-ldflags -X main.buildID=$BUILD_ID`)" and "httpapi
stage 5 writes it"); 07 (`07-ui-shell.md:799`); 09 S0 (`09-abo-on-core.md:565`).

**Evidence / Fix:**
- If there is no ldflags value, `Options.BuildID` is empty and P8's `ProblemBuildID` check is always
  disabled. If there is one, stage 5 has two candidate values.
- Pick 06/07/09's model (ldflags value in `Options.BuildID`; P8 compares it with the embedded ID; stage 5
  writes `Options.BuildID`, else the embedded ID, else `dev`), and fix 05-http-api §4.2 and its review
  note.

### COR4-19: MINOR. The deferred-members check reads the wrong files and matches bare identifiers

**Where:** 06 §6.4.9 (`06-app-sdk.md:1119-1124`: list generated from the headings of "06, 05-authn,
05-http-api, 05-rbac and 05-metadata"); 03 §3.4 (`03-architecture.md:315-318`: "Each 05 file lists its
deferred members under a 'Deferred-build' heading"); 05-download (`05-download.md:45,452,488`); 09 S7
done-check (`09-abo-on-core.md:719`).

**Evidence / Fix:**
- 05-rbac and 05-metadata have no "Deferred-build" heading. 05-download has two (§4.3, §4.5) and a
  deferred table, but is not a source. 05-ai's usage ledger has none. So the grab pipeline that the S7
  done-check wants absent is checked only by prose.
- A bare identifier match would also flag `audit.Event`, `events.Watch…` and others for download's
  deferred `Event` and `Watch`.
- Generate the list from every file's "Deferred-build" heading, add those headings to 05-download and
  05-ai, and match package-qualified identifiers (`download.Pool.Grab`, `download.Event`).

### COR4-20: MINOR. S2 landing residues of the config loader

**Where:** 05-config `ResolveStateDir` (`05-config.md:288-293`) and §1.7 (`:770-778`); 05-config
`ErrNoLedgerRow` (`:570-574`); 06 §6.4.10 S2b `CompatLedger` (`06-app-sdk.md:1137`); 09 S2 Config row
(`09-abo-on-core.md:621`), S2b Data/keys (`:633`); 06 `TestLegacyConfigHonoured` (`06-app-sdk.md:1382`);
09 S2 HTTP row (`09-abo-on-core.md:617`).

**Evidence / Fix:**
- **DB path at S2.** `ResolveStateDir` falls back to `filepath.Dir(core.storage.path)` "from the pre-boot
  layers". `core.storage.path` (with aliases `DATABASE_PATH`, `--db`) is a `storage` section key, and
  `storage` lands at S2b. Register `core.storage.path` as a kernel pre-boot key from S2. Otherwise, at
  S2, a Docker or launchd install without the explicit state-directory variable fails boot.
- **Ledger rows at S2.** Every alias "whose row is in neither [ledger] fails registration", but the
  ledger arrives at S2b. State that the ledger-row check starts at S2b.
- **Names.** 09 S2 says the loader is told through `Changes`; the API is `Rows.Changed()`. 09 S2b says
  "replacing `LegacyConfig.Load`", a method that does not exist (`Rows.Get`).
- **Test.** `TestLegacyConfigHonoured` asserts "Basic gate on" at S2 through config's layer, but the
  Basic gate stays in gin until S3 and reads `AppConfig` live. Drop the Basic-gate case at S2, or move it
  to the S3 test.

### COR4-21: MINOR. Normative Go and table residues (COR3-13, COR3-15)

| Item | Where | Fix |
|---|---|---|
| The `realtime` import block imports `rbac`, which the block uses only inside a comment (`RedactFunc` text): "imported and not used" is a compile error | `05-realtime.md:68-73,86` | drop the import, or use `rbac.Checker` in a declared type |
| 05-http-api §7 lists `time.Time` among `httpapi`'s reflector overrides; 03 R4 assigns `time.Time` to `jsonschemax.Base()` (SIMP3-1) | `05-http-api.md:895-897`; `03-architecture.md:352` | remove `time.Time` from httpapi's list |
| A blank line inside the §4.7 table splits it, so the `core.system.status-changed.v1` and `books.book.changed.v1` rows render as a header-less fragment (COR3-15's move left the gap) | `05-realtime.md:297-300` | delete the blank line |

### COR4-22: MINOR. The three grant lists disagree in detail

**Where:** 06 §6.11.6 comment and example (`06-app-sdk.md:1534-1557`); 09 §9.2.3 stage table
(`09-abo-on-core.md:292-299`) and S7 Data/keys (`:714`); 05-storage §5S.9.1 rows 12, 19
(`05-storage.md:1500,1508`); 09 S5 done-check (`09-abo-on-core.md:693`); 05-tasks tests (`05-tasks.md:1305`).

**Evidence / Fix:**
- 05-storage §5S.9.1 (normative) grants `core/ai` (`aijob*`, ReadWrite, Excl.) and
  `core/providerhttp` (`provider_*`, ReadWrite, Excl.) at S7, and `core/tasks` Read on the v1
  families at S5.
- 06's stage comment ("index S7") and 09's S7 grant row omit `core/ai` and `core/providerhttp`. 09
  S7 still says these families "stay books claims read through adapters until their owning spec adopts
  them", which is pre-R3-1 vocabulary contradicting the `Exclusive` grants.
- 09 S5 names `TestOpStateWriters`; 05-tasks renamed it `TestLegacyOpsWriters`.
- Generate 09's stage table and 06's comment from §5S.9.1 (the "Grant" column already carries the
  stage), replace the S7 sentence with a pointer to rows 19's grants, and use the current test name.
