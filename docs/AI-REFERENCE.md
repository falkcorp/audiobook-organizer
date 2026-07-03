<!-- file: docs/AI-REFERENCE.md -->
<!-- version: 1.0.0 -->
<!-- guid: e5f4g3h2-i1j0-k9l8-m7n6-o5p4q3r2s1t0 -->
<!-- last-edited: 2026-07-03 -->

# AI Reference Guide — Audiobook Organizer

> **Purpose**: Complete project reference for AI agents. Read this before making any changes.
> Keep this file updated with every architectural change.

**Last updated**: 2026-07-01 | **Server version**: 1.117.0 | **Total API routes**: see `grep -rn '\.GET(\|\.POST(\|\.PUT(\|\.DELETE(\|\.PATCH(' --include='*.go' internal/server | grep -v _test.go | wc -l` for current count (~395 as of 2026-07-03)

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
- **pebble_store.go** — Primary implementation. Key schema: `book:<ulid>`, `book:path:<filepath>`, `book:hash:<hash>`, `author:<id>`, `series:<id>`, etc.
- **embedding_store.go** — `EmbeddingStore` wrapping the main audiobooks.pebble DB for embeddings + dedup candidates + labeled dataset. Key-space: `emb:v:*`, `emb:c:*`, `dedup:r:*`, `dedup:p:*`, `dedup:seq`, `dedup:label:*`.
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

### `internal/operations` — Background job queue
- **queue.go** — `OperationQueue` with timeout (configurable, default 30min), checkpoint/resume support, cancellation
- Key: `Enqueue(id, type, folderPath, fn)`, `SetOperationTimeout()`, `SaveCheckpoint()`, `LoadCheckpoint()`

### `internal/config` — Configuration
- **config.go** — `Config` struct with 100+ fields. Global: `config.AppConfig`
- Key settings: `RootDir`, `DatabasePath`, `DatabaseType`, `FolderNamingPattern`, `FileNamingPattern`, `ConcurrentScans`, `OperationTimeoutMinutes`, `MetadataSources`, `ITunesPathMappings`

### `internal/logger` — Logging
- **operation.go** — `OperationLogger` wraps operation progress reporting
- Interface: `UpdateProgress(current, total, message)`, `Info()`, `Warn()`, `Error()`, `IsCanceled()`

### `internal/cache` — Generic cache
- **cache.go** — `Cache[T]` with TTL. Thread-safe.

### `internal/backup` — Backup/restore
- **backup.go** — `CreateBackup()`, `ListBackups()`, `RestoreBackup()`

### `internal/models` — Legacy models
- **audiobook.go** — `Audiobook` struct (mostly superseded by `database.Book`)

### `internal/dedup` — Book deduplication engine
- **engine.go** — `Engine` type; per-layer candidate emitters (`checkExactTitle`, `checkExactISBN`, `checkExactFileHash`, `checkExactMetadataSourceHash`, `checkDurationMatch`; the AcoustID/LSH/embedding/metadata-fuzzy collectors live in `collectors_*.go`); `PairEligibility` pre-filter; `hasPlausibleAudio(book) bool` gate (returns true when `Duration > 0` OR `FileSize >= 256 KiB`). The exact-title and exact-ISBN emitters call this gate for both sides before emitting a candidate — prevents stub/unscanned books from creating false-positive pairs.
- **dataset/** (sub-package) — Pure builder + deterministic catchers for the labeled training dataset. No side effects.
  - `BuilderStore` interface: `GetBook(id string)`, `GetBookFiles(id string)`
  - `BuildExample(BuilderStore, DedupCandidate) (LabeledExample, error)` — loads both books, computes `DurationRatio`, `FolderRelation`, `SharesRecordingID`, `SignatureRelation`. Label fields left empty; caller must run `Classify`.
  - `Classify(LabeledExample) (label, reason string, fires bool)` — three catchers in priority order: `wholeBookSignatureMatch` (→ `true_dup`; both sigs present + similarity ≥ 0.95), `missingFile` (→ `not_dup`), `partVsWhole` (→ `not_dup`; ratio < 0.5 with both durations known). Returns `("","",false)` when no catcher fires.
  - Note: `BookFeatures.Author` is left empty — author name resolution would require an additional store method not in `BuilderStore`.

### `internal/plugins/dedup` — Dedup plugin operations
- **dataset_backfill.go** — `dedup.dataset-backfill` UOS op. Iterates all pending candidates, calls `dataset.BuildExample` + `dataset.Classify`, writes `LabeledExample` to `dedup:label:` keyspace. With `apply=true`, dismisses candidates labeled `not_dup` by a catcher. Dry-run by default. Idempotent.
  - `builderAdapter` bridges `database.Store.GetBookByID` → `BuilderStore.GetBook` (name mismatch bridged without touching either interface).
  - Known limitation: pairs where one side has `Duration=0` but file records exist are NOT caught by the current catchers — they are left unlabeled for human/ML review. The engine gate stops NEW such pairs from being created; existing residual pairs need a future FileSize-aware catcher.

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
| DELETE | `/audiobooks/purge-soft-deleted` | `purgeSoftDeletedBooks` | `?delete_files=&older_than_days=` |
| GET | `/audiobooks/:id` | `getAudiobook` | Full book with enrichment |
| PUT | `/audiobooks/:id` | `updateAudiobook` | **FULL replacement** — always pass complete object. Supports `overrides` for field provenance. |
| DELETE | `/audiobooks/:id` | `deleteAudiobook` | `?block_hash=true` to also block the hash |
| POST | `/audiobooks/:id/restore` | `restoreAudiobook` | Restore soft-deleted |
| GET | `/audiobooks/:id/tags` | `getBookTags` | Per-field provenance (file/fetched/override/effective) |
| GET | `/audiobooks/:id/segments` | `getBookSegments` | Physical file segments |
| GET | `/audiobooks/:id/segments/:segId/tags` | `getSegmentTags` | Tags for one segment file |

### Authors
| Method | Path | Handler |
|--------|------|---------|
| GET | `/authors` | `listAuthors` |
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
| GET | `/series` | `listSeries` |
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
| DELETE | `/operations/history` | `deleteOperationHistory` |
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
| POST | `/itunes/sync` | `handleITunesSync` |
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
| POST | `/metadata/bulk-fetch` | `bulkFetchMetadata` |
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
| BookDedup | `/dedup` | Tabs: Author dedup (AI scan pipeline), Series dedup, Book dedup |
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
book:hash:<sha256>             → ULID (index)
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
emb:v:<entityType>:<entityID>  → embRec JSON          (embedding vector record)
emb:c:<model>:<textHash>       → raw float32 blob      (embedding cache)
dedup:r:<id16hex>              → DedupCandidate JSON   (candidate record)
dedup:p:<type>:<aID>:<bID>     → id16hex               (pair uniqueness index)
dedup:seq                      → [8]byte LE int64      (auto-increment counter)
dedup:label:<id16hex>          → LabeledExample JSON   (labeled training dataset)
```

Key format note: `<id16hex>` is a 16-character lowercase hex string encoding a signed int64 as uint64 (e.g., `fmt.Sprintf("%016x", uint64(candidateID))`). This zero-pads the key so prefix scans return rows in stable order.

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
