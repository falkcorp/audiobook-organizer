<!-- file: docs/AI-REFERENCE.md -->
<!-- version: 1.8.1 -->
<!-- guid: e5f4g3h2-i1j0-k9l8-m7n6-o5p4q3r2s1t0 -->
<!-- last-edited: 2026-10-30 -->

# AI Reference Guide — Audiobook Organizer

> **Purpose**: Complete project reference for AI agents. Read this before making any changes.
> Keep this file updated with every architectural change.

**Last updated**: 2026-07-17 | **Server version**: `v0.217.x` (ldflags-injected from the git tag into `main.version` → `server.SetVersion()`; defaults to `dev` in local builds — latest stable release `v0.217.7`) | **Total API routes**: see `grep -rn '\.GET(\|\.POST(\|\.PUT(\|\.DELETE(\|\.PATCH(' --include='*.go' internal/server | grep -v _test.go | wc -l` for current count (~608 as of 2026-07-17)

**Live status docs** (read these for current numbers — this file documents structure, not state):

- [`docs/dedup/STATUS.md`](dedup/STATUS.md) — dedup single source of truth (candidate counts, backlog composition; "if a number conflicts with an older doc, this doc wins")
- [`docs/operations/pending-prod-actions.md`](operations/pending-prod-actions.md) — queued prod actions awaiting execution/human decision
- [`docs/plans/DECISIONS-PENDING.md`](plans/DECISIONS-PENDING.md) — open decisions awaiting the owner
- [`docs/audits/2026-07-17-multi-discipline-review.md`](audits/2026-07-17-multi-discipline-review.md) — latest cross-cutting code review findings

## Quick Facts

| Aspect | Detail |
|--------|--------|
| **Language** | Go 1.24 backend, React 18 + TypeScript frontend |
| **Framework** | Gin (HTTP), Material UI (frontend) |
| **Database** | PebbleDB (sole production KV store; SQLite backend was removed — `InitializeStore` errors on `dbType: sqlite`) |
| **ID format** | ULID strings for Books/Operations/Users; auto-increment int for Authors/Series/Narrators |
| **Frontend embed** | `//go:embed web/dist` with build tag `embed_frontend` |
| **Build** | `make build` (full), `make build-api` (backend only), `make deploy` (cross-compile + scp to Linux) |
| **Test** | `make test` (Go), `make test-all` (Go + frontend), `make test-e2e` (Playwright) |
| **Production** | Linux (<your-hostname>), `https://<your-server-ip>:8484`, self-signed cert, PebbleDB |
| **Config** | Viper-based, persisted to DB. Global: `config.AppConfig` |
| **Globals** | `database.GlobalStore`, `database.GlobalQueue`, `config.AppConfig` |

---

## Architecture Overview

```
Browser → React SPA → /api/v1/* → Gin handlers → Service layer → Store interface → PebbleDB
                    → /api/events (SSE)                        → Scanner/Organizer → Filesystem
                                                               → Metadata fetchers → External APIs
                                                               → AI parser → OpenAI API
```

The Go binary embeds the compiled React app. A single process serves both the API and the UI.

---

## Go Package Map

### `internal/server` — HTTP layer (THE largest package)
- **server.go** (~1025 lines — was ~8000; domain logic moved to `internal/server/handlers/*`) — route registration, `Server` struct, middleware wiring
- **handlers/** — extracted per-domain handlers (audiobooks, metadata, operations, dedup, system, itunes, …), each taking narrow consumer-side interfaces
- **itunes_ops.go / itunes_path_ops.go** — thin op-registration wrappers on `*Server` that delegate to `s.itunesSvc`; the iTunes integration itself now lives in `internal/itunes/service/` (see below), NOT in `internal/server`
- **reconcile.go** — File reconciliation, orphan VG assignment, series prune
- **auth.go** — Login, session management, RBAC middleware
- **config_update_service.go** — Hot-reload config via `PUT /api/v1/config`
- **error_handler.go** — `ParsePaginationParams()` (limit capped at 10000), error responses
- **scan_service.go** — Scan orchestration

### `internal/database` — Data layer
- **store.go** — `Store` interface (composite of ~36 role interfaces: `BookStore`, `AuthorStore`, `MetadataCacheStore`, …; PebbleStore is the ONLY implementation), ALL entity struct definitions (Book, Author, Series, Work, Narrator, BookSegment, Operation, etc.)
- **pebble_store.go** — Primary implementation. Key schema: `book:<ulid>`, `book:path:<filepath>`, `book_hash:<hash>\x00<ulid>` (multi-valued; `pebble_store_book_hash_index.go`), `author:<id>`, `series:<id>`, etc.
- **embedding_store.go** — `EmbeddingStore` wrapping the main audiobooks.pebble DB for embeddings + dedup candidates + labeled dataset. Key-space: `emb:v:*`, `emb:c:*`, `dedup:r:*`, `dedup:p:*`, `dedup:e:*` (entity index), `dedup:s:*` (status index), `dedup:seq`, `dedup:label:*`.
- **dedup_automerge_journal.go** — `dedup:automerge:<unixNano16hex>` journal entries written by the `dedup.auto-resolve` apply path; each merge records the book_ver snapshot timestamps so `Engine.UnmergeAuto` can reverse it.
- **review_store.go** — generic human-review queue on `*PebbleStore` (`review_item:r:*` records, `review_item:status:*` index, `review_item:dedupkey:*` idempotency index). v1 producer is the regroup op; `UpsertReviewItem` upserts by stable DedupKey and never resurfaces an already-decided (approved/rejected/applied) item.
- **pebble_store_ops_v2.go** — operations-registry-v2 persistence (`opv2:*` keyspace, see below).
- **dedup_label.go** — Labeled dedup training dataset keyspace (`dedup:label:<candidateID,16hex>`). Types: `LabeledExample` (candidate pair + feature snapshot + label fields), `BookFeatures` (per-book evidence: title, author, path, durations, file count, has_cover, files_exist, recording_ids, itunes_pid_present, whole_book_sig_present), `LabeledExampleFilter`. Methods on `*EmbeddingStore`: `UpsertLabeledExample`, `GetLabeledExample`, `ListLabeledExamples`, `CountLabeledExamples`.
- **ai_scan_store.go** — Separate PebbleDB (`ai_scans.db`) for AI dedup pipeline
- **mock_store.go** — Test mock (generated with mockery patterns)
- **settings.go** — Encrypted settings (AES-256-GCM)

### `internal/scanner` — File scanning
- **scanner.go** — Walk directories, extract metadata via ffprobe, resolve authors/series/narrators, compute SHA256 hashes, incremental scan cache
- Key functions: `ScanDirectory()`, `ComputeFileHash()`, `ComputeSegmentFileHash()`

### `internal/organizer` — File organization
- **organizer.go** — Move files per naming pattern, write tags via ffmpeg, create backups, clean empty dirs
- Pattern tokens: `{author}`, `{series}`, `{title}`, `{print_year}`, `{narrator}`, `{track_title}`, `{ext}`

### `internal/metadata` — External metadata
- **metadata.go** — Multi-source search + apply. Sources: Audible, Open Library, Audnexus, Google Books, Hardcover, Wikipedia
- **ffprobe.go** — Extract media info (duration, bitrate, codec, tags)
- **ffmpeg.go** — Write metadata tags back to audio files
- **cover.go** — Extract/download cover art

### `internal/itunes` — iTunes library handling (low-level ITL layer)
- **parser.go** — Parse iTunes Library.xml (plist format)
- **import.go** — `ValidateImport()`, `ConvertTrack()`, `ExtractPlaylistTags()`, path mapping
- **writeback.go** — Write changes back to iTunes .itl binary files
- Types: `Track`, `Playlist`, `Library`, `ImportOptions`, `PathMapping`

### `internal/itunes/service` — iTunes integration service (extracted from `internal/server`)
- The import/sync/write-back/grouping orchestration formerly coupled to `*Server` now lives here (~5,500 LOC, ~25 files) behind an explicit `Deps` struct (`service.go`) and its own narrow `Store` interface — no `*Server` captures. `*Server` keeps only thin op-registration wrappers that delegate to `s.itunesSvc`.
- Key files: `service.go` (`Deps`), `importer.go` (`groupTracksByAlbum`, `albumGroupKey`, `buildBookFromAlbumGroup`), `enqueuer.go`, `fs_regroup.go`, `config.go`
- **Note:** TODO 4.13 ("extract iTunes into `internal/itunes`") is effectively DONE at the structure level; the low-level ITL binary layer above was intentionally left untouched.

### `internal/ai` — AI integration
- **openai_parser.go** — `OpenAIParser` with methods: `ParseFilename()`, `ParseAudiobook()`, `ParseCoverArt()`, `ReviewAuthorDuplicates()`, `DiscoverAuthorDuplicates()`, `CreateBatchAuthorDedup()`, `CheckBatchStatus()`, `DownloadBatchResults()`
- **embedding_client.go** — Local embedding/LLM backend via Ollama: `bge-m3` (1024-dim vectors) for embeddings; `qwen2.5:7b-instruct` for LLM tasks. Primary backend for dedup/candidate ranking. See `internal/database/hnsw_embedding_store.go`, `internal/dedup/engine.go`, `internal/server/registry_wire.go` for integration points. OpenAI backend (`openai_parser.go`) remains in use for specific flows already documented above.

### `internal/operations` — Operation support types and subpackages
The legacy v1 queue (`OperationQueue`, `Enqueue`, `SaveCheckpoint`) no longer exists; plugin ops run through the registry below. This package now holds shared types and helpers. Redesign plan: `docs/proposals/2026-10-holistic/05-operations-v3.md`.
- **state.go** — `OperationState` (persisted operation state) and the state reader/writer interfaces
- **progress.go** — `ProgressReporter`, `OperationFunc`, `LoggerFromReporter`
- **selection.go** — `ResolveBookIDs`, resolves a `SelectionSpec` to book IDs
- **trigger_source.go** — `WithTriggerSource` / `TriggerSourceFromContext` / `IsManual`, carry what triggered a run in the context
- **types.go** — `SelectionSpec`, `FilterSpec` and related types for server-side bulk targeting
- Subpackages: `childop`, `freshness`, `opmode`, `registry`

### `internal/operations/registry` — Operations registry v2 (UOS)
The plugin-op execution system. All `<plugin>.<op-name>` ops (dedup.*, maintenance.*, acoustid.*, …) run through this, persisted in the `opv2:` keyspace via `database.OpsV2Store` (`internal/database/pebble_store_ops_v2.go`).

- **registry.go** — central `Registry`: owns every `OperationDef`, run handles, per-plugin `max_concurrent` caps, the ConcurrencyKey holder map, enqueue/cancel entry points
- **dispatcher.go** — `runDispatcher`: 100ms-tick dispatch loop; walks queued ops in priority DESC / queued_at ASC order and promotes eligible ones to the worker pool (gates: plugin cap, ConcurrencyKey, deps)
- **worker.go** — worker goroutines that execute op handlers with panic recovery, OTel tracing, abandoned-goroutine tracking (grace period before freeing the slot), infinite-restart detection
- **watchdog.go** — periodic (30s) inspection of in-flight ops; writes `opv2:strike:` rows for uncheckpointed `ResumeRestart` ops and cancels ops stuck past `def.ProgressTimeout`
- **resume.go** — `resumeAfterStartup`: walks `queued|running` rows on start and applies the def's `ResumePolicy` (`ResumeRestart` = re-dispatch with saved state, `ResumeRequeue` = fresh queued op, drop = discard)
- **batch.go** — coalesces burst enqueues of a `Batchable` op type into ONE operation row via a debounce timer
- **run_items.go** — `RunItems[T]`: generic bounded worker-pool helper (Concurrency, PerItemTimeout, ErrModeFail/ErrModeCollect). **The mandated pattern for any whole-library per-item loop** (see CLAUDE.md concurrency rules); never write a plain `for range books` loop with per-item DB/network/subprocess work

`opv2:` key families (from the `pebble_store_ops_v2.go` header comment):

```text
opv2:def:{def_id}                                → OpDefinitionV2Row JSON
opv2:op:{op_id}                                  → OperationV2Row JSON
opv2:q:{999-priority:03d}:{ts_nano:020d}:{op_id} → op_id   (queue index; low byte-order = high priority, FIFO within priority)
opv2:act:{op_id}                                 → ""      (active index: present while queued|running)
opv2:state:{op_id}                               → OpStateV2Row JSON (checkpoint state)
opv2:log:{op_id}:{ts_nano:020d}:{seq:010d}       → OpLogV2Row JSON
opv2:err:{op_id}:{ts_nano:020d}                  → OpErrorV2Row JSON
opv2:strike:{def_id}:{ts_nano:020d}:{op_id}      → OpStrikeV2Row JSON (watchdog strikes)
```

**ConcurrencyKey dedupe semantics**: an `OperationDef` may declare a `ConcurrencyKey` string; the dispatcher never runs two ops holding the same key simultaneously (the holder map in `Registry` maps key → running op ID; a queued op whose key is held stays queued). Use it to serialize op families that would corrupt shared state if overlapped.

### `internal/config` — Configuration
- **config.go** — `Config` struct with 100+ fields. Global: `config.AppConfig`
- Key settings: `RootDir`, `DatabasePath`, `DatabaseType`, `FolderNamingPattern`, `FileNamingPattern`, `ConcurrentScans`, `OperationTimeoutMinutes`, `MetadataSources`, `ITunesPathMappings`
- **Locked settings** (`envLockedSettings`, `configFileLockedSettings`, `flagLockedSettings` in `config.go`): a few settings are operator-authoritative. When the environment, an explicit flag or (Deluge keys only) the config file viper loaded (`--config`, default `~/.audiobook-organizer.yaml`; NOT the `<db dir>/config.yaml` that `SaveConfigToFile` writes) supplies one, `applyEnvAuthoritativeConfig` re-applies that value over the stored settings blob on every load, and `GET /config` reports it in `env_locked` (keys) and `setting_locks` (key to the variable name, the flag, or `config file <path>`) so the UI shows the field read-only. `SettingLockedBy(key)` is the single test for both the apply and the annotation. An empty variable or empty file value is not a lock; with nothing supplied the stored value wins. The five Deluge keys (`deluge_web_url`, `deluge_web_password`, `deluge_discovery_label`, `deluge_discovery_enabled`, `deluge_move_enabled`; variables `DELUGE_*`) are locked this way since 2026-10-11. `PUT /config` on a locked key is accepted and in effect until the next load, then overridden; it is saved too, except `deluge_web_password`: `SaveConfigToDatabase` carries the previously stored blob value forward instead, so neither the env/file secret nor the PUT value is persisted and the operator's stored password survives removal of the lock.

### `internal/logger` — Logging
- **logger.go** — the `Logger` interface, composed of `LevelLogger` (`Trace/Debug/Info/Warn/Error`), `ProgressReporter` (`UpdateProgress(current, total, message)`), `ChangeRecorder` (`RecordChange`, `ChangeCounters`), `CancellationReporter` (`IsCanceled`) and `SubLoggerFactory` (`With(subsystem)`). Also `Level`, `Change`, `WithOperation` / `FromContext` (context-carried `*slog.Logger`)
- **standard.go** — `StandardLogger`, built with `logger.New(subsystem)` (or `NewWithActivityLog` to also write INFO+ to the system activity log). It logs to stdout only: `UpdateProgress`, `RecordChange` and `IsCanceled` are no-ops
- Other packages implement `Logger` when they need real progress or cancellation, e.g. `operations/progress.go`'s `reporterLogger` (built by `LoggerFromReporter`), which wraps a `logger.New("operation")` logger, delegates everything else to it, and forwards `UpdateProgress` (and keeps the reporter across `With`) to the operation's `ProgressReporter`

### `internal/cache` — Generic cache
- **cache.go** — `Cache[T]` with TTL. Thread-safe.

### `internal/backup` — Backup/restore
- **backup.go** — `CreateBackup()`, `ListBackups()`, `RestoreBackup()`

### `internal/models` — Legacy models
- **audiobook.go** — `Audiobook` struct (mostly superseded by `database.Book`)

### `internal/dedup` — Book deduplication engine
- **engine.go** — `Engine` type; per-layer candidate emitters (`checkExactTitle`, `checkExactISBN`, `checkExactFileHash`, `checkExactMetadataSourceHash`, `checkDurationMatch`; the AcoustID/LSH/embedding/metadata-fuzzy collectors live in `collectors_*.go`); `PairEligibility` pre-filter; `hasPlausibleAudio(book) bool` gate (returns true when `Duration > 0` OR `FileSize >= 256 KiB`). The exact-title and exact-ISBN emitters call this gate for both sides before emitting a candidate — prevents stub/unscanned books from creating false-positive pairs.
- **Candidate `Status` is a VERDICT, not derived data** (PR #1973): `dismissed` and `merged` record a decision (human review or auto-resolve). `EmbeddingStore.UpsertCandidateNew` never lets a rescan's `pending` overwrite a terminal status (`isTerminalCandidateStatus`) — before the fix, every `dedup.full-scan` resurrected dismissals (43 flips measured on a prod replica), so the review queue could never converge. Layer precedence on re-upsert: `exact` never overwritten; `llm` protected except by `exact`; a row with a non-empty `FormulaVersion` is never downgraded by a legacy writer without one (T015).
- **No-silent-caps pattern**: whole-backlog list calls pass an explicit `Limit: 1_000_000` (e.g. `auto_resolve.go`, `engine.go`) instead of relying on a default page size that silently truncates; `drain_stale.go` deliberately streams with real Limit/Offset paging instead of materializing the backlog. Never add a whole-library read that inherits a silent default cap (see `feedback_getallbooksfrom_memdb_cap`).
- **auto_resolve.go** — `Engine.AutoResolveCertain`: Tier-1 auto-merge of Band-CERTAIN candidates (≥2 primary signals or a whole-book-signature `true_dup` label). Dry-run by default; `apply=true` is refused unless the `dedup.auto_resolve_enabled` kill switch is on (owner greenlight) and respects a `max_merges` cap. Every applied merge writes a `dedup:automerge:` journal entry so `UnmergeAuto` can reverse it via book_ver snapshots.
- **dataset/** (sub-package) — Pure builder + deterministic catchers for the labeled training dataset. No side effects.
  - `BuilderStore` interface: `GetBook(id string)`, `GetBookFiles(id string)`
  - `BuildExample(BuilderStore, DedupCandidate) (LabeledExample, error)` — loads both books, computes `DurationRatio`, `FolderRelation`, `SharesRecordingID`, `SignatureRelation`. Label fields left empty; caller must run `Classify`.
  - `Classify(LabeledExample) (label, reason string, fires bool)` — three catchers in priority order: `wholeBookSignatureMatch` (→ `true_dup`; both sigs present + similarity ≥ 0.95), `missingFile` (→ `not_dup`), `partVsWhole` (→ `not_dup`; ratio < 0.5 with both durations known). Returns `("","",false)` when no catcher fires.
  - Note: `BookFeatures.Author` is left empty — author name resolution would require an additional store method not in `BuilderStore`.

### `internal/plugins/dedup` — Dedup plugin operations
- **dataset_backfill.go** — `dedup.dataset-backfill` UOS op. Iterates all pending candidates, calls `dataset.BuildExample` + `dataset.Classify`, writes `LabeledExample` to `dedup:label:` keyspace. With `apply=true`, dismisses candidates labeled `not_dup` by a catcher. Dry-run by default. Idempotent.
  - `builderAdapter` bridges `database.Store.GetBookByID` → `BuilderStore.GetBook` (name mismatch bridged without touching either interface).
  - Known limitation: pairs where one side has `Duration=0` but file records exist are NOT caught by the current catchers — they are left unlabeled for human/ML review. The engine gate stops NEW such pairs from being created; existing residual pairs need a future FileSize-aware catcher.
- **auto_resolve.go** — `dedup.auto-resolve` op wrapping `Engine.AutoResolveCertain` (dry-run default; apply gated by the `dedup.auto_resolve_enabled` kill switch + `max_merges` cap — see engine section above).
- **rescore_labeled_examples.go** — `dedup.rescore-labeled-examples` op: recomputes each labeled pair's `ScoreBreakdown` with the engine's existing scorer (calibration was blocked because labeled examples captured before T015 carry no breakdowns). A sibling backfill for pre-T015 CANDIDATE rows (`dedup:r:` records lacking `ScoreBreakdown`) is in progress on a feature branch, not yet in this tree.
- **drain_stale.go** — `dedup.drain-stale` op: pages the pending-exact backlog with real Limit/Offset (never a one-shot 1M materialization), checkpoint/resume via `SaveCheckpoint`, capped per-bucket samples in the dry-run report.

### `internal/plugins/maintenance` — Maintenance ops (selected)
- **regroup_shattered_ai.go** (PR-B1) — dry-run producer: writes one review-queue HOLD per candidate regroup folder; touches ZERO books.
- **regroup_apply.go** (PR-B2) — the APPLY path for the regroup review queue. One apply function per CONFIDENT kind: `regroup.multidisc` → `CombineBooks(ids, primary, nil)` (nil override so the survivor row is never rewritten — no write-back wipe), `regroup.version-group` → re-fetch-and-patch `UpdateBook` mutating ONLY `VersionGroupID`. `regroup.anthology`/`regroup.ambiguous` are deliberately handler-less (need human sub-decisions). Prod apply is gated by config `review_apply_enabled` (`internal/config/config.go`, **default OFF** — plain `viper.GetBool`, no default set); the review handler refuses the merge and says so when the flag is off.
- **title_backfill.go** — `maintenance.title-backfill` ("Strip chapter prefixes from book titles", the CONS-17b title-leak repair — called "title-repair" in `docs/operations/pending-prod-actions.md`). Two-phase: slim `BookCore` scan, then per-book hydrate-full-row-then-write so denormalized Author/Series are never wiped.

---

## API Route Map

All routes are under `/api/v1/` via Gin. Auth middleware on `protected` group.

### Books (CRUD)
| Method | Path | Handler | Notes |
|--------|------|---------|-------|
| GET | `/audiobooks` | `listAudiobooks` | Paginated. Params: limit, offset, search, author_id, series_id, sort, order |
| GET | `/audiobooks/count` | `countAudiobooks` | Returns `{count: N}` |
| GET | `/audiobooks/search` | `searchAudiobooks` | `?q=` query |
| GET | `/audiobooks/soft-deleted` | `listSoftDeletedBooks` | `?limit=&offset=&older_than_days=` |
| DELETE | `/audiobooks/purge-soft-deleted` | `purgeSoftDeletedBooks` | `?delete_files=&older_than_days=&trashed_before=`. `trashed_before` (RFC 3339) is **required**: it is the instant the preview returned, and the purge takes no book trashed after it (400 without it or with a malformed one). **Async: answers 202** with the `library.purge-soft-deleted` operation id (it answered 200 with the result before); the breakdown is the operation result (`GET /operations/:id/result`: `purged`, `skipped_owns_files`, `kept_undo_possible`, `kept_has_progress`, `carried_to_sibling`, `carry_failed`, `failed`, `stopped_early`, `stop_reason`). Refuses while the operation-journal by-book index is not built (`maintenance.opchange-book-index-rebuild`), and while any combine-journal row (`merge:combine-journal:<id>`) or auto-merge journal row cannot be decoded (fail closed; the refusal names the key, and `maintenance.combine-journal-check` lists the unreadable combine-journal rows; repair is manual). |
| POST | `/audiobooks/purge-soft-deleted/preview` | `previewPurgeSoftDeleted` | 202; runs the same operation as a dry run that classifies every trashed book and changes nothing. The result carries `trashed_before` (send it to the purge), `trashed_after_preview`, and the files a purge with `delete_files` would remove (`files_to_delete` capped, `files_to_delete_count`, `files_kept`). The result carries `library_root` (`ready`, `problem`, `hung_stats`) on every preview, so a root that is not ready is visible before the purge. The result also carries `files_waiting_for_folder`, a separate count of earlier-pass removals whose folder is missing or empty (a purge does not remove them; they are not in `files_pending_from_earlier_pass`) |
| GET | `/audiobooks/purge-soft-deleted/waiting-file-records` | `listWaitingPurgeFileRecords` | `library.delete`. The file paths the forget action would delete, so the owner can review them first: only paths whose every journal record is marked as waiting for a folder, sorted, capped at 500. `{paths, total, limit}` (`total` is the uncapped count) |
| POST | `/audiobooks/purge-soft-deleted/forget-waiting-file-records` | `forgetWaitingPurgeFileRecords` | `library.delete`. Body `{"paths": [...]}`: **exactly the paths the owner was shown** (400 for an empty list, more than 500, or a bad body). The owner's explicit choice to delete the journal records of file removals that wait for a folder that is missing or empty. Deletes **records only**, never a file or a book, and only the sent paths that are still waiting; each is re-checked first, and a path whose file is back, whose folder holds files again, or that cannot be checked (dead mount, other stat error) is kept and named in `kept`. Refuses (503 `ROOT_NOT_READY`) while the library root is not ready, and 409 `PURGE_BUSY` when a purge holds the journal for longer than 10 seconds. Writes an audit row (`trash_forget_pending_file_records`: actor, every forgotten path, `paths_sent`, `paths_no_longer_waiting`, `paths_kept`). 200 `{records_forgotten, paths_forgotten, forgotten_paths, paths_no_longer_waiting, kept: [{path, reason}], warnings?}`; 503 (nothing deleted) when there is no activity log to record it in. `GET /audiobooks/soft-deleted` carries the counts as `files_pending_from_earlier_pass` and `files_waiting_for_folder` (each null when the journal could not be read) |
| DELETE | `/audiobooks/purge-soft-deleted/preview/:id` | `cancelPurgePreview` | `library.delete`. Cancels a purge **preview** operation (dry run) that the caller started: 404 when the id is not a `library.purge-soft-deleted` preview, 403 for another user's preview, 204 otherwise (also when it already finished). Never cancels a real purge. The dialog calls it when the owner closes it while the preview runs |
| POST | `/audiobooks/link` | `linkBooks` | 202; queues `dedup.book-merge` (versions in a group). 409 for a soft-deleted keep book or an iTunes-protected selected book. Result: `merged`, `skipped`, `no_op`, `failed`, `partial`, `error` |
| POST | `/audiobooks/combine` | `combineBooks` | **Async: answers 202** (it answered 200 with the finished result before); queues `dedup.book-combine`. 400 malformed ids, 404 missing book, 409 soft-deleted or iTunes-protected book. Result: `primary_id`, `files_moved`, `books_deleted`, `journal_id`, `operation_id` (always set on success; revert it with `POST /operations/:id/revert`), `journal_pending`, `warnings`, `message`, and on failure `failed`, `incomplete`, `error` |
| GET | `/audiobooks/:id` | `getAudiobook` | Full book with enrichment |
| PUT | `/audiobooks/:id` | `updateAudiobook` | **FULL replacement** — always pass complete object. Supports `overrides` for field provenance. |
| DELETE | `/audiobooks/:id` | `deleteAudiobook` | `?block_hash=true` to also block the hash. A hard delete of a trashed book answers 503 with code `ROOT_NOT_READY` while the library root is not ready (the batch endpoint reports the same refusal as `code: "ROOT_NOT_READY"` on the failed item) |
| POST | `/audiobooks/:id/restore` | `restoreAudiobook` | Restore soft-deleted (409 `MERGE_BUSY` while a merge or combine holds the merge lock; also on the hard delete, purge-now and discard-progress-and-purge routes) |
| GET | `/audiobooks/:id/tags` | `getBookTags` | Per-field provenance (file/fetched/override/effective) |
| GET | `/audiobooks/:id/segments` | `getBookSegments` | Physical file segments |
| GET | `/audiobooks/:id/segments/:segId/tags` | `getSegmentTags` | Tags for one segment file |

### Authors
| Method | Path | Handler |
|--------|------|---------|
| GET | `/authors` | `listAuthors` (optional `limit`, `offset`, `q`, `filter`, `sort`, `order`) |
| GET | `/authors/with-counts` | `listAuthorsWithCounts` |
| GET | `/authors/count` | `countAuthors` |
| GET | `/authors/:id/books` | `getAuthorBooks` |
| PUT | `/authors/:id` | `renameAuthor` |
| DELETE | `/authors/:id` | `deleteAuthor` |
| POST | `/authors/bulk-delete` | `bulkDeleteAuthors` |
| POST | `/authors/:id/split` | `splitCompositeAuthor` |
| POST | `/authors/:id/reclassify-narrator` | `reclassifyAsNarrator` |
| POST | `/authors/merge` | `mergeAuthors` |
| GET | `/authors/duplicates` | `getAuthorDuplicates` |
| POST | `/authors/duplicates/refresh` | `refreshAuthorDuplicates` |
| GET | `/authors/:id/aliases` | `getAuthorAliases` |
| POST | `/authors/:id/aliases` | `createAuthorAlias` |
| DELETE | `/authors/:id/aliases/:aliasId` | `deleteAuthorAlias` |

### Series
| Method | Path | Handler |
|--------|------|---------|
| GET | `/series` | `listSeries` (optional `limit`, `offset`, `q`, `filter`, `sort`, `order`) |
| GET | `/series/count` | `countSeries` |
| GET | `/series/:id/books` | `getSeriesBooks` |
| PATCH | `/series/:id` | `updateSeriesName` |
| DELETE | `/series/:id` | `deleteSeries` |
| POST | `/series/bulk-delete` | `bulkDeleteSeries` |
| POST | `/series/:id/split` | `splitSeries` |
| POST | `/series/merge` | `mergeSeriesGroup` |
| GET | `/series/duplicates` | `getSeriesDuplicates` |
| POST | `/series/duplicates/refresh` | `refreshSeriesDuplicates` |
| POST | `/series/deduplicate` | `deduplicateSeries` |
| GET | `/series/prune/preview` | `seriesPrunePreview` |
| POST | `/series/prune` | `seriesPrune` |

### Operations
| Method | Path | Handler |
|--------|------|---------|
| POST | `/scan` | `startScan` |
| POST | `/organize` | `startOrganize` |
| POST | `/transcode/:id` | `startTranscode` |
| GET | `/operations` | `listOperations` |
| GET | `/operations/active` | `getActiveOperations` |
| GET | `/operations/:id` | `getOperation` |
| DELETE | `/operations/:id` | `cancelOperation` |
| GET | `/operations/:id/logs` | `getOperationLogs` |
| GET | `/operations/:id/changes` | `getOperationChanges` |
| POST | `/operations/:id/revert` | `revertOperation` |
| POST | `/operations/clear-stale` | `clearStaleOperations` |

### iTunes
| Method | Path | Handler |
|--------|------|---------|
| POST | `/itunes/validate` | `handleITunesValidate` |
| POST | `/itunes/import` | `handleITunesImport` |
| POST | `/itunes/test-mapping` | `handleITunesTestMapping` |
| POST | `/itunes/write-back` | `handleITunesWriteBack` |
| GET | `/itunes/write-back/preview` | `handleITunesWriteBackPreview` |
| GET | `/itunes/books` | `handleITunesBooks` |
| GET | `/itunes/status` | `handleITunesLibraryStatus` |
| GET | `/itunes/import-status/:id` | `handleITunesImportStatus` |
| POST | `/itunes/import-status/bulk` | `handleITunesImportStatusBulk` |

### Metadata
| Method | Path | Handler |
|--------|------|---------|
| POST | `/audiobooks/:id/metadata` | `fetchBookMetadata` |
| POST | `/audiobooks/:id/metadata/search` | `searchMetadataForBook` |
| POST | `/audiobooks/:id/metadata/apply` | `applyMetadataCandidate` |
| POST | `/audiobooks/:id/metadata/no-match` | `markNoMatch` |
| POST | `/audiobooks/:id/write-back` | `writeBackMetadata` |
| POST | `/audiobooks/:id/extract-tracks` | `extractTrackInfo` |
| GET | `/audiobooks/:id/metadata/history` | `getBookMetadataHistory` |
| POST | `/audiobooks/:id/metadata/undo-apply` | `undoLastApply` |
| POST | `/audiobooks/:id/metadata/undo/:field` | `undoMetadataChange` |
| POST | `/audiobooks/:id/metadata/revert` | `revertToSnapshot` |
| GET | `/audiobooks/:id/versions` | `getBookCOWVersions` |
| POST | `/audiobooks/:id/versions/prune` | `pruneBookVersions` |
| GET | `/audiobooks/:id/field-states` | `getAudiobookFieldStates` |

### Version Management
| Method | Path | Handler |
|--------|------|---------|
| GET | `/audiobooks/:id/book-versions` | `getBookVersions` |
| POST | `/audiobooks/:id/link-version` | `linkBookVersion` |
| POST | `/audiobooks/:id/unlink-version` | `unlinkBookVersion` |
| POST | `/audiobooks/:id/set-primary` | `setPrimaryVersion` |
| GET | `/version-groups/:groupId` | `getVersionGroup` |

### Config & System
| Method | Path | Handler |
|--------|------|---------|
| GET | `/config` | `getConfig` |
| PUT | `/config` | `updateConfig` |
| GET | `/system/status` | `getSystemStatus` |
| GET | `/system/storage` | `getSystemStorage` |
| GET | `/system/logs` | `getSystemLogs` |
| GET | `/system/announcements` | `getAnnouncements` |
| POST | `/system/factory-reset` | `factoryReset` |
| GET | `/health` | `healthCheck` |
| GET | `/version` | `getAppVersion` |

### AI
| Method | Path | Handler |
|--------|------|---------|
| POST | `/ai/parse-filename` | `parseFilenameWithAI` |
| POST | `/ai/test-connection` | `testAIConnection` |
| POST | `/audiobooks/:id/ai-parse` | `parseAudiobookWithAI` |
| POST | `/ai/scans` | `startAIScan` |
| GET | `/ai/scans` | `listAIScans` |
| GET | `/ai/scans/:id` | `getAIScan` |
| GET | `/ai/scans/:id/results` | `getAIScanResults` |
| POST | `/ai/scans/:id/apply` | `applyAIScanResults` |
| DELETE | `/ai/scans/:id` | `deleteAIScan` |
| POST | `/ai/scans/:id/cancel` | `cancelAIScan` |

### Auth
| Method | Path | Handler |
|--------|------|---------|
| GET | `/auth/status` | `getAuthStatus` |
| POST | `/auth/setup` | `setupAdmin` |
| POST | `/auth/login` | `login` |
| GET | `/auth/me` | `getMe` |
| POST | `/auth/logout` | `logout` |
| GET | `/auth/sessions` | `listSessions` |
| DELETE | `/auth/sessions/:id` | `revokeSession` |

*(Plus ~30 more routes for filesystem, backups, blocked hashes, import paths, dedup, rename, reconcile, tasks, updates, Open Library dumps)*

---

## Frontend Architecture

### Pages (web/src/pages/)
| Page | Route | Key Features |
|------|-------|------|
| Dashboard | `/dashboard` | Stats cards, disk usage, recent ops, quick actions |
| Library | `/library` | Grid/list view, search, filters, pagination, soft-deleted section, import |
| BookDetail | `/library/:id` | Full metadata, provenance locks, fetch/AI parse, rename, versions, segments |
| Authors | `/authors` | ConfigurableTable, merge/split/alias/reclassify, action history |
| Series | `/series` | ConfigurableTable, merge/split/prune, action history |
| Review | `/review` | One workspace with five lanes, in tab order: Duplicates (layer filters, bulk keep-older/keep-newer, Clusters view, CSV export, Labels and Suspicious sub-views), Metadata, Review queue (the regroup queue), Authors & series (author and series duplicates, AI scans), Repairs (fixers, including series pruning that merges duplicate series and deletes empty ones, `reconcile.missing-files` and `dedup.split-books`). The old dedup page is gone: `/dedup`, `/dedup/labels`, `/authors/dedup` and `/books/dedup` redirect into `/review` (`web/src/pages/DedupRedirect.tsx` maps `?tab=` to a lane; `?book=` and `?band=` are kept) |
| Settings | `/settings` | 12 tabs: General, Import Paths, Metadata, AI, Storage, Advanced, Auto-Update, iTunes, Blocked Hashes, System Info, Auth, Backup |
| Operations | `/operations` | Active ops (live), history with logs/changes/revert |
| Maintenance | `/maintenance` | Background task management (enable/disable/run/interval) |
| System | `/system` | System Info, Storage, Quota, Logs tabs |
| Works | `/works` | Read-only Work entity list |
| FileBrowser | `/files` | Server filesystem browser |
| Login | `/login` | Login or first-run admin setup |

### State Management
- **useAppStore** (Zustand): theme mode, notifications, loading state
- **useOperationsStore** (Zustand): active operations polling
- **AuthContext** (React Context): user, sessions, auth flow
- **eventSourceManager**: SSE connection to `/api/events` for live updates

### API Service (web/src/services/api.ts)
~200 exported functions covering every endpoint. Key pattern:
```typescript
export async function getBooks(limit: number, offset: number): Promise<{ items: Book[], count: number }> {
  const response = await fetch(`/api/v1/audiobooks?limit=${limit}&offset=${offset}`);
  // ... error handling ...
  const data = await response.json();
  return data; // { items, count, limit, offset }
}
```

---

## Database Key Schema (PebbleDB)

```
book:<ulid>                    → Book JSON
book:path:<filepath>           → ULID (index)
book_hash:<sha256>\x00<ulid>   → (empty) one key per hash and book; also book_originalhash:, book_organizedhash:
                                 (replaced the single-valued book:hash:<sha256> → ULID, deleted by maintenance.book-hash-index-repair)
book:author:<author_id>:<ulid> → ULID (index)
book:series:<series_id>:<ulid> → ULID (index)
book_ver:<ulid>:<unix_nano>    → Book JSON snapshot (COW)
book_authors:<ulid>            → []BookAuthor JSON
book_narrators:<ulid>          → []BookNarrator JSON
tombstone:<ulid>               → Book JSON (safe deletion copy)

author:<int>                   → Author JSON
author:name:<lowercase>        → int (index)
author_tombstone:<old_id>      → canonical_id int (redirect)
author_alias:<int>             → AuthorAlias JSON

series:<int>                   → Series JSON
narrator:<int>                 → Narrator JSON
work:<ulid>                    → Work JSON

operation:<ulid>               → Operation JSON
operationlog:<op_id>:<ts>:<seq>→ OperationLog JSON
opchange:<op_id>:<change_ulid> → OperationChange JSON
opstate:<op_id>                → checkpoint bytes

metadata_state:<ulid>:<field>  → MetadataFieldState JSON
metadata_change:<ulid>:<field>:<ts> → MetadataChangeRecord JSON

bf:<segment_ulid>              → BookSegment JSON
bfs:<numeric_book_id>:<seg_id> → "1" (index)

setting:<key>                  → Setting JSON (may be AES-256-GCM encrypted)
blocked:hash:<sha256>          → DoNotImport JSON
import_path:<int>              → ImportPath JSON

u:<user_ulid>                  → User JSON
sess:<session_ulid>            → Session JSON
```

### Dedup / Embedding keyspace (within the main audiobooks.pebble DB)

```
emb:v:<entityType>:<entityID>            → embRec JSON          (embedding vector record)
emb:c:<model>:<textHash>                 → raw float32 blob      (embedding cache)
dedup:r:<id16hex>                        → DedupCandidate JSON   (candidate record)
dedup:p:<type>:<aID>:<bID>               → id16hex               (pair uniqueness index; A/B canonicalized aID < bID)
dedup:e:<entityType>:<entityID>:<id16hex>→ (empty)               (entity index — BOTH sides written, so "all candidates for entity X" is O(k))
dedup:s:<status>:<id16hex>               → (empty)               (status index, INIT-2 T4 — "all pending" without a full dedup:r: scan)
dedup:seq                                → [8]byte LE int64      (auto-increment counter)
dedup:label:<id16hex>                    → LabeledExample JSON   (labeled training dataset)
dedup:automerge:<unixNano16hex>          → AutoMergeJournalEntry JSON (auto-resolve merge journal; UnmergeAuto reversal)
```

Key format note: `<id16hex>` is a 16-character lowercase hex string encoding a signed int64 as uint64 (e.g., `fmt.Sprintf("%016x", uint64(candidateID))`). This zero-pads the key so prefix scans return rows in stable order.

**Index-maintenance invariant** (fixed 2026-07-17): `DeleteCandidate` deletes all four candidate key families in one batch — `dedup:r:`, `dedup:p:`, both `dedup:e:` entries, and `dedup:s:`. Any bulk delete/purge path MUST do the same; deleting only `dedup:r:` leaves orphaned pair/entity/status index rows that block re-emission and corrupt status-filtered counts. Status changes must move the `dedup:s:` row (delete old-status key, write new-status key) — see `UpdateCandidateStatus` / `WriteCandidateStatusIndexRow` in `embedding_store.go`.


### Decisions keyspace (internal/decisions, main audiobooks.pebble DB)

```
decision:r:<ULID>                                              → Record JSON       (append-only log; one labelled example per decision; carries IndexShape edges|members)
decision:v:<op_kind>:<subject_key>                             → Verdict JSON      (current reject state: none|active|unrejected|reopened; every rejected MaterialHash)
decision:va:<op_kind>:<subject_key>                            → ActiveRow JSON    (exists exactly while the verdict is active or reopened; Load scans this, not every verdict)
decision:pe:<type>:<lo>:<hi>:<family>:<record_id>              → (empty)           (pair-edge veto; one row per rejecting record, so un-rejecting one owner keeps a shared pair vetoed)
decision:mb:<type>:<id>:<family>:<record_id>                   → (empty)           (membership row for a rejected set over 50 entities; vetoes every pair inside it)
decision:ent:<type>:<id>:<ULID>                                → (empty)           (history of one entity)
decision:op:<op_kind>:<ULID>                                   → (empty)           (history of one op kind; export order)
```

Every variable component is escaped (`%` and `:` and control bytes become `%XX`) so an `op_kind` such as `repairs:dedup.split-books` cannot be mistaken for a delimiter. `<subject_key>` is `SubjectKey(op_kind, entity_type, ids, scope)` (sha256 hex, ids sorted and de-duplicated, length-prefixed fields; frozen by golden vectors). A record, its verdict and its veto rows are written with one `ApplyRawBatch` commit.

**Semantics**
- **Per question family.** A reject vetoes a pair for the family it was made in (`same_work`, ...). A split-books `same_work` reject also stops fragment-consolidation and folder-books from proposing the same pair; it does not stop an unrelated family. `FamilyAny` stored on an edge would veto every family, but `Record` refuses it until the never-merge override lands (a later PR); as a `PairVetoed` query it means "vetoed in any family". An unknown family is an error everywhere (fail closed), never a quiet miss.
- **Reopen never lifts the original veto.** A `reopened` verdict allows new suggestions only when the material changed; the originally rejected material hash stays blocked, so if the data flips back the proposal is still vetoed. Reopen needs a previous and a new `MaterialHash` that differ, and is refused only for a sticky reject (one that recorded no material hash) or when the new hash is one that was already rejected.
- **Only a human lifts or replaces a human reject.** Unreject requires `SourceHuman`; a non-human reject over a live human reject is refused.
- **Ids are canonicalised at every entry point.** Leading/trailing whitespace is refused; ULIDs are upper-cased; subject keys are lower-cased hex. Golden vectors are unchanged for canonical input.
- **Writers serialise per engine.** The subject lock is a package-level registry keyed by `KV.LockIdentity()` (`*PebbleStore` returns its engine handle; a wrapper that embeds the KV inherits it), so two `Store` values share it whether they were built over the same KV value or over different wrappers of one store. `LockIdentity` is on `decisions.KV` only, not on `RawKVStore`. `Load` reads one point-in-time snapshot (`database.RawSnapshotter`), so a record committing mid-load cannot be half-seen; the raw rows are collected and the snapshot closed before any id is resolved, and resolver output is canonicalised. `Iterate` walks ULID order, which can differ slightly from commit order across `Store` instances (a subject's own `History` follows `SupersedesID` and is exact).
- **Book merges are not resolved yet.** The author-tombstone resolver is provided; a caller-composed resolver over `merge.ResolveMergeSurvivor` is required before a book-merging consumer lands (until then snapshots fail open for merged-away book ids).

**Durability invariant**: `decision:` keys are never pruned and have no TTL. They are owner decisions and training data. `internal/decisions/retention_test.go` runs the prune, sweep, purge, expiry and cleanup entry points of the log, activity, undo-journal, metrics and session stores plus `resultjournal.Journal.Prune`, `AudiobookService.PurgeSoftDeletedUnattended` and `dedup.Engine.PurgeStaleCandidates`, and fails when a method of the four reflected store types matching `Prune|Sweep|Purge|DeleteExpired|Cleanup` is added without coverage. A sweep on any other type is listed by hand; plugin sweeps with their own fixed prefixes (`pcache_sweep`, the resultjournal) carry a `decision:` sentinel in their own tests. Only `internal/decisions` may build `decision:` keys (source guard).

---

## Critical Implementation Details

### UpdateBook is FULL replacement
`store.UpdateBook(id, book)` replaces ALL fields. Always fetch the complete book first, modify fields, then save. Never pass a partial `&database.Book{}`.

### Metadata Provenance System
Each book field has provenance tracking:
- **file_value**: extracted from audio file tags
- **fetched_value**: from external metadata source
- **override_value**: user manual edit
- **override_locked**: prevents auto-updates
- **effective_value**: computed from priority: override > fetched > file

### Version Groups
Books are linked via `version_group_id` (string). `is_primary_version=true` marks the preferred copy. Organized library copies are primary; iTunes originals are non-primary.

### Soft Delete
PebbleDB has no `deleted_at` column. Soft delete = set `marked_for_deletion=true` + `marked_for_deletion_at=now`, then `UpdateBook()`. `ListSoftDeletedBooks()` scans for this flag.

### Author Tombstones
When authors are merged, old IDs get tombstone entries (`author_tombstone:<old_id>` → canonical_id). `ResolveTombstoneChains()` periodically flattens chains (A→B→C becomes A→C, B→C).

### iTunes Import
- XML has ~88K tracks but only ~12K unique books (grouped by Album+Artist)
- Path mapping: `file://localhost/W:/itunes/iTunes%20Media` → `file://localhost/mnt/bigdata/books/itunes/iTunes Media`
- Two-phase: quick import (file path matching, no hashing) then hash validation (only new books)
- `linkITunesMetadata()` copies iTunes fields to existing books matched by path
- `linkAsVersion()` creates non-primary version linked to existing book's VG

### Operation Queue
Background operations run with configurable timeout (default 30min, currently set to 120min). Support checkpoint/resume for interrupted operations. Progress reported via `log.UpdateProgress(current, total, message)` which maps to the operation's `progress`, `total`, `message` fields.

### Naming Patterns
- Folder: `{author}/{series}/{title} ({print_year})`
- File: `{title} - {author} - read by {narrator}`
- Configurable in settings

### Metrics
`/metrics` (Prometheus text, default registry) is the primary metrics surface and must stay compatible (owner decision D66). New instruments are OTel: get a meter with `telemetry.Meter("<area>")` (`internal/telemetry/meter.go`); never add a `client_golang` constructor. Naming (spec `docs/proposals/2026-10-holistic/11-metrics-strategy-otel-prometheus.md` §3.1): dotted lowercase names prefixed `audiobook_organizer.` (`ai_dispatch.` and `ai.` are the only unprefixed exceptions we create; otelgin's `http_server_*` families are third-party and also unprefixed); counters never end in `_total` (the exporter adds it); histograms set unit `s` or `By` and need an entry in the views table `internal/telemetry/views.go`; never unit `"1"` (it appends `_ratio`); attribute keys come from `AttributeKeys()` in `internal/telemetry/attr.go` (the legacy `op_id` and otelgin's semconv keys are scrape-only and rejected on new instruments), with closed, small value sets (no ids, paths or free text). Scope labels are off (`prometheus.WithoutScopeInfo()`). The series contract `internal/telemetry/contract/testdata/series.golden` pins the name, type and label names of every family. `TestSeriesContract` checks it against a seeded `/metrics` scrape (missing, retyped, relabelled or unlisted families fail), and `TestDeclaredFamiliesInGolden` parses every `client_golang` constructor and OTel instrument call under `internal/`, `pkg/` and `cmd/`, so a new family fails even if nothing seeds it; families from third-party code (otelgin) are seen only through seeding. `testdata/histogram_buckets.golden` pins every histogram's `le` text independently of `views.go`. Adding, renaming or removing a family means editing the golden(s) and the test's `seedingTable` in the same PR, plus a recording rule if a dashboard or alert reads the old name. `series_reserved.txt` holds names reserved for a PR that has not landed; it is empty now that the AI families are in the golden.

The `client_golang` ratchet is `TestClientGolangConstructorRatchet` in `internal/telemetry/ratchet_test.go`. It parses non-test Go under `internal/`, `pkg/` and `cmd/` and counts `prometheus`/`promauto` constructors, `newPebbleDesc("literal", ...)` calls and stray `prometheus.NewDesc` calls, and fails when the count rises above the number in `internal/telemetry/testdata/client_golang_baseline.txt`. It runs in `go test ./...` on every runner, so there is no separate CI step. To lower it, edit that number down in the PR that migrates a family to OTel (`go test -count=1 -v -run TestClientGolangConstructorRatchet ./internal/telemetry/` prints the current count); never raise it.

#### AI call metrics and traces

Every direct AI request (the seven chat sites in `openai_parser.go`, `scoreMetadataBatch` in `metadata_llm_review.go`, and the per-attempt embeddings call in `embedding_client.go`) goes through `observe` in `internal/ai/telemetry.go`, which owns the span, the timer and the counters. Five OTel instruments on `telemetry.Meter("ai")` (`internal/ai/ai_metrics.go`) appear on the existing `/metrics` scrape; the `ai_` names stay unprefixed, like `ai_dispatch_*`.

| Series | Labels | Meaning |
|---|---|---|
| `ai_requests_total` | `provider`, `model`, `task`, `outcome` | One per HTTP attempt. A retry is a new attempt whoever retries: the retry layer (`DoWithRetry`) or the SDK's own two retries, which every constructor except the routed parser keeps and which `attemptMiddleware` records (it is on every parser client; the embedding clients build their own and disable SDK retries per request with `WithMaxRetries(0)`, so they have no absorbed attempt to record), so a 429 or 5xx the SDK absorbs is counted as an `error` request with its `ai_failures_total` reason. If the caller's context ends while the SDK waits to retry, that last response is still what is counted (an `error` request and, for a 429, `rate_limit`), not a `timeout` or `canceled` one. `outcome` is `ok`, `error`, `timeout` or `canceled`. |
| `ai_request_duration_seconds` | `provider`, `model`, `task` | Wall time of one HTTP attempt (to the end of the reply for the final attempt), excluding the SDK's retry backoff. Buckets come from the view in `internal/telemetry/views.go`. |
| `ai_tokens_total` | `provider`, `model`, `task`, `direction` | `direction` is `input` or `output`, from the usage block the provider returned. A reply with all-zero usage (some local servers omit it) records nothing. Embeddings record `input` only. |
| `ai_failures_total` | `provider`, `model`, `task`, `reason` | `reason` is `rate_limit`, `quota`, `unreachable`, `timeout`, `permanent`, `reply_parse`, `count_mismatch` or `other`. |
| `ai_parse_results_total` | `task`, `accepted` | One per item of a scan-phase batch parse at its final disposition: `accepted="true"` when the model's result had a field merged into the book, `"false"` for a nil result, a result that merged nothing, or an item the batch could not resolve. Usable, not high-confidence: nothing compares `ParsedMetadata.Confidence`. |

Closed enums. `task` is one of `filename_parse`, `audiobook_parse`, `batch_parse`, `cover_art`, `cover_text`, `author_dedup_review`, `author_discovery`, `metadata_review`, `embed`. `provider` is `openai` (base URL empty or the OpenAI host; an empty base URL also consults `OPENAI_BASE_URL`, as the SDK does) or `local`. `model` is the model string sent when that model is **configured in settings**, and `other` for every other model. The configured models are the (provider, model) pairs the settings make a call send: the embedding model (in local embedding mode `ai_backend.local_embedding_model`, then `embedding.model`, then the client default `text-embedding-3-large`; in `openai` and `openai-fallback-local` mode `embedding.model`, or that same default when empty, and only while an OpenAI key is set: those modes never read `local_embedding_model`); the per-feature OpenAI models `filename_parse_model`, `metadata_review_model` and `cover_art_model`, each falling back to the parser default; the local parsers' models (with `llm_mode` `local`: `filename_parse_model` for the scanner's local parser, only with routing off, and `metadata_review_model` for the metadata/author-review parser, each falling back to `local_llm_model`, and to the parser default when that is empty, because that default is what is then sent to the local backend; with `openai-fallback-local` and routing off, only the scanner's local rung, which is skipped when `local_llm_model` is empty and otherwise names `filename_parse_model`, falling back to `local_llm_model`); and, with `ai_endpoints_routing` on, the models of the pool rows the dispatcher would hand a routed call to. Pool rows are asked through the dispatcher itself: only `embed.text` (local embedding mode, pinned to the client's embedding model), `llm.filename_parse` (llm_mode `local` or `openai-fallback-local`; local rows only in `local`) and `llm.cover_art_vision` (local rows, then cloud) have a routed call site, and a row is named for one of them only if it is enabled, ticks the capability, has the protocol, features, GPU label and model the dispatcher requires, is the right locality for that call, and has a secret for its `auth_ref`. With routing on the legacy local pairs the pool replaces are not named. `dedup.review_model` is not named: dedup submits its review through the Batch API (`ai.SubmitDedupReviewJob`), which records no `ai_*` metric. Each pair carries the provider its call site resolves from its base URL, so the same name under `openai` and `local` is two pairs. A model that is empty, longer than 64 characters or containing whitespace is `other` too (there is no `unknown` label value; the span still says `unknown`). The named set is a function of the settings, not of which call arrives first: `internal/ai` fills it from `config.Snapshot` on first use and again whenever `config.Generation` moves (the startup load, `PUT /config`, the update service rollback), so the same settings name the same models after every restart, and a model an admin only tried without saving never takes a name. It is bounded to 5 pairs per process life (`maxModelLabels`, `modelLabelSet` in `ai_metrics.go`; `configuredModelPairs` in `ai_metrics_models.go` fixes the priority when settings hold more: the embedding model, then with routing on the pool's models for filename parse and cover text in dispatcher order, then the OpenAI per-feature models, then the local parsers' models). Names are sticky: after a settings change the model it replaced keeps its name until restart, because the OTel series stays exported until then and a series cannot be un-counted against the cap. So a model configured after 5 pairs were already named in this process is reported as `other` and logs `ai metrics: configured model cannot be named`; restarting names exactly the configured set. The histogram exports 15 series per (provider, model, task), so the worst case is (5 named + 2 `other`, one per provider) x 9 tasks x 15 = 945, under the 1,000-series cap; `TestSeriesCardinalityBound` pins it, including an empty model and settings changes past the cap.

- A reply that arrives but cannot be used (`*ReplyParseError`, `*ResultCountError`) is an `ok` request plus an `ai_failures_total` increment (`reply_parse` or `count_mismatch`), recorded where the error is returned. It is the one case where a failure has no matching `outcome="error"` request. The alert and the dashboard's failure-ratio panel exclude those two reasons; a separate panel shows them.
- A cancellation is `outcome="canceled"` and no failure; a deadline is `outcome="timeout"` with `reason="timeout"`.
- Batch-API submissions (`embedding_batch.go`, `openai_batch.go`) and `internal/server/bench.go` are not wrapped, and the tokens of a batch job arrive in its result file, so they are not recorded. Pool-routed calls reach the same wrapped SDK sites, so they are counted here as well as in `ai_dispatch_*`, which measures the routing layer.
- `endpoint` and `node` are deliberately not labels: the pool is growable and the histogram multiplies every label by its bucket count. The pool endpoint id, which a pool-routed parser knows, is a span attribute (`ai.endpoint_id`) only. No filename, prompt, reply or title is ever a label or span attribute.
- Spans are named `ai.<task>`, nest under the caller's context, and carry `ai.provider`, `ai.model` (the real model name; only the metric label is capped), `ai.task`, `ai.endpoint_id` (routed parsers), the token counts and, when the SDK retried, `ai.http_attempts`. A failed call sets the span status to Error with `error=true` and only a classification (`ai.outcome`, `ai.failure_reason`, `http.response.status_code`); the error text is never recorded, because it can carry the endpoint URL and the provider's response body. A cancellation leaves the span status alone. A reply failure is an `ai.reply_failure` event on the caller's span, never an error status, since the batch split recovers a count mismatch by design.
- Dashboard: `deploy/grafana/dashboards/audiobook-organizer-ai.json`. Alert: `AudiobookOrganizerAIFailureRatioHigh` in `deploy/prometheus/alert-rules.yml`.

---

## Common Gotchas

1. **`contains` helper** in error_handler_test.go — don't redeclare in same package
2. **`intPtr` helper** in itunes.go — don't redeclare in test files
3. **Parallel Go tests** can be flaky due to global state (`GlobalStore`, `GlobalQueue`)
4. **Book.AuthorID** (int, legacy single-author) coexists with `BookAuthor` junction table (multi-author)
5. **BookSegment.BookID** is a numeric int, not a ULID — legacy inconsistency
6. **Self-narrating authors** (Neil Gaiman, etc.) — don't reclassify as narrators
7. **Series count should NEVER exceed book count** — indicates duplicates needing merge/prune
8. **Production is Linux**, not macOS — don't suggest Mac-specific tools
9. **iTunes is on Windows**, NOT Mac — write to .itl binary files, no AppleScript
10. **Real librivox M4B files**: composer overrides artist in metadata extraction
11. **`UsedFilenameFallback`** is true whenever ANY field filled from filename, even if main tags present
12. **Pagination limit** capped at 10,000 in `ParsePaginationParams()`

---

## File Header Convention

All files require versioned headers. Bump version on every change:
```go
// file: internal/server/itunes.go
// version: 2.11.1
// guid: 719912e9-7b5f-48e1-afa6-1b0b7f57c2fa
```

---

## Diagrams

All diagrams are in `docs/diagrams/` in both Mermaid (`.mmd`) and Graphviz DOT (`.dot`) format:

| Diagram | Files | Description |
|---------|-------|-------------|
| Entity-Relationship | `entity-relationship.mmd`, `.dot` | All entities, fields, and relationships |
| Component Diagram | `component-diagram.mmd`, `.dot` | System architecture: frontend, backend, services, storage |
| iTunes Import Flow | `flow-itunes-import.mmd` | Two-phase import: quick + hash validation |
| Book Scan Flow | `flow-book-scan.mmd` | Directory scan → metadata extraction → dedup → save |
| Organize Flow | `flow-organize.mmd` | File moves, renames, tag writing |
| Metadata Fetch Flow | `flow-metadata-fetch.mmd` | Multi-source search → apply with provenance |
| AI Dedup Flow | `flow-ai-dedup.mmd` | Multi-model scan → cross-validation → apply (AUTHOR dedup via OpenAI batch API) |
| Book Dedup Candidate Lifecycle | `flow-book-dedup.mmd` | Scan → exact/LSH emitters (gated by `hasPlausibleAudio`) → candidate → backfill op → labeled dataset → (future) review UI / JSONL export |

Render Mermaid: `mmdc -i file.mmd -o file.svg`
Render DOT: `dot -Tsvg file.dot -o file.svg`
