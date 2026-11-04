<!-- file: docs/plans/platform-core/reviews/spec-round-1-correctness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a52e47f-dd43-443c-868b-407c665a3b42 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 1: correctness and contracts

Lens: **correctness and contracts**. Question asked: do the Go interfaces actually work for ABO, TV
and Movies? Method: five flows were traced end to end through the Go blocks of 03, 05-*, 06, 08 and 10.
Each signature was checked against the file that owns its type, and each import against the R4 table
in 03 v1.2.0. Code anchors are at `748fd787a`. Spec anchors are `file:line` in
`docs/specs/platform-core/`.

**Verdict.** As written, the contracts do not compose. Three things block the design: (1) modules
cannot reach several services the flows need, (2) `storage` and `storage/index` import each other,
and (3) `05-download` and `10/acquire` each claim ownership of download tracking and completion.
Most of the other findings are places where the two sides of one call disagree. The `hello` example
in 06, which is the G6/G7 proof, does not compile against the owning 05 files.

## Flow traces (where each one breaks)

| Flow | Path through the contracts | Breaks at |
|---|---|---|
| 1 ABO scan → UI | `books.Register` → `r.Tasks(tasks.Task/Batch…)` → `RunItems(rc, tasks.CPU(), …)` → `ic.Writer().Do(…)` → `Deps.Storage.Legacy` batch + `index.MarkDirty(b)` → `events.Publish` → realtime `Bridge` → SSE `/api/core/v1/events` → `useEventStream` | `storage`/`index` cycle (COR-2). The Writer journal has no `ChangeSink` registration path (COR-1). Books cannot reach `realtime.Register`/`Bridge`/`Projection` (COR-1). The bus has four task topics but realtime has one, with a 1:1 Bridge (COR-20). |
| 2 TV RSS → import → history | `core.acquire.rss-sync` → `indexer.Pool.RSS` → `MediaType.Parse/Map` → `Engine.Decide` → `Service.Grab` → `download.Pool.Grab(GrabRequest)` → completion → `Importer.Import` → history by `entity.Ref` | Modules get no `acquire.Service`, `indexer.Pool` or `download.Pool` (COR-1). Two pollers and two completion paths (COR-3). The "same batch" history write cannot be expressed (COR-4). Concurrent search and RSS can double-grab (COR-7). Single `events.EntityRef` vs `[]entity.Ref` (COR-17). |
| 3 Movies availability, cutoff | `movies` spec `MinimumAvailability` → `Target.Held()` → `QualityProfile.Upgrade/CutoffMet` → `movies.search.cutoff-unmet` | Held files' format score cannot be computed (COR-5). The wanted index freezes a time-dependent predicate (COR-6). |
| 4 Module route + RBAC + audit | `httpapi.Handle(r.HTTP(), Route{Perm: rbac.Need(...)})` → auth middleware (`rbac.WithChecker`, `audit.WithActor`) → stage 12 `Satisfies` → handler → `audit.RecordIn(ctx, b, …)` | Mostly works. The `RecordIn` hash chain breaks on discarded or reordered batches (COR-15). Type names disagree between 06 and 05-rbac (COR-16). Core routes have no layer-legal home (COR-18). |
| 5 `app.New(opts, tv, movies)` | P1 → P2 → … → P8; one `acquire.Service`, two `MediaType`s, two facades | Enablement is read before the layer that stores it (COR-14). The acquire service has a chicken-and-egg construction (COR-1). One rss-sync cannot serve both RSS types (COR-25). Disabling a module trips the op ledger gate (COR-13). |

## Summary

| Severity | Count | IDs |
|---|---|---|
| BLOCKER | 3 | COR-1, COR-2, COR-3 |
| MAJOR | 16 | COR-4 … COR-18, COR-21 |
| MINOR | 8 | COR-19, COR-20, COR-22 … COR-27 |

## Findings

### COR-1: BLOCKER. Modules cannot reach services the flows depend on (`module.Deps` and `Registrar` gaps)

**Where:** 06 §6.4.3 (`06-app-sdk.md:236-331`) and §6.4.4 (`06-app-sdk.md:334-373`).

**Evidence:** The other contracts say a module reaches the services below through `module.Deps` or
`Registrar`. Neither 06 struct has a field or method for any of them.

| Needed by | Claimed path | In 06? |
|---|---|---|
| TV/Movies `Register` calls `acquire.Register[T](svc, mt)` (`10-tv-movies-proof.md:769`) | "`acquire.Service` … handed to modules via module.Deps" (`10-tv-movies-proof.md:876`) | no `Deps.Acquire` |
| Module search tasks need `indexer.Pool.SearchAll` (`10-tv-movies-proof.md` §10.5.4 item 4, `05-indexer.md:228`) | none | no |
| books calls `Pool.Relocate`/`Items`/`Files`/`MarkImported` (`05-download.md` §10, §4.5). Today: `internal/plugins/deluge/path_update.go:167` (`MoveStorage`) | none | no `Deps.Downloads` |
| books registers `books.book.changed.v1` and the legacy projections (`05-realtime.md:244`, §4.8) | `realtime.Register(h Registrar, …)`, where the registrar is one "only the kernel builds" (`05-realtime.md:99`), and `Hub.Projection` | no `r.Realtime()`, no hub handle |
| books' undo journal (`opchange:`) | `r.ChangeSink(s)` (`05-tasks.md:217`, open item `:932`) | no |
| books' Open Library mirror (today a separate `pebble.Open`, `internal/openlibrary/store.go:43`) | `AuxOpener.OpenAuxiliary`, "exposed to modules through module.Deps" (`05-storage.md:584-589`) | `storage.Handles` has no `Aux` |
| completion and event consumers | `Registrar.OutboxHandler` (`05-events.md:182,280`; `05-download.md:304`) | no. 06 says `outbox.Consume(d.Outbox, …)` (`06-app-sdk.md:269`), but `outbox.Consume` takes no Outbox and returns a `Handler` value (`05-events.md:186`) that nothing accepts |
| books AI calls | `ai.Router` | no `Deps.AI` |
| backup `Ensure` | "module.Deps.Backup" (05-backup Go block) | no |

There is also a construction cycle. The kernel builds `acquire.Service` "if any module registers a
media type" (`10-tv-movies-proof.md:937`). Registration happens inside `Register` (P6), and
`Register` needs the service to exist first.

**Fix:**
- Add these to `module.Deps`: `Acquire *acquire.Service`, `Downloads *download.Pool`,
  `Indexers *indexer.Pool`, `AI *ai.Router` and `Backup backup.Ensurer`. Add `Aux storage.AuxOpener`
  to `storage.Handles`. Each one stays non-nil, as the "never nil" rule requires.
- Add these to `module.Registrar`: `Realtime() realtime.Registrar`, `ChangeSink(tasks.ChangeSink)`,
  `OutboxHandler(...outbox.Handler)`, and `OutboxTopics(...outbox.TopicSpec)` (see COR-11).
- Add a manifest field `Acquire *AcquireDecl{Vocabulary, Categories}`. In P5 the kernel builds the
  service when any enabled manifest declares it. In P6, `acquire.Register[T]` only attaches the
  typed hooks.
- Update §6.14 so that every row names the exact owning symbol.

### COR-2: BLOCKER. `storage` and `storage/index` import each other

**Where:** 05-storage §5S.3. `storage.Handles{Indexes index.Opener}` is in package `storage`
(`05-storage.md:327-331`). `index.Index.MarkDirty(b *storage.Batch, id string)` is in package
`index` (`05-storage.md:520`).

**Evidence:** Go forbids import cycles, so the foundational L1 package does not compile as
specified. `module.Deps.Storage` is typed `storage.Handles`, so every module depends on the broken
pair.

**Fix:** Move `Handles` out of `storage`, either into `module` as
`module.StorageHandles{Keyspace, Legacy, Indexes, Aux}` or into a leaf `storage/handles` package.
Keep `index → storage`. State in R4 that `storage/index` may import `storage` and the reverse is
forbidden, and add that edge to the core layering test.

### COR-3: BLOCKER. Two owners for download tracking, recovery and completion (05-download vs 10/acquire)

**Where:** 05-download §4.3–§4.5 (`05-download.md:374-413`) and 10 §10.5.3–§10.5.5
(`10-tv-movies-proof.md:780-830, 955-956, 992`).

**Evidence:** The same job is specified twice, and the two versions conflict at every step.

| Concern | 05-download | 10/acquire |
|---|---|---|
| Poller | task `core.download.poll` (`:399`) | task `core.acquire.refresh-downloads`, "poll every enabled client's items" (`:955`) |
| Grab intent row | `c/download/grabs/by-key/<key>` `pending` → `added` (`:376-380`) | `StateGrabbing` row in `c/acquire/tracked/<id>` |
| Crash recovery | core tag `mc:<grabID>`, then hash, then name (`:381`) | "owner tag = tracked-download ID", then hash, then title (`:992`, G2) |
| Completion | outbox `core.download.item.completed.v1`. "The owning module's outbox handler imports … then calls `Pool.MarkImported`" (`:411`) | acquire's own completed check → `core.acquire.import` → `Importer` (`:956`). `MarkImported` is never called |
| Owner identity | `GrabRequest.Entity events.EntityRef`, single, and `Entity.Module` must be a registered module (`:103, :376`) | `TrackedDownload.Refs []entity.Ref`, owned by core |

If both are implemented, every client is polled twice, two intent rows are written that are not in
one batch, a season pack's completion goes to `tv`'s handler and skips the TD state machine, and an
import can run twice. There is no consistent reading of flow (2).

**Fix:** Give tracking to `download.Pool` alone, and make acquire a consumer of it.
- Delete `core.acquire.refresh-downloads` (acquire calls `Pool.Refresh`).
- Change `GrabRequest.Entity` to `Owner string` (`"core.acquire"` or a module ID) plus
  `Refs []entity.Ref`, and let `Owner == "core.acquire"` pass validation.
- Have acquire set `IdempotencyKey = TD.ID` and store the `GrabID`.
- Have acquire consume `core.download.item.{completed,failed,removed}.v1` with `ConsumeTx`, applying
  the TD transitions in `c/acquire/` in the relay batch, and start `core.acquire.import` with
  `ConsumeIntoTask`.
- Have acquire call `Pool.MarkImported` after the import commits.
- Rewrite 05-download §4.5 to say that items whose `Owner` is a module go to that module, and items
  owned by acquire go to acquire.

### COR-4: MAJOR. The "history write in the same batch as the module's file write" cannot be expressed

**Where:** 10 §10.5.5 (`10-tv-movies-proof.md:999`), §10.1.2 (`:159`), and `Importer`
(`:752-753`).

**Evidence:** `Import(ctx, td, files) (ImportResult, error)` gives the module no batch. Acquire's
history and TD rows live in `c/acquire/`, which the module does not hold, so it cannot `Bind` it.
No outbox topic is declared for "imported". The result is returned after the module has already
committed its `EpisodeFile`. A crash between the two leaves a file row with the TD still in
`Importing`, and the history row is lost. That is the exact G4 case. 05-library already has a
`TransferIn(b, …)` variant (`05-library.md:322`), but `Manager` does not expose it
(`05-library.md:150`).

**Fix:**
- Change the signature to
  `Import(ctx, td *TrackedDownload, files iter.Seq[library.Entry], tx *acquire.ImportTx) (ImportResult, error)`.
  `tx.Batch()` returns a `*storage.Batch` already bound to `c/acquire/`. The module writes its rows
  into it, uses `Manager.TransferIn(tx.Batch(), …)` for the journaled file move, and calls
  `tx.Imported(refs...)`.
- Acquire writes the history and `StateImported` into the same batch and commits.
- Add `TransferIn` to `library.Manager`.
- State that an import is idempotent by `(TD.ID, destination digest)`.

### COR-5: MAJOR. Held files' format score cannot be computed, so CF upgrades loop and cutoff-unmet cannot be found

**Where:** 10 §10.5.3. `Grade.FormatScore` is "computed; never persisted for files"
(`10-tv-movies-proof.md:480`, `:127`). `Target.Held() (g Grade, ok bool)` is at `:610`. The
`Engine[T]` API is only `Decide` (`:698`).

**Evidence:**
- The compiled `*Formats`, the condition registry and the module's custom formats all live inside
  the engine's snapshot. No method grades a file. A module that implements `Held()` therefore
  returns `FormatScore == 0`.
- Under that zero, every candidate of the same quality with a positive CF score passes `Upgrade`
  with `UpgradeFormatScore` and `UpgradeDisk`. Nothing remembers that the file on disk already has
  that score. The result is an upgrade loop: repeated grabs of equivalent releases.
- `movies.search.cutoff-unmet` (`10-tv-movies-proof.md` §10.2.2) needs `CutoffMet(g)` with a real
  `FormatScore` for every file, and that also cannot be computed.
- Separately, `UpgradePrefs{PreferPropers bool}` (`:494`) cannot represent the three values of
  `core.acquire.prefer_propers` (`:1035`). `do_not_upgrade` and `do_not_prefer` differ.

**Fix:**
- Add `Engine[T].Grade(ctx, p ProfileID, f *Facts, q QualityID, rev Revision) (Grade, error)` and
  `Engine[T].CutoffUnmet(ctx, p ProfileID, f *Facts, g Grade) (bool, error)`. Both read the current
  snapshot.
- Redefine `Target.Held` as `HeldFacts() []Facts`, so the engine computes the grade itself.
- Make `UpgradePrefs.Propers` a 3-valued enum.
- Add a test, `TestHeldFormatScoreRecomputedNoUpgradeLoop`.

### COR-6: MAJOR. Movies minimum availability: the wanted index freezes a time- and config-dependent predicate

**Where:** 10 §10.2.1 (`10-tv-movies-proof.md:272`) and §10.5.3, module specs table (`:931`).

**Evidence:**
- `m/movies/ix/wanted/<MovieID>` is maintained as "monitored ∧ no file ∧ available". Availability
  depends on `now` and on `core.acquire.availability_delay`, which is hot-reloadable (`:1043`).
- A movie whose release date passes overnight never enters Wanted until something rewrites its row.
  A change to the delay invalidates the whole index, and no rebuild path exists.
- TV's index has the same written-in time condition ("∧ aired", `10-tv-movies-proof.md:143`), but
  it is keyed by air date, so it can be fixed by filtering by key range at read time. Movies' key
  has no time component.
- `MinimumAvailability` is "Temporary for RSS". That parks every RSS release for every unavailable
  monitored movie in the pending store (`core.acquire.pending.max_age` 168h) on every 15-minute
  sync. Radarr's `AvailabilitySpecification` is permanent and is skipped for user-invoked search,
  and this spec defines no bypass for `SourceUserSearch`/`SourceInteractive`.

**Fix:**
- Replace the index with `m/movies/ix/avail/<availableAt:be64>/<MovieID>`, where `availableAt` is
  computed from dates only (no delay; TBA/Announced → 0). Evaluate `availableAt + delay <= now` and
  "no file" at read time, and maintain the entry in the same batch as the movie/metadata write.
- Make `MinimumAvailability` Permanent and skip it for `SourceUserSearch`/`SourceInteractive`, or
  record the divergence and its pending-store cost explicitly.

### COR-7: MAJOR. Double grab across concurrent tasks, and import partitioning is inconsistent with multi-ref downloads

**Where:** 10 §10.5.6 (`10-tv-movies-proof.md:1008-1016`) and §10.5.4 item 4.

**Evidence:**
- The union-find grouping only prevents double grabs within one rss-sync run.
- `tv.search.*` and `movies.search.*` tasks call `Decide` and `Service.Grab` concurrently with
  rss-sync. `QueueHasBetter`/`HistoryHasBetter` are evaluated at Decide time, not under a lock at
  Grab.
- `c/acquire/` has no ref-to-tracked index (only `ix/tracked-by-client`), so `Grab` cannot re-check
  anything atomically.
- Imports are "partitioned by owner `entity.Ref`", but a season pack's TD has many refs. A pack and
  a single episode that overlap can therefore import in parallel into the same `EpisodeFile` slot.

**Fix:**
- Make `Service.Grab` take keyed locks on the sorted `Refs`, re-run the StageDatabase specs under
  the lock against a new `ix/tracked-by-ref/<module>/<kind>/<id>/<tdID>`, and write the TD with a
  CAS.
- Partition imports with the same union-find over `TD.Refs`.
- Alternatively, have module search kinds and rss-sync share a `core.acquire.grab.<module>`
  `ConcurrencyKey` (05-tasks 5T.3.4 already lets modules use `core.` keys).

### COR-8: MAJOR. Start/stop order of bus subscriptions, outbox relay and module hooks contradicts itself across 06, 05-events and 05-tasks

**Where:** 06 §6.5.2 (`06-app-sdk.md:600-613`), 05-events §5E.5.3 and §5E.6
(`05-events.md:257-288`), and 05-tasks 5T.4.

**Evidence:**
- 06 starts the "outbox relay" as a core background service before module hooks, and stops it
  after module hooks. 05-events makes the relay a task (`core.outbox.relay`) started by the
  scheduler's `OnStart`, which comes after hooks, and canceled in executor drain, which comes
  before hooks.
- 05-events says "bus subscriptions are made in the module's start hook" (`05-events.md:281`).
  06 says they are made in `Register` and frozen there (`06-app-sdk.md:269`), and P7 validates
  them.
- 06's stop order never calls `Bus.Close`.
- Net effect: bus handlers can run after their module's Stop hook has closed the module's
  resources, and before its Start hook has run.

**Fix:** Pick one order and write it in 06 only, with 05-events and 05-tasks referencing it.
- Subscriptions and consumers are declared in `Register`.
- Start: core services → hooks → workers → `Bus.Open` (begin delivery) → executor → scheduler
  (the relay starts via `OnStart`) → listeners.
- Stop: readiness → listeners → scheduler → executor drain (the relay stops) → `Bus.Close(drain)`
  → workers → hooks → realtime → storage.

### COR-9: MAJOR. Outbox handlers run inside `core.outbox.relay` and bypass the task write gate

**Where:** 05-events §5E.5.3 (`05-events.md:257-268`), 05-download §4.5 (`05-download.md:411`),
05-tasks 5T.3.5 ("Writer is the run's only write path").

**Evidence:**
- `Consume` and `ConsumeTx` handlers execute in the relay run. That run declares `ReadOnly` plus
  `Writes(Res("core","outbox"))`.
- When a module handler writes module entities (directly, or into the relay batch through
  `ConsumeTx`), it skips `Effects` (G8), resource exclusion against the module's own tasks (for
  example `tv.disk.rescan`) and fencing.
- 05-download says the owning module's handler performs the whole import: hardlink, copy or move of
  multi-GB files. That runs on one of `relay_workers` (4) under per-partition head-of-line blocking.
  It is canceled when the relay rotates (1 h) or shuts down, and has no progress or cancel UI.

**Fix:**
- Restrict `Consume` and `ConsumeTx` to bounded, short effects with a per-handler timeout
  (`core.outbox.handler_timeout`).
- Require `ConsumeIntoTask` for any handler that writes module entities or does file or network
  work. Either make `archtest` flag `Consume` in module packages, or have `ConsumeTx` take the
  consumer module's declared `Effects`.
- Change 05-download §4.5 to `ConsumeIntoTask(..., "<module>.download.import")`.

### COR-10: MAJOR. The outbox gap-safe horizon is global, depends on an API storage does not have, and one leaked batch stalls all delivery

**Where:** 05-events §5E.5.2 (`05-events.md:247`) and the requirement at `:209-212`. 05-storage
`Batch` (`05-storage.md:186-207`) has no `OnResolve`.

**Evidence:**
- The relay never advances past `min(reserved-but-unresolved seq) − 1`, and the sequence is
  per-Outbox, shared by all topics.
- A batch that stages a message and is never `Close`d (an error path or a panic) never resolves.
  A long-open batch (a large migration step or Fixer apply) does not resolve until it ends.
- In both cases every consumer of every topic stops.
- `Batch.OnResolve` itself is missing from 05-storage (open item 3 in 05-events).

**Fix:** Two options.
- (a) Use commit-ordered intake. `Stage` writes `c/outbox/in/<ULID>`. The relay, as the single
  writer, moves `in/` → `m/<topic>/<pp>/<seq>` and assigns `seq` on observation. Remove the horizon.
- (b) Add `OnResolve` to 05-storage and bound reservations with
  `core.outbox.reservation_ttl`. When the TTL expires, the reservation is treated as discarded, and
  a late commit is re-sequenced by the relay rather than skipped.

Option (a) is simpler and cannot wedge.

### COR-11: MAJOR. Cross-module outbox consumption requires importing the producer (R3 violation)

**Where:** 05-events `outbox.Consume[T](t Topic[T], …)` (`05-events.md:186`). 03 §3.3 describes
outbox as "for correctness-critical cross-module work".

**Evidence:**
- Outbox consumers are typed only. A consumer in module B needs module A's `outbox.Topic[T]`
  variable and its payload type, which means importing A. That breaks R3.
- `events` has `SubscribeRaw` for this case. `outbox` has no equivalent.
- `outbox.Topic` also has no `Spec()`, while `Registrar.OutboxTopics` takes `events.TopicSpec`
  (`06-app-sdk.md:272`).

**Fix:** Add
`outbox.ConsumeRaw(topic, consumer string, h func(ctx, RawMessage) error, opts...) Handler`, with
the payload validated against the declared schema, and `ConsumeIntoTaskRaw`. Add
`outbox.Topic[T].Spec() outbox.TopicSpec{Name, Schema, Partitions}` and use it in
`Registrar.OutboxTopics`.

### COR-12: MAJOR. Disabling a module silently loses its correctness-critical outbox messages

**Where:** 05-events retention (`05-events.md:252`, `:305`) vs 06 §6.5.3 (`06-app-sdk.md:625`)
and G1.

**Evidence:**
- A message is deleted "when every consumer **registered** for its topic has acked it". A disabled
  module's consumers are not registered, because `Register` is not invoked.
- Two losses follow. If another consumer exists, the message is deleted once that consumer acks it.
  If none exists, it is dropped after `orphan_retention` (7 d).
- On re-enable, the module's persisted cursor (`c/outbox/cur/<consumer>/…`) points past messages
  that no longer exist. This contradicts 06's statement that the messages "stay pending", and loses
  work that was outboxed precisely because it is correctness-critical.

**Fix:**
- Declare consumer names in the manifest (`Manifest.OutboxConsumers []string`), so they are known
  while the module is disabled.
- Compute retention from the persisted cursors of all declared consumers, enabled or not.
- Apply `orphan_retention` only to consumer names that no manifest declares any more.
- Show disabled-module backlog in `GET /api/core/v1/outbox/consumers`.

### COR-13: MAJOR. Disabling any module with task history trips the op-ledger boot gate, and the `held` state is undefined

**Where:** 06 §6.5.3 says runs "are held (05-tasks state `held`)" (`06-app-sdk.md:624-625`).
05-tasks 5T.4 step 1 "rejects an op-ID ledger entry that resolves to nothing" (`05-tasks.md:696`).

**Evidence:**
- 05-tasks has no `held` state; grep finds none.
- At HEAD the gate is fatal (`internal/startup/steps.go:110`). It already had to consult static
  plugin def lists so that "a correctly configured server with no Deluge client … start"
  (`internal/server/server_lifecycle.go:74-88`).
- A disabled module's kinds are never registered, because `Register` is not called. Every ledger
  line and every queued, interrupted or `awaiting_decision` run of those kinds therefore resolves to
  nothing. Disabling `books`, or any future module, fails boot, and the dispatcher and resume sweep
  behaviour for those runs is unspecified.

**Fix:**
- Add a `held` (non-dispatchable) classification that is computed rather than stored: a run is
  held when `Kind.Module()` names a disabled manifest.
- Have the ledger gate treat IDs whose module prefix is a disabled manifest, or whose kind appears in
  a disabled module's `FormerKinds`, as resolved.
- Have the dispatcher and resume sweep skip held runs, and have `GET /runs` show them as held.
- Add `coretest.AssertModuleInert` coverage that includes a module with existing runs.

### COR-14: MAJOR. Module enablement is decided in P1, but the UI stores it in the DB layer, which loads in P3

**Where:** 06 §6.5.1 P0/P1/P3 (`06-app-sdk.md:585-589`) vs §6.5.3, where "the settings UI writes
the DB override layer" (`06-app-sdk.md:618`).

**Evidence:**
- P1 resolves `core.modules.<id>.enabled` from file, env and flags only, because DB overrides are
  applied in P3 after storage opens in P2. A disable written through the UI is therefore ignored at
  P1 (or applied inconsistently if re-read later).
- `ProblemUnknownModuleInConfig` is fail-closed. If the stale key is in the DB layer, the app
  cannot boot to remove it, and no offline tool is specified.
- The same P1-before-P3 problem affects `modules.<id>.arrfacade.url_base` collision detection
  (08 §8.4).

**Fix:** Either (a) make `core.modules.*` and `*.arrfacade.url_base` operator-layer-only
(`Protection` = operator; the UI shows the value and how to set it), or (b) split P2 into P2a (open
storage, read `c/config/` overrides for `core.modules.*` only) and P1′ (enablement validation),
and downgrade `ProblemUnknownModuleInConfig` from a DB-layer key to WARN plus health.

### COR-15: MAJOR. `audit.RecordIn` into caller batches breaks the "gap-free" sequence and the hash chain

**Where:** 05-rbac §3.5 (`05-rbac.md:326,340`) and §7.2 (`:642`).

**Evidence:**
- `seq` and `PrevHash` are assigned under the store mutex when `RecordIn` stages into the caller's
  batch.
- If that batch is discarded (the handler fails after `RecordIn`), seq N never exists and N+1's
  `PrevHash` points to nothing.
- If two batches commit in the opposite order to their reservation, the stored order differs from
  the chain order.
- In both cases `Verify` reports tampering that never happened, and `mediacore_audit_chain_breaks_total`
  fires on ordinary errors.

**Fix:**
- `RecordIn` stages `c/audit/pending/<ULID>` in the caller's batch, with no seq and no hash.
- The audit writer goroutine, as the single chainer, assigns `seq` and `PrevHash` after commit and
  deletes the pending row in one batch.
- Alternatively, chain in commit order through `Batch.OnResolve` (see COR-10).
- Drop "gap-free" or define it over chained records only.

### COR-16: MAJOR. SDK type vocabulary disagrees between 06 and its owning files, and the `hello` proof does not compile

**Where:** 06 §6.4.2 (`06-app-sdk.md:173-176`), the hello example (`:886-891, :970, :1010, :1142-1144`),
05-rbac §3.1–§3.3, 05-http-api (`05-http-api.md:207, :307`), 08 (`Permissions(module) []rbac.PermissionDef`).

**Evidence:**

| 06 / hello uses | Owning file defines |
|---|---|
| `rbac.Scope`, `rbac.PermissionDef{Scope, Title}` | `rbac.Permission`, `rbac.PermissionSpec{Name, …, DefaultGrants []RoleName}` |
| `rbac.Grant{Role: "core.viewer", Scopes}` | no `Grant` type. The core seed role is `"viewer"` (`05-rbac.md` §3.3) |
| `tasks.Permissions{Start: PermAdmin}` | `Start rbac.Requirement`, so this needs `rbac.Need(PermAdmin)` |
| `httpapi.Invalid("name", msg)` | `Invalid(fields ...FieldError)` |
| Role ID `<moduleID>:<role>`, e.g. `books:editor` (03 §3.6) | `"<ns>.<name>"`, e.g. `tv.admin` (`05-rbac.md:122`) |

The 06 note at `:1182` claims 05-rbac uses `PermissionDef`, which it does not. Hello is the G6/G7
acceptance artifact and the §12 second consumer.

**Fix:** Adopt 05-rbac's names everywhere: `Permission`, `PermissionSpec` and `DefaultGrants`, and
delete `Grant`. Rewrite 06 §6.4.2, the hello example, `coretest.WithUser(..., ...rbac.Permission)`
and 08 `Permissions()`. Amend 03 §3.6's Role row to `<moduleID>.<name>` (05-rbac objection 1). Add
a CI step that extracts and compiles every Go code block marked as compilable in the spec.

### COR-17: MAJOR. The polymorphic entity reference has four names and two cardinalities

**Where:** 03 §3.3 `entity` row (normative: `entity.Ref`, L0). The deviations:
`05-download.md:103, :277, :296` (`events.EntityRef`), 05-notify (`*events.EntityRef`), 08 `IDMap`
(`events.EntityRef`), `05-storage.md:276` (`core.EntityRef`), and `audit.Target` (a duplicate struct).

**Evidence:** 05-events never defines `EntityRef`. 03 says `entity.Ref` is what "download grabs …
audit" use. Acquire carries `[]entity.Ref` (one TD covers many episodes), but `GrabRequest.Entity`
holds a single ref. The acquire→download boundary therefore needs a lossy conversion.

**Fix:** Replace every occurrence with `entity.Ref`. Change `GrabRequest.Entity` to
`Refs []entity.Ref` (with COR-3). Make `audit.Target` a defined type over `entity.Ref`, or use
`entity.Ref` directly. Add an `archtest` check that bans `EntityRef` identifiers outside `entity`.

### COR-18: MAJOR. Core capability HTTP routes have no layer-legal home under R4

**Where:** 03 R4. 05-http-api §5 says "Core registers its own routes on `CoreGroup()`"
(`05-http-api.md:547`). Each capability spec defines `/api/core/v1/...` routes: tasks 5T.3.10,
rbac §4.7, audit §7.6, events and outbox §5E.11, auth, library, backup, updater, health, and acquire
queue/history (10 §10.5).

**Evidence:** Typed routes need `httpapi` (L6). The owning packages are L4 (acquire is L5), so they
cannot import it. No package is named for the handlers, and putting about 100 handlers in `kernel`
contradicts "kernel = catalog/services/supervisor". The core layering test will fail on the first
core-route PR.

**Fix:** Add an R4 row at L6 (or a new L6.5): "`<capability>/<capability>api` route packages (for
example `tasks/tasksapi`, `rbac/rbacapi`, `audit/auditapi`, `acquire/acquireapi`, …) may import
their capability and `httpapi`. They are wired only by `kernel`." List them in the §3.3 tree.

### COR-21: MAJOR. Handle scoping (06 A3) is not enforceable with the types given

**Where:** 06 A3 (`06-app-sdk.md` §6.3), the Deps block (`:343-360`), 05-library
(`05-library.md:93, :150, :166`).

**Evidence:**
- `Deps.Events *events.Bus` is one shared pointer. The bus cannot tell which module is publishing,
  so "publish only declared `<ID>.*` topics" is unenforceable.
- `Deps.Library *library.Manager` is a pointer to an interface. `Manager.Transfer` takes no caller,
  and `Root.FS(caller storage.Owner)` accepts a caller the module itself supplies, so it can be
  spoofed. "RoleLibrary written by its owner module only" therefore has no enforcement point.
- `Deps.RBAC rbac.Checker` is a per-principal compiled set, not a module handle.
- `StartRequest.Actor` lets a module start a task as any actor.

**Fix:** Define module-scoped handle types and make the field names match 05-*:

| Field | Type |
|---|---|
| `Events` | `events.Scope`, which has `Publish`/`Subscribe` and knows its module |
| `Library` | `library.Scope`, with `FS(rootID)` and `Transfer`/`TransferIn` carrying the module's owner implicitly |
| `RBAC` | drop the field (use the `rbac.Can`/`Require` ctx functions) |
| `Tasks` | `tasks.Client`. The kernel overwrites `Actor` from ctx, or with the module's system actor |
| `Metrics` | `*metrics.Scope` |
| `Audit` | `audit.Logger` |
| `HTTP` | `*providerhttp.Registry` |
| `Auth` | `*auth.Service` read view; or define `auth.Identities` in 05-authn |
| `Secrets` | define `secrets.Scope` in 05-config |

### COR-19: MINOR. 05 files use pre-v1.2.0 layer numbers, and internal bridge packages break the "internal takes its public package's layer" rule

**Where:** "Layer 3" or "layer 2" claims in `05-tasks.md:12`, `05-events.md:12`, `05-storage.md:81`,
`:404`, `:885-887`, `05-library.md:14`, `05-health.md:10`, `05-backup.md:14`, `05-updater.md:11`,
and `05-config.md:14`, all against 03 R4 (L1 storage, L2 secrets, L3 config, L4 events/tasks/…).

**Evidence:** `core/internal/storagemigrate` implements storage (L1) but imports tasks (L4).
`core/internal/logjournal` implements log (L0) but imports storage and tasks (05-logging §5). Under
R4's last sentence both are violations.

**Fix:** Renumber every 05 header to R4 v1.2.0. Amend R4 with "an `internal/*` package that bridges
capabilities takes the layer of its highest import, and must not be imported by any lower-layer
package". List `storagemigrate` and `logjournal` at L4.

### COR-20: MINOR. events/realtime/metrics signature mismatches and task-topic mapping

**Where:** `05-realtime.md:97, :118, :120, :242, :271`; 05-rbac and 05-authn `Deps`; 05-events
§5E.13; 05-tasks 5T.4 step 3.

**Evidence:**
- `Bridge(h, bus events.Bus, …)` and `Deps.Events events.Bus` copy an opaque struct. They should be
  `*events.Bus` or a scope.
- `*metrics.Registry` is used in realtime, rbac, authn and httpapi, but 05-metrics defines only
  `*Provider`/`*Scope`.
- The bus publishes four task topics (`core.task.created/progress/log/finished.v1`). Realtime
  exposes `core.task.run.v1` and `core.task.log.v1`, and `Bridge` is 1:1, so no mapping is defined.
- `core.task.run.v1`, `core.notify.v1` and `core.stream.open.v1` violate 03 §3.6
  `<noun>.<verb-past>`.
- §5 calls `Hub.Close`, but the API has only `Run`.
- The topic `Perm` is `core:tasks.view` AND the definition's `View`, while REST needs only the
  definition's `View`. A books-only user sees runs in REST but gets no live progress.

**Fix:** Use pointer or scope types. Add `realtime.BridgeMap` (many bus topics → one realtime topic
with a reducer), or publish a single `core.task.run-changed.v1` on the bus. Rename the topics to the
grammar or amend 03 §3.6. Add `Hub.Close(ctx)`. Make the topic `Perm` `AnyAuthenticated` and let
`Visible` enforce the definition's `View`.

### COR-22: MINOR. `storage.Compat` names two different things; the read-adapter type is undefined

**Where:** `05-storage.md:323` (`Spec.Compat []Compat // read adapters`) vs `:364`
(`type Compat uint8 // Additive/Breaking`).

**Evidence:** As written, `Spec.Compat` is a slice of the enum. An `Online` DataMigration "must
declare a Compat read adapter" (`:431`), and no such type exists.

**Fix:** Rename the enum `MigrationCompat`, define `type ReadAdapter struct{ Family string;
Read func(ctx, key, raw []byte) ([]byte, error) … }`, and make the field `ReadAdapters []ReadAdapter`.

### COR-23: MINOR. R7 enforcement rejects pass-through parallelism, and some callers cannot call `RunItems` at all

**Where:** 03 R7 (`03-architecture.md:171`), `05-tasks.md:250, :720`, `05-indexer.md:212, :228, :237`,
`10-tv-movies-proof.md:152, :954-956`.

**Evidence:**
- `archtest.RunItemsDeclareParallelism` fails any `RunItems` call whose parallelism argument is not
  a direct constructor call. Core's own `RunItems(rc, o.Parallelism, …)` (indexer options,
  `FromParam`) fails it.
- `indexer.Pool.SearchAll(ctx, …)` and `RSS(ctx, …)` have no `*tasks.RC`, so they cannot use
  `RunItems`.
- `Engine.Decide` takes no parallelism ("sized by the calling task's `tasks.CPU()`", which cannot be
  observed from `ctx`).
- 10 uses `tasks.Network(n, reason)`, which does not exist (05-tasks has `IO`/`Fixed`).
- 03 names the error `ErrNoParallelism`; 05-tasks names it `ErrParallelismUnset`.
- 05-tasks says the error is returned bare (`:231-233`) and also wrapped in `*ItemsError` (`:720`).

**Fix:** Make the AST rule accept a `tasks.Parallelism`-typed value whose origin is a constructor or
a struct field documented as required. Give `SearchAll`/`RSS` and `Decide` an explicit
`par tasks.Parallelism` and an `rc *tasks.RC` (or a `tasks.Pool(ctx, par)` helper usable without
`RC`). Replace `Network` with `IO`. Pick one error name and one return shape.

### COR-24: MINOR. Context is not threaded into `Register`, and `app.New` claims to start no goroutines

**Where:** `06-app-sdk.md:137-143` (`Register(r *Registrar) error`) and `:411-415`.

**Evidence:** `Register` "must not block on I/O beyond reading its own config/keyspace", but every
`Keyspace` read needs a `ctx`, and `Register` has none, so modules will use
`context.Background()` and `app.New(ctx)` cancellation is lost. `New` "does not start goroutines",
but Pebble (P2), log taps (05-logging), the audit writer (05-rbac §5 step 7) and the realtime
coalescer all start during construction.

**Fix:** Add `(r *Registrar) Context() context.Context`, derived from `app.New`'s `ctx`. Reword
`New` to "starts no module goroutines, workers, executor or listeners; core services' internal
goroutines started in P2–P5 are stopped on error".

### COR-25: MINOR. TV and Movies in one app: one rss-sync cannot serve two RSS query types, and the vocabulary owner is undefined

**Where:** `05-indexer.md:236` (`RSSOptions.Types map[integrations.Ref]SearchType`, "TV app passes
TypeTV, Movies TypeMovie"), `10-tv-movies-proof.md:954`, and `MediaType.Vocabulary`.

**Evidence:**
- `core.acquire.rss-sync` is one task over all indexers, but the RSS type is set per indexer. With
  `app.New(opts, tv, movies)` sharing an indexer, one module's RSS type wins.
- Both modules declare `Vocabulary: "video"`. No registration API exists for vocabularies, so
  "unknown vocabulary" boots fail and a duplicate declaration is undefined.

**Fix:** Run RSS per (indexer, module) with the module's type and categories, or use `t=search`
with the union of categories and route by category. Add
`acquire.RegisterVocabulary(svc, Vocabulary)`, owned by core `shared/videoquality` once two
modules use it, with idempotent equal-content registration.

### COR-26: MINOR. Naming contradictions with 03 §3.6 (task-kind grammar, metric prefix, app ID field)

**Where:** 03 §3.6 (`03-architecture.md:209, :216`), `05-tasks.md` 5T.3.1, 05-metrics `Options`,
06 `app.Options`.

**Evidence:**
- Task-kind segments: 03 allows `[a-z0-9_-]+`; 05-tasks allows `[a-z][a-z0-9-]*`. No ABO op ID has
  an underscore (`internal/server/testdata/op_ids.golden`, 288 lines), so the stricter rule is safe.
- Module metrics: 03 uses `<AppID>_<module>_*` (example `abo_books_*`). 05-metrics uses
  `AppPrefix` "audiobook_organizer".
- 06 names the field `Options.ID`; 03 names it `AppID`.

**Fix:** Adopt 05-tasks' grammar in 03. Pick one metric prefix: keep `audiobook_organizer_*` via
05-metrics aliases and amend 03, or make `AppPrefix` default to `ID`. Name the field `AppID` in
`app.Options`.

### COR-27: MINOR. 10 and 08 reference library and indexer identifiers that do not exist

**Where:** `10-tv-movies-proof.md:93, :129, :151, :154, :326, :593, :804`; 08 `LibraryRootFolders(roots
library.Roots, …)` and `CoreIndexers(set indexer.Set, …)`.

**Evidence:** These names do not exist in 05-library, 05-indexer or 05-storage: `library.RelPath`,
`library.Location`, `library.MediaInfo`, `library.PathMap` (the actual type is `PathMapper`/`Mapping`),
`library.PreferHardlink` (actual: `Auto`/`Hardlink`), `indexer.ID` (actual: `integrations.Ref`),
`Keyspace.NextID` (actual: `Sequence(name).Next`), "scans are `iter.Seq2[[]byte, Series]`" (actual:
`iter.Seq2[KV, error]` / `codec.Typed.All`), `library.Roots`, and `indexer.Set`. 10's own open item
6 acknowledges this.

**Fix:** Replace each with the owning symbol: `library.Path` (root+rel), `download.Item.LocalPath`,
`PathMapper`, `TransferOptions{Mode: Auto}`, `integrations.Ref`, `Sequence`, `codec.Typed[T].All`,
`library.Manager.Roots` and `*indexer.Pool`. Add `library.MediaInfo` to 05-library, or keep it in
`shared/`, and say which.
