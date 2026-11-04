<!-- file: docs/plans/platform-core/reviews/spec-round-3-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: c3e979ae-5f89-4c13-9373-ccc6b9573ce1 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 3: correctness and contracts

Lens: **correctness and contracts** (ID prefix `COR3`). Inputs: the revised spec in
`docs/specs/platform-core/` (03 v1.5.1 normative, 06 v1.3.1, 05-tasks v1.3.1, 05-storage v1.3.1,
05-events v1.3.0, 05-realtime v1.2.0, 05-http-api v1.3.1, 05-rbac v1.3.1, 05-config v1.3.1, 05-backup
v1.2.1, 09 v1.2.1, 10 v1.2.0), the round-1 and round-2 correctness reviews, and both disposition files
(round-1 cross-cutting resolutions and R2-1…R2-8 treated as binding). Code anchors are at `748fd787a`;
spec anchors are `file:line` in `docs/specs/platform-core/`.

Method: the four flows were traced again through the current Go blocks, every cross-file type, name
and signature on each path was looked up in its owning file, imports were checked against R4 (L0–L9)
and against the landing table of 04 §8.1 / 06 §6.4.10, and the `hello` blocks of 06 §6.12 were
checked line by line against the owning definitions:

1. ABO scan → `RunItems` → keyspace writes → outbox → realtime → UI.
2. Module route → `httpapi` → `rbac` → `audit`.
3. `app.New(books, hello)` through P−1…P8 (06 §6.5.1), per stage.
4. TV acquisition at proof depth, from grab through download events to the import.

**Verdict.** Round 2's contracts landed almost everywhere: projections, notify, retention, the relay
definition, schedule keys (all 16 declared with `tasks.ScheduleConfig`), `*storage.Batch`, `Describe`,
the module-kind rule for acquire and AI, and `hello` (it now compiles and boots against the current
definitions). What breaks now is mostly **stage-specific** and **ordering**:

- Between S2 and S3 r1 no boot phase can read `config_blob`, so the perimeter that moves to core at
  S2 r2 loses every UI-persisted value, the Basic gate included (COR3-1, BLOCKER).
- P4d re-applies the MAC'd revocation journal one step before the keyring and the DB overrides exist
  (COR3-2).
- COR2-9 is half-fixed: the S2 surface (`httpapi`, the `hello` skeleton, ABS `OwnAuth`) still names
  packages that land at S2b, S4 and S9 (COR3-3).
- acquire's single consumer reads five topics through five independent cursors, so tracked-download
  transitions arrive out of order (COR3-4).
- Module `Online` data migrations still run module writes under a core kind (COR3-5).
- Stage 9 loses HEAD's compression exclusions at S2 r2 (COR3-6).
- The derived-index fix is half-applied (COR3-7).

## Regression check (round-1 and round-2 BLOCKER and MAJOR, correctness lens)

| ID | Sev | Status in round 3 | Where |
|---|---|---|---|
| COR-1 | BLOCKER | Fixed (projection handle in `realtime.Registrar`, `Deps.Notify`). | — (landing stage of `Deps.Notify` wrong: COR3-3) |
| COR-2 | BLOCKER | Fixed. | — |
| COR-3 | BLOCKER | Fixed for ownership and recovery. The consumer side has a new ordering defect. | COR3-4 |
| COR-4, COR-5, COR-6 | MAJOR | Fixed. | — |
| COR-7 | MAJOR | Fixed (one ref-lock table, `RefLocker`, `Decide(par)`); naming residue. | COR3-8 |
| COR-8 | MAJOR | Fixed except the derived loader, which still runs before module hooks. | COR3-7 |
| COR-9 | MAJOR | Fixed for outbox consumers, acquire, AI and derived builds; not for module `Online` data migrations. | COR3-5 |
| COR-10 … COR-18, COR-21 | MAJOR | Fixed (re-verified: commit-ordered intake, raw consumers, retention, held runs, boot-record enablement, no audit hash chain, `hello` compiles, one `entity.Ref`, `internal/<capability>http`, scoped handles). | — |
| COR2-1 | MAJOR | Fixed (06:416-433, 05-realtime:87-96, 09:668). | — |
| COR2-2 | MAJOR | Fixed (`TopicSpec.Redact`, `tasks.RedactRunView`); a signature adapter is missing. | COR3-13 |
| COR2-3 | MAJOR | Fixed in shape; `Deps.Notify` is scheduled at S6 but `notify` lands at S7. | COR3-3 |
| COR2-4 | MAJOR | Fixed (05-events:437, `TestCoreOnlyTopicRetainedUntilAck`). | — |
| COR2-5 | MAJOR | Fixed (05-events:445-455, 05-tasks:607-611); the nudge names a method that collides with the lifecycle `Start`. | COR3-13 |
| COR2-6 | MAJOR | Fixed: every `ScheduleFromConfig` literal names a key its owner declares with type `tasks.ScheduleConfig`. | — |
| COR2-7 | MAJOR | Fixed as one table; the new P4d order is wrong for the journal. | COR3-2 |
| COR2-8 | MAJOR | Fixed (`StateDir`, `EnvPrefix`, `core.storage.path` from the `BootView`). | — |
| COR2-9 | MAJOR | **Half-fixed.** | COR3-3 |
| COR2-10 | MAJOR | Fixed for the two-consumer race; one consumer over five topics is still unordered. | COR3-4 |
| COR2-11 | MAJOR | **Half-fixed** (data migrations). | COR3-5 |
| COR2-12, COR2-13, COR2-14 | MAJOR | Fixed. | — |
| COR2-15 | MAJOR | **Half-fixed.** | COR3-7 |

## Flow traces (where each one breaks now)

| Flow | Path | Breaks at |
|---|---|---|
| 1 ABO scan → UI | `r.Tasks(books.scan)` → `RunItems(rc, tasks.CPU(), …)` → `ic.Writer().Do(ctx, w, fn)` → `Legacy.NewBatch()` + `outbox.Stage(d.Outbox, b, …)` + `d.Notify.Publish(ctx, b, n)` → `Commit` → `events.Publish(d.Events, ctx, BooksChanged, …)` → hub raw subscription → coalesce → per-subscriber filter → projection A (S6) / native stream (S9) | From S2 r2, stage 9 gzips the gin SSE route `/api/events` (COR3-6). S5 bridge is described as core→ABO in two files (COR3-9). Otherwise it composes. |
| 2 Route → RBAC → audit | `httpapi.Handle(r.HTTP(), Route{Perm})` → stage 12 `auth.Service.Middleware` (`rbac.WithChecker`, `audit.WithActor`) → stage 13 `Checker.Satisfies` → handler → `d.Audit.RecordIn(ctx, b, e)` | Composes from S4. At S2 the `httpapi` types do not compile and a `Public` route needs an undeclared allowlist (COR3-3); `Route.Strict` cannot default to true (COR3-13); one legacy path is both alias and gin route (COR3-12). |
| 3 `app.New(books, hello)` | P−1 → P0a → P0 → P1 → P2 → P3 → P4a–d → P5 → P6 → P7 → P8 → Run (1)…(8) | S2–S3 r1: blob unreadable (COR3-1). P4d step 2 before step 3 (COR3-2). Derived `Load` in P7 (COR3-7). Online module data migrations at Run step 5 under a core kind (COR3-5). |
| 4 TV acquire | `Engine.Grab(ctx, rc, d)` → `download.Pool.Grab(…, rec)` (stages `grab.added`) → `core.download.poll` (stages `item.completed`) → `core.acquire.download-events` (`ConsumeTx` + `Where`) → `tx.StartRun(<module>.acquire.import)` → `Importer.Import(…, ImportTx)` → `MarkImported` | `grab.added` and `item.completed` are on different topics with independent cursors (COR3-4). |

`hello` (06 §6.12, S9 state) was checked symbol by symbol against 05-rbac §3.1–§3.3, 05-http-api
§3.1–§3.3, 05-tasks 5T.3.1–5T.3.7, 05-storage §5S.3.1–§5S.3.5, 05-config §1.3 and 06 §6.4: every
identifier, signature and import resolves, the manifest declares `hello:settings.manage`, and the task,
route and migration pass the registration rules. Its S2 skeleton does not (COR3-3).

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 1 | COR3-1 |
| MAJOR | 6 | COR3-2 … COR3-7 |
| MINOR | 8 | COR3-8 … COR3-15 |

## Findings

### COR3-1: BLOCKER. From S2 to S3 r1 no boot phase can read `config_blob`, so core-owned keys lose their UI-persisted values; at S2 r2 the perimeter Basic gate silently turns off

**Where:** 09 S2 "Config" (`09-abo-on-core.md:587`) and S3 r1 (`:618`); 06 §6.5.1 P3 (`06-app-sdk.md:971`)
and "Stages" (`:981-986`); 06 §6.11.1 step 2 (`:1177-1178`); 05-config §1.4 step 3 (`05-config.md:571-574`)
and §1.12.1 stage 1 (`:823-834`); 04 §8.3 row `internal/config` (`04-package-mapping.md:915`); 05-http-api
S2 row (`05-http-api.md:884`) and §10 (`:1031-1032`).

**Evidence:**
- 09 S2 says the core loader "reads the **same** sources (YAML, env, `config_blob`) through a
  books-provided legacy source". No file defines that source: no type, no option, no phase.
- 06 P3 builds every section "from the operator layers and the boot record **only**", and the DB layer
  is attached at P4d step 3, which "does not exist" until S3 r2 (`:984-985`).
- At S2 the kernel has no storage, and books opens the store in a **start hook** (Run step 2), after
  P5 has built the services and P8 the router. HEAD loads the blob right after opening the store and
  before building the server (`cmd/root.go:364-365` → `internal/config/persistence.go:915,956`).
- S2 r2 moves the perimeter into core stages 0–11 and removes it from gin in the same PR, so stage 8
  reads `core.http.basic_gate.enabled` (alias `basic_auth_enabled`). That key is blob-persisted and
  admin-writable at HEAD (`internal/config/config.go:1578-1580`, `protected_fields.go:56-58`). The
  legacy middleware reads it live (`internal/server/middleware/basicauth.go:26`). The same holds for
  `enable_rate_limit` (`protected_fields.go:77`).
- Result as written: an install whose admin enabled the Basic gate (or changed the rate limiter, CORS
  origins or log level) in the UI boots S2 r2 with the file/env value. The perimeter gate is off, and
  stays off at runtime too, because UI edits still go to ABO's legacy config path and never reach
  core config.
- The S2 done-check tests the gate's **exempt set**, not its enablement source.
- From S3 r1 viper leaves the boot path and ABO reads config only through the façade over core handles
  (05-config:832-833). With no defined blob reader, every UI-persisted setting is at risk there. The
  S3 r1 done-check compares against S2b, which already lacks the blob.
- 04:915 moves the loader and the façade at S2, while 05-config and 09 S3 put the façade at S3 r1.

**Fix:**
- Specify the legacy source in 05-config: `type LegacyLayer interface { Load(ctx) (map[Path]json.RawMessage, error); Changes() <-chan struct{} }`,
  passed as `app.Options.LegacyConfig` until S3 r2. P3 applies it with the precedence of the DB override
  layer (unlocked keys only). A change signal republishes live keys, so a UI edit through ABO's legacy
  config API reaches stages 6/8/10 as it does today.
- Data source per stage:
  - **S2**: ABO's `serve` opens the store before `app.New`, as HEAD does, and hands it to
    `books.New(legacy)` and to the layer. Amend 06 §6.5.1 "Stages" and §6.11.1 step 2.
  - **S2b–S3 r1**: the kernel reads `setting:config_blob` read-only at P3 through books' claim. This is
    an explicit exception to P3's "no DB read", and it ends when P4d step 3 exists.
- Alternatively, keep the Basic gate and the rate limiter in gin until S3 r2 and move only the
  stateless perimeter stages at S2 r2.
- Align 04:915 with 05-config §1.12.1 (core loader for core sections at S2, façade at S3 r1).
- Add S2 done-check `TestPerimeterHonoursBlobOnlyValues` (gate enabled only in the blob → stage 8 on;
  rate limit disabled only in the blob → stage 10 off; a runtime edit through `PUT /api/v1/config` takes
  effect without a restart).
- Compare the S3 r1 effective-config golden against HEAD's, not S2b's.

### COR3-2: MAJOR. P4d re-applies the revocation journal before the keyring that verifies it and before the DB override that locates it

**Where:** 06 §6.5.1 P4 (d) steps 2–3 (`06-app-sdk.md:972`); 05-storage §5S.4 step 11 (b)/(c)
(`05-storage.md:1248-1249`); 05-backup §5B.3.6 "Format and integrity" and "Boot re-apply"
(`05-backup.md:673`, `:678`), §5B.4 (`:718-720`), §5B.7 `core.backup.dir` (`:787`); 05-config §1.4 steps
5–7 (`05-config.md:578-587`) and `DeriveKeyInUse` (`:1194-1198`, `:1253-1258`).

**Evidence:**
- Each journal line's `mac` is `HMAC-SHA256(DeriveKeyFor(kid, "core.backup/revocations/v1"), …)`, and
  re-apply uses only **verified** lines. `DeriveKeyFor` is a method of `*secrets.Store`, which
  `secrets.Open` returns at P4d **step 3**. The journal is "opened by the kernel … the derive function
  from `secrets`", but it runs at **step 2**.
- As written, step 2 has no key, so every line fails verification. The re-apply then "stops at a chain
  break", the boot only reports `revocation_journal_broken`, and a store replaced by a file copy or a
  checkpoint restore keeps credentials that were revoked after its boundary. That is the R2-2 / DATA2-5
  guarantee.
- The journal lives under `core.backup.dir`. That key is admin-protected and hot-reloadable, so it can
  be a DB override, and overrides are attached at step 3 after `secrets.Open`. Step 2 therefore reads
  the journal from the default directory when an admin moved backups in the UI.
- `secrets.Open` has the same defect: its `DeriveKeyInUse` scan of "the backup directory" runs before
  `AttachOverrides`. It can therefore retire a derive key that archives in the overridden directory
  still need, and those archives then fail their provenance MAC and can only be restored as foreign.

**Fix:**
- Reorder P4d to: 1 mirror rebuilds; 2 `secrets.Open` → configstore import → `AttachOverrides`;
  3 revocation-journal re-apply (the journal resolved from the effective `core.backup.dir`); 4 tasks
  reconcile; 5 post-restore hooks; 6 compat records.
- Apply the same order in 05-storage step 11 (letters), 05-config §1.4 step 6 ("after the mirror
  rebuilds, before the journal re-apply"), 05-backup §5B.3.6 and §5B.4.
- Take derive-key retirement out of `Open`: decide it in P5, once the effective config is known
  (the backup service calls `secrets.RetireUnused`).
- Add `TestJournalReapplyVerifiesWithKeyring` and `TestJournalReapplyUsesOverriddenBackupDir`, and
  extend `TestBootPhaseOrder_NoWriteBeforeCheckpoint` to assert the new order.

### COR3-3: MAJOR. COR2-9 half-fixed: the S2 surface still names packages that land at S2b, S4, S7 and S9, so `httpapi`, the `hello` skeleton and ABO's S2 mount do not compile or boot as specified

**Where:** 05-http-api §3 "Minimum surface" (`05-http-api.md:137-143`), `In` (`:270-275`), `Deps`
(`:451-458`), `ErrPublicNotAllowed` (`:391`, `:774`), stage 8 (`:573`), §8.2 admission (`:931`), §10
(`:1031-1032`); 06 §6.4.10 (`06-app-sdk.md:930-947`), §6.4.1 (`:131`, `:144-156`), §6.4.3 `AuthMethod`
(`:366-371`); 04 §8.1 (`04-package-mapping.md:679-688`); 03 §3.6 Module ID row ("a test asserts that it
equals this row exactly").

**Evidence:** `httpapi` and `hello` land at S2 (04:679). At that stage:

| Item at S2 | Needs | Lands |
|---|---|---|
| `httpapi.In.Principal *auth.Principal`; `Deps.Auth *auth.Service`, `Deps.RBAC *rbac.Service`, `Deps.Audit audit.Logger` ("nil until S4" still needs the types to compile) | `auth`, `rbac` service, `audit` | S4 |
| `httpapi.Deps.Compat *compat.Ledger` (05-http-api says `compat` is available at S2) | `compat` | S2b (04:680) |
| hello's S2 route `Perm: rbac.Public`, checked by `ErrPublicNotAllowed` against "the module's declared public list" | a declaration API | none exists (no `Manifest` field, no rule) |
| ABO's ABS routes: `OwnAuth` needs a compat method registered with `Registrar.AuthMethod(auth.MethodSpec)`, without which they lose today's Basic-gate exemption (stage 8) and `TestBasicGate_ExemptSetEqualsHEAD` fails, or `Build` refuses with `ErrOwnAuthMethod`. 05-http-api:931 says "the kernel records method declarations from S2, before `auth` exists", but no S2 API or type does that | `auth.MethodSpec`, `Registrar.AuthMethod` | S4 (06:935) |
| `module.ReservedIDs()` calls `ui.ReservedSegments` (06:144-156); 03 requires equality with the full row | `ui` | S9 |
| `core.http.basic_gate.password` typed `secrets.Ref` | `secrets` | S3 |
| 06 §6.4.10 S2 row: `app.Options` "all but `UI`, `Authenticators`", i.e. including `Adapters []integrations.Descriptor` and `Updater *updater.Options` (its own S7/S8 rows add them later) | `integrations`, `updater` | S7, S8 |
| 06 §6.4.10 S6 row: `Deps.Notify notify.Publisher` | `integrations/notify` | S7 (04:685-686, 09:665) |
| `storage/datamigrate`, `internal/storagemigrate`, `internal/jsonschemax` (config's reflector at S2), `internal/logjournal`, `internal/uihttp`, `internal/eventhttp` | — | in no row of 04 §8.1 |

**Fix:**
- Land an `auth` leaf at S2 next to rbac's (`auth.Principal`, `auth.MethodSpec`, `auth.Method`). Make
  `httpapi.Deps.Auth/RBAC/Audit` S4 additions, or interfaces declared in `httpapi`.
- Land `compat`'s counters at S2, or add `Deps.Compat` at S2b.
- Either define the native Public list (`module.Manifest.PublicRoutes []string`, route names, checked
  by `Build`) and declare `getGreeting` in hello's S2 skeleton, or delete `ErrPublicNotAllowed` and rely
  on `TestOwnAuthRoutesGolden`-style golden review of public routes.
- Make `Registrar.AuthMethod` (name and `auth.MethodSpec` leaf) an S2 member, recording only, so
  `OwnAuth` admission holds from S2. Otherwise state that at S2–S3, under `LegacyAuthGin`, the stage-8
  exemption follows `LegacyRoute.OwnAuth` unadmitted and admission starts at S4. Fix 05-http-api:931
  accordingly.
- Land `ui.ReservedSegments` as a pure-function leaf at S2, or start the 03 equality test at S9 and
  hard-code the shell list in `module` until then.
- Type the S2 basic-gate password as a plaintext env/file value until S3.
- Change 06 §6.4.10: S2 `Options` excludes `Adapters` and `Updater`; `Deps.Notify` moves to the S7 row.
- Add the six packages to 04 §8.1: `datamigrate`/`storagemigrate` S5, `jsonschemax` S2, `logjournal`
  with its first consumer, `uihttp` S4, `eventhttp` S6.

### COR3-4: MAJOR. acquire's "one consumer" reads five topics through five independent cursors, so tracked-download transitions can apply out of order, and a refused transition blocks its partition

**Where:** 10 §10.5.4 consumer paragraph (`10-tv-movies-proof.md:710-720`), transition table
(`:737-745`), §10.5.5 "Illegal state transition" (`:767`), §10.5.6 (`:791-792`); 05-download §4.3
(`05-download.md:438-449`), §4.5 step 3 and routing table (`:476-487`); 05-events `ConsumeTx` (`05-events.md:350`),
cursor and try keys (`:421-422`), ordering (`:431`, `:433`), retry (`:435`), relay workers (`:460-462`).

**Evidence:**
- `ConsumeTx[T](t Topic[T], consumer, h, opts...)` takes **one** topic. The consumer
  `core.acquire.download-events` is declared over `core.download.grab.{added,failed}.v1` and
  `core.download.item.{completed,failed,removed}.v1`.
- Cursors are `c/outbox/cur/<consumer>/<topic>/<pp>`, topics are "sequenced independently", and the
  relay delivers (consumer, partition) pairs in parallel. Nothing orders `grab.added` against
  `item.completed`.
- `grab.added` is staged by `Pool.Grab` after `Add` returns, or by a later poll's recovery
  (`grab.added.v1{Recovered: true}`). `item.completed` is staged by the poll when it sees the item done.
  A cached torrent or usenet job, or a recovered grab whose item is already complete, can therefore
  deliver `item.completed` while the TD is still `Grabbing`.
- `Grabbing → ImportPending` is not in the table, so the transition is refused with
  `ErrIllegalTransition`.
- 10 does not say whether the handler acks or errors on a refused transition:
  - Error: the message is retried with backoff and head-of-line-blocks every later download event in
    that partition for that consumer, then dead-letters a valid completion after `MaxAttempts` (20).
  - Ack: the completion is lost, and the TD sits in `Downloading` forever (no import is staged).
- 05-events never says that several handlers may share one consumer name.
- TD rows are also written by the import run (`ImportTx`) and by user actions (a keyed mutex plus CAS).
  `c/acquire/tracked/` is not declared `Serialized`, so the relay-batch transition and an import-run
  write are not mutually checked.

**Fix:**
- Put the whole download lifecycle on **one** durable topic, `core.download.event.v1{Kind: grab_added |
  grab_failed | completed | failed | removed, Owner, Refs, GrabID, ItemRef, …}`, keyed by `GrabID`
  (foreign items: `ItemRef`). One cursor per partition then gives per-key order. The bus companion
  `item.changed` stays.
- Update the deferred-topic table (05-events:152-156), 05-download §4.3/§4.5 and 10 §10.5.4.
- Alternatively, make the transition function order-tolerant: `completed` on `Grabbing` applies
  `Grabbing → Downloading → ImportPending`, and a `grab.added` that arrives after `Downloading` is
  acked as stale.
- Either way, the handler **acks** a stale or illegal transition, logs it and counts
  `mediacore_acquire_transition_refused_total`. It never returns an error for it.
- Declare `tracked/` `Serialized`, and have every TD writer use `GetForUpdate`.
- If several handlers may share one consumer name, state that rule in 05-events §5E.4.
- Add `TestCompletedBeforeGrabAddedStillImports` and `TestRefusedTransitionDoesNotBlockPartition`.

### COR3-5: MAJOR. COR2-11 residue: a module's `Online` data migration runs module writes under the core kind `core.storage.migrate`

**Where:** 05-storage §5S.3.6 (`05-storage.md:722-726`, `:774-775`, `:791-794`); 05-tasks 5T.3.12
"Module writes run only under module kinds" (`05-tasks.md:952-961`), 5T.3.1 (`:220-221`), 5T.4 step 0
(`:976-979`); 06 `Manifest.DataMigrations` (`06-app-sdk.md:250-253`), §6.5.2 step 5 (`:998`).

**Evidence:**
- Modules declare `DataMigration`s in their manifest, and books must, because
  `TestNoNewLegacyMigrations` makes every large books rewrite a `DataMigration`.
- An `Online` one is a run of the core kind `core.storage.migrate` with resource
  `{core, storage-range, <id>}`, and that run calls the module's `Step`, which writes module keys.
- 05-tasks forbids exactly this: "A core run never invokes a module callback that writes module
  entities"; a core kind cannot declare the module's resources.
- The dispatch conflict rule therefore never serialises an online rewrite against the module's own
  writers (`books.*` runs).
- Precondition protection exists only "over a `Serialized` range". Most books families are not
  `Serialized`, so a rewrite and a concurrent book edit race on the same row with last-writer-wins.

**Fix:**
- Run a module's `Online` data migration as a **module** kind,
  `<module>.datamigrate.<name>`, which the kernel registers on the module's behalf (as for derived
  builds).
- Add `Writes []entity.Ref` to `DataMigration`, naming the resource kinds the rewrite touches, and use
  it as the kind's `Effects` (plus `Subjects` per shard).
- Keep `core.storage.migrate` for core owners only.
- Alternatively, require `Online` migrations to write only through `SetIfUnchanged`/`SetIfAbsent`
  (every write, any range) and state that this is the concurrency contract.
- Update 05-storage §5S.3.6, 05-tasks 5T.4 step 0 and 06 §6.5.2 step 5.

### COR3-6: MAJOR. Stage 9 compression drops HEAD's per-path exclusions when the perimeter moves at S2 r2 (SSE `/api/events`, the log download, accept-invite)

**Where:** 05-http-api §4.2 stage 9 (`05-http-api.md:574`), §8.1 S2 row (`:884`), §8.2 `LegacyRoute`
(`:913-921`); 05-realtime §4.1 (`05-realtime.md:182`) and §4.9 (`:358-360`); code
`internal/server/server.go:1590-1606` and `:719-725`.

**Evidence:**
- HEAD's compressor excludes `/api/events` ("an SSE stream; a compressor buffers it"), `/metrics`,
  `/api/v1/auth/accept-invite` (body read deadline), and
  `^/api/v1/operations/v2/[^/]+/logs/download$`. The last one is a production bug fix: double-gzipped
  log files that `gunzip` rejects.
- Core's stage 9 is "skipped for `HandleRaw` streams and `/metrics`" only, and it applies to "all".
- At S2 r2 gzip is removed from gin, and every legacy manifest route goes through stage 9:
  - stream A (`/api/events`, a gin route at S2 and a projection served from a gin manifest route at S6)
    is buffered;
  - the log download regains its corruption;
  - accept-invite loses the raw response controller that HEAD's comment depends on.
- `LegacyRoute` has no field to opt out, and stage 9's scope is "all".

**Fix:**
- Add `LegacyRoute.NoCompress bool`, set on the five HEAD exclusions, and stage 9 also skipping any
  response whose `Content-Type` is `text/event-stream`.
- The core compressor's writer implements `Unwrap` and `Flush`.
- S2 done-check `TestCompressionExclusionsEqualHEAD`: a golden generated from `compressionMiddleware`'s
  two lists, like `TestBasicGate_ExemptSetEqualsHEAD`.
- Carry HEAD's access-log `SkipPaths` for the two operations streams (`server.go:696`).

### COR3-7: MAJOR. COR2-15 half-fixed: the derived loader still runs module code inside `app.New`, before the module's Start hooks, and the build kind cannot be registered from `DerivedHooks`

**Where:** 06 P7 (`06-app-sdk.md:975`), `Registrar.DerivedIndex` (`:457-465`), §6.5.2 start step (3a) and
invariant (`:993-1001`), `Describe` (`:714-721`), `TestDescribeInMemoryNoIO` (`:1136`); 05-storage
§5S.3.9 (`05-storage.md:1051-1062`, `:1072-1077`), §5S.4 step 13 (`:1257-1258`); 05-tasks 5T.3.1
required fields and registration refusals (`05-tasks.md:127-133`, `:217-228`), Task timeout default
(`:556`).

**Evidence:**
- **Load timing.** 06 P7 runs "the derived-index version/generation check and `Load`" as the last step
  of P7, inside `app.New`. `Load` is a module closure over services built in `Register`.
  - 06 §6.5.2 says "No bus handler, outbox consumer or other module callback can run before its
    module's hooks have started", and 05-storage:1076-1077 says "the derived loader … start[s] after
    module hooks". Both contradict a P7 `Load`.
  - Books' server construction moves into start hooks (06 §6.11.1 step 4). Any `Load` that needs a
    hook-built object (books' hnsw store, `server.go:845-866` truth count) runs before that object
    exists.
  - `Describe` runs P0–P7, so it calls `Load`, does snapshot I/O, and stages a `Build` run,
    contradicting `TestDescribeInMemoryNoIO`.
- **Build kind.** `DerivedHooks{Build, Snapshot, Load}` carries none of the fields a definition must
  have: `Title`, `Effects`, `Perms.Start` and `Resume` (zero refused), plus `Timeout` and `Parallelism`
  for a library-scale rebuild. Yet the registrar "registers [the kind] on the module's behalf with the
  module's own Effects", a phrase no file defines.
  - Read literally, P7 refuses the synthesised definition, and books' S7 build (memdb, hnsw) does not
    boot.
  - With invented defaults, a large hnsw rebuild hits the Task default `Timeout` of 30 min, which is
    terminal, and a `Blocking` index then keeps readiness false.
- `DerivedIndex.Name` examples carry the module prefix (`"books.hnsw"`), which yields the kind
  `books.derived.books.hnsw.build`.

**Fix:**
- Move the version/generation check, `Load` and the `c/storage/derived/<name>` record from P7 to Run
  start step (3a), before `Bus.Open`. `Describe` skips it.
- Correct 06 P7, 05-storage step 13 and §5S.3.9 to match.
- Replace `DerivedHooks.Build` with `BuildTask tasks.Definition`, which the module builds through a
  core helper `storage.DerivedBuildTask(name, spec)` taking `Title`, `Effects` (`ReadOnly` over named
  module resources), `Perms.Start`, `Timeout` (default 24 h, as Batch) and the rebuild's
  `tasks.Parallelism`. Alternatively, add those fields to `DerivedHooks`. P7 then checks the kind name
  `<ID>.derived.<name>.build`.
- Make `Name` module-relative (`"hnsw"`).

### COR3-8: MINOR. Core→module run starters have three names, and the AI apply kind three spellings

**Where:** 05-tasks `Stager` (`05-tasks.md:867-876`, "`<module>.ai.batch_apply`" at `:868`) and Engine.Grab
note (`:479`); 05-ai (`05-ai.md:371-379`, `:491`); 06 (`06-app-sdk.md:442-445`); 10 (`10-tv-movies-proof.md:526-536`,
`:707`, `:716`); 05-events (`05-events.md:336`, `:457`).

**Evidence / Fix:**
- 05-tasks says core services hold a `tasks.Stager`, "never the Executor". 05-ai and 10 use "the
  `Executor.StartIn` handle the kernel gives the service", and 06 says `core.ai.batch-poll` starts it
  "through `Client.Start`". Use `tasks.Stager.StartIn` in all four files, and keep `Executor.StartIn`
  only for the relay.
- The AI kind is `<module>.ai.batch_apply` in 05-tasks, which `ParseKind` refuses because of the
  underscore, `<module>.ai.batch-apply` in 06, and `<module>.ai.batch-apply.<name>` in 05-ai. Use 05-ai's
  spelling everywhere.
- 05-tasks:479 says `Engine.Grab` "takes `tasks.LockRefs(ctx, refs...)`", while 10 passes a
  `RefLocker` (the caller's `rc`). Align 05-tasks with 10's signature.

### COR3-9: MINOR. The S5 legacy-stream bridge is described as core → ABO in two files (an R1 violation) and as books-side in a third

**Where:** 05-tasks 5T.4 step 3 (`05-tasks.md:999-1003`); 09 S5 (`09-abo-on-core.md:655`); 05-realtime §4.9
FE2-3 table (`05-realtime.md:388`).

**Evidence / Fix:**
- 05-tasks says "the executor also publishes every run change onto ABO's legacy `registry.EventHub`
  … (adapter in `internal/opsbridge`)". 09 says "`core/tasks` publishes every run change to ABO's legacy
  `registry.EventHub`" and names only streams A/B.
- Core cannot import ABO (R1, `TestCoreImportsOnlyCore`). 05-realtime has the implementable version: a
  books bus subscriber on `core.task.*` (registered with `Registrar.Subscribe` at S5) feeds streams A,
  B and C.
- Reword 05-tasks and 09 to that version, list all three streams, and add the S5 done-check
  "streams A, B, C show live progress".

### COR3-10: MINOR. `Manifest.TaskStarts` is validated by three different rules; 06's rule fails boot when the target module is disabled

**Where:** 05-tasks 5T.3.1 (`05-tasks.md:227-228`) and 5T.3.10 (`:860-861`); 06 `Manifest.TaskStarts`
(`06-app-sdk.md:224-232`), §6.6 (`:1058`), `TestModuleSystemActor_CannotStartUndeclaredKind` (`:1143`).

**Evidence / Fix:**
- 05-tasks 5T.3.1 checks against "registered kinds plus every manifest's task-ID ledger", which
  includes disabled modules. 5T.3.10 says "validated to exist at **P1**", where no kind is registered
  yet. 06 refuses at P7 "a kind that no **enabled** module or core registers".
- Under 06's rule, disabling module B fails the boot of module A when A lists one of B's kinds, which
  contradicts G1 ("disabling a module never fails boot").
- Adopt 5T.3.1's rule at P7 (registered ∪ every manifest's `TaskLedger`). A start of a disabled
  module's kind is refused at runtime with `unknown kind` (06's O3). Fix 06's comment, its §6.6 row and
  its test text, and 5T.3.10's "at P1".

### COR3-11: MINOR. The operator dial `core.tasks.kinds.<kind>.parallelism_cap` is not a valid config path for most kinds

**Where:** 05-tasks §5T.10 (`05-tasks.md:1246`), §5T.3.2 (`:273-275`); 09 (`09-abo-on-core.md:413`, `:653`,
`:939`); 05-config `Path` grammar (`05-config.md:86-90`).

**Evidence / Fix:**
- Config path segments are `[a-z][a-z0-9_]{0,47}`. Task kinds contain dots and hyphens
  (`books.dedup.full-scan`, `core.acquire.rss-sync`), so the key cannot be declared or set. The kind in
  CLAUDE.md's single-core incident is exactly such a kind.
- Declare `core.tasks.kinds` as one map-typed key (`map[tasks.Kind]KindDial{ParallelismCap int}`), with
  env as a JSON value. Alternatively, define an encoding (`.` → `__`, `-` → `_`) and state it in
  05-config §1.3.6.

### COR3-12: MINOR. `PUT /api/v1/auth/me/password` is specified as both an alias and a gin route

**Where:** 05-http-api §4.6 (`05-http-api.md:653-662`); 09 S4 "Alias targets" (`09-abo-on-core.md:638`);
`ErrDuplicateRoute`/`ErrAliasTarget` (`05-http-api.md:385-393`).

**Evidence / Fix:**
- 09 aliases the path to `PUT /api/core/v1/me/password` "without `user_id`" and keeps "with `user_id`"
  as a gin route whose pattern "is not deleted".
- One ServeMux pattern cannot be both: `Build` fails with `ErrDuplicateRoute`. An alias `Targets` list
  can only name native routes of the group, so the selector cannot hand the `user_id` case to gin.
- Pick one: keep the whole path a gin route calling the dominance-checked `auth.Service` for both
  cases, with no alias; or add a native `PUT /api/core/v1/users/{id}/password` as the second alias
  target, with selector `user_id`.

### COR3-13: MINOR. Normative Go blocks with compile or type errors

**Where / Evidence / Fix:**

| Item | Where | Fix |
|---|---|---|
| `tasks.Executor` embeds `Client` (`Start(ctx, kind, req)`) and declares `Start(ctx) error`: a duplicate method, so the interface does not compile; 05-events then nudges the relay "through `Executor.Start` with `Mode: ModeLive`" | 05-tasks:774-778, :879-891; 05-events:455 | rename the lifecycle methods `Run`/`Shutdown`; the nudge calls `Executor.Start(ctx, "core.outbox.relay", StartRequest{Mode: ModeLive})` |
| `Route.Strict bool // default true`: a Go `bool` cannot default to true, so every route that omits it is non-strict, and aliases decode leniently, which reopens SEC2-5's retargeting | 05-http-api:175, §3.4 | invert to `Lenient bool` (zero = strict) |
| The `realtime` import block lacks `errors`, `log/slog`, `compat`, `metrics`, all used by `Deps` and the error vars | 05-realtime:60-69, :114-122, :156-161 | complete the block |
| `tasks.RedactRunView(who, RunView) RunView` is set "as the `Redact` function", but `events.RedactFunc` is `func(rbac.Checker, json.RawMessage) json.RawMessage` | 05-tasks:818-828; 05-events:101 | add `tasks.RunViewRedactor() events.RedactFunc` (decode, redact, re-encode) and name it in the catalog row |
| `StartRequest{Mode: ModeLive, Params: TD.ID}`: `Params` is `json.RawMessage` | 10:715 | `Params: mustJSON(ImportParams{TD: td.ID})` |
| `Topic.Spec` uses "the JSON Schema reflector owned by 05-config", but R4 makes it `internal/jsonschemax` (L0) | 05-events:77-78 | point to `internal/jsonschemax` |

### COR3-14: MINOR. The migration DAG's tie-break list omits core owners that have steps, so boot order is non-deterministic

**Where:** 05-storage §5S.3.5 "Order" (`05-storage.md:702`); 09 S7 adopt step `core/integrations#1`
(`09-abo-on-core.md:680`).

**Evidence / Fix:**
- Ties are "broken by the kernel's fixed core subsystem list (`storage`, `secrets`, `config`, `auth`,
  `rbac`, `audit`, `tasks`, `outbox`, `library`, `backup`, `updater`, `health`, `log`)". That list lacks
  `integrations`, whose `core/integrations#1` adopt step ships at S7, and `compat`, `index`, `download`,
  `ai`, `notify` and `acquire`.
- Complete the list, and state that an owner absent from it fails registration
  (`ErrMigrationCycle`-class "unordered owner").

### COR3-15: MINOR. Boot-table bookkeeping and stale text

| Item | Where | Fix |
|---|---|---|
| The write rule lists "seeded grants" among writers that run "in the order of P4d", but grants are seeded at P7. P7's "Store writes" column omits the def snapshots `c/tasks/def/<kind>` (05-tasks 5T.4 step 1) | 06:960-962, :975; 05-tasks:980-983 | list P7's writes (seeded grants, def snapshots, derived records) in the P7 row, and drop "seeded grants" from the P4d sentence |
| "still to add to 06 (open item 6): `r.ChangeSink`": 06 already has it | 05-tasks:213 | delete the line |
| The "Successor scope" paragraph is inserted inside the §4.7 table, so its last two rows render as text | 05-realtime:292-299 | move the paragraph below the table |
