<!-- file: docs/proposals/2026-10-holistic/07-design-decisions-and-modularity/B-decisions.md -->
<!-- version: 1.0.1 -->
<!-- guid: b0c07363-6f2d-4bd2-88ce-ee029d22e7b7 -->
<!-- last-edited: 2026-10-08 -->

# Appendix B: decision register

Each decision has the same five parts: the current choice and why it was made,
the cost it imposes today, the alternatives, the recommendation and the
migration effort. Measurements are in [A-measurements.md](A-measurements.md).
HEAD is `f7211eb39`.

Labels used below:

- **BUILDS ON** means an owner decision already exists and this decision only
  extends or finishes it. It is not re-argued here.
- **NEW** means it needs an owner decision. See section 7 of the main doc.

---

## D1. The search-index decorator pushes store methods onto `database.Store`; a store-side observer already exists to replace it (NEW)

**Current choice.**

- With search on, prod wraps the store in `indexedStore`
  (`internal/server/indexed_store.go:49-54`). The wrapper embeds
  `database.Store` and overrides five writers to enqueue a Bleve update:
  - `CreateBook` (`:80`);
  - `UpdateBook` (`:103`);
  - `ModifyBook` (`:116`);
  - `FillBookMediaInfo` (`:128`);
  - `DeleteBook` (`:153`).
- Why: it kept the index in sync "without threading explicit index calls
  through every handler and service" (header comment `:6-10`).
- Since 2026-09-25 the store also has its own observer:
  - `database.ChangeObserver` (`internal/database/change_observer.go:22`) has
    four hooks: `BooksChanged`, `BooksNeedReindex`, `AuthorRenamed` and
    `SeriesRenamed`.
  - They fire "at the memdb write-through choke points, which every
    book-row writer already calls after its Pebble commit" (`:15-17`).
  - The server implements it as `searchChangeObserver`
    (`internal/server/search_result_cache.go`), but it reindexes only for
    `BooksNeedReindex` (tags and aggregates) and for rename fan-outs. For
    plain book writes it still relies on the decorator.
- So two mechanisms cover overlapping writes today.

**Cost today.**

- Go promotes only the embedded interface's methods. Any `*PebbleStore`
  method that an operation reaches by type assertion has to be declared on
  `database.Store`, or the assertion fails in prod and passes in every local
  test (memory `project_prod_store_is_indexedstore_capability_assertions`,
  confirmed by a compile probe on 2026-09-12).
- This is a forcing function on `Store`'s size, though not the only cause of
  its growth.
- Evidence, same instruments on `a0312c104` (2026-08-19) and on HEAD:
  - The flattened method set went from 398 to 455: 60 names added and 3
    removed. Some additions are genuinely new features, such as `ModifyBook`
    and the fingerprint-window methods.
  - Conformance assertions of the form `var _ narrowIface = database.Store(nil)`
    went from 3 to 14 non-test lines. They pin an operation's narrow interface
    to `Store`, which exists so that it resolves through the decorator.
    Examples: `plugins/maintenance/fs_regroup_xml.go:176`,
    `repoint_book_file_rows.go:231`, `move_book_file_rows.go:156`,
    `maintenance/jobs/merge_chapter_groups.go:38-39`.
- The escape hatch, `database.AsCapability` (`store_capability.go:90`), has
  189 non-test call lines. Per the memory note it bypasses the decorator's
  reindex overrides.
- The dirty-set comment at `indexed_store.go:17-24` records 56,537 dropped
  index events in seven days before the reconciler existed.

**Alternatives.**

- (a) Keep the decorator and keep adding methods to `Store`.
- (b) Have the decorator forward capabilities through generated code.
- (c) Drive all indexing from the existing `ChangeObserver`:
  - `searchChangeObserver.BooksChanged` enqueues a reindex, or a delete when
    the row is soft-deleted or gone. The decision is read back off the
    worker, never inside the hook, because hooks may fire under store locks
    (`change_observer.go:19-21`).
  - The decorator is then deleted.

**Recommendation: (c).**

- It needs no new mechanism, and it does not have to wait for storage
  release B.
- When release B lands its P1 book chokepoint (storage design section 4.5:
  row, history and indexes commit in one batch at step 6), the observer call
  moves to after that commit. The hook stays post-commit, as it is today.

The event set must be derived from every path that reaches
`enqueueIndex`/`markIndexDirty` today, not only from the five decorator
overrides:

- the five writers above;
- `BooksNeedReindex` from tags (`pebble_store_tags.go:36,79,111,248`),
  aggregates (`pebble_store_book_aggregates.go:195`), credits
  (`credits_store.go:181`) and `pebble_store.go:5434`;
- the author and series rename fan-outs (`pebble_store_authors.go:338,470`;
  `search_result_cache.go` `AuthorRenamed`/`SeriesRenamed`);
- the coverage repair (`search_coverage.go:150`);
- the reconciler's dirty set (kept as is).

A test asserts, per path, that a write reaches the index queue after the
decorator is gone.

**Migration effort.** M. The work, in order:

1. Extend `searchChangeObserver.BooksChanged` to enqueue.
2. Add the per-path tests.
3. Delete `internal/server/indexed_store.go`.
4. Point `Server.OpsStore()` at the raw store.
5. Replace the 14 conformance assertions with assertions against
   `*PebbleStore`, or delete them.

See PR 07-S3 in the main doc. Coordinate with workstream 02, which owns
search.

---

## D2. `database.Store` is a 455-method composite with one implementation (NEW)

**Current choice.**

- `Store` (`internal/database/store.go:46`) embeds six private groups:
  - `catalogStore`, 130 methods;
  - `operationsStore`, 97;
  - `mediaStore`, 77;
  - `enrichmentStore`, 65;
  - `accountStore`, 44;
  - `platformStore`, 42.
- `*PebbleStore` is the only implementation.
- Why: SQLite was once a second backend. The August sweep then pushed
  consumers onto narrow, consumer-declared interfaces (playbook memory).

**Cost today.**

- Every new store method is written three times: the PebbleStore method, the
  hand-written `MockStore` (`internal/database/mock_store.go`, 4,119 lines,
  header version 1.139.0) and the mockery mock (`internal/database/mocks/`,
  32,024 lines).
- The `feat` commits that added store methods (`c1cfeb77d`, `497782eff`,
  `ee2cc0960`) each touch both `internal/database` and
  `internal/database/mocks`.
- `interfacebloat` counts declared entries, which is 6, so the gate never sees
  the 455 methods.
- The same text grep for `database.Store` (non-test, non-mock, comments
  dropped) gives 9 lines at `a0312c104` and 31 at HEAD. Classified at HEAD:
  - **11** are conformance assertions (see D1). There are 14 in total; the
    filter drops 3 that carry a trailing comment:
    - `server/maintenance_fixups.go:66`;
    - `dedup/engine.go:261`;
    - `dedup/lifecycle.go:254`.
  - **13** are wiring or unwrap lines:
    - `server.go` ×5;
    - `indexed_store.go` ×2;
    - `operations/registry/register.go` ×3;
    - the `scanner.go:4335-4336` unwrapper;
    - `testutil/integration.go:31`;
  - **2** are test-helper signatures (`dbtest/invariants.go:62,207`);
  - only **about 5** are genuine wide consumers:
    - `plugins/maintenance/deps.go:524`;
    - `server/provider_throttle_wire.go:38,63`;
    - `server/catalog_harvest_op.go:159`;
    - `cmd/root.go:666`.
- So genuine wide consumers have not re-accreted much. The growth is in
  assertions that exist because of the decorator.

**Alternatives.**

- (a) Status quo.
- (b) A ratchet on the flattened method count plus a ratchet on the classified
  consumer count.
- (c) Retire `Store` as a consumer-facing type. Keep it only at the
  composition root (`cmd/`, `internal/server` wiring), where it describes
  `*PebbleStore`. Everything else depends on role interfaces or on
  consumer-declared ones.

**Recommendation: (b) now, (c) after D1.**

- (b) is a small addition to `scripts/check-interface-width.sh`: run the
  flattener and compare against a baseline.
- (b) also stops growth by itself. D1 removes the main reason new methods
  have to land on `Store`.

**Migration effort.**

- (b) is S.
- (c) is M: about 5 genuine consumers, 14 assertions, and the `AsCapability`
  sites that can then call methods directly.
- Follow the playbook order: leaf-first, compile probes, and let the compiler
  partition the call sites.

---

## D3. memdb is a full in-memory mirror with a silent fallback (NEW, defers to storage release C)

**Current choice.**

- `hashicorp/go-memdb` mirrors book and file rows (`internal/database/memdb_*.go`,
  5,567 non-test lines).
- It warms up asynchronously after open. Prod logs `memdb warmup published`
  after about 130 to 200 seconds (memory `project_memdb_warmup_is_async_after_restart`).
- Until then, every guarded read falls back to a Pebble full scan. There are
  35 non-test guard lines matching `p.mem() != nil`.
- Why: Pebble has no secondary indexes or sorting, and the library UI needs
  sorted, faceted pages in milliseconds.

**Cost today.**

- The fallback is silent. A maintenance op launched in the warmup window ran
  at 5 rows per 30 seconds, and nothing in its log said why.
- `/health` does not report warmup (`internal/server/handlers/system/handler.go:169`).
- The ops dispatcher does not wait for it.
- `IsMemReady()` exists (`pebble_store.go:273`). Only `internal/server` and two
  test helpers call `IsMemReady`/`WaitForWarmup`.
- The approved storage design measured 110 of the 134 warmup seconds going to
  file rows, of which 3,017 MB was fingerprints. Release C removes those.

**Alternatives.**

- (a) Make the warmup synchronous, so the server does not serve until it is
  warm.
- (b) Make readiness explicit:
  - a `memdb_ready` field in `/health`;
  - a counter metric per fallback read;
  - a `NeedsWarmIndex` flag on op definitions, so the dispatcher holds them.
- (c) Persist a memdb snapshot and load it at boot.
- (d) Replace memdb with Pebble-native secondary indexes.

**Recommendation: (b) now; decide between (a), (c) and (d) after the release C
warmup numbers.**

- If release C brings warmup under about 20 seconds, (a) becomes cheap and
  removes the dual path entirely.
- Re-arguing memdb before C would duplicate the approved design's measurement
  plan.
- The dispatcher half of (b) belongs to workstream 05 (operations v3).

**Migration effort.** (b) is S for health and the metric. The dispatcher flag
is S inside 05's SDK.

---

## D4. Seven storage engines are linked into the binary; the activity default differs from prod (NEW for the default flip; deletion owned by 01)

**Current choice.** The engines are:

- Pebble: main store, AI scans, OpenLibrary and activity;
- go-memdb;
- Bleve, which uses bbolt;
- coder/hnsw;
- chromem-go;
- NutsDB;
- modernc SQLite.

`internal/activity/register.go:86-106`:

- an empty or `sqlite` `ActivityBackend` selects the SQLite migrating store;
- `pebble` selects Pebble-only.

Prod sets `ACTIVITY_BACKEND=pebble` in the systemd drop-in. The owner decided
this on 2026-09-19 after SQLite compaction took 36 minutes to 1 hour 50
minutes on the HDD vdev and dropped rows on `SQLITE_BUSY` (memory
`project_activity_sqlite_oom_and_rollback`).

**Cost today.**

- A fresh install, the sandbox and every test that does not set the variable
  run a backend that prod abandoned.
- NutsDB has no production constructor call. Only tests open it
  (`internal/activity/writer_test.go:95`, `service_test.go:25`, and others), so
  those tests exercise a backend prod never uses.
- The activity-store code is 14,241 lines including tests.
- Each engine is a separate dependency to keep upgraded.

**Alternatives.**

- (a) Leave it as it is.
- (b) Flip the code default to Pebble and keep SQLite behind the variable.
- (c) Flip the default, port the activity tests to the Pebble store, and
  delete the NutsDB and SQLite activity stores.

**Recommendation: (c), in two steps.**

1. Flip the default and port the tests. This is the reliability change and
   belongs to this workstream.
2. Delete the dead stores. Workstream 01 owns deletion.

hnsw versus chromem stays. The fallback exists because of a recorded hnsw
crash (`registry_wire.go:50`).

**Migration effort.** The flip is S. Porting the tests is M. Deletion is M
under 01. There are no data-migration steps: prod already runs Pebble, and
`activity.sqlite` on disk is the owner's to delete.

---

## D5. The book row is wide and denormalized (BUILDS ON the ModifyBook, credit-list and storage-P2 decisions)

**Current choice.**

- `type Book struct` (`store.go:208`) has 115 top-level fields:
  - iTunes state;
  - transcription fields;
  - three overlapping author and narrator representations;
  - the version-group flags.
- `BookFile` has 65 fields.
- Writes replaced the whole row until the ModifyBook migration.

**Cost today.**

- The "field not loaded, saved as empty" incident class is the reason for
  storage design P2.
- `.UpdateBook(` still has 11 non-test call lines (listed in A.6).
- The row-version check that the owner approved on 2026-09-13 is not built:
  `grep -rn 'RowVersion\|StaleWrite' internal/database` returns nothing.

**Alternatives.**

- (a) Finish the approved items:
  - the row-version check;
  - the credit-list migration (memory `project_author_narrator_always_composite`, approved 2026-10-04);
  - the storage design's P2, "types cannot carry what they must not wipe".
- (b) Additionally split `Book` into a core row and separate facet records:
  - the iTunes facet;
  - the transcription facet;
  - the provenance facet.

  Each facet would have its own key and chokepoint.

**Recommendation.**

- (a) is already decided, so schedule it as PR 07-S2.
- Do not add a new `Rev` field. The approved storage design already gives
  every book a monotonic per-book version id (section 4.2:
  `id = max(now, newest id + 1)`), and its book chokepoint already reads the
  newest history entry and its post-hash `h` under the write stripe (section
  4.5, step 1).
- The stale-write check is therefore "the caller presents the version id it
  read; the chokepoint refuses if the newest id differs". It lands inside
  release B's chokepoint, not before it, so it does not build a mechanism that
  release B would replace.
- One gap needs the release B brief to decide it: writes that change only
  unversioned keys write no history entry (4.5, step 3), so the newest id
  does not move for them.
- Raise (b) as a direction, not a commitment. It follows naturally from P2.
- The iTunes facet must keep every field `internal/writeback/` reads,
  reachable unchanged, because that package is out of bounds. In practice the
  iTunes facet is excluded from any split until the owner says otherwise.

**Migration effort.** The row-version check is M on top of release B: the
chokepoint compare, the 11 sites and race tests. A facet split is L per facet and uses the approved cut-over
mechanism (P3, `RunCutover`, the `storage_format` stamp), not dual writes.

---

## D6. "Exactly one primary per version group" is enforced by locks and repair ops, not by structure (NEW)

**Current choice.**

- Each `Book` carries `VersionGroupID *string` and `IsPrimaryVersion *bool`.
- The invariant "exactly one live explicit primary" is maintained by:
  - process-local striped locks (`internal/versionprimary/ensure.go:187-272`);
  - hand-off rules in every writer that changes membership;
  - repair ops: `version-group-primary-repair`, `normalize-primary-flags` and
    `ElectMissingPrimaries`.
- No version-group record exists. A search for `vg:`, `vgroup:` or
  `version_group:` key literals in `internal/database` finds nothing.

**Cost today.**

- 10,780 groups once held two or more primaries, and hundreds held none
  (memory `project_is_primary_version_state`; the prod repair ran on
  2026-08-13).
- nil has two meanings:
  - `EffectiveIsPrimaryVersion` says nil means primary;
  - 35 non-test lines read `IsPrimaryVersion != nil && *...`, which treats
    nil as not primary.

  Most of those 35 are election or merge code inside `versionprimary`,
  `merge` and the maintenance fixers, where "explicitly elected" is intended:
  `versionprimary/rank.go:298` names it `explicitTrue`. At least these sit
  outside election code and need classifying:
  - `server/library_list_warmer.go:113`;
  - `server/transcode_version.go:91`;
  - `server/handlers/versions.go:442`;
  - `plugins/maintenance/audible_read_status_fixer.go:506,949`;
  - `scanner/ai_parse_async.go:569`.

  The same grep on `63a5eb807` (2026-08-24) returns 11, so 24 such reads
  were added in six weeks.
- `IsPrimaryVersion` appears on 407 non-test lines.

**Alternatives.**

- (a) Status quo plus more repair ops.
- (b) A `version_group:<id>` record that holds `primary_book_id` and the
  member list, with one chokepoint. The book's flag becomes derived and is
  served on reads.
- (c) Keep the flag, but make it non-nullable through a cut-over (nil becomes
  explicit true or false).

**Recommendation: (b).** "Exactly one primary" then becomes a property of the
data shape, not of every writer's discipline. (c) is a cheaper half-step and
can ride the release B or C cut-over if the owner prefers it.

**Migration effort.** L. The plan:

1. A new key family.
2. A converter that elects from current flags and reports the zero- and
   multi-primary groups to the owner as a list.
3. Repoint the 407 reader lines through one accessor.
4. Use the approved cut-over (P3).

The record must agree with the approved archive (storage design section 6):

- a version-group member is not eligible for the archive;
- so `member_ids` holds live books only;
- the archive's eligibility check becomes a point read of the book's group
  record instead of a scan;
- a retired book leaves the group (with a primary hand-off if it was the
  primary) before it can be archived.

No book_file rows are touched.

---

## D7. Key families are string literals scattered across packages (BUILDS ON storage design section 8)

**Current choice.** Pebble key prefixes are literals:

- 236 distinct `"xxx:"`-shaped literals in `internal/database`;
- 77 non-test raw-KV calls (`SetRaw`, `GetRaw`, `ScanPrefix`, `DeleteRaw*`)
  outside it, led by `internal/merge` (23) and `internal/server` (12).

**Cost today.**

- There is no single place to see who owns a family, what its retention is,
  or whether a census covers it.
- `docs/database-architecture.md` still describes the SQLite backend and
  integer key examples.
- `docs/database-pebble-schema.md` documents a ULID `a:`/`b:` layout and
  carries a caveat that core entities never used it.

**Alternatives.**

- (a) Hand-maintained docs.
- (b) The family registry the storage design already specifies (section 8,
  "Census": "The family list is one registry that the schema document is
  generated from"), plus a rule that each family has exactly one owning
  package.

**Recommendation: (b).** It is approved; this decision adds two things:

- the ownership rule, enforced by a grep ratchet like P1's;
- regenerating both database docs from the registry, which retires the stale
  text.

**Migration effort.** S once release A's registry exists. The 77 raw calls
move behind owning-package helpers over M-sized PRs. `internal/merge` goes
first.

---

## D8. `internal/config` is not a leaf, and config has one global mutable value (NEW)

**Current choice.**

- `config.AppConfig` is a package-level `Config` with 158 top-level fields.
- It is read directly at 636 non-test sites in 204 files.
- Only writes go through `Mutate` (4 call sites). Direct reads are "tolerated
  with residual risk" (`config.go:1791-1816`).
- `internal/config` imports `database`, `auth`, `backup`, `aidispatch`,
  `dedup/unified`, `serviceregistry` and `tools`, because persistence, update
  and registration live inside it.
- A field is declared in up to five places:
  - a struct tag;
  - `viper.SetDefault` (250 calls);
  - the env map and `BindEnv` (134 calls);
  - the `applySetting` switch (133 `case` labels, `persistence.go:993`);
  - the frontend types.
- `SaveConfigToDatabase` (`persistence.go:1544`) writes the whole struct as
  one blob.

**Cost today.**

- Config has a fan-in of 47 packages. Every one of them depends transitively
  on the store, auth and dedup.
- The whole-blob save turns any zero into a persisted override. The
  chapter-consolidation threshold sat at 0 for 11 days and cost the P0 (memory
  `feedback_silent_failure_shapes_config_and_tooling`). It was patched for one
  field (`normalizeChapterConsolidationThreshold`, `persistence.go:1557`).
- Eight blob migrations (`migrate*Blob`, `persistence.go:161-597`) exist
  because the blob mirrors the struct shape.

**Alternatives.**

- (a) Keep it, and patch fields one at a time.
- (b) Three changes together:
  - **Leaf config.** Move persistence and update into `internal/config/configstore`,
    so `internal/config` imports nothing in the module.
  - **One declarative field table.** Each entry carries key, type, default,
    env name, secret flag, restart-required flag and validation. Defaults, env
    binding, DB apply and the API shape are generated or derived from it, and
    the `applySetting` switch goes away.
  - **Sparse persistence.** Store only keys the user changed, one row each, so
    an unset key always means "use the current default".
- (c) Pass config views by injection instead of the global.

**Recommendation: (b), then (c) gradually.**

- Add a ratchet on direct `config.AppConfig.` reads (baseline 636) so new code
  uses `Snapshot()` or an injected view.
- The flags in the config, 34 bool fields and 17 `*enabled*` keys, join the
  same table with an `owner_gate` marker, so the UI can list every kill switch
  and its state.

**Migration effort.**

- Leaf move: M.
- Field table plus generated apply: M.
- Sparse persistence: M. It needs a one-time converter that drops blob keys
  equal to the default. The converter runs at startup, as the owner approved
  for storage.
- Removing the global: L, done gradually behind the ratchet.

---

## D9. No enforced layering; three known edges point the wrong way (NEW)

**Current choice.** The layering is implicit. Measured violations (A.2):

- `config` imports `database` and more;
- `metadata` imports `operations/registry` (`internal/metadata/enhanced.go:25`);
- `database` imports domain packages (`matcher`, `fingerprint`, `chaptershape`,
  `metastate`, `personname`, `titleutil`, `syncapi/progress`).

There are 37 non-test comments about avoiding an import cycle. The
`scanner` / `organizer` / `metafetch` cycle is broken with function fields that
`internal/server` fills in (`scanner/service.go:67,601`,
`organizer/service.go:174-178`, `organizer/rename.go:35-39`).

**Cost today.**

- `internal/server` imports 105 module packages.
- `plugins/maintenance` imports 57.
- Changes in leaf packages recompile most of the tree.
- Callback fields set by the server are invisible to the compiler: a nil hook
  is a silent no-op.

**Alternatives.**

- (a) Write the layering down in docs only.
- (b) Enforce it with golangci-lint `depguard` rules and a baseline file,
  matching `.interface-width-baseline` and `.errcheck-baseline`.
- (c) Split into multiple Go modules.

**Recommendation: (b). Reject (c).** Multi-module development needs `go.work`,
which `CLAUDE.md` bans because it breaks the build (`ambiguous import` on the
genproto split). Target layers are in the main doc, section 3.2. Fix the three
edges in the PR that introduces the rule, or allowlist them in the baseline and
burn them down.

**Migration effort.** The rule plus baseline is S. Each edge fix is S to M.
The pipeline callbacks are the subject of D12.

---

## D10. Two god packages: `plugins/maintenance` and `internal/server` (NEW, shape set by 05)

**Current choice.**

- `internal/plugins/maintenance` is one package: 130 non-test files, 78,102
  lines, 103 `OperationDef` literals, fan-out 57.
- `internal/server` has 154 non-test files, 36,336 lines and fan-out 105, even
  after the handler extraction (`internal/server/handlers/*`).

**Cost today.**

- Any edit recompiles and retests the whole package. Measured on a warm cache,
  compiling the `internal/server` test binary takes 17.95 seconds of wall time.
- Unexported helpers are shared freely, so an op cannot be understood or
  deleted alone.
- File collisions between parallel PRs are frequent. The CLAUDE.md
  parallel-sweep rules exist for this reason.

**Alternatives.**

- (a) Leave them.
- (b) Split by domain, with subpackages under `internal/plugins/maintenance/`:
  - `authors`;
  - `versions`;
  - `fragments`;
  - `files`;
  - `itunesread`: read-only iTunes-derived repairs. It never touches
    `internal/writeback/`.
  - `metadata`;
  - `activity`;
  - `reports`.

  A thin `register.go` keeps the op IDs stable.
- (c) Wait for the ops-v3 SDK (workstream 05) and split into SDK-shaped
  modules.

**Recommendation: (c)'s shape, with (b)'s boundaries.**

- Workstream 05 decides what an op package looks like.
- This workstream supplies the domain grouping and the dependency rule that
  subpackages may not import each other, only shared `internal/repairs` and
  domain services.
- Op IDs and ConcurrencyKeys are unchanged by construction. A test asserts the
  registered ID set before and after.

**Migration effort.** L in total. Each domain move is one M PR; file moves
only, by `git mv` plus the export of the helpers that cross. For
`internal/server`, continue the handler extraction. Lifecycle moves to D11.

---

## D11. Startup is a 400-line imperative sequence beside a half-adopted service container (NEW)

**Current choice.**

- `internal/serviceregistry` is a string-keyed container:
  - `Register` is called from `init()`;
  - `Build` returns `any`;
  - `TryGet[T]` is used from 29 non-test files.
- Only 4 services implement the container's `Start(ctx) error`.
- `Server.Start` (`internal/server/server_lifecycle.go:160`) starts the rest by
  hand: 10 `go func`/`go s.` statements, plus the tickers.
- There are 47 non-test `time.NewTicker` calls across `internal/`.
- The systemd unit is `Type=simple` (`deploy/audiobook-organizer.service:59`),
  so systemd counts the service as up the moment the process forks.

**Cost today.**

- Shutdown order for the hand-started loops is not declared. It is whatever
  the `defer` and `shutdown` channel happen to do.
- Readiness is guessed by grepping the journal. The memory notes say to wait
  for `memdb warmup published`.
- A fatal startup step plus `Restart=on-failure` gives a crash loop.
- The 2026-10-03 outage was 75 seconds of this, caused by tracing init. PR
  #3690 fixed tracing: `internal/telemetry/telemetry.go:47-79` now degrades
  instead of failing. No rule covers the next fatal step.

**Alternatives.**

- (a) Status quo.
- (b) Every background loop becomes a container service with `Start` and
  `Stop`, or an operation or schedule in the ops system. Workstreams 04 and 05
  decide which.
- (c) Classify each startup step:
  - **fatal**: store open, cut-over, config validation, listener bind;
  - **degraded**: everything else.

  Degraded failures are listed under a `degraded` key in `/health`.
- (d) `Type=notify` with `READY=1` sent after the listener is up and the memdb
  is warm (D3). Deploy scripts then wait on `systemctl` instead of the journal.

**Recommendation: (b), (c) and (d).** (c) generalizes #3690. (d) makes "is it
ready" a fact systemd knows.

**Migration effort.**

- (c) is S: a table plus `/health`.
- (d) is S. It needs a `sd_notify` call; `coreos/go-systemd` or 20 lines over
  the socket. Installing the unit is the owner's step, because the deploy user
  has no general sudo.
- (b) is M, done per loop and coordinated with 04.

---

## D12. Pipeline stages are wired by callbacks to dodge import cycles (NEW, coordinate with 02 and 05)

**Current choice.**

- Scan, organize and metadata fetch call each other through function fields
  that the server fills in at startup (see D9).
- The memory note `project_identification_has_no_orchestrator` records that
  the owner chose a driver ("ID pipeline → state machine").

**Cost today.**

- The compiler cannot check this wiring.
- A test that builds a scanner without the hook silently skips auto-organize.

**Alternatives.**

- (a) Keep the callbacks.
- (b) Move the hooks into constructor parameters, so a missing hook is a
  build error.
- (c) The stages emit events and the orchestrator drives them.

**Recommendation: (b) now, and defer (c) to workstreams 02 and 05.** (b)
alone makes a missing hook a build error.

**Migration effort.** (b) is S to M.

---

## D13. The API contract is written by hand twice and checked nowhere (NEW, tooling choice shared with 06)

**Current choice.**

- `web/src/services/api.ts` is 8,266 lines with 314 exported functions and
  260 exported types, all written by hand.
- `docs/api/openapi.json` is also written by hand: 305 operations, last
  touched `a2684d736` on 2026-10-06.
- The server registers 528 routes in non-test files under `internal/server`.
  The paths are group-relative, so this is approximate.
- No script, test or build step reads `openapi.json`. A grep finds it only in
  archived task tooling.

**Cost today.**

- A Go JSON tag change breaks the frontend at runtime.
- The audit memories record several such cases: `omitempty` differences, and
  fields the ABS handler accepted and ignored.
- The spec covers at most about 58% of the registered routes.

**Alternatives.**

- (a) Keep writing both by hand.
- (b) Generate TypeScript types from the Go DTO structs (tygo-style). Keep the
  functions hand-written.
- (c) Generate OpenAPI from the routes and types (a typed-handler framework or
  route annotations), then generate the TS client from it.
- (d) Contract tests: record real responses in Go handler tests and type-check
  them against the TS types in Vitest.

**Recommendation: (b) plus (d) first, because they are cheap and remove the
drift class. Then split `api.ts` by domain (`api/books.ts`, `api/review.ts`,
and so on).**

- (c) is the long-term option.
- The OpenAPI file should be deleted or generated, never left hand-written and
  unchecked. That call is owner question Q6.
- Workstream 06 owns the choice of generator.

**Migration effort.** (b) is M. (d) is M. The split is M as file moves.

---

## D14. Frontend server state is cached ad hoc in each Review lane hook (NEW)

**Current choice.**

- State is held in Zustand stores (`web/src/stores/`, 4 stores) plus local
  hooks.
- Each Review lane hook does its own fetching, caching and staleness:
  - `useMetadataLane.ts`, 2,365 lines;
  - `useDupesLane.ts`, 1,223;
  - `useRegroupLane.ts`, 904;
  - `useRepairsLane.ts`, 893.
- The lane *descriptor* pattern (`components/review/lanes/types.ts`) is good:
  a total `verbs` map, so a new action that is not named fails to compile.

**Cost today.**

- Four implementations of loading, refetch, abort and stale handling.
- `services/api.ts` grows module-level caches. The tests
  `api.cachedCandidatesQuery.test.ts` and `api.cachedReviewResults.test.ts`
  exist because of them.
- The largest page files are `ActivityLog.tsx` (3,236 lines) and `Library.tsx`
  (2,671).

**Alternatives.**

- (a) Status quo.
- (b) Adopt a server-state library, such as TanStack Query, for fetch, cache,
  dedupe, abort and invalidation. Lanes keep their descriptors and lose their
  caching code.
- (c) A shared in-house `useResource` hook.

**Recommendation: (b).** It replaces code, not adds it. Migrate one lane first
(repairs, the smallest), then measure lines removed and test runtime before
doing the rest. Workstream 06 may already list the library; this decision is
about the pattern.

**Migration effort.** M per lane, so L in total.

---

## D15. Tests build real on-disk Pebble stores, and two hand-maintained store mocks coexist (NEW)

**Current choice.**

- 367 test call lines use `NewPebbleStore(` on disk, against 242 for
  `NewPebbleStoreInMemory(` (`vfs.NewMem()`).
- There is no shared constructor helper.
- The hand-written `MockStore` lives in a non-test file, so it is compiled
  into the production binary. It is used by 239 test files.
- The mockery `mocks.NewMockStore` is used by 84 test files.

**Cost today.**

- `Makefile:233-239` records `internal/server` taking 532 seconds on a normal
  macOS TMPDIR against 33.7 seconds on a RAM disk (15.8 times), because
  per-test Pebble setup is write-bound.
- `make test-short` takes about 500 seconds (`Makefile:254-263`).
- On GitHub, "Go Tests (short, race)" took 19.1 minutes and "Coverage Floor"
  16.9 minutes on the latest `main` run.
- The disk-filling incidents (memory `feedback_go_build_cache_fills_the_mac`)
  showed up as `TempDir: no space left` failures in `internal/database`.

**Alternatives.**

- (a) Keep the RAM-disk wrapper as an opt-in.
- (b) A single `dbtest.NewStore(t)` that defaults to in-memory vfs and opts
  into disk only for tests that copy, back up or reopen by path.
  `NewPebbleStoreInMemory`'s comment (`pebble_store.go:440-447`) lists exactly
  those cases.
- (c) Replace mocks with the real in-memory store wherever a test is not
  asserting call expectations.

**Recommendation: (b), then (c).**

- Move `MockStore` to a `_test`-only helper package, or delete it once (c)
  covers its 239 users. A permissive zero-value mock of 455 methods hides
  wiring mistakes, so a real in-memory store is the better default.
- Keep mockery for narrow handler interfaces, where expectations matter.

**Migration effort.** (b) is S for the helper and M to migrate the 367 sites
mechanically (a parallel sweep). (c) is L and gradual.

---

## D16. CI has three runners with different gate sets, and two-way ratchets fail main on improvements (NEW)

**Current choice.**

- **GitHub Actions** runs `ci.yml`. Its jobs include Interface Width Ratchet,
  Errcheck Ratchet, Repo Guards, Mock Freshness, TODO Fragment Headers,
  Coverage Floor and Fixture Tests.
- **Woodpecker** runs `.woodpecker/*.yaml`: six workflows that call `make`
  targets.
- **`make ci` / `make ci-remote`** run locally.
- Interface Width Ratchet and Coverage Floor exist only on GitHub (memory
  `feedback_woodpecker_misses_github_only_checks`).
- `.errcheck-baseline` and `.interface-width-baseline` fail in both directions
  by design.

**Cost today.**

- Of the last 30 `ci.yml` runs on `main`: 1 success, 9 failures, 20 cancelled
  (`gh run list --workflow ci.yml --branch main --limit 30`).
- The latest run (37843473291) failed:
  - Errcheck Ratchet, because findings went **down**, 779 to 770, without the
    baseline being lowered;
  - Interface Width Ratchet, with baseline 0 and actual 1.
- A red `main` therefore carries no information. Merges happen on Woodpecker
  green, which does not run these gates.

**Alternatives.**

- (a) Keep it as it is.
- (b) One gate manifest (`ci/gates.txt`), read by `make ci`, Woodpecker and
  GitHub, so the sets cannot diverge.
- (c) Ratchets fail only on an increase in PRs. A scheduled job lowers the
  baseline after an improvement lands on `main`.
- (d) Keep the two-way ratchets but auto-fix them in the merging PR.

**Recommendation: (b) plus (c).** (c) keeps the ratchet's purpose, which is
that the count never rises, and removes the "improvement turns main red"
failure. The two-way design was deliberate (`.interface-width-baseline`
header), so this is owner question Q7.

**Migration effort.** (b) is M. (c) is S.

---

## D17. Upgradeability: version pins in many places, tools outside go.mod, API v1 only (NEW, tooling owned by 06)

**Current choice.**

- **Go.** `go1.27.1` is pinned in `.envrc`, the `Makefile`, six Woodpecker
  files, `Makefile.local.example`, agent docs and more. CLAUDE.md lists the
  copies by hand.
- **Mockery.** v3.8.0 is pinned in `scripts/setup-mockery.sh:8`, `ci.yml:364`
  and the Makefile comment. The playbook memory says v3.7.1, which is stale.
- **Tools.** No `tool` directives in `go.mod`.
- **Pebble.** The format version is pinned by the approved storage design.
- **HTTP API.** Only `/api/v1`, plus the ABS-compatible surface used by
  AudioBooth.

**Cost today.** Every pin bump is a multi-file hunt. Contract changes to the
ABS surface are found by the external app.

**Alternatives.**

- Put tools into `go.mod` `tool` directives (workstream 06).
- Have one `GO_VERSION` file read by every runner.
- Make the ABS compat responses golden-file fixtures shared with the AudioBooth
  repo.

**Recommendation.**

- `GO_VERSION` single source and the ABS golden fixtures: this workstream.
- Tool directives: 06.
- No `/api/v2` is needed. Additive changes plus contract tests (D13) are
  enough for a single-owner app.

**Migration effort.** S each.
