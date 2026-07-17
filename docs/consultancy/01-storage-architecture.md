<!-- file: docs/consultancy/01-storage-architecture.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8eda33bd-6c97-42bd-95bd-98f2f8e81e12 -->
<!-- last-edited: 2026-07-17 -->

# Consultancy Evaluation — Storage & Architecture (2026-07-02)

Evaluation run by a read-only multi-agent workflow (schema-auditor, db-design, and systems-expert specialists, cross-checked by an advisor pass). All findings are cited as `file:line` against the repo state on 2026-07-02. Production context: PebbleDB is the only primary store, ~50K books live on 172.16.2.30.

## Executive Summary

The storage core is sound and disciplined. PebbleDB-as-primary fits the single-node, prefix-scan/point-lookup workload: 128 of 129 iterators are prefix-bounded, backfill flags are version-suffixed (`activity_pebble_v1_done`, `book_aggregates_v1_done`, `lsh_index_v1_done`), id16hex dedup keys are zero-padded, batch hygiene is good, and the recent HNSW hardening (#1740 stale-dimension snapshot discard, #1741 `safeAdd` panic containment) is correctly implemented. Derived in-memory vector indexes (chromem brute-force, coder/hnsw) are correctly treated as disposable mirrors of the authoritative `emb:v:` keyspace in Pebble — an architecture that made the recent 3072→1024 dimension cutover survivable by discarding artifacts instead of migrating data. The memdb atomic-publish warm layer, the `stats:library` dirty-flag cache with stampede mutex, and the unusually high WHY-comment density are genuine assets.

The material risks cluster in three places:

1. **A live data-loss footgun on the Book entity (STOR-1, high).** The BookFile memdb round-trip footgun (PERF-7) is now guarded in both `UpsertBookFile` and `BatchUpsertBookFiles`, but the *same footgun class* is unguarded on Book: memdb-stripped Books (Description, VersionNotes, and all `BookSig*` dedup-signature fields nil'd) flow from `GetAllBooks` into `UpdateBook`, a full JSON replacement preserving only `CreatedAt`. Reconcile, migrations, quarantine, and merge all do this read-modify-write, silently wiping descriptions and dedup signatures. Advisor verification adds a mitigating nuance: `UpdateBook`'s copy-on-write `book_ver:` snapshot captures the full pre-wipe book, so existing damage is recoverable — provided recovery is sequenced **before** any snapshot pruning (STOR-2).

2. **Derived-index rebuild gaps (ARCH-1/ARCH-2, high/medium).** The HNSW snapshot fast-path skips hydration with no staleness check against the Pebble source of truth — after any unclean shutdown, every vector upserted since the last clean shutdown is silently missing, which is the highest-value exposure during the in-flight bge-m3 re-embed. Export is non-atomic and Import can install partial state.

3. **Lifecycle and legacy residue (SYS-1..4, ARCH-3/STOR-3/SYS-3).** Shutdown's 30s escape hatch closes stores under still-running, non-context-aware startup jobs — the same race class #1733 fixed inside the registry, patched point-by-point but never enforced as an invariant. And the NutsDB tier no longer earns its keep: its Pebble replacement is fully scaffolded and reads have flipped, yet every boot still opens `activity.nutsdb`, pays double writes, keeps `metrics.nutsdb` NutsDB-primary, and carries a documented goroutine leak per `Open`. All three specialists independently reached the same conclusion: finish the cutover and delete NutsDB.

Secondary hygiene items: unpruned CoW snapshots growing monotonically under bulk sweeps, the repo's only unbounded full-keyspace scan in `KeyCount`, in-memory pagination in `ListAIJobs`, a corrupted/drifted canonical schema doc, unwrapped `Graph.Delete` despite documented library bugs, stale SQLite artifacts on prod disk, an 11,398-line `pebble_store.go`, v1→v2 operations-migration residue, and AI-REFERENCE.md contradicting itself on where the embedding keyspace lives.

### Advisor verification

The advisor pass spot-checked the specialist reports against the code:

- **STOR-1 confirmed**: `memdb_strip.go:34-40` nils Description/BookSig\*; `GetAllBooks` routes to memdb at `pebble_store.go:1391-1393`; `reconcile.go:1115` + `:1149` does read-modify-write via `UpdateBook`'s full JSON replace at `:2664-2683`.
- **ARCH-1/SYS-1 citations genuine**: the lifecycle comments (`server_lifecycle.go:468-471`) themselves admit the 30s-timeout risk, so SYS-1 is *known-but-unfixed* rather than undiscovered; severity is fair given the #1733 precedent.
- **NutsDB triplication**: STOR-3, ARCH-3, and SYS-3 are the same finding reported by all three specialists; they are consolidated below. ARCH-3's metrics-still-NutsDB-primary detail is the most complete angle.
- **Missed nuance (adjusts STOR-1/STOR-2)**: `GetBookByID` is Pebble-direct (`pebble_store.go:1691-1693`), so `UpdateBook`'s CoW `book_ver:` snapshot captures the full pre-wipe book. STOR-1 damage is therefore **recoverable from the very unpruned snapshots STOR-2 wants pruned** — sequence recovery before pruning.

## Findings Table

| ID | Severity | Impact | Effort | Title |
|----|----------|--------|--------|-------|
| STOR-1 | High | High | Low | memdb-stripped Book → UpdateBook full replacement silently wipes Description and BookSig dedup fields |
| ARCH-1 | High | High | Medium | HNSW snapshot fast-path skips hydration with no staleness check against the Pebble source of truth |
| SYS-1 | High | High | Medium | Shutdown 30s escape hatch closes stores under still-running, non-ctx-aware startup jobs |
| STOR-3 / ARCH-3 / SYS-3 | Medium | Medium | Medium | NutsDB no longer earns its keep — finish the Pebble cutover and delete it (consolidated) |
| STOR-2 | Medium | Medium | Low | Unbounded CoW `book_ver:` snapshot written on every UpdateBook; pruning is manual-only |
| ARCH-2 | Medium | Medium | Low | HNSW Export is non-atomic and Import mutates store state mid-loop on failure |
| ARCH-4 | Medium | Medium | Low | Canonical schema doc describes a ULID keyspace the code never adopted, and contains a corrupted duplicate of itself |
| SYS-2 | Medium | Medium | High | Server.Start is a 670-line monolith with dual lifecycle authorities (inline sequence vs container) |
| SYS-4 | Medium | Medium | Low | v1→v2 operations migration residue: hardcoded legacy resume shim, dead GlobalQueue references, stale docs |
| STOR-4 | Low | Low | Low | KeyCount performs the repo's only unbounded full-keyspace iteration |
| STOR-5 | Low | Low | Low | ListAIJobs loads and sorts the entire `aijob:` keyspace per request |
| ARCH-5 | Low | Medium | Low | coder/hnsw Graph.Delete is not panic-wrapped despite documented Delete+Add invariant bugs |
| ARCH-6 | Low | Low | Low | Legacy SQLite tier is correctly fenced in code but stale artifacts (embeddings.db) linger as an operational trap |
| SYS-5 | Low | Medium | Medium | pebble_store.go at 11,398 lines is the navigability and merge-conflict hotspot |
| SYS-6 | Low | Medium | Low | AI-REFERENCE contradicts itself on where the embedding/dedup keyspace lives |
| STOR-6 | Info | Low | Low | Positive: PERF-7 BookFile guards, bounded iterators, versioned backfill flags, and HNSW hardening verified |
| SYS-7 | Info | Low | Low | Positive: cache and derived-index layers are architecturally sound |

## STOR-1 — memdb-stripped Book → UpdateBook full replacement silently wipes Description and BookSig dedup fields (High)

**Detail.** `stripBookForMemdb` nils Description, VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments, BookSigBuiltAt, and BookSigCoveragePct before memdb insertion. In prod (`UseMemDB=true`) `PebbleStore.GetAllBooks` delegates to the memdb projection (`pebble_store.go:1392-1393`), so callers receive stripped Books. `UpdateBook` (`pebble_store.go:2664`) marshals the incoming struct verbatim, preserving only `CreatedAt`. Multiple live paths do GetAllBooks → mutate → UpdateBook: `reconcile.AssignOrphanVGs` (1115→1149), `migration014UpPebble` (615→628), quarantine restore (275), `merge/service.go:154`. Every touched book loses its description and its BookSigV1 dedup signature (expensive to rebuild; feeds the signature dedup layer). This is exactly the BookFile PERF-7 footgun class, but the Book entity has NO preserve-on-empty guard.

**Advisor adjustment (recoverability).** Because `GetBookByID` is Pebble-direct (`pebble_store.go:1691-1693`), `UpdateBook` reads the full unstripped old row before overwriting and writes it to the `book_ver:` CoW snapshot. Existing wipe damage is therefore recoverable from snapshots — the severity of *permanent* loss is bounded, but only while those snapshots survive. **Sequence any prod recovery of wiped Description/BookSig fields before implementing STOR-2's snapshot pruning.**

**Recommendation.** Add a preserve-on-nil guard in `UpdateBook` mirroring the PERF-7 BookFile guard: `UpdateBook` already fetches `oldBook`, so copy Description/VersionNotes/BookSig\* from `oldBook` when the incoming pointers are nil (zero extra reads). Add a regression test modeled on `pebble_bookfile_preserve_test.go`. Then audit prod for books with `BookSigBuiltAt` set but `BookSigV1` nil to size existing damage — and recover from `book_ver:` snapshots before any pruning.

**Citations:**
- internal/database/memdb_strip.go:29-46
- internal/database/pebble_store.go:1391-1393
- internal/database/pebble_store.go:2664-2683
- internal/reconcile/reconcile.go:1115
- internal/reconcile/reconcile.go:1149
- internal/database/migrations.go:615
- internal/database/migrations.go:628
- internal/quarantine/service.go:275

## ARCH-1 — HNSW snapshot fast-path skips hydration with no staleness check against the Pebble source of truth (High)

**Detail.** The HNSW snapshot is exported only on clean shutdown (`server_lifecycle.go:833`). On boot, Import loads it (`server.go:444`) and PostInit skips hydration entirely if `CountByType("book")>0` (`lifecycle.go:118-131`). Nothing compares the snapshot against the authoritative `emb:v:` keyspace in PebbleDB. After any unclean shutdown (panic, OOM — this app has a 69GB-bloat history — or kill -9), the next boot loads the last clean-shutdown snapshot and silently omits every vector upserted since, permanently until a manual re-embed or snapshot delete. During the current 29K-book re-embed this is the highest-value data at risk: a crash mid-scan means dedup Layer 2 quietly misses duplicates with no error anywhere.

**Recommendation.** On import, compare snapshot `graph.Len()` per entity type against `EmbeddingStore.CountByType` (already cheap, `embedding_store.go:369`) and either delta-hydrate the difference or discard the snapshot and hydrate fully when counts diverge beyond a small tolerance. Alternatively write a monotonic watermark (max emb ID / count) alongside the snapshot and validate it at Import. Periodic export (e.g. after embed-scan completion) would also shrink the window.

**Citations:**
- internal/server/server.go:434-453
- internal/dedup/lifecycle.go:115-131
- internal/server/server_lifecycle.go:832-845
- internal/database/hnsw_embedding_store.go:370-430

## SYS-1 — Shutdown 30s escape hatch closes stores under still-running, non-ctx-aware startup jobs (High)

**Detail.** Comments at `server_lifecycle.go:469-471, 480-482, 502-503` state `stripMovementAtoms`, `remuxMalformedM4BFiles`, and `transcodeMalformedM4BFiles` "do not check bgCtx" and are known contributors to the 30s grace timeout. At `:766-768`, when `bgWG.Wait()` times out the code logs a warning and "proceeds with shutdown anyway" — then closes `embeddingStore` (`:857`) and, via cmd/root.go's deferred `closeStore`, the main PebbleStore, while those goroutines may still iterate Pebble. This is structurally the same race #1733 fixed inside the registry (`registry.go:65-73`) and `Close()` fixed for warmup (`pebble_store.go:308-320`): each instance is patched point-by-point, but the invariant "no goroutine touches the store after Close" is not enforced anywhere. First-run-after-upgrade deploys (when these one-time jobs run for minutes on 50K books) are the exposure window.

**Advisor note.** The lifecycle comments themselves admit the timeout risk, so this is known-but-unfixed rather than an undiscovered defect; severity remains fair given the #1733 precedent.

**Recommendation.** Make the three one-time startup jobs ctx-aware (check `s.bgCtx` per file, as `backfillAcoustIDs` already does) so the 30s timeout stops firing. As defense-in-depth, add a closed atomic flag + iterator refcount to PebbleStore so post-Close access returns `ErrStoreClosed` instead of panicking.

**Citations:**
- internal/server/server_lifecycle.go:469-476
- internal/server/server_lifecycle.go:480-486
- internal/server/server_lifecycle.go:502-509
- internal/server/server_lifecycle.go:763-769
- internal/server/server_lifecycle.go:856-873
- internal/database/pebble_store.go:307-320

## STOR-3 / ARCH-3 / SYS-3 — NutsDB no longer earns its keep: finish the Pebble cutover and delete it (Medium, consolidated)

All three specialists independently flagged NutsDB; this section consolidates their three angles.

**Schema-auditor angle (STOR-3) — intrinsic costs.** (1) Bucket-per-book and bucket-per-op secondary indexes (`actOpBucket`/`actBookBucket`) — with 50K books this creates massive bucket counts, each carrying a RAM hint index under `HintKeyAndRAMIdxMode`; (2) Query's general path scans ALL tiers over the time range into memory, then filters/sorts/paginates in Go (`nuts_activity_store.go:194-232`) — O(entire in-range log) per `/api/v1/activity` request; (3) Summarize matches `keysToDelete` via a nested entries×keyLookup scan (`:302-311`), quadratic on large tiers; (4) documented Close goroutine leak pinned to v1.1.0 sentinels blocking upgrades; (5) `SyncEnable=false`. Meanwhile `DualWriteActivityStore` + `PebbleActivityStore` + the versioned flag `system:backfill:activity_pebble_v1_done` are already deployed (`register.go:78-82`) — the app pays double writes today.

**DB-design angle (ARCH-3) — the most complete version: metrics is still NutsDB-primary.** The NutsDB→Pebble activity migration completed ~Jun 10 (flag set; reads served from Pebble), yet every boot still opens `activity.nutsdb` and double-writes all activity (`register.go:62-82`). Separately, metricsstore is still NutsDB-primary at `metrics.nutsdb` (`registry_wire.go:162-178`) even though `PebbleMetricsStore` exists with a TTL sweep job compensating for Pebble's lack of native expiry (`sweep_pebble_metrics_ttl.go`). NutsDB v1.1.0 leaks one goroutine per Open on Close (documented TODO.md:902-913) and DB-3 transactional work is deferred pending "NutsDB evaluation". Two sidecar databases, double write amplification, and a leaky dependency remain for a rollback window that has been open ~3 weeks.

**Systems angle (SYS-3) — lifecycle cost.** F5-T024 shipped Jun 10 with a dual-write window (TODO.md:353); the follow-up T024b (remove NutsDB) never happened. `register.go:62-63` unconditionally opens `activity.nutsdb`; every activity Record/Summarize/Prune pays a double write forever (`dual_write_activity_store.go:60-76`), even after the backfill flag flips reads to Pebble (`:78-82`). The Pebble backend shares the main DB (`pebble_activity_store.go:54`) so removal costs nothing at the storage layer. Three storage engines (audiobooks.pebble, activity.nutsdb, ai_scans.db) each need distinct close ordering in shutdown (`server_lifecycle.go:815-873`).

**Recommendation (merged).** Verify `activity_pebble_v1_done` is set on prod and run the activity backfill if not; run one release cycle dual-write; then collapse `DualWriteActivityStore` to a pass-through `PebbleActivityStore`, flip metricsstore to `PebbleMetricsStore`, delete the nutsdb dependency, and archive/delete the `.nutsdb` dirs. This removes a whole storage engine, the goroutine leak, the double-write cost, and one shutdown-ordering hazard, and unblocks DB-3 — matching the TODO perf-cleanup NUTSDB task.

**Citations:**
- internal/database/nuts_activity_store.go:66-68
- internal/database/nuts_activity_store.go:88
- internal/database/nuts_activity_store.go:104-115
- internal/database/nuts_activity_store.go:194-232
- internal/database/nuts_activity_store.go:302-311
- internal/activity/register.go:53-83
- internal/database/dual_write_activity_store.go:60-76
- internal/database/dual_write_activity_store.go:195-201
- internal/database/pebble_activity_store.go:52-66
- internal/database/pebble_activity_backfill.go:34
- internal/server/registry_wire.go:158-179
- internal/maintenance/jobs/sweep_pebble_metrics_ttl.go:7-57
- TODO.md:902-913

## STOR-2 — Unbounded CoW `book_ver:` snapshot written on every UpdateBook; pruning is manual-only (Medium)

**Detail.** `UpdateBook` snapshots the full old Book JSON to `book_ver:<id>:<unixnano>` on every call, including whole-library sweeps (reconcile, migrations, maintenance plugins, organizer). A fingerprinted book's JSON carries ~22KB of BookSigV1 base64, so a single 50K-book sweep can add >1GB of snapshot keys. `PruneBookSnapshots` exists but its only caller is the manual metadata handler endpoint (`handler.go:698`) — nothing prunes automatically, so the keyspace grows monotonically with every bulk operation and inflates compaction and the KeyCount diagnostic.

**Advisor adjustment.** These same snapshots are what makes STOR-1's damage recoverable — they capture the full pre-wipe book. Any STOR-1 recovery work MUST be sequenced before pruning is enabled.

**Recommendation.** Enforce a per-book retention cap inside `UpdateBook` itself (e.g., after committing, delete snapshots beyond N=10 using the existing prefix scan — amortized cheap), or add a periodic maintenance op that iterates `book_ver:` and prunes. Also consider skipping snapshot writes for no-op field changes during bulk sweeps. Do not enable either until STOR-1 recovery is complete.

**Citations:**
- internal/database/pebble_store.go:2688-2701
- internal/database/pebble_store.go:3108-3133
- internal/server/handlers/metadata/handler.go:698

## ARCH-2 — HNSW Export is non-atomic and Import mutates store state mid-loop on failure (Medium)

**Detail.** Export writes `.bin` and `.meta.json` directly to their final paths via `os.Create`/`os.WriteFile` — no temp+rename, no fsync (`hnsw_embedding_store.go:342-364`). A crash mid-export leaves a truncated snapshot. Import installs `s.graphs[entityType]` (line 411) before reading the metadata sidecar; if the meta read/unmarshal fails (lines 414-424) it returns an error with the graph already installed but meta absent. The caller only logs the error (`server.go:446`), then PostInit sees `CountByType>0` and skips hydration (`lifecycle.go:119`) — leaving a graph whose FindSimilar metadata filter rejects every node (`metadataMatches` fails on missing keys, line 315-324), so filtered dedup queries silently return nothing.

**Recommendation.** Export to `<file>.tmp` then `os.Rename`; Import into local maps and only commit to `s.graphs`/`s.meta` after every file for every entity type parses (all-or-nothing). On any Import error, leave the store empty so the existing hydration fallback actually runs.

**Citations:**
- internal/database/hnsw_embedding_store.go:341-365
- internal/database/hnsw_embedding_store.go:381-427
- internal/dedup/lifecycle.go:118-122

## ARCH-4 — Canonical schema doc describes a ULID keyspace the code never adopted, and contains a corrupted duplicate of itself (Medium)

**Detail.** `docs/database-architecture.md` declares `database-pebble-schema.md` "canonical for any new feature touching persistence", but that doc specifies ULID-keyed `a:`/`b:`/`idx:` prefixes while production code uses integer-ID `author:%d`, `book:%d`, `author:name:<norm>` keys with `"book:0".."book:;"` ASCII-range iteration bounds (`pebble_store.go:432-433, 1102-1103`). Only the dedup/emb section (lines 561-600) matches reality (`embedding_store.go:75-84`). Worse, the file physically contains two concatenated versions (a second header at line 312) with content interleaved mid-JSON-schema (e.g. the User entity split by a backfill section at lines 419-431). Anyone following the repo's own decision framework ("read existing schema first") gets actively misled.

**Recommendation.** Regenerate the schema doc from the real keyspace (grep the `fmt.Sprintf` key constructors in pebble_store.go, embedding_store.go, pebble_activity_store.go, ai_scan_store.go, pebble_store_lsh.go) and delete the duplicated block. Mark the ULID design explicitly as an unimplemented proposal or remove it. Consider a small test that asserts documented prefixes exist in code to prevent re-drift.

**Citations:**
- docs/database-pebble-schema.md:24
- docs/database-pebble-schema.md:312-645
- docs/database-architecture.md:22-27
- internal/database/pebble_store.go:432-457
- internal/database/pebble_store.go:1102-1112

## SYS-2 — Server.Start is a 670-line monolith with dual lifecycle authorities (Medium)

**Detail.** `Start()` mixes container start, cache warmers, HTTP/2/3/TLS setup, role seeding, resume logic, file watchers, four ticker loops, signal handling, and the entire ~175-line teardown sequence. The SERVER-LIFECYCLE-FLIP comment (`:801-813`) admits the split-brain: "Inline Stops above remain the source of truth for the carefully-sequenced teardown... Container.Stop is idempotent on already-stopped services for those." Two stop orders coexist, relying on idempotence rather than a single dependency graph. Meanwhile teardown coordinates FIVE concurrency trackers: inline stops, container.Stop, the named `bgWG`, a function-local `backgroundWG` + shutdown channel (`:522-525`), and the registry's internal `goroutineWG`. Every new background task must pick the right one; picking wrong reproduces SYS-1.

**Recommendation.** Finish the flip: move the remaining hand-ordered Stops (opRegistry drain, writeBackBatcher flush, itunesSvc, HNSW export, embed/ollama, embeddingStore/aiScanStore closes) into container Stoppers with declared deps, so ordering is derived not hand-maintained. Fold the local backgroundWG tickers into bgWG or container-managed services. Extract `Start()` into startHTTP/startBackground/shutdown methods.

**Citations:**
- internal/server/server_lifecycle.go:211-878
- internal/server/server_lifecycle.go:801-813
- internal/server/server_lifecycle.go:522-525

## SYS-4 — v1→v2 operations migration residue: hardcoded legacy resume shim, dead GlobalQueue references, stale docs (Medium)

**Detail.** The v1 OperationQueue is gone (internal/operations/ has no queue.go; zero non-test GlobalQueue references), but: (a) `resumeLegacyOp` (`server_lifecycle.go:111-209`) hardcodes ~10 pre-UOS op-type names in a switch, several with near-identical re-enqueue boilerplate — every entry must be maintained until no prod DB can contain a v1 op row; (b) `transcode_integration_test.go:401` (behind the `integration` build tag) still calls `operations.GlobalQueue.Enqueue` and cannot compile — the tagged test silently rotted; (c) `docs/AI-REFERENCE.md:21` lists `database.GlobalQueue` as a live global and `:87-89` documents internal/operations queue.go/OperationQueue. Agents (and this repo's own project-context skill) are told to trust that doc.

**Recommendation.** Update AI-REFERENCE §internal/operations to describe the registry (dispatcher/worker/watchdog/ResumePolicy). Fix or delete the integration-tagged transcode test (add a CI job that at least compiles `-tags integration`). Add a cutoff: after N releases, `resumeLegacyOp` collapses to "mark failed, please retry".

**Citations:**
- internal/server/server_lifecycle.go:111-209
- internal/transcode/transcode_integration_test.go:401
- docs/AI-REFERENCE.md:21
- docs/AI-REFERENCE.md:87-89

## STOR-4 — KeyCount performs the repo's only unbounded full-keyspace iteration (Low)

**Detail.** `KeyCount` opens `NewIter(nil)` and steps every key in audiobooks.pebble to count them — the only unbounded iterator in internal/ (all other 128 NewIter sites are prefix-bounded). With 50K books + 308K book files + secondary indexes + dedup/emb/act keyspaces + unpruned `book_ver:` snapshots (STOR-2), this is millions of keys per call on the DB-health diagnostics endpoint, holding an iterator (and its snapshot) open for the duration and churning block cache.

**Recommendation.** Replace the exact count with pebble's estimates: `db.Metrics()` table/entry stats or `EstimateDiskUsage` per known prefix, and label the value "estimated". If an exact count is genuinely needed, compute it in a rate-limited background job and cache the result (cached-aggregate + dirty-flag pattern already used elsewhere).

**Citations:**
- internal/database/pebble_store.go:10488-10498

## STOR-5 — ListAIJobs loads and sorts the entire `aijob:` keyspace per request (Low)

**Detail.** `ListAIJobs` prefix-scans all `aijob:` keys, unmarshals every job, filters, sorts by CreatedAt desc in memory, then applies offset/limit. Keys are `aijob:<id>` (not time-ordered), so pagination cannot push down. Fine at current volumes, but AI-scan job counts grow monotonically with the local-Ollama re-embed/scan cadence and there is no visible pruning of completed jobs; each list call's cost grows linearly forever.

**Recommendation.** Either key jobs time-ordered (`aijob:<20-digit-unixnano>:<id>`, matching the activity-store convention) so reverse iteration yields CreatedAt-desc natively with early exit at limit, or add retention pruning for terminal-state jobs older than N days. Low urgency; pair with any next touch of ai_scan_store/aijob code.

**Citations:**
- internal/database/pebble_store.go:10447-10484

## ARCH-5 — coder/hnsw Graph.Delete is not panic-wrapped despite documented Delete+Add invariant bugs (Low)

**Detail.** PR #1741 wrapped `Graph.Add` in `safeAdd` with `recover()` because coder/hnsw v0.6.1 panics on some re-insert states, and `server.go:436-438` documents a production crash loop caused by a nil-deref "when Delete+Add violates the per-layer node invariant" (HNSW-CRASH-2026-06-18). Yet `HNSWEmbeddingStore.Delete` calls `g.Delete(entityID)` bare (line 194). Delete runs on live paths (book merge/removal mirrors) from goroutines where an unrecovered panic kills the process — exactly the failure class #1741 was shipped to contain.

**Recommendation.** Apply the same recover-to-error wrapper to `g.Delete` (and any other Graph method invoked on live paths), mirroring `safeAdd`. Trivial change; add a companion test alongside `hnsw_panic_safe_test.go`.

**Citations:**
- internal/database/hnsw_embedding_store.go:189-199
- internal/database/hnsw_embedding_store.go:161-171
- internal/server/server.go:436-438

## ARCH-6 — Legacy SQLite tier is correctly fenced in code but stale artifacts (embeddings.db) linger as an operational trap (Low)

**Detail.** SQLite requires an explicit `--enable-sqlite3-i-know-the-risks` flag and config validation rejects other values (`config.go:1165`) — good fencing. But the stale embeddings.db is still acknowledged as present on prod (status doc line 51), and code comments still describe "the SQLite/Pebble EmbeddingStore" as source of truth (`chromem_embedding_store.go:13, 63`) years after Pebble became sole primary. The risk is human, not mechanical: a future operator or agent finding embeddings.db on disk (now full of dead 3072-dim OpenAI vectors) could waste time or wire something against it.

**Recommendation.** Archive-and-delete embeddings.db (and any audiobooks.db) from prod after the bge-m3 re-embed completes; sweep code comments that still say "SQLite/Pebble" to say Pebble; note the removal in the schema doc's legacy section.

**Citations:**
- internal/config/config.go:1165-1167
- cmd/root.go:350-351
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:51
- internal/database/chromem_embedding_store.go:12-14

## SYS-5 — pebble_store.go at 11,398 lines is the navigability and merge-conflict hotspot (Low)

**Detail.** One file implements the bulk of the ~36 role interfaces composing `Store` (store.go). It is 4x the next-largest non-generated file and the first thing a new engineer opens. The struct itself is clean (`pebble_store.go:120-141`: atomic memPtr, three scoped mutexes, warmup lifecycle fields, all with contract comments), and the team has already established the split pattern — pebble_store_lsh.go, pebble_store_ops_v2.go, pebble_store_isbn_index.go, pebble_quick_queries.go, pebble_store_book_aggregates.go exist as domain files. The remaining monolith is inertia, not design. Given the mandated parallel-subagent workflow (CLAUDE.md worktree discipline), a single 11K-line file is also the most likely rebase-conflict site across concurrent waves.

**Recommendation.** Continue the established split: peel book/author/series/operation/settings method groups into `pebble_store_<domain>.go` files (same package, zero behavior change — a good /parallel-sweep candidate). Keep the key-schema header comment in one place and point the others at it.

**Citations:**
- internal/database/pebble_store.go:120-141
- internal/database/store.go:1

## SYS-6 — AI-REFERENCE contradicts itself on where the embedding/dedup keyspace lives (Low)

**Detail.** `AI-REFERENCE.md:53` says EmbeddingStore "wrap[s] a separate PebbleDB for embeddings + dedup candidates", while the same doc's key-schema section (`:357`) correctly says the `emb:`/`dedup:` keyspace is "within the main audiobooks.pebble DB". Code confirms shared DB: `registry_wire.go:69` builds it with `database.NewEmbeddingStore(ps.DB())`. This is not cosmetic: an agent believing embeddings live in a separate DB could scope a backup, wipe, or restore to audiobooks.pebble alone and assume the dedup/labeled-dataset keyspace is unaffected (or vice versa). With the bge-m3 re-embed in flight and embeddings.db SQLite already stale/legacy per docs/status/2026-07-02, the number of places an engineer can look for "the embeddings" is confusing enough without the reference doc disagreeing with itself.

**Recommendation.** One-line fix to AI-REFERENCE.md:53 ("backed by the main audiobooks.pebble DB"). Add a short "storage inventory" subsection listing all on-disk artifacts: audiobooks.pebble (incl. emb:/dedup:), ai_scans.db, activity.nutsdb, hnsw snapshot dir, legacy embeddings.db (dead).

**Citations:**
- docs/AI-REFERENCE.md:53
- docs/AI-REFERENCE.md:357
- internal/server/registry_wire.go:69
- internal/database/embedding_store.go:206-208

## STOR-6 — Positive: PERF-7 BookFile guards, bounded iterators, versioned backfill flags, and HNSW hardening verified (Info)

**Detail.** Verified healthy: (1) the memdb AcoustIDFingerprint strip is now guarded in BOTH `UpsertBookFile` and `BatchUpsertBookFiles` (preserve-on-empty for fingerprint + 3 diagnostic pointers), closing the previously-latent UpsertBookFile gap; (2) AcoustIDSeg0-6 memdb stripping is safe because T020 drops segs from stored values entirely; (3) all migration/backfill flags are version-suffixed (activity_pebble_v1_done, book_aggregates_v1_done, versiongroup_index_v1_done, lsh_index_v1_done, book_isbn_index_v1_done); (4) HNSW `safeAdd` panic containment (#1741) and stale-dimension snapshot discard on Import (#1740) are correct — Import's discard path leaves the graph empty for rebuild-by-hydration as intended, and FindSimilar guards query-dim mismatch before the library can panic.

**Recommendation.** No action. Keep the UpsertBookFile/BatchUpsertBookFiles guards in sync as commented, and extend the same discipline to Book writes per STOR-1.

**Citations:**
- internal/database/pebble_store.go:9969-9985
- internal/database/pebble_store.go:10033-10053
- internal/database/memdb_strip.go:87-114
- internal/database/pebble_store_lsh.go:44
- internal/database/pebble_activity_backfill.go:34
- internal/database/hnsw_embedding_store.go:163-171
- internal/database/hnsw_embedding_store.go:399-410

## SYS-7 — Positive: cache and derived-index layers are architecturally sound (Info)

**Detail.** Three patterns a reviewer should recognize as deliberate and defend: (1) `stats:library` cache — single k:v with lazy dirty-flag invalidation via NoSync delete (crash-safety reasoned in the comment at `pebble_store.go:196-199`), TTL fallback, and a recompute-stampede mutex (`:131`); (2) memdb warm layer — `atomic.Pointer` publish with reads falling back to Pebble until ready, and an explicitly documented `WaitForWarmup` test contract explaining the write-through race it prevents (`:154-170`); (3) HNSW as derived best-effort state — `safeAdd` recovers coder/hnsw library panics into errors precisely because "a single bad mirror cannot be allowed to abort a 44K-book re-embed" (`hnsw_embedding_store.go:145-150`), with stale-dim snapshot discard on Import (#1740). Startup warmers at `server_lifecycle.go:266-278` are fire-and-forget and now cache typed counts (post 69GB-bloat fix). The WHY-comment density in this codebase is well above industry norm and is a genuine asset for onboarding.

**Recommendation.** No change. When adding new caches, copy the stats:library pattern (dirty flag + TTL + stampede mutex) per the documented feedback_cached_aggregates_dirty_flag convention; when adding derived indexes, copy the HNSW containment pattern.

**Citations:**
- internal/database/pebble_store.go:194-230
- internal/database/pebble_store.go:126-170
- internal/database/hnsw_embedding_store.go:143-171
- internal/server/server_lifecycle.go:266-278

## Steelman Analyses (db-design specialist)

**(a) PebbleDB-as-primary.** Strongest case: this is a single-node, single-writer embedded app deployed cross-arch (amd64 Lenovo + arm64 RPi fleet); pure-Go Pebble gives one static binary with zero CGO, which SQLite demonstrably failed at (the scary opt-in flag exists because cross-compilation actually broke builds). The workload — 50K books, prefix scans, point lookups, write-heavy import/scan bursts — is precisely LSM-shaped, and every observed query pattern is served by hand-rolled secondary index keys plus the memdb read layer. Co-locating `dedup:`, `emb:`, `aiscan:`, and activity in one Pebble instance buys atomic cross-domain batches and one backup unit. Postgres would add a server, migrations, and ops surface to a homelab appliance for zero query-shape benefit; the 69GB incident and pagination caps were application-cache bugs, not storage-engine limits. VERDICT: correct decision — keep it. The tax is discipline costs (full-replacement UpdateBook, fingerprint-strip footguns, an 11,398-line pebble_store.go, doc drift per ARCH-4), which are cheaper to pay down than a migration.

**(b) Derived vector indexes coupled to the primary store.** Strongest case: making PebbleDB's `emb:v:` keyspace the single source of truth and treating chromem/HNSW as disposable in-memory mirrors is what made the last month survivable. The 3072→1024 dimension cutover was resolved by discarding a derived artifact (#1740), not migrating data; coder/hnsw's crash bugs are containable (#1741) precisely because the graph is rebuildable; chromem's per-document gob persistence failure (MAYDEPLOY-D2) was absorbed by deleting persistence, not data. The VectorANNStore interface makes the buggy library swappable with zero data migration, and vectors ride the same backup/consistency domain as books. A standalone vector DB (qdrant/weaviate) would reintroduce a second source of truth, network sync, and drift. VERDICT: right architecture; the one betrayal of its own principle is the snapshot fast-path that skips rebuild-from-truth without a staleness check (ARCH-1). Fix that check and this design is exactly what a derived index should be.
