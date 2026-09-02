<!-- file: docs/audits/2026-09-01-go-ts-work-discovery.md -->
<!-- version: 1.0.0 -->
<!-- guid: 583cd97c-20a9-48c8-b130-aafc151d195b -->
<!-- last-edited: 2026-09-01 -->

# Go and TypeScript work discovery — 2026-09-01

## Purpose and scope

Read-only discovery. Nothing in this document was fixed; the user's instruction was "find the work and document it". Four parallel read-only agents surveyed the `refactor/go-modernize` worktree: a Go work-discovery audit (source 1), a Go hand-modernization census of what `go fix` will not do (source 2), a TypeScript/React work-discovery audit (source 3) and a TypeScript tooling/package census (source 4). Source 1 records that everything under `internal/` and `cmd/` was at main HEAD `611cbbe61` with the sibling agent's uncommitted Go 1.27 toolchain edits on top (source 1: go.mod → 1.27.0, Makefile, 10 workflow files; source 2 and source 3: only `go.mod`/`go.sum` modified, worktree HEAD `df642d11c`). Every finding below traces to one of those four reports; this document is the input to the modernization PRs B/C/D and adds no findings of its own.

## How to read this

- **Cost** is the source's own sizing: S (hours), M (a PR or two), L (multi-PR, needs its own plan). **Confidence** is carried where the source gave it.
- **Anchors** are `file:line` at the HEAD above, repo-relative (the sources wrote them under `.worktrees/go-modernize/`). Counts are the sources' counts, reproduced verbatim; where two sources disagree both figures are given with attribution.
- Section numbering inside each Part mirrors the source report (D1…D8, C1…C5, S1…S10, A…J, 1.1…7) so the follow-on PRs can cite items by the same IDs.
- The sources did not have the gopls/TypeScript LSP tools; symbol references were grep-counted and hand-verified (see each "Method and limits").

## Top findings across both codebases

Merged from the four sources' own top-10 tables, interleaved by (impact, cost) with each source's rank preserved in the "from" column. No finding was added or re-ranked relative to its own source; the order between sources is editorial.

| # | Item | Anchors | Impact | Conf | Cost | From | Why this rank |
|---|---|---|---|---|---|---|---|
| 1 | Bare `store.UpdateBook` flipping `IsPrimaryVersion`, both return values discarded | `internal/reconcile/reconcile.go:828,837` | High | High | S | Go audit #1 | Feeds the live `is_primary_version` divergence; two lines |
| 2 | `getBooksByAuthor` reads `data.items` off a `{data:{items,…}}` envelope and always returns `[]` | `web/src/services/api.ts:1560-1565`; consumer `components/dedup/DedupAuthorTab.tsx:155` | High | High | S | TS audit #1 | Confirmed at source and consumer; the author popover renders "No books found" for every author |
| 3 | `Book` type declares `bitrate`/`sample_rate`/`series_position`; Go sends `bitrate_kbps`/`sample_rate_hz`/`series_sequence` | `api.ts:138,140,113` vs `internal/database/store.go:223,225,190` | High | High | S | TS audit #2 | Two Library columns and three BookDetail fields are permanently blank |
| 4 | Legacy `cleanup_backups.go` deletes `.bak` matches with no retention age, no error, no count | `internal/maintenance/jobs/cleanup_backups.go:24,39-86` | High | High | S–M | Go audit #2 | Data-loss-adjacent; three predicates for one op name; zero tests |
| 5 | Author-split twins discard `UpdateBook` error then `booksUpdated++` | `internal/scheduler/extra_ops.go:385-411`, `internal/plugins/maintenance/author.go:222-252` | High | High | S fix / L dedupe | Go audit #3 | Op reports N updated when 0 may have been; two copies of the same bug |
| 6 | Vitest `coverage.include` unset, so 58 of 214 source files (~24k lines) are absent from the 48% headline; dead `test:` block in `vite.config.ts` disagrees with `vitest.config.ts` | `web/vitest.config.ts`, `web/vite.config.ts` | High | High | S | TS audit #3, TS census #2 | The coverage gate measures loaded files only; the number is an inflated instrument |
| 7 | Handler returns 200 after unchecked `UpdateBook` writes | `internal/server/handlers/versions.go:318,329,448`; `:311` | Med-High | High | S | Go audit #4 | Version-group linkage written without evidence |
| 8 | Revert/undo failures swallowed; stale "endpoint not yet deployed" catch on a live route | `web/src/pages/ActivityLog.tsx:693`, `ChangeLog.tsx:105`, `MaintenanceTab.tsx:99-107` | High | High | S | TS audit #4 | User believes a revert happened; the route exists at `internal/server/wire_operations_routes.go:95` |
| 9 | Startup file log goes dead after `server.New` re-installs `slog.SetDefault` | `cmd/root.go:517-545`, `internal/server/server.go:870-893` | Med | High | S | Go audit #5 | Every `serve` run has an empty file log; one-line root cause |
| 10 | 13 batch callers ignore `failed/errors/skipped` in a 200 body | see Part 3 §4.6 | High | High | M | TS audit #5 | Partial failures report success; good in-repo models exist |
| 11 | Async loading inside components (`try/finally`) makes the React Compiler bail on 218/329 components (66%) | 206 `finally` sites in 69 files; `web/src/hooks/useAsyncAction.ts` (4 callers) | — | — | medium risk | TS census #1 | Unlocks the compiler for two-thirds of the tree; deletes 57+57 loading/error state pairs |
| 12 | 10 N+1 `GetBookFiles` loops while `GetBookFilesForIDsCore` exists | `internal/audiobooks/service_filtering.go:1041` + 10 loops (Part 1 D7) | Med | High | M | Go audit #6 | Perf on 60K books; the `TODO(PERF-5)` claiming a new API is needed is stale |
| 13 | `t.TempDir`/`t.Setenv`/`t.Chdir` at 15+8+12 = 35 test sites | `internal/database/pebble_store_test.go:24` and Part 2 §C | — | — | pure | Go census #1 | Removes hand cleanup and `/tmp/test_pebble_*` litter; check `t.Parallel` conflicts |
| 14 | Pointer helpers → `new(expr)`: 461 call sites, `stringPtr` defined 8 times | Part 2 §H | — | — | pure | Go census #2 | Deletes ~68 helper definitions with zero behaviour risk |
| 15 | Two environment-dependent Go test failures on main | `internal/ai/embedding_client_test.go:258-263`, `internal/scanner/chapter_persistence_test.go:146-150` | Med | High | S | Go audit #9 | Every local `make test` is red on the auditing machine; both fail on main *Editor's note: the embedding test was made hermetic in #3039; the chapter-persistence constant is filed in `todo.d`.* |

Deliberately not in this table (per source 1): **D1** (retire one of three maintenance frameworks, L) and **D6** (unify the two MockStores, L) — the source judged them highest long-term payoff but multi-PR refactors needing their own plan.

## Part 1 — Go: work discovered

Source: Go work-discovery audit. Root for anchors: repo root at main HEAD `611cbbe61`. Commands ran with `GOTOOLCHAIN=go1.27.1`.

### 1.1 Duplication (by behaviour)

| # | Behaviour duplicated | Copies | Why it matters | Cost |
|---|---|---|---|---|
| D1 | Whole maintenance-op catalogue exists three times: legacy `internal/maintenance/jobs/*` (blank-imported `internal/server/server.go:39`, bridged by `internal/server/maintenance_job_op.go` + `maintenance_dispatcher.go`), plugin `internal/plugins/maintenance/*` (`sdk.OperationDef` + cron `Schedule`), and `internal/scheduler/extra_ops.go` (985 lines, 13 `scheduler.*` op IDs, wired via `internal/server/scheduler_extra_ops.go`, `server.go:294-299,620-625`, triggered from `internal/scheduler/tasks.go:564,675,731` and `scheduler/maintenance.go:162-169`). `extra_ops.go:580` says "NOTE: this is one of THREE implementations". | A fix lands in one and not the others. Concretely the author-split loop in `scheduler/extra_ops.go:385-411` (comment: "Duplicate of internal/plugins/maintenance/author.go — keep in sync") and `plugins/maintenance/author.go:222-252` both do `_, _ = store.UpdateBook(...)` and count `booksUpdated++` regardless. 14.6K vs 30.2K LOC across the two frameworks. | L |
| D2 | Backup-file cleanup, 3 predicates: `internal/maintenance/jobs/cleanup_backups.go:24` regex `(?i)\.(backup\|bak)$\|\.bak-\d{8}-\d{6}$` with no retention age (`:39-86`, `if rerr := os.Remove(path); rerr == nil { removed++ }`, `_ = removed`; comment `:51` admits the match is a "NAMING COINCIDENCE, not a control"); `internal/plugins/maintenance/cleanup.go:205-270` (`.bak-` substring + retention days; comment: "this rule exists in three places with two different predicates"); `scheduler/extra_ops.go` `scheduler.cleanup-old-backups`. | Same op name, different deletion semantics; the legacy one deletes anything matching `.bak` with no age check. Data-loss-adjacent. | M |
| D3 | Pagination parsing — canonical `internal/httputil/parse.go:74 ParsePaginationParams` (default 50, cap 1000) vs 14 stray parsers: `internal/server/fingerprint_diagnosis_handler.go:48` (no cap), `handlers/cache.go:240` (no cap), `handlers/activity.go:260` (cap 10000), `handlers/ai.go:859` (`limit, _ := strconv.Atoi(...)`, no cap), `handlers/operations_v2.go:198,332`, `handlers/audiobooks/handler_metadata.go:41,83`, `handlers/system/handler.go:330` (no cap), `handlers/abs/browse.go:141`, `handlers/review/handler.go:279` + private `atoiDefault` `:607`, `handlers/entities/handler.go:347` (no cap), `handlers/dedup/label_review.go:38,147` + private `clampAtoi` `:319`, `handlers/abs/stats.go:182 queryInt`. | Five endpoints accept `limit=999999999` (memory/latency). Three private helpers reimplement the same clamp. | M |
| D4 | Retry/backoff loops + Retry-After parsing: `internal/ai/retry.go:117 DoWithRetry` (quadratic); `internal/ai/embedding_client.go:347` hand-rolled loop with `// TODO(#TASK-12-followup): route embedBatchRaw through DoWithRetry` (`:376`, 60 days old); `internal/metadata/providerhttp/providerhttp.go:221 RoundTrip` / `:277 shouldRetry` / `:295 retryDelay` / `:310 parseRetryAfter` (int and HTTP-date, cap 5 min); `internal/acoustid/client.go:50 parseRetryAfter` (int only, cap 60 s) + loop `:133`. | Two `parseRetryAfter` with different accepted grammars; AcoustID ignores a date-form header and retries early. | M |
| D5 | Two slog default setups: `cmd/root.go:517-545 setupFileLogging` installs a Debug-level text handler to `logs/audiobook-organizer-<date>.log` + stdout (called from scan/organize/serve at `:74,:167,:226`); then `internal/server/server.go:870-893` does `slog.SetDefault(slog.New(slog.NewTextHandler(aw, &slog.HandlerOptions{Level: slog.LevelInfo})))`. Plus two logging APIs: `slog.*` 1,673 call sites vs `internal/logging` 172. | The file log configured at startup goes dead after `server.New` runs; the file records only startup lines. | S |
| D6 | Two MockStores: hand-written permissive `internal/database/mock_store.go` (header v1.95.0 claims "~22 server test files" use it — measured 128 test files) vs mockery-generated strict `internal/database/mocks` (28,653 lines, 67 test files). ~30 near-identical `newTestStore/setupPebbleStore/…` constructors across 139 `_test.go` files. | Two mock semantics (`nil,nil` vs sentinel) is the exact shape of the #2860 wedge; the header's justification is stale by ~6x. | L |
| D7 | "Load every book, filter in memory" instead of the batch getter: `GetBookFilesForIDsCore` exists (`internal/audiobooks/service_filtering.go:1041`, 6 callers) but 10 non-test loops still do per-book `GetBookFiles` inside `for range books`: `internal/server/maintenance_fixups.go`, `plugins/maintenance/regroup_apply.go`, `plugins/maintenance/itunes_regroup.go`, `merge/service.go`, `maintenance/jobs/dedup_books.go`, `maintenance/jobs/backfill_book_files.go`, `itunes/service/importer.go`, `itunes/cleanup_merged.go`, `dedup/split_book_merge.go`, `itunes/backfill.go:97`. | The itunes/backfill TODO says the fix "requires a new Store interface method" — the method effectively exists; the TODO is stale and 9 sibling loops never got it. | M |
| D8 | Known-open third path→author parser: `internal/metadata/folder_parser.go` (shares no function name with the two collapsed in #3035). Cited only. | — | M |

### 1.2 Concurrency hotspots (verified at HEAD)

Items 1-5 of `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` now use `registry.RunItems`/errgroup — confirmed by the source. Still serial:

| # | Site | Shape | Why it matters | Cost |
|---|---|---|---|---|
| C1 | `internal/dedup/auto_resolve.go` `AutoResolveCertain` | plain `for range` over candidate groups with per-group DB writes (2026-07-05 audit item 6, still open) | Whole-library apply path. Needs disjoint partitioning by group ID, not naive fan-out. | M |
| C2 | `internal/itunes/backfill.go:85-105` | N+1 `GetBookFiles` per book, `if fErr != nil { continue }` | 60K books × 1 read, serial, and a read error silently drops that book's file PIDs from the mapping batch. | S |
| C3 | `internal/quarantine/service.go:225-246 AutoQuarantineFailedScans`, `:252-275 ProcessITunesPurgePending` | serial over `GetAllBooksCore`, `n, _ := GetScanFailCount`, `_ = qs.QuarantineBook` | Both have zero non-test callers (S6) — dead, but would be a hotspot if wired. | S |
| C4 | `internal/plugins/itunes/position_sync.go:86-104`, `plugins/maintenance/archive_sweep.go:38-56`, `plugins/maintenance/series_dedup.go`, `plugins/maintenance/fs_regroup_xml.go`, `plugins/maintenance/author.go:222-252`, `plugins/maintenance/author_conjunction_repair.go`, `plugins/maintenance/title_backfill.go`, `plugins/maintenance/drain_stale.go` | serial `for range books` with a write per item | New since the 2026-07-05 audit; each is a whole-library maintenance op written as a plain loop, contrary to CLAUDE.md's mandate. `author.go` and `extra_ops.go` twins must partition by book ID (`UpdateBook` is full-row replacement — two workers on one row would clobber). | M each |
| C5 | `internal/maintenance/jobs/{dedup_books,relink_missing_to_itunes,fix_version_groups}.go` | serial N+1 (`GetBookFiles` inside book loop) | Legacy-framework twins of C4; fixing only the plugin side leaves these. | S each |

### 1.3 Silent failure

Counts are non-test, excluding `mocks/` and `mock_store.go`, Python-matched by shape. `_=f()` is dominated by `reporter.Log`/`UpdateProgress` (plugins: 206+197 of 406; server: 104+57 of 230), which the source treats as acceptable; the other three columns are the signal.

| package | `_ = f()` | `_, _ = f()` | `x, _ := store.Op(...)` | `if err != nil { continue/return/break }` |
|---|---|---|---|---|
| internal/server/handlers | 46 | 2 | **24** | 10 |
| internal/server (excl. handlers) | 230 | 3 | **20** | 8 |
| internal/plugins | 406 | 2 | 5 | 2 |
| internal/database | 65 | 1 | 10 | **27** |
| internal/itunes | 50 | 5 | **16** | 12 |
| internal/maintenance | 23 | 0 | 5 | **14** |
| internal/dedup | 31 | 0 | 9 | 1 |
| internal/audiobooks | 2 | 0 | 9 | 1 |
| internal/scheduler | 47 | 2 | 3 | 3 |
| others (quarantine, reconcile, metafetch, organizer, scanner, versions) | 74 | 1 | 6 | 5 |

`internal/database` also has 48 bare `p.UpsertXToMemDB(...)` / `MarkQuickQueryDirty` calls — those return nothing and are not silent failures.

Ten worst, by consequence:

| # | Site | What is dropped | Why it matters | Cost |
|---|---|---|---|---|
| S1 | `internal/reconcile/reconcile.go:828` `store.UpdateBook(kept.ID, kept)` and `:837` `store.UpdateBook(orig.ID, orig)` — bare, both return values discarded | `IsPrimaryVersion` flip on kept/orig | Directly feeds the live `is_primary_version` divergence; a failed write leaves two primaries or none, and the op reports success. | S |
| S2 | `internal/server/handlers/versions.go:318,329,448` bare `h.store.UpdateBook(...)`; `:311 newFiles, _ := h.store.GetBookFiles` | version-group linkage | Handler returns 200 after a write it never checked. | S |
| S3 | `internal/scheduler/extra_ops.go:385-411` + `internal/plugins/maintenance/author.go:222-252` `_, _ = store.UpdateBook(...)` then `booksUpdated++` | author-split write | Op summary reports N books updated when 0 may have been. Twin sites (D1). | S |
| S4 | `internal/maintenance/jobs/cleanup_backups.go:39-86` `if rerr := os.Remove(path); rerr == nil { removed++ }`, `_ = removed` | delete failures and the count | Deletes with no age gate, never reports what it removed or failed to remove. | S |
| S5 | `internal/metafetch/service_fetch.go:299` bare `mfs.db.UpdateBook(id, updatedBook)` (CoverURL); `service_apply.go:453 _, _ = mfs.db.UpdateBook` | metadata apply write | The apply pipeline says "applied" with no evidence. | S |
| S6 | `internal/quarantine/service.go:131,206 _ = qs.store.RecordPathChange`; `:225-246 n, _ := GetScanFailCount`, `_ = qs.QuarantineBook` | path-change audit trail; quarantine decision | Path history is the undo mechanism; losing an entry silently breaks undo. | S |
| S7 | `internal/server/bootstrap.go:204-206` `_ = store.DeleteSetting(bootstrapTokenKey)` after a successful exchange (also `:136-137,:193-194`) | one-time-token invalidation | If the delete fails, the bootstrap token stays reusable for the rest of its 10-min TTL. Single-use is not enforced. | S |
| S8 | `internal/itunes/backfill.go:99-101` `if fErr != nil { continue }` | a book's file-level PID mappings | Backfill completes "ok" with holes; nothing counts the skips. | S |
| S9 | `internal/server/handlers/audiobooks/handler_crud.go:46 oldBook, _ = store.GetBookByID(id)` | pre-image for change history | A read error yields a nil pre-image, so the change record shows every field as "added". | S |
| S10 | `internal/telemetry/metrics_handler.go:30-49 MetricsHandler` | the metrics themselves | Writes two placeholder lines and `_ = exporter`; a Prometheus scrape gets a 200 with no data. Zero callers — today it is dead rather than lying. | S |

### 1.4 Interface width (>15 methods, transitive through embeds)

| Interface | Width | Threaded through | Verdict | Cost |
|---|---|---|---|---|
| `database.Store` (`internal/database/store.go:69`, embeds `BookFileStore` etc.) | 369 (source 1); source 2 cites "398 methods" for the store interface | constructors only (store decoupling done, refs 172→8) | Leave. | — |
| `maintenance.JobStore` (`internal/maintenance/job.go`) | 55 | 43 function signatures across `internal/maintenance/jobs/*` | `JobStore→per-job` split is PARKED; D1 (retire the legacy framework) would delete these signatures outright rather than narrow them — the "remove, don't shrink" pattern from CLAUDE.md's worked example. | L (folds into D1) |
| `handlers.*Store` narrowed interfaces | ≤14 | — | Below threshold; no live "needs the full Store" comment found. `internal/importer/service.go:25-36` documents a re-narrowing (#2582), not a standing claim. | — |

The source found no comment currently asserting a wide interface must stay wide; the residual width is in `JobStore`.

### 1.5 Test gaps

`go test -short -cover ./...` exited 1 (three failures below). Bottom 15 by coverage among packages with tests:

| Coverage | Package | LOC (non-test) | Note |
|---|---|---|---|
| 10.0% | internal/operations | 440 | `LoadRawParams` (`state.go:199`) has zero refs |
| 12.3% | internal/telemetry | 179 | `MetricsHandler` placeholder, `GlobalMeter/GlobalTracer` zero refs |
| 16.0% | internal/aiscan | 1,326 | |
| 16.6% | internal/updater | 550 | |
| 17.9% | internal/metadata/mocks | — | generated |
| 22.2% | internal/maintenance | 489 | legacy framework core; its `jobs/` are the D1/C5 twins |
| 29.5% | internal/transcode | 506 | |
| 30.4% | internal/plugins/acoustid | 1,754 | |
| 32.6% | internal/plugins/itunes | — | 4 of 5 op files are unregistered stubs |
| 33.3% | internal/writeback | 146 | `EnqueueWithOutbox` (`outbox.go:117`) zero refs |
| 35.2% | internal/reconcile | 2,658 | S1 lives here, uncovered |
| 36.6% | internal/plugins/deluge | | |
| 37.4% | internal/importer | | |
| 40.2% | internal/oauth | | |
| 42.1% | internal/download | | 9 zero-ref exported funcs (`GetQueueStats`, `GetUploadStats`×2, …) |

Reference points: `internal/server` 70.7%; `internal/database` 71.2% but 654 s under `-short` (it hit the default 10-min timeout in the full run inside `TestPebbleVersionManagement`, `pebble_store_test.go:397`, blocked in pebble WAL `commitPipeline.publish` via `CreateBook→RecordPathChange` `pebble_store_scancache.go:495`; passed with `-timeout 25m`). `-short` is not short for this package.

| # | Test | Finding | Cost |
|---|---|---|---|
| T1 | `internal/ai/embedding_client_test.go:258-263 TestEmbeddingClient_LocalGating` | Fails deterministically when a real Ollama listens on `127.0.0.1:11434` (it does on the auditing Mac; fails on main too). `EmbedBatch` (`embedding_client.go:246-254`) re-probes live when `!localOllamaOK`; the test does not stub the probe. Environment-dependent, not a code bug. | S *Editor's note: the embedding test was made hermetic in #3039; the chapter-persistence constant is filed in `todo.d`.* |
| T2 | `internal/scanner/chapter_persistence_test.go:146-150` | `chs[5].EndSec = 9975.827, want 9975.431111 ±0.001` — reproduced 5/5 runs on main and worktree. Hard-coded sum-of-tracks constant depends on the local ffprobe build's MP3 duration estimate (real Odyssey fixtures). Passes in CI, fails locally. Made non-skipping on 2026-08-19 (`3454323ca`). | S |
| T3 | `internal/ai/retry_test.go:92-104` | Fixture `&openai.Error{StatusCode: 500}` makes `Error()` panic inside the log formatter (noise only; test passes). | S |
| T4 | LFS fixtures (3rd occurrence per memory) | Not re-measured by the source; still listed as open. | — |
| T5 | `internal/maintenance/jobs/cleanup_backups.go` | No test exercises the retention-free delete path; D2 has no guard. | S |

### 1.6 Stale comments and dead code

Zero-reference exported symbols (Python counter, mocks and interface declaration lines excluded): 98 have zero refs even in tests, 395 have refs only in tests. Hand-verified subset:

| Symbol | Anchor | Note | Cost |
|---|---|---|---|
| `AutoQuarantineFailedScans`, `ProcessITunesPurgePending` | `internal/quarantine/service.go:225,252` | Never wired; carry S6 + C3. | S |
| `IncOperationStarted/Completed/Failed/Canceled`, `ObserveOperationDuration` | `internal/metrics/metrics.go:210-214` | Operation metrics exist but nothing increments them — part of the Prometheus gap. | S |
| `MetricsHandler`, `GlobalMeter`, `GlobalTracer` | `internal/telemetry/metrics_handler.go:30`, `telemetry.go:76,81` | Placeholder handler (S10). | S |
| `GetDecryptedSetting` | `internal/database/settings.go:276` | | S |
| `BestTitleMatch` | `internal/dedup/service_scoring.go:994` | | S |
| `PrefixFromArgs` | `internal/database/memdb_indexers.go:349` | | S |
| `GetQueueStats`, `GetUploadStats`×2 | `internal/download/sabnzbd.go:176`, `deluge.go:174`, `qbittorrent.go:165` | | S |
| `EnqueueWithOutbox` | `internal/writeback/outbox.go:117` | Outbox pattern implemented, never used. | S |
| iTunes plugin stubs `runSync/runImport/runPathRepair/runPathReconcile` | `internal/plugins/itunes/{sync,import,path_repair,path_reconcile}.go` | Explicitly excluded from registration (`plugin.go:68-83`); four whole files of `return errNotImplemented`. `positionSyncDef` is registered and honestly errors; comment says `svc.Positions.Sync` "exists and has never been wired". | S |
| `MigrateMaintenanceWindow` | `internal/config/persistence.go:916` | Zero callers — verify it already ran before deleting (the v2 migration was the cause of the 7-night panic). | S |

`FromObject` ×10 in `memdb_indexers.go` are reflection-invoked by go-memdb — false positives, excluded.

TODO/FIXME older than 60 days (git blame; 23 total in Go, 20 over 60 days):

| Age (d) | Anchor | Text / status |
|---|---|---|
| 296 | `internal/backup/backup.go:972`, `:502` | "Implement scheduled backups using a ticker" (memory: an in-memory ticker's ceiling is process uptime — do not do this); "Store checksums in metadata file and verify" |
| 158 | `internal/itunes/itl_be.go:71,302` | "extract checked state from hptm" |
| 136 | `internal/versions/swap.go:222` | "implement full scan when ListBookVersionsByStatus is available" — check if it now is |
| 117 | `internal/plugins/itunes/{sync,position_sync,path_repair,path_reconcile,import}.go` | "Implement …" — four are dead stubs (above) |
| 111 | `internal/backup/backup.go:992` | "Add methods to Store interface to get database path and type" |
| 106 | `internal/telemetry/metrics_handler.go:43`, `internal/ai/telemetry.go:45` | placeholders |
| 90 | `internal/server/metadata_ops.go:448` | `TODO(ADR-003 Phase 2)` |
| 70 | `internal/itunes/backfill.go:97` | `TODO(PERF-5)` — stale: the batch getter exists (D7) |
| 60 | `internal/ai/embedding_client.go:376` | route through `DoWithRetry` (D4) |

Comments referencing removed things:

| Anchor | Stale claim | Cost |
|---|---|---|
| `.envrc:6 export GOEXPERIMENT=jsonv2`; `.vscode/settings.json:3,6,9` (`GOEXPERIMENT`, `-tags=goexperiment.jsonv2`) | jsonv2 is default in 1.27; the build tag no longer selects anything. Harmless but misleading. *Editor's note: removed in #3039, which replaces both with the `GOTOOLCHAIN=go1.27.1` pin.* | S |
| `.claude/agents/test-runner.md:56` "Use `GOEXPERIMENT=jsonv2` (already set in Makefile)" | Source 1: the Makefile no longer sets it (Makefile:36-39, in the sibling's uncommitted edit). Source 2 states the opposite: "Build already sets `GOEXPERIMENT=jsonv2` (Makefile:11, ci.yml:48, binary-smoke.yml:49)". Both recorded; the difference is likely committed vs uncommitted state. *Editor's note: confirmed — source 2 ran before the Makefile edit; #3039 removes the flag from every build path and rewrites this line.* | S |
| `internal/database/scan_state.go:18` "this repo is part-way through (GOEXPERIMENT=jsonv2; 17 files already import encoding/json/v2, internal/database is still on v1)" | The migration framing is obsolete; the `omitzero` argument the comment makes remains correct and load-bearing. Reword, do not delete. | S |
| `internal/database/store.go:862` "Measured … under GOEXPERIMENT=jsonv2 and without it" | Measurement provenance now describes a flag that no longer exists. | S |
| `.github/workflows/{codeql,prerelease,release-prod}.yml` comments | Already rewritten as historical notes in the sibling's uncommitted diff. | — |
| `internal/database/mock_store.go` header "~22 server test files" | 128 measured (D6). | S |
| `internal/maintenance/jobs/cleanup_backups.go:51` "NAMING COINCIDENCE, not a control" | Accurate, and that is the problem — a documented hazard is not a control. | see D2 |

No live reference to `DiscardUnknownMembers` or `cockroachdb/swiss` remains outside CHANGELOG/Makefile history comments.

### 1.7 Method and limits (source 1)

- No edits, no `go fix`, no commits, no subagents. The gopls LSP tool was not exposed, so symbol references were established with grep plus a Python token counter and spot-read by hand; the counter's false-positive class (interface method names shared across types, reflection-invoked methods like memdb `FromObject`) is noted where it applies.
- Source tally: 6 sections — duplication (8), concurrency (5), silent failure (10 + per-package table), interface width (3), test gaps (15-package table + 5 findings), stale/dead (≈20 symbols, 20 aged TODOs, 7 stale comments); top-10 ranked. Remaining: 0.
- Blocked: 1 — `deadcode@latest` (x/tools v0.49.0) segfaults under go1.27.1 in `rta.visitFunc`, so dead-symbol detection used a grep/token counter with hand verification instead of whole-program reachability; the 98/395 totals are upper bounds, the named symbols were each read.
- T4 (LFS fixtures) was not re-measured.

## Part 2 — Go: modernization census (what `go fix` will not do)

Source: Go hand-modernization census. Worktree HEAD `df642d11c` per the source; `git grep -n … -- '*.go' ':!vendor' ':!web'` (abbreviated `G`); `GOTOOLCHAIN=go1.27.1`, `GOEXPERIMENT=jsonv2` for `go fix`. The source re-ran `go fix -diff ./...` (557 files) and checked each hand item against it; where go fix already covers part of an item, only the hand remainder is reported.

### 2.A Errors

- **A1 `errors.As` two-step → `errors.AsType[T]`** — 12 non-test (11 real, 1 comment), 9 test. go fix converts 8 of the 11. Hand remainder = 3: `internal/ai/retry.go:44` (`if !errors.As(err, &apiErr)`), `internal/plugins/maintenance/intro_transcribe.go:605` (`case errors.As(err, &te):` inside a `switch`; go fix skips because the target var is declared outside the case), `internal/transcribe/errors.go:60` (`return errors.As(err, &te)`, bool-returning wrapper). Pure refactor.
- **A2 string-matched errors** — `strings.Contains(err.Error()` 18 non-test / 100 test; `err.Error() ==|!=` 4 / 17; other `strings.Contains(xErr.Error()` 2. Total **24 non-test sites**, e.g. `internal/server/handlers/organize.go:147,180,207,235` (`"not found"`), `internal/audiobooks/service_mutation.go:475` (`err.Error() == "book not found"`), `internal/server/handlers/collections.go:189` (`"already in use" || "duplicate"`), `internal/server/fingerprint_rescan.go:45` (`err.Error() != "EOF"`, should be `errors.Is(err, io.EOF)`). **Risk: NOT a pure refactor.** There is no domain not-found sentinel: `ErrNotFound` in `internal/database` (134 hits) is `pebble.ErrNotFound`; the store constructs not-found as `fmt.Errorf("book not found")` (3 sites) or returns `(nil, nil)` (per comments at `internal/server/duplicates_ops.go:862`). Only 4 `var Err* = errors.New` exist in `internal/database` (`ErrSettingNotFound`, `ErrCollectionVersionConflict`, `ErrMemdbIncomplete`, `ErrNoHNSWSnapshot`). Converting these 24 sites requires introducing sentinels at the store layer first, and mocks return `(nil,nil)` where prod wraps a sentinel, so every mock needs the same change.

### 2.B WaitGroups

- **B1 `Add(1)` + `go` within 3 lines (non-test)** — 39 non-test WaitGroup vars; **5 sites**, none touched by go fix (it converts only 3 test files: `author_create_race_test.go`, `embedding_store_chaos_test.go`, `inflight_test.go`). Convertible: `internal/activity/writer.go:141` (`w.wg.Add(1); go w.drain()` → `w.wg.Go(w.drain)`), `internal/itunes/service/importer.go:345` (`hashWG.Add(1); go func(r *trackRow)` → `hashWG.Go(func(){ … r … })`, safe under 1.22 loop vars). NOT convertible: `internal/dedup/lifecycle.go:130` (Add deliberately under `bgMu` to order against a concurrent `Wait`), `internal/operations/registry/registry.go:279` and `:299` (`goroutineWG.Add(1)` under `r.mu` with a `notifyStopped` guard; comment at 270-272 says why). Test files: 21 more (`inflight_test.go` 5, `embedding_store_chaos_test.go` 5, …) — go fix handles 3 files; the rest are hand.
- **B2 `.Add(n)` not followed by `go func` / batch Add** — 3 sites that must stay: `internal/itunes/service/path_repair_resolver.go:164` `wg.Add(workers)`, `internal/mtls/bridge.go:46` `wg.Add(2)`, `internal/operations/registry/batch.go:210` `fireWG.Add(1); defer …Done()` under `batchMu` (enrolment of the current goroutine, no `go`). `internal/server/bg_wg.go:42` `n.wg.Add(1)` is inside the repo's own `namedWaitGroup.Add(name)`; its `.Go(name, fn)` at line 66 already mirrors `WaitGroup.Go`. Everything else matching `\.Add\(1\)` is `atomic.Int64` counters (e.g. `pebble_activity_store.go:710`, `backfill_sync_ids.go:82-130`) — the raw grep overstates ~4x, confirmed.
- **B3 fire-and-forget `go` in non-test** — 77 `go` statements; **66** have no `.Add/.Go/errgroup` within the 6 preceding lines. Heuristic — many are joined by channel (`subprocess.go:215`), by a loop owner (`itunes/library_watcher.go:44`, `watcher/watcher.go:104`, `updater/scheduler.go:55`), or intentional (`update_handlers.go:63 go s.updater.RestartSelf()`). Worth a look for `namedWaitGroup`/`bgWG.Go`: `internal/aiscan/pipeline.go:359-363,442-446` (7 pipeline stages), `internal/scheduler/tasks.go:434,992`, `internal/server/server_lifecycle.go:578,706,1011,1035,1044,1076,1157` (7), `internal/metafetch/service.go:660`, `service_fetch.go:32`, `internal/openlibrary/store.go:144,188`, `internal/database/pebble_store.go:375`, `pebble_store_stats.go:197`, `pprof_debug.go:34`. Risk: behaviour change (shutdown ordering) — each needs a lifecycle decision, not a mechanical edit.

### 2.C Tests

| Item | Count | go fix | Hand remainder / risk |
|---|---|---|---|
| `context.Background()/TODO()` in `*_test.go` | 1638 lines / 343 files; `t.Context()` used 0× today | 70 (only the `ctx, cancel := context.WithCancel(context.Background()); defer cancel()` form) | ≈1568; 96 are `ctx := context.Background()`, 1534 inline args (`internal/acoustid/client_test.go:19,52,72`). Behaviour change in helpers that outlive the test (a `t.Context()` is cancelled at test end; helpers reused across subtests will see cancellation). Pure for straight-line tests. |
| `for i := 0; i < b.N; i++` → `for b.Loop()` | 9 (`internal/database/hnsw_embedding_store_bench_test.go:41`, `pebble_activity_index_pushdown_bench_test.go:109`, `pebble_store_test.go:1370`); `b.Loop()` used 2× | 0 | Pure, but `b.Loop()` disables inlining of the loop body's calls — the pushdown bench numbers recorded in `9c7ee61c2` would change. |
| `time.Sleep` in tests | 147 in 68 files; `synctest` used 21× | — | **81 sleeps / 38 files "pure"** (synctest candidates; top: `internal/operations/registry/coverage_test.go` 7, `abandoned_test.go` 6, `batch_test.go` 4, `dispatcher_test.go` 4, `internal/transcribe/inflight_test.go` 4) vs **66 / 30 files I/O-bound — NOT convertible** (`internal/realtime/events_test.go` 9, `internal/backup/backup_test.go` 6, `internal/watcher/watcher_test.go` 5, `internal/metrics/metrics_test.go` 4, `resume_shutdown_roundtrip_test.go` 4). synctest requires every goroutine in the bubble to be durably blocked; the registry tests spawn a Pebble store — verify they are truly in-memory first. |
| `os.Setenv` in tests → `t.Setenv` (used 64×) | 8: `internal/ai/openai_parser_test.go:1584,1662`; `internal/metadata/write_test.go:52,55,77,80,102,105` (PATH manipulation) | — | Pure, but `t.Setenv` panics under `t.Parallel()`. |
| `os.MkdirTemp` in tests → `t.TempDir()` (used 1443×) | 15: `internal/database/dedup_label_test.go:15`, `internal/fileops/service_test.go:44,70,91`, `internal/scanner/integration_format_test.go:23,123,189`, `multi_format_test.go:75,138,195`, `internal/server/server_test.go:68`, `user_tags_authz_test.go:43`, `versions/lifecycle_prop_test.go:34`, `database/do_not_import_test.go:18`, `pebble_store_prop_test.go:38`; plus `internal/database/pebble_store_test.go:24` hand-builds `/tmp/test_pebble_<ulid>` | — | Pure. Prop tests: `t.TempDir` under `rapid`/`testing/quick` creates one dir per test not per iteration — check whether the loop relies on a fresh dir. |
| `os.Chdir` in tests → `t.Chdir` (0 today) | 12: `cmd/commands_test.go:105,109`, `cmd/root_test.go:33,37,64,68,238,242`, `internal/server/server_backup_restore_test.go:29,30,72,73` | — | Pure; `t.Chdir` also forbids `t.Parallel`. |
| Hand mocks vs mockery | 87 generated files under `*/mocks/` (45 `name:` entries in `.mockery.yaml`); 112 hand-written `type (mock\|fake\|stub)X struct` in test files + 1 non-test | — | Not a Go-version item; listed for completeness. |

### 2.D Deprecated stdlib

- `io/ioutil`: 0. `rand.Seed`: 0. `runtime.SetFinalizer`: 0.
- **`math/rand` v1**: 2 non-test imports (`internal/database/hnsw_embedding_store.go:48`, `internal/metadata/providerhttp/providerhttp.go:27`), 12 test; `rand.New(rand.NewSource(N))` 29× (test-only, deterministic seeds e.g. `embedding_f16_zstd_test.go:139`). go fix: 0. **Risk: behaviour change** — v2 PCG with the same seed yields a different sequence; the HNSW level-assignment RNG and golden fixtures derived from seeded v1 streams would change. Keep v1 in the seeded tests unless fixtures are regenerated.
- **`sort.*` → `slices.*`** (non-test / test): `sort.Slice` 80/11, `sort.SliceStable` 46/8, `sort.Strings` 76/35, `sort.Ints` 9/2, `sort.Sort` 0/1. **211 non-test sites**; go fix converts only 2. e.g. `internal/activity/changelog.go:162`, `cmd/itl-diff/main.go:93`, `internal/itunes/itl_diff_helpers.go` (6). Comparator changes `bool`→`int`; ordering semantics identical (both unstable; `SliceStable`→`SortStableFunc`). Hand work because multi-key comparators need `cmp.Or(cmp.Compare(...), cmp.Compare(...))`.
- **`for … range strings.Split/Fields(…)`**: 23 non-test + more in tests; go fix already converts 36 (16 non-test files) to `SplitSeq/FieldsSeq`. Hand remainder: 6 two-step `parts := strings.Split(…)` / `for range parts` cases and 8 `strings.Split(s, "\n")` → `strings.Lines` (`Lines` keeps the `\n` on each element — behaviour change unless callers `TrimSpace`).
- **`reflect.TypeOf((*T)(nil)).Elem()`**: 2 (`internal/database/mocks/mock_store_coverage_test.go:14`, `metadata/mocks/mock_metadata_extractor_coverage_test.go:14`) — go fix covers these (18 `TypeFor` hunks incl. others).
- **`sync.Once` + value → `sync.OnceValue`**: 26 non-test `sync.Once`; `OnceValues` already used once (`internal/audioutil/mediainfo.go:36`). Most are warn-once/close-once/abort-once guards (`service_filtering.go:946 warnOnce`, `embedding_store.go:252 closeOnce`, `ai_batch_phase.go:79 abortOnce`) — not value-producing. Genuine candidates (~7): `internal/dedup/boilerplate.go:38 boilerplateInit`, `internal/diagnosis/probe.go:81 toolsOnce`, `internal/itunes/service/path_repair.go:239 tierBOnce`, `path_repair_resolver.go:67 once`, `internal/maintenance/jobs/repair_missing_files.go:148 idxOnce`, `internal/transcribe/cuda.go:29 cudaOnce`, `internal/metrics/metrics.go:16 registerOnce` (→ `OnceFunc`). Pure.
- **`strings.LastIndex` + slice → `strings.CutLast` (1.27)**: 19 non-test `LastIndex` + 3 `LastIndexByte`; 0 `CutLast` today. ~17 slicing-form candidates: `internal/database/pebble_store_tags.go:242,286,470,510`, `internal/dedup/series_dedup.go:75`, `internal/maintenance/jobs/relink_missing_to_itunes.go:295`, `repair_missing_files.go:343,399,475`, `internal/server/file_io_pool.go:266`, `internal/server/handlers/abs/browse.go:228`, `internal/organizer/organizer.go:276`, `pathbuild.go:376`, `pathutil/abbreviate.go:53`, `metadata/folder_parser.go:442`, `junk_title_derive.go:80`, `search/query_parser.go:356`. **Risk:** many use `idx > 0` (reject a leading separator) whereas `CutLast` reports `found` for idx 0 too — each site needs the guard preserved. `bytes.LastIndex`: 0.
- **`interface{}` in non-go-fix positions**: 427 total, go fix rewrites 373; the remaining ~54 are inside non-empty interface literals (9 non-test, e.g. `internal/operations/registry/reporter.go:50`, `internal/plugins/dedup/plugin.go:24`) and comments — nothing to do. 0 `[T interface{` constraints.

### 2.E Iterators (design candidates only)

Store-family methods returning `([]T, error)` with no paging param: **84** in `internal/database/*.go`. Biggest by non-test callers (callers / callers that `range` on the next line):

| Method | Callers | Range-immediately | Note |
|---|---|---|---|
| `GetAllBooksCore(limit, offset)` | 83, **63 of them `GetAllBooksCore(0, 0)`** (whole library) | 11 (28 use `len()`) | by package: `internal/maintenance/jobs` 18, `internal/itunes/service` 7, `internal/server/handlers` 6, `internal/plugins/maintenance` 5, `internal/plugins/dedup` 4, `internal/sweep/*` 2, `internal/writeback/outbox.go:87` |
| `GetAllBookFilesCore()` | 32 | 2 | |
| `GetAllAuthors()` | 31 | 5 | |
| `ListBookIDs()` | 23 | 1 | |
| `GetAllSeries()` | 16 | 3 | |
| `ScanPrefix(prefix)` | 7 | 4 | |
| `GetAllWorks()` | 6 | 2 | |

`iter.Seq` is used 0× in non-test code. `GetAllBooksCore` is served from the in-memory memdb (`memdb_reads.go`), so an `iter.Seq` would remove the slice copy, not the I/O. Risk: design change — the store interface has 398 methods and the decoupling is closed; adding `AllBooksSeq()` siblings is additive but every mock (87 files) regenerates. The range-only population is small (≈25 sites across every method).

### 2.F Security / os.Root

- Prefix checks: 13 non-test hits (6 tag-prefix false positives). Real path-containment checks: `internal/fileops/service.go:65`, `internal/organizer/organizer.go:517`, `internal/security/pathvalidation/pathvalidation.go:262 isWithinRoot`, `internal/util/path.go:17 SafeJoin` + `:30 WithinRoot`, `internal/security/safepath/safepath.go` (`Join/Validate/MustJoin`, 28 callers). `filepath.Rel`+`..` checks: 6 (`internal/deluge/import.go:134`, `maintenance/jobs/bulk_deluge_import.go:256`, `pathutil/hidden.go:127`, `plugins/deluge/centralization.go:198`, `server/handlers/abs/mapper.go:914`). `EvalSymlinks` 6. `os.OpenRoot`: **0**.
- **Three parallel path-safety packages**: `internal/security/safepath` (28 callers), `internal/util` (`SafeJoin` 3, `WithinRoot` 3), `internal/security/pathvalidation` (**0 callers** for `SecureJoin`/`SecureJoinResolved` — dead exported API).
- **CodeQL `go/path-injection`, open: 18, not 12**: `internal/fileops/safe_operations.go:108,109,116,148,189,297`; `internal/fileops/copy.go:101,217,235,244,263,290,298,312`; `internal/fileops/hash.go:34`, `internal/filehash/filehash.go:82`; `internal/metafetch/service.go:324` (`os.ReadFile(coverPath)`); `internal/audiobooks/service_mutation.go:63` (already carries `// lgtm[go/path-injection]`, still open). Would `os.Root` resolve them? The 14 in `internal/fileops` and 2 hash sites only partially: `fileops` is a leaf library taking absolute `src, dst string` with no root concept; moving them to `root.Open/root.Stat` requires a `*os.Root` parameter threaded from every caller, and the library has multiple roots (`RootDir` plus 40 `GetAllImportPaths()` callers moving files from import dirs into the library). `os.Root` has no cross-root `Rename`/copy, so moves would become streaming copies and the atomic `os.Rename` fast path is lost (`OrganizeOneBook` is same-root and fine). `metafetch/service.go:324` and `service_mutation.go:63` are single-root and would be cleanly resolved by `os.OpenRoot(rootDir)`. Net: 2 resolved cleanly, 16 need an API change to `fileops` first. Behaviour change, not a refactor.

### 2.G HTTP

- **CSRF**: no hand-rolled CSRF middleware; the only CSRF code is the OAuth `state` token (`internal/oauth/state.go:19`). API auth is cookie-based with `SameSite=Strict` (`internal/server/handlers/auth.go:266,279`) plus API keys. `http.CrossOriginProtection` (1.25) would be a new defence layer, not a replacement: it rejects cross-origin non-safe requests lacking `Sec-Fetch-Site`, so API-key clients, `/api/events` SSE and the ABS-compatible clients must bypass it. Risk: behaviour change for third-party clients.
- **`http.Server`**: main server `internal/server/server_lifecycle.go:960` sets `ReadHeaderTimeout: cfg.ReadTimeout` and `MaxHeaderBytes: 1<<20`. Redirect server at `:1065` sets neither (only `Addr`/`Handler`) — 1 site to fix. `MaxHeaderValueCount` (1.27): 0 uses; 2 sites to add.
- Manual body drain: `io.Copy(io.Discard, resp.Body)` 0, `ioutil.Discard` 0 — nothing to delete.
- **UUID**: `google/uuid` is indirect only (go.mod:107), 0 direct uses. `oklog/ulid/v2` is direct: 33 non-test files, 80 call sites, every one `ulid.Make()` except `internal/database/pebble_store.go:459-460` (`ulid.Monotonic(rand.Reader,0)` + `ulid.New`). Stdlib `uuid` (1.27) is not a fit: IDs are ULIDs (26-char Crockford base32, sortable) persisted as DB keys; switching would break key ordering and every existing row. No hand-rolled UUID generation found.

### 2.H Generics / language

- Package-level generic functions (non-test): **12**. Generic-method candidates: `internal/serviceregistry/container.go:265 Get[T]` and `:290 TryGet[T]`, `internal/operations/state.go:182 LoadParams[T]`, `internal/database/store_capability.go:86 AsCapability[T]`. 4 sites; `RunItems[T]`/`paginate[T]`/`sortByLowerName[T]` are genuinely free functions. Pure, low value.
- Nested positional struct literals: 10 (table fixtures). Negligible.
- **Pointer helpers → `new(expr)` (1.26)**: **12 distinct names across 20 definitions** — `stringPtr` defined **8 times** (`internal/audiobooks/helpers.go:42`, `importer/service.go:398`, `metafetch/helpers.go:24`, `organizer/rename.go:505`, `plugins/deluge/protected_paths.go:52`, `scanner/scanner.go:2970`, `server/handlers/metadata/handler.go:177`, `server/server_helpers.go:45`), `boolPtr` 3×, `intPtrHelper` 2×, plus `f64Ptr`, `int64PtrLocal`, `intPtr`, and `internal/util/pointers.go` `StringPtr/IntPtr/BoolPtr/Int64Ptr`. Call sites: **115 non-test, 346 test**. Test files define 37 more helper names (48 definitions). Target `new("value")`, `new(true)`, `new(42)`; delete ~68 helper definitions. Pure; go fix does not do it (0 `new(expr)` today).

### 2.I JSON

- `"encoding/json"` v1: 294 non-test / 241 test files. `encoding/json/v2`+`jsontext`: 22 files (`internal/metadata` 8, `download` 3, `server` 3, `updater` 3, `database` 1) using `MarshalWrite` 21, `UnmarshalRead` 18, `jsontext.Value` 8. Source 2: build sets `GOEXPERIMENT=jsonv2` (Makefile:11, ci.yml:48, binary-smoke.yml:49) — see the Part 1 disagreement note.
- **omitzero analyzer** (`go fix -diff -omitzero ./...`; off by default): **7 files / 8 fields**, time.Time except one struct — `internal/database/activity_types.go` (Timestamp), `ai_jobs_types.go` (SubmittedAt, CompletedAt), `pebble_store_abssession.go` (GraceUntil, StartedAt), `internal/openlibrary/downloader.go` (CompletedAt), `internal/plugins/dedup/calibrate_composite.go` (`Rec bandStat`), `internal/server/metadata_batch_candidates.go`, `internal/updater/updater.go` (PublishedAt). **Risk: wire change.** Under v1 semantics `omitempty` on a struct never omits, so these emit `"0001-01-01T00:00:00Z"` today; `omitzero` drops the key, and frontend/ABS consumers must tolerate absence. Broader census: 155 `omitempty` on bool/int/float fields and 7 on `time.Time` — fine under v1, but any file migrated to `json/v2` proper must switch them to `omitzero` (v2 `omitempty` emits `false`/`0`).
- Removed/renamed v2 features (`format:`, `,unknown`, `,inline`, `DiscardUnknownMembers`, `SkipFunc`): 0. `,string` tag: 0.

### 2.J slog

- `log.Printf` in `internal/`: **3** (`internal/itunes/itl_le.go:763,795`, `itl_le_metadata_update.go:164`) + `fmt.Fprintf(os.Stderr` 1 (`internal/config/config.go:1319`). `fmt.Print*` in `internal/`: 0. `cmd/` + `tools/`: `fmt.Printf` 196, `Println` 65, `Fprintf(os.Stderr` 104, `log.Fatal` 5 — CLI output, leave.
- slog with `fmt.Sprintf` message: 5, 4 of them the `plugins/itunes/adapter.go:34-46` printf-style shim adapting a legacy `Logger` interface — intentional. `go vet -slog ./...`: 0 findings. 38 `slog.X("…" +` hits are multi-line literal continuations. Total slog calls: 1730. Nothing material.

### 2.K Ranked top-10 (sites × benefit ÷ risk, source 2's order)

| # | Migration | Sites | Notes |
|---|---|---|---|
| 1 | `t.TempDir`/`t.Setenv`/`t.Chdir` | 15+8+12 = 35 | pure; removes `/tmp/test_pebble_*` litter (`pebble_store_test.go:24`); check `t.Parallel` conflicts |
| 2 | pointer helpers → `new(expr)` | 461 call sites | deletes 8 duplicate `stringPtr` definitions and ~68 helpers total; zero behaviour risk |
| 3 | `sort.*` → `slices.*` | 211 non-test | pure, per-site comparator translation; drops the `sort` import repo-wide |
| 4 | `context.Background()` → `t.Context()` | ~1568 hand sites | pure for straight-line tests; audit helpers reused across subtests |
| 5 | `wg.Go` on the 2 safe sites + 18 test sites go fix skips | `activity/writer.go:141`, `itunes/service/importer.go:345` | explicitly skip the 3 mutex-ordered ones |
| 6 | `strings.CutLast` | ~17 | pure if the `idx > 0` guard is preserved per site |
| 7 | redirect `http.Server` timeouts + `MaxHeaderValueCount` | 2 | security hardening, no client impact |
| 8 | `sync.OnceValue/OnceFunc` | ~7 | pure |
| 9 | residual `errors.AsType` | 3 | pure, trivial |
| 10 | `omitzero` on 8 fields | 8 | wire change; do it only with a frontend/ABS consumer check, in its own PR |

Design-level (not modernizations, decide separately): A2 not-found sentinels (24 sites + store + mocks), E `iter.Seq` store methods, F `os.Root` (needs a `fileops` API change; resolves 2 of 18 alerts cleanly), G `CrossOriginProtection` (client-impacting), B3 fire-and-forget goroutine lifecycle (66 to triage).

### 2.L DO NOT convert

The safety list for the follow-on refactor PRs. Each item is verbatim from source 2.

| Site | Reason |
|---|---|
| `internal/dedup/lifecycle.go:130`, `internal/operations/registry/registry.go:279,299` | `Add(1)` deliberately under a mutex to order against `Wait`/`notifyStopped`; `wg.Go` would reintroduce Add-after-Wait. |
| `internal/operations/registry/batch.go:210` | `fireWG.Add(1)` enrols the current goroutine under `batchMu`; no `go` involved. |
| `internal/itunes/service/path_repair_resolver.go:164` `Add(workers)`, `internal/mtls/bridge.go:46` `Add(2)` | Batch Adds. |
| `internal/server/bg_wg.go` | Repo's own `namedWaitGroup.Go`; keep the name registry. |
| Every `oklog/ulid` site (80) | Persisted sortable DB keys; stdlib `uuid` is a different format. |
| Seeded `math/rand` v1 in tests (29 `rand.NewSource` sites) and `hnsw_embedding_store.go:48` | v2 changes the sequence for a given seed. |
| 66 I/O-bound `time.Sleep` test sites (`realtime/events_test.go`, `backup_test.go`, `watcher_test.go`, `metrics_test.go`, `resume_shutdown_roundtrip_test.go`, …) | Real fsnotify/fsync/subprocess/network waits; `synctest` cannot bubble them. |
| `internal/plugins/itunes/adapter.go:34-46` `slog.X(fmt.Sprintf(...))` | A printf shim for a legacy logger interface. |
| `strings.Split(s, "\n")` → `strings.Lines` at the 8 sites | Only if each caller already trims — `Lines` keeps the terminator. |
| `pebble_activity_index_pushdown_bench_test.go:109` `b.N` loop | Convert only if the recently recorded benchmark numbers (`9c7ee61c2`) are re-measured; `b.Loop` changes inlining. |
| `internal/audiobooks/service_mutation.go:63` | Already carries `// lgtm[go/path-injection]` and is still open — the suppression comment is inert; resolve via the alert UI or `os.Root`, don't add more comments. |

### 2.M Method and limits (source 2)

- Read-only: only `go.mod`/`go.sum` were modified in the worktree (the sibling's toolchain edit); nothing edited or committed. `ggrep` was not on PATH, so `git grep -P` replaced it.
- Source tally: 10 — sections A through J censused, go-fix overlap checked, CodeQL alerts enumerated (18 open, not 12). Remaining: 0. Blocked: 0.
- B3 (66 fire-and-forget goroutines) and the `HasPrefix` path check are heuristic greps; the C-section `context.Background()` remainder (≈1568) was counted, not read per site.

## Part 3 — TypeScript/React: work discovered

Source: TypeScript/React work-discovery audit of `web/`. Anchors below are under `web/src/` unless another prefix is given. Every `file:line` was opened and confirmed by the source; heuristic counts are labelled as such.

### 3.0 Confirmed bugs

**Bug 1 — envelope mismatch, `services/api.ts:1560-1565` (cost S, confidence high).** `getBooksByAuthor` does `if (!response.ok) return []; const data = await response.json(); return data.items || [];`. The server answers `{data:{items,count,limit,offset}}` (`internal/server/audiobooks_helpers.go:131` via `RespondWithOK`); `getBooks` at `api.ts:1074` correctly does `body.data ?? body`. This function therefore always returns `[]`. Sole caller: `components/dedup/DedupAuthorTab.tsx:155` → the author popover renders "No books found" for every author (~`:192-200`). Server failure and empty result are also conflated.

**Bug 2 — `Book` field names that Go never sends** (TS `api.ts` vs Go `database.Book` `internal/database/store.go:185` + `enrichedBookResponse` `internal/server/server.go:117`; the source's first comparison against `models.Audiobook` was wrong and discarded). Rename fix cost S; generating `Book` from Go (e.g. `tygo`) so the class cannot recur, M.

| TS field | Go actually sends | Consumers rendering blank |
|---|---|---|
| `bitrate` `api.ts:138` | `bitrate_kbps` (`store.go:223`) | `Library.tsx:80` maps `bitrate_kbps: book.bitrate` → column `columnDefinitions.ts:302-307` always empty; `BookDetailInfoTab.tsx:251` Bitrate row |
| `sample_rate` `:140` | `sample_rate_hz` (`:225`) | `Library.tsx:82` → `columnDefinitions.ts:327-332` always empty |
| `series_position` `:113` | `series_sequence` (`:190`); 0 TS reads of `series_sequence` | `BookDetailInfoTab.tsx:230` "#N" never renders; `BookDetailDialogs.tsx:478` Series Index blank; `MetadataSearchDialog.tsx:368`; `DedupAdvancedScanTab.tsx:349` (23 reads total; some legit on metafetch candidates, `internal/metafetch/service.go:188`) |
| `isbn` `:125` | — | `keepDecision.ts:41`, `DedupAcousticTab.tsx:532`, `useMetadataLane.ts:254`, `BookDetail.tsx:939` |
| `cover_image` `:123`, `organize_error` `:165`, `duration_delta_sec` `:181` | — | 2 / 3 / 7 reads |

TS already declares `bitrate_kbps`/`sample_rate_hz` at `api.ts:298-299` on another type but the `Book` mapper does not use them. 33 Go tags are absent from TS `Book` (`audible_*`, `coverage_percent`, `fingerprint_status`, `transcribe_*`, `needs_rescan`, `merged_into_book_id`, …) — not bugs, but the type is a hand-maintained guess.

**Also confirmed — stale response type `BatchWriteBackResponse` `api.ts:3210-3220`** declares `written, written_files, renamed, organized, failed, errors`; the Go handler (`internal/server/handlers/metadata/handler.go:1369-1373`) sends only `operation_id, message, book_count`. Comment at `L3252` ("Server returns operation_id at top level; data key carries legacy fields") is false — `operation_id` is inside `data`. `Library.tsx:1488` does `void result;`. Cost S.

### 3.1 Duplication

| # | Finding | Anchors | Why it matters | Cost |
|---|---|---|---|---|
| 1.1 | `formatDuration`-family defined 12× | `columnDefinitions.ts:9`, `utils/mediaFormat.ts:20`, `CacheStatsPanel.tsx:33`, `BulkMetadataSearchDialog.tsx:96`, `bookDetailUtils.ts:12`, `review/spine/rowState.ts:100`, `DedupAdvancedScanTab.tsx:139`, `CandidateCompareDrawer.tsx:65`, `DedupAcousticTab.tsx:44`, `DedupLabels.tsx:150`, `OperationsIndicator.tsx:84`, `AIJobsPanel.tsx:39` | Same value renders differently per screen; a fix to one misses eleven | M |
| 1.2 | `formatBytes/FileSize` defined 15×, semantics diverge | `mediaFormat.ts:11` caps at MB → 1.2 GB prints `1228.8MB`; `rowState.ts:107` prints `<1024 B` as `0 KB`; `bookDetailUtils.ts:24` returns `—` for 0; `StorageTab.tsx:80` uses `Math.log` (negative unguarded); plus `columnDefinitions.ts:17`, `OpenLibraryDumps.tsx:35`, `ITunesTransfer.tsx:119`, `BulkMetadataSearchDialog.tsx:103`, `AudiobookList.tsx:330`, `SystemInfoTab.tsx:118`, `QuotaTab.tsx:71`, `ServerFileBrowser.tsx:215`, `FolderFilesChip.tsx:36`, `DedupAdvancedScanTab.tsx:132`, `CandidateCompareDrawer.tsx:58` | `utils/mediaFormat.ts` exists but only 2 files import it (`RegroupSpine`, `FileInfoCompare`); 6 copies are defined inside component bodies (re-created every render) | M |
| 1.3 | `formatDate/Timestamp` 11× | `columnDefinitions.ts:25,44`, `BatchActivityEntry.tsx:51`, `OperationActivityPanel.tsx:59`, `ChangeLog.tsx:37`, `BlockedHashesTab.tsx:136`, `ITunesTransfer.tsx:125`, `bookDetailUtils.ts:6`, `ServerFileBrowser.tsx:222`, `ActivityLog.tsx:104,110` | Locale/relative-time behaviour differs by page | S |
| 1.4 | Hand-rolled `Set` toggle reducer 39× (55 `new Set(prev)` sites in 24 files; 37 `useState<Set<`) | `AudiobookList.tsx`, every `Dedup*Tab.tsx`, `useDupesLane.ts`, `useRegroupLane.ts` | Same `has ? delete : add` ten-liner; no `useSelectionSet()` hook exists | M |
| 1.5 | 7 files ship a private `<Snackbar>` while 12 use `useToast()` | `MetadataHistory`, `BlockedHashesTab`, `ServerFileBrowser`, `DedupSplitBookTab`, `DedupEmbeddingTab`, `pages/Series.tsx`, `pages/Authors.tsx` | Two toast stacks can overlap; Undo snackbars in Authors/Series are 83 identical lines | S |
| 1.6 | `Authors.tsx` vs `Series.tsx` near-twins: 301 identical lines in blocks ≥8 after entity-word normalisation (difflib ratio 0.45) | `Authors 1164-1246 == Series 1096-1178` (Undo snackbar + history list), `1248-1267==1180-1199`, `762-782==739-759`, `492-508==519-535`, `218-235==238-255` | 2,500 lines to maintain as 1,300; both in the zero-test list (§3.5) | L |
| 1.7 | `MetadataSearchDialog` vs `BulkMetadataSearchDialog`: 347 identical lines (ratio 0.55) | `571-614==801-844`, `656-692==878-914`, `694-718==916-940`, `42-76==51-85` (toast prop type + `FIELD_OPTIONS`) | Bulk and single-book search drift independently; bulk is 1,093 lines | L |
| 1.8 | `DedupAuthorTab` vs `DedupSeriesTab`: 151 identical lines; every one of the 9 `Dedup*Tab` carries its own `loading/error/selected*/page/rowsPerPage` state | `components/dedup/Dedup*Tab.tsx` | A shared `useDedupTable()` would delete ~40 state declarations | M |
| 1.9 | Three review lanes, three fetch-guard designs | Dupes `useDupesLane.ts:303-372` (abortRef + aborted guard); Regroup `useRegroupLane.ts:469-556` (abortRef + `reqSeq/appliedSeq`); Metadata `useMetadataLane.ts:529-652` (`fetchIdRef` counter only, no `AbortController`) | The metadata lane cannot cancel its 120 s `getCachedReviewResults(0,0)` on unmount/lane switch — the request keeps running server-side | M |
| 1.10 | 15 non-service files call `apiFetch` directly (34 call sites) | `Users.tsx` 6, `DedupLabels.tsx` 5 (`:365`, `:507`), `ITunesTransfer` 4, `DelugeSettingsTab` 4, `PluginsTab` 3, `ChangeLog` 2, and 1 each in `App`, `AnnouncementBanner`, `ITunesImport`, `VersionsPanel`, `WelcomeWizard`, `Login`, `BookDetail`, `TrashedVersions`, `Setup` | These bypass `buildApiError`, timeouts and the envelope unwrap; where the "flat vs `data`" mistakes will recur | M |
| 1.11 | `useDebouncedSearch.ts` exists but `Library.tsx:570-596` keeps its own timer setting two states (`debouncedSearch`, `debouncedParsedSearch`) | `Library.tsx:217,570-596,801-803` | `debouncedParsedSearch` is derivable as `parseSearch(debouncedSearch)`; the hook's doc says this was deliberately left — worth re-deciding | S |

Lane hooks by the numbers (evidence for 1.9): Dupes 21 `useState`/4 effects/12 `useCallback`/2 AbortController/6 catch; Metadata 17/4/19 + 14 `useMemo`/0 AbortController/15 catch; Regroup 11/3/16/3/4. `lanes/types.ts` `LaneDescriptor` already abstracts lane/label/verbs — only the data layer is unshared.

### 3.2 `services/api.ts` — size and shape (v2.77.0, 6,521 lines)

| Metric | Value | Anchor |
|---|---|---|
| Exported functions / types | 280 / 220 (source 3; source 4 counts 278 exported functions) | 501 `export` lines |
| `apiFetch(` calls; raw `fetch` | 270; 0 | — |
| `!response.ok` checks / `buildApiError` | 267 / 258 | `ApiError` L65-75, `buildApiError` L84-91 |
| Unwrap `.data` / return flat `json()` / defensive `body.data ?? body` | 175 / 28 / 36 | e.g. `getOperationV2` L576, `getBookFacets` L1082, `searchBooksPage` L1129, `getConfig` L2332, `batchApplyFromCache` L3915, `getReviewCount` L6425 |
| Hand-built `?x=${}` query strings vs `URLSearchParams` | 13 vs 13; no shared builder | hand-built: L565, 1106, 1560, 2167, 2206, 2788, 3466, 3817, 3831, 4084, 4593, 4634, 5682 |
| `apiFetch` with no `.ok` check | 1 | `testMetadataSource` L3396-3409 — a 500 becomes `undefined` |
| Functions swallowing non-2xx as `[]`/`null` | 8 | `getOperationTimeline` L565, `getBookChangelog` L1419, `getAnnouncements` L1504, `getBooksByAuthor` L1560, `getAuthorAliases` L1575, `getBookExternalIDs` L4982, `getUserColumnConfig` L5105, `getSavedFilterPresets` L5149 |
| Trigger wrappers (`wrapTrigger`/`trigger*`/`start*`) | 21 | L58-63 (`wrapTrigger`), L1980-6149 |
| Per-call timeouts defined | 4 constants | L31-45: REVIEW_COUNT 15 s, REVIEW_ITEMS 30 s, DEDUP_CANDIDATES 60 s, CACHED_REVIEW 120 s; the other ~266 calls rely on `apiFetch`'s default |
| Coverage | 5.91% statements | §3.5 |

Proposed split (mechanical, by route prefix; keep `api.ts` as a re-export barrel for one release so 100+ importers don't churn): `services/client.ts` (`ApiError`, `buildApiError`, `unwrapData<T>()`, `buildQuery(params)`, timeout constants, `wrapTrigger`; 6 fns), `booksApi.ts` (~55), `authorsSeriesApi.ts` (~25), `operationsApi.ts` (~30), `reviewApi.ts` (~35), `dedupApi.ts` (~40), `metadataApi.ts` (~30, incl. `testMetadataSource`), `settingsApi.ts` (~25), `systemApi.ts` (~25), `itunesApi.ts` (~10); existing `activityApi`, `readingApi`, `playlistApi`, `fileOpsApi`, `versionApi` unchanged. Cost L overall, S per module once `client.ts` exists; `unwrapData` alone would retire the 36 defensive fallbacks and would have caught Bug 1.

### 3.3 State / render risks

| # | Finding | Anchors | Why it matters | Cost |
|---|---|---|---|---|
| 3.1 | 87 `react-hooks/set-state-in-effect` warnings (rules are `warn`, nothing fails) | `eslint.config.mjs` v1.4.0; per file: `BookDetail` 7, `MaintenanceTab` 5, `ActivityLog` 5, `Library` 4 (source 4 counts `Library.tsx` 3), `VersionManagement` 3, `DedupLabels` 3, ~50 files with 1-2 | Each is a double render on mount; several are derived state that should be `useMemo` | M (triage) |
| 3.2 | Missing effect deps | `OperationActivityPanel.tsx:244` (`limit`), `Library.tsx:767` (`searchParams`) | Panel does not refetch when its own limit changes | S |
| 3.3 | Refs read during render | `SearchBar.tsx:380` (`anchorEl={helpAnchorRef.current}`), `BookDetailInfoTab.tsx:361-362`, `ActivityLog.tsx:996` | Popover anchor is stale on first paint under React Compiler | S |
| 3.4 | Impure render | `APIKeysTab.tsx:282,292` (`Date.now()` in JSX) | Expiry colouring flips between renders; untestable deterministically | S |
| 3.5 | DOM mutation in render / access-before-declare | `TagComparison.tsx:157-158` (`document.body.style`); `MetadataSearchDialog:137`, `DedupReconcileTab:54`, `BlockedHashesTab:53`, `ITunesImport:253`, `StorageTab:50`, `SystemInfoTab:66`, `Settings.tsx:429-431` | Compiler bails out of optimising these components entirely | S |
| 3.6 | Uncapped lists, no virtualisation dependency in `package.json` | `MaintenanceTab.tsx:698/713` renders `result.groups`→`g.files` from `scanChapterGroups` (`api.ts:5902`, no limit param); `:1132/1147`, `:1214`; `TagComparison.tsx` 7 unpaginated maps; `Diagnostics.tsx:559/590` | With 60k books a chapter-group scan result renders every file row; QueueRail's cap at 100 is the exception | M |
| 3.7 | `ActivityLog.tsx` (2,595 lines): 21 `.map` render sites, 1 paginated | `ActivityLog.tsx` | Largest page; pagination applies to one list only | M |
| 3.8 | Contexts | `AuthContext.tsx:129` value memoised (good); `ToastProvider.tsx:45` `value={{ toast }}` new object every render | Every `useToast()` consumer (12 files) re-renders whenever the provider does | S |
| 3.9 | Duplicate-loader heuristic (same `loadX()` from >1 effect in one file) | `Dashboard.tsx:199/207` — second effect is a 15 s auto-refresh, not a duplicate mount fetch; `MaintenanceTab.tsx:415/757/869` are three sibling components in one 1,550-line file each with its own `loadStats`; `useLibraryQuery.ts:378/384/392` are scan/organize completion hooks | No double-mount fetch found by this heuristic; the MaintenanceTab result argues for splitting the file | — |
| 3.10 | Unused eslint-disable directives | `ChangeLog.tsx:75`, `ITunesImport.tsx:205`, `Library.tsx:777` | The suppressed problem was fixed; the directive now hides a future one | S |

In-source suppressions: 16 `exhaustive-deps` disables across 12 files (`ChangeLog`, `ITunesImport`, `MetadataSearchDialog`, `VersionManagement`, `BookDetailInfoTab`, `ReviewWorkspace`, `useMetadataLane`, `DedupReconcileTab`, `DedupAIReviewTab`, `Settings`, `Library`, `ActivityLog`), 19 `no-explicit-any`, 4 `only-export-components`, 1 `set-state-in-effect`.

### 3.4 Error handling

411 `catch` blocks total; 35 empty bodies, 22 console-only.

| # | Finding | Anchors | Why it matters | Cost |
|---|---|---|---|---|
| 4.1 | Revert/undo failures swallowed | `ActivityLog.tsx:693` (revert failed → dialog closes silently), `ChangeLog.tsx:105`, `:65` | User believes a revert happened | S |
| 4.2 | Stale "not yet deployed" swallow | `MaintenanceTab.tsx:99-107` — `// degrade silently if backend endpoint not yet deployed`; `GET /maintenance-window/status` does exist (`internal/server/wire_operations_routes.go:95`); 3 more empty catches at `:408,750,862` | Real failures of a live endpoint are hidden | S |
| 4.3 | Failure rendered as "empty" | `Authors.tsx:178-190` (`getAuthorBooks` error → empty list = "no books"); `BookDetail.tsx:191-199` silent refresh; `Library.tsx:230-236` presets `console.error` only | Error and empty are indistinguishable | S each |
| 4.4 | Empty catches in data paths | `useMetadataLane.ts:116,130,163,173`, `BookDetail.tsx:196,322,341,348`, `Library.tsx:466`, `PlaylistDetail.tsx:64,90`, `usePendingFileOps.ts:67`, `DedupAcousticTab.tsx:605`, `DedupReconcileTab.tsx:60`, `DedupEmbeddingTab.tsx:236`, `ITunesImport.tsx:114,123,227`, `WelcomeWizard.tsx:283`, `api.ts:4133`, `eventSourceManager.ts:80`, `apiFetch.ts:95`, `tagDisplay.ts:49`, `AnnouncementBanner:44`, `AddToPlaylistDialog:81`, `VersionsPanel:52`, `ReadStatusChip:65`, `OperationsIndicator:206`, `TagCloud:104` | Some are legitimately best-effort (banner, tag cloud); the lane/BookDetail ones are not | M (triage) |
| 4.5 | Console-only | `useReviewStore.ts:82`, `useOperationsStore.ts:186`, `TagEditor.tsx:96,106`, `AudiobookList.tsx:840`, `WelcomeWizard.tsx:262,296`, `MetadataHistory.tsx:111`, `DirectoryTree.tsx:76`, `useImportFolderHandlers.ts:55,71,80,180`, `Library.tsx:1090,1849,1865`, `ActivityLog.tsx:634,648,662,682` | Never surfaces to the user | S |
| 4.6 | 200-with-failure-in-body ignored: batch callers not reading `failed/errors/skipped` | `BulkTagDialog.tsx:51`; `MaintenanceTab.tsx:285` (`mergeChapterGroups`); `useMetadataLane.ts:912` (`batchApplyFromCache` — result has `skipped: BatchApplySkip[]` `api.ts:3891`), `:1179`; `ReviewWorkspace.tsx:285,294,334`; `DedupAcousticTab.tsx:711`; `Library.tsx:1330` (`combineBooks`, `errors: string[]` `api.ts:1629`), `:1485` (`void result`), `:1512`, `:2006`, `:2029` | Partial failures report success. Good models exist in-repo: `useRegroupLane:819`, `useDupesLane:848`, `DedupEmbeddingTab:586`, `Series:671`, `Library:1410`, `Authors:604` | M |
| 4.7 | api.ts non-2xx→`[]` (8 fns, §3.2) and `testMetadataSource` no `.ok` | see §3.2 | Same conflation at the service layer | S |

22 api.ts result types carry `failed/errors/skipped` fields (L396, 410-412, 437, 1203, 1375-1376, 1522, 1629, 2896, 3219-3220, 3320, 3716, 3775, 3839, 3891, 4943, 5341-5342, 5435, 6037-6038, 6224, 6413) — 4.6 lists the callers that ignore them.

### 3.5 Test gaps

Coverage run (`./node_modules/.bin/vitest run --coverage`, worktree binary; 109 files, 979 tests pass, 49.6 s): statements 48.40% (5,675/11,725), branches 44.71% (4,022/8,995), functions 42.95% (1,359/3,164), lines 49.79% (5,263/10,569).

The headline is inflated. `vitest.config.ts` sets thresholds (lines 30 / functions 20 / branches 20 / statements 25) but no `coverage.include`; Vitest 4 removed `coverage.all`, so files never imported by a test are absent from the denominator. **58 of 214 source files (~24k lines)** are invisible to the number:

| Group | Files (lines) |
|---|---|
| Pages | `Authors` 1285, `Series` 1216, `Settings` 1395, `Diagnostics` 946, `Users` 338, `FileManager` 303, `TrashedVersions` 277, `PlaylistDetail` 245, `Setup` 170, `BookDedup` 167, `System` 89, `FileBrowser` 78, `main.tsx` 49 |
| Dedup tabs | `DedupAcousticTab` 1332, `DedupSeriesTab` 1026, `DedupAuthorTab` 1014, `DedupAIReviewTab` 529, `DedupSplitBookTab` 512, `DedupReconcileTab` 454, `DedupAdvancedScanTab` 392, `DedupBookTab` 356 |
| Settings/system | `ITunesImport` 1610, `MaintenanceTab` 1550, `SettingsGeneral` 959, `SystemInfoTab` 564, `APIKeysTab` 559, `MetadataSettingsTab` 509, `ITunesTransfer` 476, `StorageTab` 412, `OpenLibraryDumps` 394, `BlockedHashesTab` 348, `PerformanceSettingsTab` 342, `PluginsTab` 328, `AutoUpdateSection` 297, `DelugeSettingsTab` 269, `PathsSettingsTab` 264, `QuotaTab` 264, `WriteBackPreviewTable` 240, `TempLoginTab` 192, `ITunesConflictDialog` 189, `ToolsSettingsTab` 82 |
| Hooks | `useSettingsHandlers` 944, `useImportFolderHandlers` 213, `useBackupHandlers` 167, `useMetadataSourceHandlers` 121, `useUnsavedChangesBlocker` 110, `useTimeout` 76, `useAsyncAction` 65 |
| Misc | `CacheStatsPanel` 221, `TagEditor` 240, `DirectoryTree` 167, `FingerprintVisualsColumn` 156, `FileSelector` 138, `MetadataDiffTable` 113, `InlineEditField` 103, `LoadingSpinner` 36, `review/lanes/types.ts` 51, `test/setup.ts` 133 |

Lowest measured files: `api.ts` 5.91%, `playlistApi` 3.7%, `versionApi` 6.25%, `operationPolling.ts` 2.85%, `BatchActivityEntry` 0%, `AudioFileCompare` 1.72%, `FingerprintCanvas` 1.56%, `dedupHelpers` 7.14%, `LibraryDialogs` 15.78%, `UserMenu` 17.72%, `OperationsIndicator` 18.93%, `MetadataSearchDialog` 21%, `WelcomeWizard` 24.61%.

Fixture quality (heuristic, overcounts): 207 test cases whose only assertions are presence (`getBy*`/`toBeInTheDocument`/`toBeDefined`), concentrated in `components/settings/*Section.test.tsx` "renders the section heading" smoke tests, `App.test.tsx:73/87`, `ReadStatusChip` tests. 60 mocks resolve `[]`/`{}`; a refined search for tests that name populated behaviour yet resolve an empty fixture found 0 by this method.

`GATE_EXEMPT` (`web/tests/e2e/check-spec-discovery.mjs:111-129`, checked both directions): `demo-full-workflow.spec.ts`, `interactive-import-workflow.spec.ts` (demo/interactive, need a human), `benchmark-library-load.spec.ts`, `benchmark-review-lanes.spec.ts` (`E2E_PERF=1` wall-clock instruments). The rationale holds for the four.

Fix for the headline: add `coverage.include: ['src/**/*.{ts,tsx}']` with matching excludes — expect the real number to land in the low 30s, at or under the existing thresholds. Cost S; the fallout (a failing threshold) is the point.

### 3.6 Type safety

| Metric | Count | Anchors |
|---|---|---|
| `any` (non-test) | 31 | 18 in `hooks/useSettingsHandlers.ts:686-722` (payload sanitiser); 3 `PathsSettingsTab`; 2 `FingerprintVisualsColumn`; 2 `DedupAcousticTab`; 1 each `api.ts`, `activityApi.ts`, `Dashboard`, `ActivityLog`, `SettingsGeneral`, `MetadataSettingsTab` |
| `as unknown as` | 8 | `test/setup.ts:46,110`; `BatchActivityEntry.tsx:71`; `AudiobookList.tsx:647`; `BookDetailVersionGroup.tsx:739,742`; `EvidencePanel.tsx:66`; `BookDetail.tsx:955` |
| `@ts-expect-error` | 8 | `test/setup.ts:91` (EventSource), 6 intentional type-level tests in `lanes.test.ts`/`reviewActions.test.ts`, `FileManager.tsx:195` (`webkitdirectory`) |
| `@ts-ignore` | 0 | `strict: true` |

### 3.7 Stale comments

| Anchor | Says | Reality | Cost |
|---|---|---|---|
| `useRegroupLane.ts:478-481` | filter "hides every non-matching loaded row on the KEYSTROKE" | `:617-625` says the opposite and cites a failed test; `searchFilter = debouncedSearch.trim()` `:417`, `useDebouncedSearch(filters.search, …)` `:359` — the later comment is correct | S |
| `api.ts:3252` | "Server returns operation_id at top level; data key carries legacy fields" | `operation_id` is inside `data`; the "legacy fields" are never sent | S |
| `MaintenanceTab.tsx:105` | "backend endpoint not yet deployed" | route exists (`wire_operations_routes.go:95`) | S |
| `hooks/useDebouncedSearch.ts` header | Library's pair-debounce "deliberately not consolidated" | Still true, but the reason (two states) is removable (§3.1 item 1.11) | S |
| Sweep | 30 comments containing legacy/temporary/deprecated/"for now"; 3 TODO/FIXME | Not individually verified | — |

### 3.8 Top 10 (source 3's order, impact × confidence ÷ cost)

| Rank | Item | Impact | Conf | Cost |
|---|---|---|---|---|
| 1 | Fix `getBooksByAuthor` envelope (`api.ts:1560`) | High | High | S |
| 2 | Rename `Book.bitrate/sample_rate/series_position` → `bitrate_kbps/sample_rate_hz/series_sequence` | High | High | S |
| 3 | Add `coverage.include` to `vitest.config.ts` — 58 files / ~24k lines excluded from the 48% headline | High | High | S |
| 4 | Surface revert failures (`ActivityLog.tsx:693`, `ChangeLog.tsx:105`) and remove the stale MaintenanceTab swallow (`:99-107`) | High | High | S |
| 5 | Read `failed/errors/skipped` in the 13 batch callers that ignore them (§3.4 item 4.6) | High | High | M |
| 6 | `client.ts` with `unwrapData`/`buildQuery`/`ApiError` + retire the 36 `data ?? body` fallbacks and 8 non-2xx→`[]` swallows; then split api.ts | High | Med | L (S per module) |
| 7 | Add `AbortController` to `useMetadataLane` load (`:529-652`) to match the other two lanes | Med | High | S |
| 8 | Consolidate formatters onto `utils/mediaFormat.ts` (12 + 15 + 11 copies; fix its MB cap first) | Med | High | M |
| 9 | Generate `Book` (and ideally every response type) from the Go structs so §3.0 Bug 2 cannot recur | Med | Med | M |
| 10 | `useSelectionSet()` hook + shared `useDedupTable()` state; then Authors/Series and single/bulk MetadataSearchDialog de-twinning | Med | Med | L |

### 3.9 Method and limits (source 3)

- No edits, no commits, no subagents, no `npx` (worktree `./node_modules/.bin` used throughout). No LSP calls — the tool was not exposed; references were grep-verified by reading each anchor. A first coverage read came from a sibling agent's Go `coverage.txt` in the shared scratchpad and was discarded; the run was redone in a private subdirectory.
- Did not run Playwright; did not run ESLint with `--fix`; did not verify the 30 "legacy/temporary" comments individually. Did not diff every api.ts response type against Go — only `Book` and `BatchWriteBackResponse`; the other 218 types are unchecked and, given two hits in two tries, likely hide more.
- Source tally: 7 sections evidence-gathered, coverage run executed, 2 product bugs confirmed at source and consumer. Remaining: 0 within audit scope. Blocked: 1 — `LSP` tool unavailable; substituted grep + read-through of every anchor.

## Part 4 — TypeScript tooling and package census

Source: frontend modernization census of `web/` (read-only). Corpus: 322 `.ts/.tsx` files (212 tsx), 107 test files, ~99.6k LOC; `src/services/api.ts` 6,521 lines / 278 exported functions. `tsc --noEmit` exits 0; `eslint .` = 0 errors / 146 warnings.

### 4.1 Inventory

| Package | Installed | Notes |
|---|---|---|
| typescript | 6.0.3 | Already past 5.x; "can't go to 7" is the only constraint left |
| react / react-dom | 19.2.8 | `createRoot` in `src/main.tsx:49`; no `onCaughtError`/`onUncaughtError` |
| @types/react / react-dom | 19.2.18 / 19.2.5 | |
| @mui/material / icons-material | 9.4.0 / 9.4.0 | no x-data-grid, no @mui/lab, no @mui/styles |
| @emotion/react / styled | 11.14.0 / 11.14.1 | |
| vite | 8.2.1 (rolldown/oxc) | `@vitejs/plugin-react` 6.1.0, `@rolldown/plugin-babel` 0.2.3, `babel-plugin-react-compiler` 1.0.0 (compiler ON) |
| vitest / @vitest/coverage-v8 | 4.1.11 | jsdom 30.0.1, RTL 16.3.3, user-event 14.6.6, jest-dom 7.0.1 |
| @playwright/test | 1.62.1 | 39 spec files under `tests/e2e/` |
| eslint / typescript-eslint | 10.9.1 / 8.68.0 | flat config `eslint.config.mjs`; `eslint-plugin-react-hooks` 7.1.1, react-refresh 0.5.5, `@eslint/js` 10, `globals` 17 |
| react-router-dom | 7.18.3 | pure re-export shim over `react-router` 7.18.3 |
| zustand | 5.0.15 | curried `create<T>()()` in 3/4 stores |
| axios | 1.20.0 (devDep) | 0 imports anywhere (src, tests, Makefile, workflows) — dead dependency |
| esbuild | 0.28.2 (direct dep) | only dedupes vite's own copy; Vite 8 no longer uses it for transforms |
| TanStack Query / swr / redux / jotai / date-fns / dayjs / luxon / zod / yup | not installed | fetching is hand-rolled (`src/utils/apiFetch.ts` → `fetch`) |

`tsconfig.json`: `target ES2022`, `module ESNext`, `moduleResolution bundler`, `lib [ES2022, DOM, DOM.Iterable]`, `strict true`, `isolatedModules true`, `noUnusedLocals/Parameters`, `noFallthroughCasesInSwitch`, `types [vitest/globals, node]`, `paths {"@/*": ["./src/*"]}`. Absent: `verbatimModuleSyntax` (enabling → 30 errors), `erasableSyntaxOnly` (→ 2 errors, both enums), `noUncheckedIndexedAccess` (→ 256 errors). Stale: `"ignoreDeprecations": "6.0"` — measured unnecessary (with it removed and `target/lib` bumped to ES2025, `tsc` still exits 0); `useDefineForClassFields: true` is redundant at target ≥ ES2022.

Config duplication: `vite.config.ts` carries a `test:` block (thresholds 15/10/15/15) and `vitest.config.ts` exists (30/20/20/25). Vitest prefers `vitest.config.ts`, so the vite block is dead and misstates the gate.

### 4.2 React 19 / 19.2

| Idiom | Old-form count | Evidence / new form | Behaviour change | Codemod |
|---|---:|---|---|---|
| `forwardRef` → ref as prop | 0 | done | — | done |
| `useContext(X)` → `use(X)` | 2 | `src/contexts/AuthContext.tsx:150`, `src/components/toast/ToastProvider.tsx:24` | no | none (2 lines) |
| `<Ctx.Provider>` → `<Ctx>` | 3 | `AuthContext.tsx:145`, `ToastProvider.tsx:45,82` | no | none |
| `react-dom/test-utils` `act` | 0 | the 14 `act` imports come from `@testing-library/react` | — | done |
| `ReactDOM.render` / string refs / `propTypes` / `defaultProps` on fn components / `useFormState` / `useRef()` no-arg | 0 each | the 57 `defaultProps` hits are MUI theme `components.MuiX.defaultProps` (`src/theme.ts`, 4) + tests | — | `react/19/migration-recipe` and `types-react-codemod preset-19` would be no-ops |
| default `import React from 'react'` | 26 files; 70 `React.X` type refs (`React.MouseEvent` 25, `React.FC` 21, `React.ReactNode` 7…) | named `import { type MouseEvent } from 'react'` | no | none; sed/ESLint autofix territory |
| `React.FC` annotations | 21 | plain function signature | no | none |
| Manual `useMemo`/`useCallback`/`memo` with compiler ON | 55 / 202 / 8 in 69 files | remove where the compiler compiles the component; keep where it bails (`react-hooks/preserve-manual-memoization` warns 3×) | no | none; gate on per-component `CompileSuccess` |
| React Compiler bailouts | 218 / 329 components (66%) in 79 files — 204 caused by `try/finally` (`docs/react-compiler-adoption.md:38-57`); 206 `finally` sites in 69 files, 90 are `setX(false)` | route async work through a hook (`useAsyncAction` exists: `src/hooks/useAsyncAction.ts`, 4 callers) or a query lib | no if done as hook extraction | none |
| `react-hooks/set-state-in-effect` | 87 warnings (`BookDetail.tsx` 7, `MaintenanceTab.tsx` 5, `ActivityLog.tsx` 5, `VersionManagement.tsx` 3, `DedupLabels.tsx` 3, `Library.tsx` 3) | compute during render / `key`-reset / event handler | yes (removes an extra render; timing-sensitive) | none |
| other compiler-lint warnings | immutability 11, static-components 6, refs 5, preserve-manual-memoization 3, purity 2, globals 1, exhaustive-deps 2 | per rule | mostly no | none |
| 19.2 `useEffectEvent` | 0 uses; 16 `eslint-disable … exhaustive-deps` are the candidate set | `const onX = useEffectEvent(...)` outside deps | no | none |
| 19.2 `<Activity>` | 0 uses; tab panels in Settings/BookDetail are `{x && <Y/>}` candidates | `<Activity mode={hidden}>` | yes (effect mount semantics) | none |
| 19 root error hooks | 0 | `createRoot(el, { onUncaughtError, onCaughtError })` + existing `ErrorBoundary.tsx` | no | none |
| Concurrent APIs already adopted | `startTransition` 5, `useTransition` 3, `useOptimistic` 5, `useDeferredValue` 4, `use(` 1, `useId` 1 | — | — | — |

### 4.3 MUI v9

0 legacy sites: Grid v1 `item`/`xs=` 0 (`size=` 94, `container` 20); `InputProps`/`PaperProps`/… → `slotProps` 0 (`slotProps=` 81; the 2 `inputProps=` hits at `SearchBar.tsx:310` `<Select>` and `TopBar.tsx:163` `<InputBase>` are not deprecated there); `components`/`componentsProps` 0; `Hidden`/`makeStyles`/`withStyles`/`@mui/styles`/`styled()` 0 (styling is `sx=` 2,312 sites + theme); v9 removals (`disableEscapeKeyDown`, `Typography paragraph`, `Divider light`, `renderTags`/`getTagProps`, `TransitionComponent`, `GridLegacy`, `*Outline` icons) 0 live uses (`WelcomeWizard.tsx:333` already comments on the `disableEscapeKeyDown` removal); `theme.ts:150-157` uses `cssVariables: { colorSchemeSelector: 'data-mui-color-scheme' }`, `colorSchemes`, `useColorScheme` (2 files). Barrel `from '@mui/icons-material'` 37 files vs 266 path imports — low value under rolldown. MUI codemods: none applicable — nothing to do.

### 4.4 TypeScript 5.5 → 6.0

| Idiom | Count | Evidence / new form | Risk |
|---|---:|---|---|
| `enum` (blocks `--erasableSyntaxOnly`, TS7/Node strip-types readiness) | 2 decls, 19 use sites | `src/types/index.ts:136` `SortField`, `:156` `SortOrder`; `tsc --erasableSyntaxOnly` → 2×TS1294; `const SortField = {…} as const; type SortField = typeof SortField[keyof typeof SortField]` | no (string enums; runtime shape identical) |
| `namespace`, parameter properties | 0 | | |
| explicit `(x): x is T` predicates 5.5 now infers | 7 total, 2 removable | `pages/Login.tsx:74`, `BookDetailVersionGroup.tsx:333`; the other 5 use `!!x`/`Boolean()`/compound checks which are not inferred | no |
| `.filter(Boolean)` (never narrows) | 13 | `src/utils/tagDisplay.ts:67`, `BulkMetadataSearchDialog.tsx:623` …; `.filter((x) => x != null)` | no |
| `satisfies` on config/lookup objects | 5 uses / 38 `as const` | more `satisfies Record<…>` on theme/lookup tables | no |
| `NoInfer`, `const` type params, `using`, `import defer` | 0 each | `using` unavailable at `lib ES2022`; `import defer` needs `module: esnext` (have it) + bundler support | |
| `arr[arr.length - 1]` → `.at(-1)` | 9 (vs 5 `.at(` uses) | `OperationActivityPanel.tsx:79`, `BulkMetadataSearchDialog.tsx:112,314` | no |
| manual group-by `reduce<Record<` → `Object.groupBy` | 1 (of 17 `.reduce(`) | needs `lib ES2024+` | no |
| manual set ops → `Set#union/intersection` | 11 (`new Set([...a, ...b])` 3, `.filter(x => set.has(x))` 8) | needs `lib ES2025`; runtime Safari 17 / Chrome 122 / FF 127 | no |
| deferred `new Promise((resolve` → `Promise.withResolvers` | 4 | `lib ES2024` | no |
| `hasOwnProperty` → `Object.hasOwn` | 3 | `lib ES2022` (already) | no |
| `Array.from(x.values()).map` → iterator helpers | 4 | `lib ES2025`; Safari 18.4 | no |
| lib/target bump | `tsc -p` with `target/lib ES2025` and no `ignoreDeprecations` → 0 errors | TS 6.0 default is `es2025`; Vite 8 default `build.target` = Chrome 111/FF 114/Safari 16.4 lacks Set methods, so the bump must come with an explicit `build.target` (MUI 9's own floor is Chrome 117/FF 121/Safari 17) | build-target change is a yes |
| `verbatimModuleSyntax` | 30 errors | mixed `import { X, type Y }` (49) vs `import type` (174); gate for TS7 / isolated transpilers; `eslint --fix` with `@typescript-eslint/consistent-type-imports` | no |
| `noUncheckedIndexedAccess` | 256 errors | large manual pass | no (types only) |

### 4.5 Vite 8 / Vitest 4

| Idiom | Count | New form | Risk |
|---|---:|---|---|
| dead `test:` block in `vite.config.ts` | 1 | delete it or merge into one file | no |
| `coverage.include` unset | — | Vitest 4 reports only loaded files; the 30/20/20/25 thresholds are computed over files a test imports, not `src/**`; set `coverage.include: ['src/**/*.{ts,tsx}']` | yes — the gate will likely go red at real coverage |
| `vitest.workspace` / `environmentMatchGlobs` / `poolOptions` / `deps.inline` | 0 | | |
| `beforeEach(() => { vi.clearAllMocks() })` boilerplate | 28 of 59 `beforeEach` (42 `vi.*AllMocks()` calls) | `test.clearMocks: true` / `mockReset: true` in config | low (Vitest 4 `restoreAllMocks` no longer touches automocks) |
| provider-wrapping boilerplate | `renderWithProviders` 103 uses, but 37 inline `<MemoryRouter>` in 23 files, 12 inline `<ThemeProvider>` | `test.extend({ render: … })` fixture or make `renderWithProviders` universal | no |
| `vi.mock('…/services/api')` | 68 mocks in 52 files (41 target `api.ts`); 3 relative-path spellings of the same module | `@/services/api` alias | no |
| manual polling `await new Promise(r => setTimeout…)` | 3 | `vi.waitFor` / `expect.poll` (0 uses; RTL `waitFor` 307 — idiomatic, keep) | no |
| `vi.mocked(` 274, `vi.hoisted` 2, `vi.importActual` 17, `it.each` 11, `it.for` 0, snapshots 0, msw not installed, `fetch` stubbed via `global.fetch =` in 5 files | — | consider `msw` or `vi.stubGlobal('fetch')` consistently | |
| `fireEvent` 129 (25 files) vs `userEvent` 75 (19 files) | — | RTL guidance: `userEvent.setup()` | timing (async) |
| `@vitejs/plugin-react` 6 `compiler: true` (oxc-native) vs `@rolldown/plugin-babel` + babel | — | would drop `@babel/core`, `@rolldown/plugin-babel`, `@types/babel__core`; `vite.config.ts:15-21` correctly flags it experimental | yes — measure bailout count before/after |
| `esbuild` as direct dep | 1 | removable once vite drops it | no |

### 4.6 ESLint 10 / typescript-eslint 8 / react-hooks 7

`eslint --print-config src/App.tsx` shows the 16 react-hooks 7 rules active (`rules-of-hooks` error, the rest warn): `set-state-in-effect`, `refs`, `purity`, `immutability`, `static-components`, `use-memo`, `preserve-manual-memoization`, `incompatible-library`, `globals`, `error-boundaries`, `set-state-in-render`, `unsupported-syntax`, `config`, `gating`, `exhaustive-deps`.

| Item | Count | New form |
|---|---:|---|
| `tseslint.config(...)` — deprecated (typescript-eslint #10935) | 1 (`eslint.config.mjs:12`) | `import { defineConfig } from 'eslint/config'` |
| `globals.es2020` | 1 (`:23`) | `globals.es2025` (needed if `lib` bumps) |
| no `parserOptions.projectService` → no type-aware rules | 0 | `projectService: true` + `recommendedTypeChecked` (uncounted — would surface `no-floating-promises` on the 86 loader fns) |
| `@typescript-eslint/no-explicit-any` | 11 warnings (31 `any` sites incl. tests) | |
| `react-refresh/only-export-components` | 15 | split non-component exports |
| stale `eslint-disable` comments | 8 files listed in `docs/react-compiler-adoption.md` | remove; `--report-unused-disable-directives` |

### 4.7 React Router 7 / zustand 5 / data layer

| Item | Count | Evidence / new form | Risk | Codemod |
|---|---:|---|---|---|
| `from 'react-router-dom'` (shim package) | 63 imports in 63 files (39 non-test) | the package README says to change imports and remove it; `from 'react-router'`; drop the dep | no (identical exports) | one `gsed -i "s/'react-router-dom'/'react-router'/"` over the matching files |
| `<BrowserRouter>`+`<Routes>` (declarative mode) | 1 router (`src/main.tsx:21`), 31 `<Route>`; 0 `createBrowserRouter`/loaders; 18 `lazy(`, 1 `<Suspense>` | data-router mode (`createBrowserRouter`, `RouterProvider`, `loader`s) would host the data layer | yes — architectural | none |
| zustand 5 | `create<T>()()` in 3/4 stores; `useLibraryCache.ts:59` uses uncurried `create<T>(`; 0 selector-less hook calls; 0 object selectors without `useShallow`; 21 `.getState()` | fix the one uncurried store | no | — |
| hand-rolled fetching | 92 `useEffect` blocks (50 files) call `api.*`/loaders; 86 `const load*/fetch*/refresh* = async` in components; 57 `[loading, setLoading]` + 57 `[error, setError]` pairs; 90 `setX(false)` in `finally`; 26 `AbortController`s; hand-rolled 60 s TTL cache in `src/stores/useLibraryCache.ts:25`; `useLibraryQuery.ts` holds 9 `useState`s for one query | TanStack Query 5 (`useQuery`, `queryOptions()`, `useSuspenseQuery`, `isPending`, `gcTime`, `useMutation().variables`) | yes (caching/refetch/dedup semantics) | `@tanstack/query-codemods` only for v4→v5; adoption itself is manual |

### 4.8 Ranked top-10 migrations (source 4's order, value = sites × benefit / risk)

| # | Migration | Sites | Benefit | Risk | How |
|---|---|---:|---|---|---|
| 1 | Move async loading out of components (`useAsyncAction`/query hook) so the React Compiler stops bailing on `try/finally` | 206 `finally` (90 `setLoading(false)`), 218 bailed components | unlocks the compiler for 66% of the tree; deletes 57+57 state pairs; removes the 3 relative `api` mock spellings | medium | phase A: extend `useAsyncAction` (4 callers) to cover the 86 loader fns; phase B: TanStack Query 5 behind `queryOptions()` per `api.ts` domain |
| 2 | Fix `coverage.include` + delete the dead `test:` block in `vite.config.ts` | 2 configs | the 30% gate is measured over loaded files only — the instrument is blind | low code / gate may go red | `coverage.include: ['src/**/*.{ts,tsx}']` |
| 3 | `react-hooks/set-state-in-effect` (87) → derive in render / key reset | 87 in ~40 files | removes double renders; biggest compiler-lint bucket | medium — timing changes | file by file (`BookDetail.tsx` 7, `MaintenanceTab.tsx` 5, `ActivityLog.tsx` 5 first) |
| 4 | `react-router-dom` → `react-router`; drop dead `axios` and direct `esbuild` | 63 imports + 3 deps | smaller lockfile/attack surface | none | one `gsed` + `npm uninstall react-router-dom axios esbuild` |
| 5 | tsconfig: drop `ignoreDeprecations`/`useDefineForClassFields`, `lib`/`target` → ES2025, set `build.target` to MUI 9's floor | 0 errors measured; 11 set-op + 4 withResolvers + 1 groupBy + 9 `.at(-1)` sites | native `Set#union`, `Object.groupBy`, `Promise.withResolvers`, `Map#getOrInsert` | verify Safari 17 floor is acceptable (MUI 9 already requires it) | `vite.config.ts build.target: ['chrome117','edge121','firefox121','safari17']` |
| 6 | `vi.clearAllMocks` boilerplate → `test.clearMocks: true`; universal `renderWithProviders`/`test.extend` | 28 `beforeEach` + 37 inline `<MemoryRouter>` | shorter tests, one provider stack | low | config + sed |
| 7 | Kill the 2 `enum`s, enable `erasableSyntaxOnly` | 2 decls / 19 uses | TS 7 & Node strip-types readiness | none (string enums) | `as const` objects in `src/types/index.ts` |
| 8 | ESLint: `defineConfig`, `globals.es2025`, `projectService` + `recommendedTypeChecked`, `reportUnusedDisableDirectives` | 1 config; 8 known stale disables | type-aware rules (floating promises over 86 loaders) | low config / unknown number of new warnings | edit `eslint.config.mjs` |
| 9 | `verbatimModuleSyntax` + `consistent-type-imports` autofix | 30 errors / 49 mixed imports | TS 7 / oxc-transpile safety | none | `eslint --fix` |
| 10 | React 19 small idioms: `use(Ctx)` (2), `<Ctx>` provider (3), `.filter(Boolean)`→`x != null` (13), drop 2 redundant predicates, `useEffectEvent` for the 16 `exhaustive-deps` disables, `createRoot` error callbacks | ~37 | correctness of narrowing; removes disables | none–low | manual; React 19 codemods are no-ops here |

Not worth doing (source 4): MUI codemods (0 legacy sites), `types-react-codemod preset-19` (0 `useRef()`, 0 `React.Reducer`), `react/19/migration-recipe` (0 targets), `noUncheckedIndexedAccess` (256 errors for type-only gain — park), `plugin-react compiler: true` (experimental; the babel path works and is measured).

### 4.9 Migration blockers

- `verbatimModuleSyntax`: 30 errors (49 mixed `import { X, type Y }`) — autofixable but gates TS 7.
- `erasableSyntaxOnly`: 2 errors, both `enum`s in `src/types/index.ts:136,156`.
- `noUncheckedIndexedAccess`: 256 errors — parked.
- `lib`/`target` → ES2025 compiles with 0 errors but requires an explicit `build.target` because Vite 8's default (Chrome 111/FF 114/Safari 16.4) lacks Set methods; Safari 17 floor must be accepted.
- `coverage.include`: turning it on will likely take the 30/20/20/25 gate red.
- `@vitejs/plugin-react` `compiler: true` is experimental; bailout count must be measured before/after.
- TanStack Query and data-router mode are architectural changes to caching/refetch/dedup semantics, not mechanical migrations.
- `set-state-in-effect` fixes change render timing and need the existing tests.

### 4.10 Method and limits (source 4)

- Source 4 did not emit a `COMPLETED/REMAINING/BLOCKED` tail; the following is what it stated about method. Read-only in the `go-modernize` worktree; counts came from grep over `src`, `tsc --noEmit` (exit 0), `eslint .` (0 errors / 146 warnings), `eslint --print-config`, and `tsc` trial runs with `erasableSyntaxOnly`, `verbatimModuleSyntax`, `noUncheckedIndexedAccess` and an ES2025 `lib`/`target`. Release-note idioms were checked against fetched upgrade guides (React 19/19.2, MUI v9, TS 5.5–6.0, Vite 8/Vitest 4, React Router 7, ESLint 10).
- Uncounted: the number of warnings `recommendedTypeChecked` would add; the compiler bailout count after a `compiler: true` switch. Runtime browser floors (Safari 17/18.4) were quoted, not tested.

## Cross-cutting observations

Only items reported by two or more sources.

| Observation | Source evidence |
|---|---|
| Coverage instruments overstate what is tested | Source 3: 58 of 214 files (~24k lines) absent from the 48.4% Vitest headline; source 4: `coverage.include` unset, dead `test:` block in `vite.config.ts` misstates the gate. Source 1 (Go side): `internal/database` runs 654 s under `-short`, and `deadcode` could not run, so Go dead-symbol totals are upper bounds. |
| Duplicated helpers with drifting semantics | Source 1: 14 pagination parsers (5 uncapped), 2 `parseRetryAfter` grammars, 3 backup-cleanup predicates, 3 maintenance frameworks, 2 MockStores; source 2: `stringPtr` defined 8×, 12 pointer-helper names across 20 definitions, three parallel path-safety packages; source 3: `formatDuration` 12×, `formatBytes` 15× (MB cap vs `0 KB` vs `—`), `formatDate` 11×, 39 `Set` toggle reducers. |
| Error shape is not modelled, so callers string-match or guess | Source 2 (A2): 24 sites string-match `err.Error()`; no domain not-found sentinel; store returns `fmt.Errorf("book not found")` or `(nil, nil)`; mocks return `(nil,nil)`. Source 1 (D6): two MockStores with `nil,nil` vs sentinel semantics. Source 3: 36 defensive `body.data ?? body` fallbacks and 8 non-2xx→`[]` swallows because the envelope is unwrapped per call site — the `getBooksByAuthor` bug is the result. |
| A success response can carry failure in its body, and callers do not read it | Source 1 (S3, S4, S5, S8): ops count `booksUpdated++` after discarded `UpdateBook` errors; backfill completes "ok" with holes. Source 3 (4.6): 13 batch callers ignore `failed/errors/skipped` on 22 result types that carry them. |
| Whole-library loops without a worker pool or a batch getter | Source 1 (C2, C4, C5, D7): 10 N+1 `GetBookFiles` loops and 8+ new serial `for range books` maintenance ops; source 2 (E): 83 `GetAllBooksCore` callers, 63 of them `(0, 0)` whole-library, 18 in `internal/maintenance/jobs`. |
| Stale comments that assert a state no longer true | Source 1: `mock_store.go` header "~22 files" (128 measured), `TODO(PERF-5)` claiming a missing API, jsonv2 flag references; source 3: `api.ts:3252` envelope comment, `MaintenanceTab.tsx:105` "not yet deployed" on a live route, `useRegroupLane.ts:478-481` contradicted by `:617-625`; source 4: `ignoreDeprecations: "6.0"` measured unnecessary, `vite.config.ts` thresholds superseded. |
| Dead code and dead dependencies | Source 1: ~20 zero-ref exported symbols incl. 4 iTunes stub files, `EnqueueWithOutbox`, 5 operation metrics; source 2: `internal/security/pathvalidation` `SecureJoin*` 0 callers, `google/uuid` indirect-only; source 4: `axios` 0 imports, direct `esbuild` redundant, `react-router-dom` a pure shim. |
| Async work inside React components | Source 3 (1.9, 3.x): three lane hooks with three fetch-guard designs, metadata lane with no `AbortController`, 87 `set-state-in-effect`; source 4: 206 `try/finally` sites cause 204 of 218 compiler bailouts, 57+57 loading/error pairs, 86 loader functions. |
| Language-server tools were unavailable to the auditors | Source 1: gopls not exposed, grep + Python token counter with hand verification; source 3: LSP not exposed, grep + read-through of each anchor. Source 1 and source 2 both lacked `ggrep` on PATH. |
| Worktree state disagreement | Source 1: uncommitted go.mod → 1.27.0, Makefile, 10 workflow files, `internal/`+`cmd/` at `611cbbe61`; source 2 and 3: only `go.mod`/`go.sum` modified, HEAD `df642d11c`. Source 1 says the Makefile no longer sets `GOEXPERIMENT=jsonv2`; source 2 says Makefile:11 sets it. Both were true at the moment each ran: sources 2 and 3 started before the toolchain edits were made in that worktree. The edits shipped as #3039. |

## What this document deliberately does not do

- It fixes nothing and changes no code, config or test; the four audits were read-only and so is this write-up.
- It does not re-measure, re-rank or re-verify any count. Numbers are the sources' numbers at the HEAD stated; where two sources disagree both figures are recorded with attribution rather than a resolution.
- It adds no prioritisation beyond the sources' own top-10 tables; the merged table at the top interleaves those tables and says so.
- It does not plan the large refactors. Source 1's D1 (retire one of three maintenance frameworks) and D6 (unify the two MockStores), source 3's `api.ts` split and Authors/Series de-twinning, and source 4's TanStack Query / data-router adoption each need their own written plan, per CLAUDE.md's plan-before-execution rule.
- It does not close the items the sources flagged as unmeasured: T4 (LFS fixtures), the 30 "legacy/temporary" TS comments, the 218 unchecked `api.ts` response types, the 66 fire-and-forget goroutines needing a lifecycle decision, and the ≈1568 `context.Background()` test sites counted but not read.
