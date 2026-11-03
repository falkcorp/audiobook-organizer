<!-- file: docs/plans/platform-core/raw/02-import-graph-hotspots.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c1a1ed7-f8ea-4a88-b727-a980e9322668 -->
<!-- last-edited: 2026-11-03 -->

# 02 - Import-graph coupling hotspots

Anchors are at commit 748fd787a (the checked-out tree adds only docs/plans/platform-core/STATE.md). Package classes (generic / audiobook-specific / mixed) are the ones assigned in raw/01-package-inventory.md. All counts are non-test imports under default tags (the same graph `internal/arch/layering_test.go:339-381` loads). "Generic-looking" below means a package whose name or role suggests platform infrastructure (logger, config, database, operations, scheduler, auth, realtime, metrics, ai, server infra, ...).

## Headline findings

1. **`internal/database` is the keystone coupling.** It is both the generic persistence layer and the audiobook entity model: `type Book struct` is at `internal/database/store.go:202`, the `Store` interface (`store.go:48`) embeds six groups, the first of which is `catalogStore` = Book/Author/Series/Narrator/Work/BookVersion/Collection stores (`store.go:57-66`). 104 packages import it (61 audiobook-specific, 37 mixed, 6 generic), so every infrastructure package that takes a store inherits the book schema.
2. **`database` itself imports 7 audiobook-specific packages directly (8 transitively) plus `models`**: `fingerprint` (9 files, e.g. `internal/database/pebble_store.go:31`, `pebble_store_lsh.go:38`), `matcher` (`pebble_store.go:33`), `titleutil` (`pebble_store.go:35`), `personname` (`pebble_store_authors.go:21`), `metastate` (`metadata_field_locks.go:15`), `chaptershape` (`book_own_folder.go:15`), `syncapi/progress` (`pebble_store_bookmarks.go:39`), and `models` (`embedding_store.go:22`); the 8th, `audioutil`, comes in via `fingerprint`. This is why **every** package that imports `database` (auth, oauth, registry, pkg/ops, pkg/plugin/sdk ...) has a non-empty audiobook-specific transitive closure (table in section (a3)).
3. **`internal/config` (layer 1) imports `database`, `auth`, `backup`, `aidispatch`, `dedup/unified`, `audioext`** (`internal/config/ai_endpoints.go:20`, `config.go:23`, `dedup_score_config.go:11`, `update_service.go:22`); five of these are on the `allowed` wrong-way list (`internal/arch/layering_test.go:291-295`). Config cannot be extracted without cutting these.
4. **`pkg/` is not a clean public boundary**: `pkg/ops` imports `internal/auth` and `internal/database` (`pkg/ops/approval.go:11`, `pkg/ops/writer.go:11`) and its `Writer` interface speaks `database.Book`/`BookFile`/`BookAuthor` (`pkg/ops/writer.go:49-59`); `pkg/plugin/sdk` imports `database` and `operations/registry` and hands `database.Book` to external plugins (`pkg/plugin/sdk/iterate.go:12,18`). `Makefile:541` (`sdkguard`) only asserts no *new* internal deps beyond an allow-list (`tools/cmd/sdkguard/main.go:77-80`: registry, auth, database, serviceregistry).
5. **`internal/models` is almost dead as a shared layer**: only 4 non-test importers; its book types (`Audiobook`, `Author`, `Series`, list/update requests) are referenced only by `internal/itunes/import.go:361,394` (`Audiobook`) and its own tests; the live content is the dedup score type family (section (b)). The real book entity is `database.Book`, not `models.*`.
6. **`internal/scheduler` (layer 4) has 80 transitive deps, 39 audiobook-specific**, because `tasks.go:21,24` and `extra_ops.go:25-26` import `dedup`, `scanner`, `metabatch`, `metafetch` directly; `internal/opscatalog` (`catalog.go:28-33`) hard-lists the five audiobook plugin bundles.
7. **Leaf infrastructure is clean**: `logger` (no in-module imports), `logging`, `telemetry`, `realtime`, `serviceregistry`, `util`, `pathutil`, `operations/state`, `mail`, `mtls`, `security/*` have empty closures (except small content leaks listed in (a2)). These are extractable as-is or with one-line changes.

## (a) Generic-looking packages coupled to audiobook domain

### (a1) Per-package summary and edges

Edge = a non-test import of a package classified `audiobook-specific` in raw/01. Identifier uses = non-comment lines naming `database.`/`models.` types with the audiobook prefixes (Book*, Author*, Series*, Narrator*, Chapter*, Playlist*, ITunes*, Dedup*, Work*, Collection*, Credit*, Master*, Scan*, Retired*, Metadata*, ExternalID*, Playback*, Bookmark*, UserBook*, Fingerprint*). Only packages with at least one edge or use are listed; packages not listed (logger, logging, metrics, telemetry, realtime, auth, oauth, plugin, cache, httputil, util, backup, updater, mail, tools, serviceregistry, watcher, appdirs, ai/aijobs, ai/resultjournal, operations, operations/state, operations/opmode, operations/freshness, operations/childop, opsmetrics, compactprogress, work-excluded) have no such edge or use in their own source; some still reach audiobook packages transitively via `database` (see (a3)).

| generic-looking package | layer | import edges to audiobook-specific pkgs | audiobook `database.*`/`models.*` identifier uses (non-comment lines) |
|---|---|---|---|
| `internal/config` | 1 | 3 | 0 () |
| `internal/operations/opswriter` | 3 | 0 | 36 (database.MetadataChangeRecord, database.BookAuthor, database.BookFile, database.Book) |
| `internal/operations/registry` | 4 | 0 | 13 (database.Book, database.WorkerProgress) |
| `internal/opscatalog` | 4 | 4 | 0 () |
| `internal/scheduler` | 4 | 4 | 0 () |
| `internal/ai` | 3 | 1 | 3 (database.DedupCandidate) |
| `internal/aiscan` | 3 | 2 | 63 (database.ScanSuggestion, database.Author, database.ScanResult, database.Scan) |
| `internal/activity` | 3 | 1 | 4 (database.MetadataChangeRecord, database.BookPathChange) |
| `internal/maintenance` | 4 | 0 | 38 (database.BookFile, database.Book, database.BookAuthor, database.Author) |
| `internal/work` | 3 | 0 | 10 (database.Work, database.WorkStore) |
| `internal/search` | 2 | 0 | 29 (database.UserBookState, database.Author, database.Series, database.Book) |
| `internal/sysinfo` | 3 | 0 | 1 (database.Playlist) |
| `internal/sweep` | 3 | 0 | 4 (database.Book, database.BookCore) |
| `internal/decisions` | 3 | 0 | 4 (database.Book, database.BookVersion, database.BookFile, database.BookVersionStatusRetired) |
| `internal/diagnostics` | 3 | 1 | 13 (database.BookCore, database.Author, database.Series) |
| `internal/fileops` | 3 | 2 | 7 (database.BookFileHashUpdater, database.BookFile, database.BookFileByPathIncludingRetired) |
| `internal/testutil` | 4 | 3 | 0 () |
| `internal/server` | 5 | 221 | 296 (database.Book, database.BookCore, database.Author, database.BookFile) |
| `internal/server/middleware` | 5 | 2 | 0 () |
| `internal/server/handlers` | 5 | 34 | 249 (database.Book, database.Collection, database.BookFile, database.CollectionTypeDynamic) |
| `internal/server/handlers/system` | 5 | 4 | 3 (database.BookCore, database.Author) |
| `internal/server/handlers/opsv3` | 5 | 1 | 3 (database.WorkerProgress) |
| `internal/server/handlers/opaccess` | 5 | 5 | 0 () |
| `internal/server/handlers/operations` | 5 | 2 | 15 (database.BookAuthor, database.BookNarrator, database.Author, database.Book) |
| `internal/server/handlers/admindebug` | 5 | 1 | 21 (database.BookFile, database.Book, database.BookFilesIncludingRetired, database.BookFileByFileIDReader) |
| `pkg/ops` | 3 | 0 | 5 (database.BookAuthor, database.Book, database.BookFile, database.BookFileLocation) |
| `pkg/ops/opstest` | 4 | 0 | 56 (database.Book, database.BookFile, database.BookAuthor, database.MetadataChangeRecord) |
| `pkg/plugin/sdk` | 4 | 0 | 2 (database.Book) |
| `internal/database` | 2 | 17 | 0 () |

### Edge list (importer file:line -> imported audiobook-specific package)

| importer pkg | file:line | imports |
|---|---|---|
| `internal/config` | `internal/config/config.go:23` | `internal/audioext` |
| `internal/config` | `internal/config/dedup_score_config.go:11` | `internal/dedup/unified` |
| `internal/config` | `internal/config/update_service.go:22` | `internal/dedup/unified` |
| `internal/opscatalog` | `internal/opscatalog/catalog.go:28` | `internal/plugins/acoustid` |
| `internal/opscatalog` | `internal/opscatalog/catalog.go:29` | `internal/plugins/dedup` |
| `internal/opscatalog` | `internal/opscatalog/catalog.go:31` | `internal/plugins/itunes` |
| `internal/opscatalog` | `internal/opscatalog/catalog.go:32` | `internal/plugins/metafetch` |
| `internal/scheduler` | `internal/scheduler/extra_ops.go:25` | `internal/metabatch` |
| `internal/scheduler` | `internal/scheduler/extra_ops.go:26` | `internal/metafetch` |
| `internal/scheduler` | `internal/scheduler/tasks.go:21` | `internal/dedup` |
| `internal/scheduler` | `internal/scheduler/tasks.go:24` | `internal/scanner` |
| `internal/ai` | `internal/ai/cover_text.go:20` | `internal/covertext` |
| `internal/aiscan` | `internal/aiscan/keep.go:12` | `internal/dedup` |
| `internal/aiscan` | `internal/aiscan/pipeline.go:20` | `internal/dedup` |
| `internal/activity` | `internal/activity/changelog.go:17` | `internal/undo` |
| `internal/diagnostics` | `internal/diagnostics/service.go:18` | `internal/itunes` |
| `internal/fileops` | `internal/fileops/book_file_hash_recorder.go:12` | `internal/filehash` |
| `internal/fileops` | `internal/fileops/write_tags_safe.go:21` | `internal/filehash` |
| `internal/testutil` | `internal/testutil/integration.go:17` | `internal/metadata` |
| `internal/testutil` | `internal/testutil/integration.go:19` | `internal/scanner` |
| `internal/testutil` | `internal/testutil/itunes_helpers.go:13` | `internal/itunes` |
| `internal/server` | `internal/server/abs_session_backfill.go:13` | `internal/server/absauth` |
| `internal/server` | `internal/server/ai_author_reassign.go:11` | `internal/authormerge` |
| `internal/server` | `internal/server/ai_ops.go:19` | `internal/authormerge` |
| `internal/server` | `internal/server/ai_ops.go:22` | `internal/dedup` |
| `internal/server` | `internal/server/ai_scan_apply_op.go:30` | `internal/server/handlers/entities` |
| `internal/server` | `internal/server/ai_scan_apply_op.go:31` | `internal/undo` |
| `internal/server` | `internal/server/apply_when_scanned_op.go:15` | `internal/applygate` |
| `internal/server` | `internal/server/apply_when_scanned_op.go:20` | `internal/metafetch` |
| `internal/server` | `internal/server/apply_when_scanned_op.go:23` | `internal/scanlock` |
| `internal/server` | `internal/server/apply_when_scanned_op.go:24` | `internal/server/handlers/metadata` |
| `internal/server` | `internal/server/apply_when_scanned_op.go:25` | `internal/tagger` |
| `internal/server` | `internal/server/audio_sample.go:14` | `internal/audio` |
| `internal/server` | `internal/server/audiobooks_compat.go:14` | `internal/audiobooks` |
| `internal/server` | `internal/server/audiobooks_helpers.go:42` | `internal/audiobooks` |
| `internal/server` | `internal/server/audiobooks_helpers.go:45` | `internal/fingerprint` |
| `internal/server` | `internal/server/auth_accept_invite.go:19` | `internal/server/absauth` |
| `internal/server` | `internal/server/author_reclassify_op.go:27` | `internal/server/handlers/entities` |
| `internal/server` | `internal/server/authority_evidence.go:14` | `internal/authorcredit` |
| `internal/server` | `internal/server/authority_evidence.go:15` | `internal/authority` |
| `internal/server` | `internal/server/batch_apply_claims.go:17` | `internal/applygate` |
| `internal/server` | `internal/server/batch_apply_claims.go:19` | `internal/metafetch` |
| `internal/server` | `internal/server/batch_apply_log.go:17` | `internal/metafetch` |
| `internal/server` | `internal/server/batch_apply_one.go:17` | `internal/applygate` |
| `internal/server` | `internal/server/batch_apply_one.go:18` | `internal/authority` |
| `internal/server` | `internal/server/batch_apply_one.go:22` | `internal/metabatch` |
| `internal/server` | `internal/server/batch_apply_one.go:23` | `internal/metafetch` |
| `internal/server` | `internal/server/batch_apply_one.go:24` | `internal/tagger` |
| `internal/server` | `internal/server/batch_apply_op.go:27` | `internal/metafetch` |
| `internal/server` | `internal/server/batch_apply_refusal.go:25` | `internal/applygate` |
| `internal/server` | `internal/server/batch_apply_refusal.go:28` | `internal/metafetch` |
| `internal/server` | `internal/server/batch_save_op.go:24` | `internal/organizer` |
| `internal/server` | `internal/server/batch_save_op.go:25` | `internal/tagger` |
| `internal/server` | `internal/server/bootstrap.go:32` | `internal/server/absauth` |
| `internal/server` | `internal/server/bulk_apply_preview.go:40` | `internal/applygate` |
| `internal/server` | `internal/server/bulk_apply_preview.go:44` | `internal/metafetch` |
| `internal/server` | `internal/server/candidate_fallback.go:28` | `internal/applygate` |
| `internal/server` | `internal/server/candidate_fallback.go:32` | `internal/metabatch` |
| `internal/server` | `internal/server/candidate_fallback.go:33` | `internal/metadata` |
| `internal/server` | `internal/server/candidate_fallback.go:35` | `internal/metafetch` |
| `internal/server` | `internal/server/candidate_feedback.go:21` | `internal/metafetch` |
| `internal/server` | `internal/server/candidate_refetch.go:16` | `internal/metabatch` |
| `internal/server` | `internal/server/candidate_refetch.go:17` | `internal/metafetch` |
| `internal/server` | `internal/server/candidate_refetch.go:18` | `internal/plugins/maintenance` |
| `internal/server` | `internal/server/catalog_coverage_op.go:29` | `internal/authorjunk` |
| `internal/server` | `internal/server/catalog_harvest_op.go:22` | `internal/authority` |
| `internal/server` | `internal/server/catalog_harvest_op.go:23` | `internal/catalog` |
| `internal/server` | `internal/server/catalog_harvest_op.go:27` | `internal/metadata` |
| `internal/server` | `internal/server/catalog_stage_wire.go:9` | `internal/authority` |
| `internal/server` | `internal/server/catalog_stage_wire.go:10` | `internal/catalog` |
| `internal/server` | `internal/server/catalog_stage_wire.go:14` | `internal/metafetch` |
| `internal/server` | `internal/server/cover_text_handler.go:11` | `internal/covertext` |
| `internal/server` | `internal/server/covers.go:18` | `internal/covers` |
| `internal/server` | `internal/server/csp_report.go:27` | `internal/server/absauth` |
| `internal/server` | `internal/server/deluge_discovery.go:23` | `internal/importer` |
| `internal/server` | `internal/server/deluge_discovery.go:25` | `internal/tagger` |
| `internal/server` | `internal/server/deluge_discovery.go:26` | `internal/versions` |
| `internal/server` | `internal/server/duplicates_helpers.go:39` | `internal/dedup` |
| `internal/server` | `internal/server/duplicates_helpers.go:41` | `internal/metadata` |
| `internal/server` | `internal/server/duplicates_helpers.go:43` | `internal/undo` |
| `internal/server` | `internal/server/duplicates_ops.go:41` | `internal/dedup` |
| `internal/server` | `internal/server/duplicates_ops.go:44` | `internal/merge` |
| `internal/server` | `internal/server/duplicates_ops.go:47` | `internal/organizer` |
| `internal/server` | `internal/server/embedding_backfill.go:15` | `internal/dedup` |
| `internal/server` | `internal/server/entities_ops.go:23` | `internal/authormerge` |
| `internal/server` | `internal/server/entities_ops.go:26` | `internal/dedup` |
| `internal/server` | `internal/server/entities_ops.go:28` | `internal/metadata` |
| `internal/server` | `internal/server/entities_ops.go:30` | `internal/personname` |
| `internal/server` | `internal/server/entities_ops.go:31` | `internal/server/handlers/entities` |
| `internal/server` | `internal/server/external_id_backfill.go:14` | `internal/itunes` |
| `internal/server` | `internal/server/file_io_pool.go:26` | `internal/metafetch` |
| `internal/server` | `internal/server/file_io_pool.go:27` | `internal/scanlock` |
| `internal/server` | `internal/server/file_io_pool.go:28` | `internal/tagger` |
| `internal/server` | `internal/server/file_work_seams.go:11` | `internal/metafetch` |
| `internal/server` | `internal/server/fingerprint_rescan.go:11` | `internal/fingerprint` |
| `internal/server` | `internal/server/folder_autoscan_op.go:26` | `internal/scanner` |
| `internal/server` | `internal/server/identification_advance_op.go:25` | `internal/identification` |
| `internal/server` | `internal/server/identification_advance_op.go:27` | `internal/metabatch` |
| `internal/server` | `internal/server/identification_advance_op.go:28` | `internal/metafetch` |
| `internal/server` | `internal/server/itl_pid.go:18` | `internal/itunes` |
| `internal/server` | `internal/server/itunes_ops.go:31` | `internal/itunes/service` |
| `internal/server` | `internal/server/leaf_stores.go:10` | `internal/versions` |
| `internal/server` | `internal/server/library_ai_parse_op.go:19` | `internal/scanner` |
| `internal/server` | `internal/server/library_core_ops.go:27` | `internal/organizer` |
| `internal/server` | `internal/server/library_core_ops.go:28` | `internal/scanner` |
| `internal/server` | `internal/server/library_core_ops.go:29` | `internal/transcode` |
| `internal/server` | `internal/server/library_list_warmer.go:28` | `internal/audiobooks` |
| `internal/server` | `internal/server/login_ceiling.go:23` | `internal/server/absauth` |
| `internal/server` | `internal/server/malformed_m4b_wrappers.go:11` | `internal/remux` |
| `internal/server` | `internal/server/master_list_handler.go:26` | `internal/masterlist` |
| `internal/server` | `internal/server/metadata_batch_candidates.go:29` | `internal/metabatch` |
| `internal/server` | `internal/server/metadata_batch_candidates.go:30` | `internal/metafetch` |
| `internal/server` | `internal/server/metadata_bulk_fetch_log.go:16` | `internal/authorname` |
| `internal/server` | `internal/server/metadata_bulk_fetch_log.go:19` | `internal/metabatch` |
| `internal/server` | `internal/server/metadata_bulk_fetch_log.go:20` | `internal/metafetch` |
| `internal/server` | `internal/server/metadata_bulk_fetch_title.go:14` | `internal/authorname` |
| `internal/server` | `internal/server/metadata_bulk_fetch_title.go:16` | `internal/metabatch` |
| `internal/server` | `internal/server/metadata_bulk_fetch_title.go:17` | `internal/metadata` |
| `internal/server` | `internal/server/metadata_bulk_fetch_title.go:18` | `internal/metafetch` |
| `internal/server` | `internal/server/metadata_candidate_op.go:25` | `internal/metabatch` |
| `internal/server` | `internal/server/metadata_candidate_op.go:26` | `internal/metadata` |
| `internal/server` | `internal/server/metadata_candidate_op.go:27` | `internal/metafetch` |
| `internal/server` | `internal/server/metadata_candidate_op_log.go:18` | `internal/authorname` |
| `internal/server` | `internal/server/metadata_candidate_op_log.go:20` | `internal/metabatch` |
| `internal/server` | `internal/server/metadata_candidate_op_log.go:21` | `internal/metafetch` |
| `internal/server` | `internal/server/metadata_candidate_op_log.go:23` | `internal/organizer` |
| `internal/server` | `internal/server/metadata_candidate_unfetched.go:26` | `internal/audiobooks` |
| `internal/server` | `internal/server/metadata_candidate_unfetched.go:28` | `internal/metabatch` |
| `internal/server` | `internal/server/metadata_candidate_unfetched.go:29` | `internal/metafetch` |
| `internal/server` | `internal/server/metadata_ops.go:44` | `internal/metabatch` |
| `internal/server` | `internal/server/metadata_ops.go:45` | `internal/metadata` |
| `internal/server` | `internal/server/metadata_ops.go:46` | `internal/metafetch` |
| `internal/server` | `internal/server/metadata_ops.go:49` | `internal/policy` |
| `internal/server` | `internal/server/metadata_ops.go:53` | `internal/tagger` |
| `internal/server` | `internal/server/metadata_stores.go:11` | `internal/applygate` |
| `internal/server` | `internal/server/metadata_stores.go:13` | `internal/metabatch` |
| `internal/server` | `internal/server/movement_atom_cleanup.go:21` | `internal/metadata` |
| `internal/server` | `internal/server/movement_atom_cleanup.go:23` | `internal/tagger` |
| `internal/server` | `internal/server/op_run_context.go:12` | `internal/plugins/maintenance` |
| `internal/server` | `internal/server/openlibrary_ops.go:16` | `internal/openlibrary` |
| `internal/server` | `internal/server/openlibrary_service.go:19` | `internal/metafetch` |
| `internal/server` | `internal/server/openlibrary_service.go:20` | `internal/openlibrary` |
| `internal/server` | `internal/server/owner_rejected_rerank.go:19` | `internal/metabatch` |
| `internal/server` | `internal/server/owner_rejected_rerank.go:20` | `internal/metafetch` |
| `internal/server` | `internal/server/provider_throttle_wire.go:13` | `internal/metadata` |
| `internal/server` | `internal/server/purge_soft_deleted_op.go:16` | `internal/audiobooks` |
| `internal/server` | `internal/server/quarantine_known_bad.go:17` | `internal/remux` |
| `internal/server` | `internal/server/reconcile.go:21` | `internal/plugins/maintenance` |
| `internal/server` | `internal/server/reconcile.go:22` | `internal/reconcile` |
| `internal/server` | `internal/server/reconcile.go:23` | `internal/versionprimary` |
| `internal/server` | `internal/server/reconcile_ops.go:26` | `internal/reconcile` |
| `internal/server` | `internal/server/registry_wire.go:17` | `internal/audiobooks` |
| `internal/server` | `internal/server/registry_wire.go:18` | `internal/batch` |
| `internal/server` | `internal/server/registry_wire.go:21` | `internal/dedup` |
| `internal/server` | `internal/server/registry_wire.go:23` | `internal/importer` |
| `internal/server` | `internal/server/registry_wire.go:24` | `internal/itunes/service` |
| `internal/server` | `internal/server/registry_wire.go:26` | `internal/merge` |
| `internal/server` | `internal/server/registry_wire.go:27` | `internal/metafetch` |
| `internal/server` | `internal/server/registry_wire.go:29` | `internal/organizer` |
| `internal/server` | `internal/server/registry_wire.go:31` | `internal/quarantine` |
| `internal/server` | `internal/server/registry_wire.go:32` | `internal/scanner` |
| `internal/server` | `internal/server/registry_wire.go:36` | `internal/work` |
| `internal/server` | `internal/server/search_observer.go:13` | `internal/searchcache` |
| `internal/server` | `internal/server/search_result_cache.go:16` | `internal/searchcache` |
| `internal/server` | `internal/server/series_rename_ops.go:23` | `internal/audiobooks` |
| `internal/server` | `internal/server/series_rename_ops.go:29` | `internal/undo` |
| `internal/server` | `internal/server/server.go:27` | `internal/audiobooks` |
| `internal/server` | `internal/server/server.go:28` | `internal/authorcredit` |
| `internal/server` | `internal/server/server.go:29` | `internal/batch` |
| `internal/server` | `internal/server/server.go:30` | `internal/bookfileaudio` |
| `internal/server` | `internal/server/server.go:35` | `internal/dedup` |
| `internal/server` | `internal/server/server.go:39` | `internal/fingerprint` |
| `internal/server` | `internal/server/server.go:41` | `internal/importer` |
| `internal/server` | `internal/server/server.go:42` | `internal/itunes/service` |
| `internal/server` | `internal/server/server.go:45` | `internal/maintenance/jobs` |
| `internal/server` | `internal/server/server.go:46` | `internal/merge` |
| `internal/server` | `internal/server/server.go:47` | `internal/metadata` |
| `internal/server` | `internal/server/server.go:48` | `internal/metafetch` |
| `internal/server` | `internal/server/server.go:52` | `internal/server/absauth` |
| `internal/server` | `internal/server/server.go:67` | `internal/organizer` |
| `internal/server` | `internal/server/server.go:69` | `internal/plugins/acoustid` |
| `internal/server` | `internal/server/server.go:70` | `internal/plugins/dedup` |
| `internal/server` | `internal/server/server.go:75` | `internal/plugins/maintenance` |
| `internal/server` | `internal/server/server.go:76` | `internal/quarantine` |
| `internal/server` | `internal/server/server.go:78` | `internal/repairs` |
| `internal/server` | `internal/server/server.go:79` | `internal/scanner` |
| `internal/server` | `internal/server/server.go:81` | `internal/searchcache` |
| `internal/server` | `internal/server/server.go:82` | `internal/server/handlers/fpworker` |
| `internal/server` | `internal/server/server.go:88` | `internal/tagger` |
| `internal/server` | `internal/server/server.go:90` | `internal/usermerge` |
| `internal/server` | `internal/server/server.go:91` | `internal/work` |
| `internal/server` | `internal/server/server_accessors.go:10` | `internal/dedup` |
| `internal/server` | `internal/server/server_accessors.go:12` | `internal/importer` |
| `internal/server` | `internal/server/server_library_clone.go:18` | `internal/organizer` |
| `internal/server` | `internal/server/server_lifecycle.go:30` | `internal/merge` |
| `internal/server` | `internal/server/server_lifecycle.go:34` | `internal/remux` |
| `internal/server` | `internal/server/server_lifecycle.go:35` | `internal/scanner` |
| `internal/server` | `internal/server/server_maintenance_deps.go:24` | `internal/applygate` |
| `internal/server` | `internal/server/server_maintenance_deps.go:29` | `internal/dedup` |
| `internal/server` | `internal/server/server_maintenance_deps.go:32` | `internal/merge` |
| `internal/server` | `internal/server/server_maintenance_deps.go:33` | `internal/metafetch` |
| `internal/server` | `internal/server/server_maintenance_deps.go:37` | `internal/plugins/maintenance` |
| `internal/server` | `internal/server/server_maintenance_deps.go:38` | `internal/repairs` |
| `internal/server` | `internal/server/server_maintenance_deps.go:40` | `internal/usermerge` |
| `internal/server` | `internal/server/server_maintenance_deps.go:42` | `internal/versions` |
| `internal/server` | `internal/server/server_middleware.go:19` | `internal/organizer` |
| `internal/server` | `internal/server/server_middleware.go:21` | `internal/tagger` |
| `internal/server` | `internal/server/server_search.go:22` | `internal/dedup` |
| `internal/server` | `internal/server/server_search.go:25` | `internal/organizer` |
| `internal/server` | `internal/server/server_search.go:27` | `internal/tagger` |
| `internal/server` | `internal/server/signals_coverage_handler.go:15` | `internal/fingerprint` |
| `internal/server` | `internal/server/transcode_version.go:18` | `internal/bookfileaudio` |
| `internal/server` | `internal/server/transcode_version.go:22` | `internal/versionprimary` |
| `internal/server` | `internal/server/version_lifecycle.go:18` | `internal/audiobooks` |
| `internal/server` | `internal/server/version_lifecycle.go:23` | `internal/merge` |
| `internal/server` | `internal/server/version_lifecycle.go:24` | `internal/versions` |
| `internal/server` | `internal/server/version_twin_metadata.go:12` | `internal/plugins/maintenance` |
| `internal/server` | `internal/server/wire_abs_routes.go:21` | `internal/server/absauth` |
| `internal/server` | `internal/server/wire_abs_routes.go:22` | `internal/server/handlers/abs` |
| `internal/server` | `internal/server/wire_audiobooks_routes.go:10` | `internal/server/handlers/audiobooks` |
| `internal/server` | `internal/server/wire_dedup_routes.go:10` | `internal/server/handlers/dedup` |
| `internal/server` | `internal/server/wire_dedup_routes.go:11` | `internal/server/handlers/duplicates` |
| `internal/server` | `internal/server/wire_entities_routes.go:10` | `internal/server/handlers/entities` |
| `internal/server` | `internal/server/wire_fpworker_routes.go:13` | `internal/server/handlers/fpworker` |
| `internal/server` | `internal/server/wire_handlers.go:12` | `internal/audiobooks` |
| `internal/server` | `internal/server/wire_handlers.go:15` | `internal/dedup` |
| `internal/server` | `internal/server/wire_handlers.go:16` | `internal/itunes/service` |
| `internal/server` | `internal/server/wire_handlers.go:17` | `internal/merge` |
| `internal/server` | `internal/server/wire_handlers.go:19` | `internal/organizer` |
| `internal/server` | `internal/server/wire_handlers.go:20` | `internal/plugins/maintenance` |
| `internal/server` | `internal/server/wire_handlers.go:24` | `internal/server/handlers/audiobooks` |
| `internal/server` | `internal/server/wire_handlers.go:25` | `internal/server/handlers/dedup` |
| `internal/server` | `internal/server/wire_handlers.go:26` | `internal/server/handlers/duplicates` |
| `internal/server` | `internal/server/wire_handlers.go:27` | `internal/server/handlers/entities` |
| `internal/server` | `internal/server/wire_handlers.go:28` | `internal/server/handlers/metadata` |
| `internal/server` | `internal/server/wire_handlers.go:30` | `internal/server/handlers/review` |
| `internal/server` | `internal/server/wire_handlers.go:34` | `internal/undo` |
| `internal/server` | `internal/server/wire_handlers.go:35` | `internal/usermerge` |
| `internal/server` | `internal/server/wire_metadata_routes.go:10` | `internal/server/handlers/metadata` |
| `internal/server` | `internal/server/wire_repairs_routes.go:20` | `internal/repairs` |
| `internal/server` | `internal/server/wire_repairs_routes.go:23` | `internal/server/handlers/repairs` |
| `internal/server` | `internal/server/wire_review_routes.go:10` | `internal/server/handlers/review` |
| `internal/server/middleware` | `internal/server/middleware/absauth.go:19` | `internal/server/absauth` |
| `internal/server/middleware` | `internal/server/middleware/request_size.go:13` | `internal/fingerprint/workerapi` |
| `internal/server/handlers` | `internal/server/handlers/abs_session_revoke.go:10` | `internal/server/absauth` |
| `internal/server/handlers` | `internal/server/handlers/activity_compact.go:22` | `internal/plugins/maintenance` |
| `internal/server/handlers` | `internal/server/handlers/ai.go:30` | `internal/dedup` |
| `internal/server/handlers` | `internal/server/handlers/ai.go:33` | `internal/metadata` |
| `internal/server/handlers` | `internal/server/handlers/auth.go:19` | `internal/server/absauth` |
| `internal/server/handlers` | `internal/server/handlers/collections.go:22` | `internal/playlist` |
| `internal/server/handlers` | `internal/server/handlers/collections_refresh.go:17` | `internal/playlist` |
| `internal/server/handlers` | `internal/server/handlers/diagnostics.go:39` | `internal/merge` |
| `internal/server/handlers` | `internal/server/handlers/diagnostics.go:42` | `internal/versionprimary` |
| `internal/server/handlers` | `internal/server/handlers/filesystem.go:24` | `internal/importer` |
| `internal/server/handlers` | `internal/server/handlers/filesystem.go:27` | `internal/scanner` |
| `internal/server/handlers` | `internal/server/handlers/itunes.go:20` | `internal/itunes` |
| `internal/server/handlers` | `internal/server/handlers/itunes.go:21` | `internal/itunes/service` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache.go:33` | `internal/metabatch` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache.go:34` | `internal/metafetch` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache_query.go:26` | `internal/applygate` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache_query.go:30` | `internal/metabatch` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache_query.go:31` | `internal/metadata` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache_query.go:32` | `internal/metafetch` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache_snapshot.go:18` | `internal/metafetch` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache_stale.go:18` | `internal/metabatch` |
| `internal/server/handlers` | `internal/server/handlers/metadata_cache_stale.go:19` | `internal/metafetch` |
| `internal/server/handlers` | `internal/server/handlers/oauth_login.go:20` | `internal/server/handlers/abs` |
| `internal/server/handlers` | `internal/server/handlers/organize.go:31` | `internal/organizer` |
| `internal/server/handlers` | `internal/server/handlers/organize_scan_lock.go:20` | `internal/organizer` |
| `internal/server/handlers` | `internal/server/handlers/organize_scan_lock.go:21` | `internal/scanlock` |
| `internal/server/handlers` | `internal/server/handlers/pcache_stats.go:22` | `internal/metafetch` |
| `internal/server/handlers` | `internal/server/handlers/playlists.go:17` | `internal/playlist` |
| `internal/server/handlers` | `internal/server/handlers/reading.go:18` | `internal/readstatus` |
| `internal/server/handlers` | `internal/server/handlers/split_book.go:17` | `internal/dedup` |
| `internal/server/handlers` | `internal/server/handlers/user.go:21` | `internal/server/absauth` |
| `internal/server/handlers` | `internal/server/handlers/user.go:23` | `internal/usermerge` |
| `internal/server/handlers` | `internal/server/handlers/user_merge.go:19` | `internal/usermerge` |
| `internal/server/handlers` | `internal/server/handlers/versions.go:22` | `internal/versionprimary` |
| `internal/server/handlers/system` | `internal/server/handlers/system/handler.go:44` | `internal/dedup` |
| `internal/server/handlers/system` | `internal/server/handlers/system/handler.go:47` | `internal/metafetch` |
| `internal/server/handlers/system` | `internal/server/handlers/system/handler.go:48` | `internal/organizer` |
| `internal/server/handlers/system` | `internal/server/handlers/system/handler.go:49` | `internal/policy` |
| `internal/server/handlers/opsv3` | `internal/server/handlers/opsv3/access.go:15` | `internal/repairs` |
| `internal/server/handlers/opaccess` | `internal/server/handlers/opaccess/repairs.go:24` | `internal/repairs` |
| `internal/server/handlers/opaccess` | `internal/server/handlers/opaccess/repairs.go:26` | `internal/undo` |
| `internal/server/handlers/opaccess` | `internal/server/handlers/opaccess/user_data_results.go:14` | `internal/merge` |
| `internal/server/handlers/opaccess` | `internal/server/handlers/opaccess/user_data_results.go:15` | `internal/repairs` |
| `internal/server/handlers/opaccess` | `internal/server/handlers/opaccess/user_data_results.go:17` | `internal/usermerge` |
| `internal/server/handlers/operations` | `internal/server/handlers/operations/handler.go:44` | `internal/audiobooks` |
| `internal/server/handlers/operations` | `internal/server/handlers/operations/handler.go:53` | `internal/undo` |
| `internal/server/handlers/admindebug` | `internal/server/handlers/admindebug/handler.go:37` | `internal/merge` |
| `internal/database` | `internal/database/book_own_folder.go:15` | `internal/chaptershape` |
| `internal/database` | `internal/database/fingerprint_era.go:8` | `internal/fingerprint` |
| `internal/database` | `internal/database/fingerprint_window.go:16` | `internal/fingerprint` |
| `internal/database` | `internal/database/memdb_reads.go:15` | `internal/fingerprint` |
| `internal/database` | `internal/database/memdb_reads.go:17` | `internal/titleutil` |
| `internal/database` | `internal/database/metadata_field_lock_source.go:16` | `internal/metastate` |
| `internal/database` | `internal/database/metadata_field_locks.go:15` | `internal/metastate` |
| `internal/database` | `internal/database/pebble_store.go:31` | `internal/fingerprint` |
| `internal/database` | `internal/database/pebble_store.go:33` | `internal/matcher` |
| `internal/database` | `internal/database/pebble_store.go:35` | `internal/titleutil` |
| `internal/database` | `internal/database/pebble_store_authors.go:21` | `internal/personname` |
| `internal/database` | `internal/database/pebble_store_bookfiles.go:25` | `internal/fingerprint` |
| `internal/database` | `internal/database/pebble_store_bookmarks.go:39` | `internal/syncapi/progress` |
| `internal/database` | `internal/database/pebble_store_fpwinidx.go:15` | `internal/fingerprint` |
| `internal/database` | `internal/database/pebble_store_lsh.go:38` | `internal/fingerprint` |
| `internal/database` | `internal/database/signal_coverage.go:18` | `internal/fingerprint` |
| `internal/database` | `internal/database/signal_coverage_era.go:16` | `internal/fingerprint` |

### First audiobook-type use sites (up to 3 per package)

| package | sites |
|---|---|
| `internal/operations/opswriter` | internal/operations/opswriter/evidence.go:58 `database.MetadataChangeRecord`; internal/operations/opswriter/evidence.go:337 `database.MetadataChangeRecord`; internal/operations/opswriter/evidence.go:356 `database.MetadataChangeRecord` |
| `internal/operations/registry` | internal/operations/registry/batch_runner.go:1191 `database.WorkerProgress`; internal/operations/registry/batch_runner.go:1195 `database.WorkerProgress`; internal/operations/registry/batch_runner.go:1197 `database.WorkerProgress` |
| `internal/ai` | internal/ai/dedup_review.go:80 `database.DedupCandidate`; internal/ai/dedup_review.go:83 `database.DedupCandidate`; internal/ai/dedup_review.go:191 `database.DedupCandidate` |
| `internal/aiscan` | internal/aiscan/cross_validation.go:23 `database.ScanSuggestion`; internal/aiscan/cross_validation.go:24 `database.ScanResult`; internal/aiscan/cross_validation.go:45 `database.ScanResult` |
| `internal/activity` | internal/activity/changelog.go:27 `database.MetadataChangeRecord`; internal/activity/changelog.go:28 `database.BookPathChange`; internal/activity/changelog.go:220 `database.MetadataChangeRecord` |
| `internal/maintenance` | internal/maintenance/job.go:254 `database.Book`; internal/maintenance/job.go:255 `database.BookCore`; internal/maintenance/job.go:256 `database.Book` |
| `internal/work` | internal/work/register.go:18 `database.WorkStore`; internal/work/service.go:16 `database.WorkStore`; internal/work/service.go:19 `database.WorkStore` |
| `internal/search` | internal/search/index_builder.go:28 `database.Author`; internal/search/index_builder.go:29 `database.Series`; internal/search/index_builder.go:84 `database.Author` |
| `internal/sysinfo` | internal/sysinfo/dashboard_service.go:23 `database.Playlist` |
| `internal/sweep` | internal/sweep/sweeper.go:39 `database.Book`; internal/sweep/sweeper.go:40 `database.Book`; internal/sweep/sweeper.go:144 `database.Book` |
| `internal/decisions` | internal/decisions/retired_versions.go:42 `database.Book`; internal/decisions/retired_versions.go:43 `database.BookVersion`; internal/decisions/retired_versions.go:44 `database.BookFile` |
| `internal/diagnostics` | internal/diagnostics/service.go:66 `database.BookCore`; internal/diagnostics/service.go:102 `database.Author`; internal/diagnostics/service.go:109 `database.Series` |
| `internal/fileops` | internal/fileops/book_file_hash_recorder.go:20 `database.BookFileHashUpdater`; internal/fileops/book_file_hash_recorder.go:21 `database.BookFile`; internal/fileops/book_file_hash_recorder.go:40 `database.BookFile` |
| `internal/server` | internal/server/ai_author_reassign.go:19 `database.BookCore`; internal/server/ai_author_reassign.go:20 `database.Author`; internal/server/ai_author_reassign.go:91 `database.BookAuthor` |
| `internal/server/handlers` | internal/server/handlers/ai.go:44 `database.Author`; internal/server/handlers/ai.go:45 `database.BookCore`; internal/server/handlers/ai.go:75 `database.Scan` |
| `internal/server/handlers/system` | internal/server/handlers/system/interfaces.go:39 `database.BookCore`; internal/server/handlers/system/interfaces.go:45 `database.Author`; internal/server/handlers/system/interfaces.go:48 `database.BookCore` |
| `internal/server/handlers/opsv3` | internal/server/handlers/opsv3/view.go:32 `database.WorkerProgress`; internal/server/handlers/opsv3/view.go:69 `database.WorkerProgress`; internal/server/handlers/opsv3/view.go:102 `database.WorkerProgress` |
| `internal/server/handlers/operations` | internal/server/handlers/operations/handler.go:276 `database.BookAuthor`; internal/server/handlers/operations/handler.go:285 `database.BookAuthor`; internal/server/handlers/operations/handler.go:313 `database.BookNarrator` |
| `internal/server/handlers/admindebug` | internal/server/handlers/admindebug/handler.go:53 `database.Book`; internal/server/handlers/admindebug/handler.go:54 `database.Book`; internal/server/handlers/admindebug/handler.go:55 `database.Book` |
| `pkg/ops` | pkg/ops/writer.go:49 `database.Book`; pkg/ops/writer.go:52 `database.BookFile`; pkg/ops/writer.go:56 `database.BookFileLocation` |
| `pkg/ops/opstest` | pkg/ops/opstest/fakes.go:74 `database.Book`; pkg/ops/opstest/fakes.go:83 `database.Book`; pkg/ops/opstest/fakes.go:93 `database.Book` |
| `pkg/plugin/sdk` | pkg/plugin/sdk/iterate.go:26 `database.Book`; pkg/plugin/sdk/iterate.go:46 `database.Book` |

### (a2) Non-import content leaks in otherwise generic packages

| package | file:line | leak |
|---|---|---|
| `internal/logger` | `internal/logger/logger.go:45` | `BookID` field in the log entry struct |
| `internal/plugin` | `internal/plugin/events.go:19-30`, `:37` | `EventBookImported/Deleted/Quarantined/Unquarantined` event types and `Event.BookID` |
| `internal/plugins/webhook` | `internal/plugins/webhook/plugin.go:124,126` | `X-Audiobook-Event` / `X-Audiobook-Signature-256` HTTP header names (brand string) |
| `internal/metrics` | `internal/metrics/metrics.go:19`, `:51` | Prometheus namespace `audiobook_organizer`; `books_total` gauge |
| `internal/telemetry` | `internal/telemetry/meter.go:17`, `views.go:39` | `ScopePrefix` and instrument names carry the `audiobook_organizer` brand |
| `internal/opsmetrics` | `internal/opsmetrics/opsmetrics.go:114` | instrument names `audiobook_organizer.ops.*` |
| `internal/util` | `internal/util/narrator_credit.go:15` | `NarratorCreditVerdict` type inside the generic string-helper package |
| `internal/pathutil` | `internal/pathutil/hidden.go:80`, `protected.go:403` | `OpenLibraryDumpDir` app dir; iTunes Media path special-case |
| `internal/httputil` | `internal/httputil/rangeserve.go:34` | `.m4b`/`.m4a` content-type switch |
| `internal/querygrammar` | `internal/querygrammar/budget.go:70` | `BookID`/`Title` fields in budget error type |
| `internal/serviceregistry` | `internal/serviceregistry/keys.go:14` | `KeyAudiobook` service key |
| `internal/tools` | `internal/tools/embed_queue.go:66` | embed queue is keyed by `book_id` |
| `internal/updater` | `internal/updater/updater.go:36` | hard-coded repo slug of this project |
| `internal/aidispatch` | `internal/aidispatch/capabilities.go:176,185` | capability descriptions are book-specific (metadata rerank, duplicate-book review) |
| `internal/watcher` | `internal/watcher/watcher.go:243` | calls `config.SupportedExtensionSet()` (audio-extension policy lives in config) |
| `internal/server/middleware` | `internal/server/middleware/absauth.go:19`, `request_size.go:13` | imports `server/absauth` (Audiobookshelf auth) and `fingerprint/workerapi` |
| `pkg/plugin/sdk` | `pkg/plugin/sdk/iterate.go:26,46` | `GetAllBooksFullFrom`/`PageBooks` hand `database.Book` to external plugins |

### (a3) Transitive closure: what each platform candidate drags in

Computed as the set of in-module packages reachable via non-test imports. `database` alone pulls 8 audiobook-specific packages: audioutil, chaptershape, fingerprint, matcher, metastate, personname, syncapi/progress, titleutil (BFS from `internal/database`).

| package | transitive in-module deps | of which audiobook-specific | of which mixed | shortest path to an audiobook-specific package |
|---|---|---|---|---|
| `internal/logger` | 0 | 0 | 0 | (none) |
| `internal/logging` | 0 | 0 | 0 | (none) |
| `internal/metrics` | 1 | 0 | 1 | (none) |
| `internal/telemetry` | 0 | 0 | 0 | (none) |
| `internal/serviceregistry` | 0 | 0 | 0 | (none) |
| `internal/util` | 0 | 0 | 0 | (none) |
| `internal/pathutil` | 0 | 0 | 0 | (none) |
| `internal/cache` | 2 | 0 | 2 | (none) |
| `internal/httputil` | 2 | 0 | 0 | (none) |
| `internal/auth` | 18 | 8 | 6 | internal/auth -> internal/database -> internal/chaptershape |
| `internal/oauth` | 18 | 8 | 6 | internal/oauth -> internal/database -> internal/chaptershape |
| `internal/realtime` | 0 | 0 | 0 | (none) |
| `internal/plugin` | 2 | 0 | 2 | (none) |
| `internal/backup` | 3 | 0 | 1 | (none) |
| `internal/updater` | 28 | 10 | 10 | internal/updater -> internal/config -> internal/audioext |
| `internal/mail` | 0 | 0 | 0 | (none) |
| `internal/tools` | 0 | 0 | 0 | (none) |
| `internal/work` | 19 | 8 | 7 | internal/work -> internal/database -> internal/chaptershape |
| `internal/activity` | 33 | 15 | 10 | internal/activity -> internal/undo |
| `internal/config` | 27 | 10 | 9 | internal/config -> internal/audioext |
| `internal/database` | 17 | 8 | 5 | internal/database -> internal/chaptershape |
| `internal/operations` | 1 | 0 | 1 | (none) |
| `internal/operations/registry` | 27 | 8 | 9 | internal/operations/registry -> internal/database -> internal/chaptershape |
| `internal/operations/state` | 0 | 0 | 0 | (none) |
| `internal/operations/opswriter` | 20 | 8 | 7 | internal/operations/opswriter -> internal/database -> internal/chaptershape |
| `internal/opsmetrics` | 2 | 0 | 0 | (none) |
| `internal/scheduler` | 80 | 39 | 18 | internal/scheduler -> internal/dedup |
| `internal/ai` | 30 | 11 | 10 | internal/ai -> internal/covertext |
| `internal/ai/aijobs` | 18 | 8 | 6 | internal/ainternal/aijobs -> internal/database -> internal/chaptershape |
| `internal/aidispatch` | 2 | 0 | 1 | (none) |
| `internal/search` | 29 | 10 | 11 | internal/search -> internal/config -> internal/audioext |
| `internal/sysinfo` | 28 | 10 | 10 | internal/sysinfo -> internal/config -> internal/audioext |
| `internal/sweep` | 34 | 15 | 11 | internal/sweep -> internal/activity -> internal/undo |
| `internal/maintenance` | 28 | 8 | 10 | internal/maintenance -> internal/database -> internal/chaptershape |
| `internal/fileops` | 29 | 11 | 10 | internal/fileops -> internal/filehash |
| `internal/watcher` | 29 | 10 | 10 | internal/watcher -> internal/config -> internal/audioext |
| `internal/appdirs` | 28 | 10 | 10 | internal/appdirs -> internal/config -> internal/audioext |
| `internal/querygrammar` | 0 | 0 | 0 | (none) |
| `pkg/ops` | 19 | 8 | 6 | pkg/ops -> internal/database -> internal/chaptershape |
| `pkg/plugin/sdk` | 28 | 8 | 10 | pkg/plugin/sdk -> internal/database -> internal/chaptershape |
| `pkg/ops/opstest` | 28 | 8 | 10 | pkg/ops/opstest -> internal/database -> internal/chaptershape |
| `internal/server/middleware` | 42 | 12 | 14 | internal/server/middleware -> internal/fingerprint/workerapi |

Reading: `logger`, `logging`, `telemetry`, `realtime`, `serviceregistry`, `util`, `pathutil`, `operations/state`, `querygrammar`, `mail`, `tools` have **zero** transitive in-module deps on audiobook-specific packages (they are extractable without database surgery). Everything that touches `database` has >= 8.

## (b) `internal/models` contents

`internal/models` has 2 non-test files (`audiobook.go` 149 lines, `dedup_score.go` 188 lines) and 337 non-test LOC. It imports only `time` (`internal/models/audiobook.go:7`, `dedup_score.go:8`). Layer 0 in `layerOf` (`internal/arch/layering_test.go:97`; also in `namedLayer` at `:520`).

| type / func | file:line | group | in-repo non-test users |
|---|---|---|---|
| `Author` | `internal/models/audiobook.go:10` | audiobook-specific (book entity) | none outside models (real type is `database.Author` at `internal/database/store.go:161`) |
| `Series` | `internal/models/audiobook.go:16` | audiobook-specific | none outside models (real type `database.Series` at `internal/database/store.go:191`) |
| `Audiobook` | `internal/models/audiobook.go:24` | audiobook-specific (narrator, audiobook release year, iTunes import fields at `:44-51`) | `internal/itunes/import.go:361,394` only |
| `AudiobookListRequest`, `AudiobookListResponse`, `AudiobookUpdateRequest`, `BatchUpdateRequest` | `audiobook.go:72,84,93,103` | audiobook-specific (HTTP DTOs) | none |
| `FileSystemItem`, `BrowseRequest`, `ExclusionRequest` | `audiobook.go:109,120,125` | generic (filesystem browse DTOs) | none |
| `SystemStatus`, `LogEntry` | `audiobook.go:131,144` | generic (system DTOs) | none |
| `SignalKind`, consts `SignalKindExactRule`/`SignalKindSamePath` | `internal/models/dedup_score.go:14,25,27` | audiobook-specific (book-pair dedup evidence) | `internal/dedup/unified/score.go:23,106,127` (aliases) |
| `Signal` | `dedup_score.go:32` | audiobook-specific | alias `internal/dedup/unified/score.go:135`; `internal/plugins/dedup/calibrate_composite.go:180,200,754,774` |
| `UnifiedDedupScore`, `IsProvenanceOnly` | `dedup_score.go:155,71` | audiobook-specific | alias `unified/score.go:144`; `internal/database/embedding_store.go:124,178,1651,1708`; `plugins/dedup/calibrate_composite.go:778` |
| `IsProvenanceKind`, `MergeProvenance`, `CarryProvenance` | `dedup_score.go:62,94,129` | audiobook-specific | `internal/database/embedding_store.go:861,867,1660,1771` |

Importers by type group (non-test): book-entity group **1 package** (`internal/itunes`); dedup-score group **3 packages** (`internal/database`, `internal/dedup/unified`, `internal/plugins/dedup`); generic DTO group **0 packages**. Total distinct non-test importers: **4** (`internal/database`, `internal/dedup/unified`, `internal/itunes`, `internal/plugins/dedup`). Test importers: 7 test files in `internal/database` (3), `internal/dedup` (1), `internal/plugins/dedup` (3) using only `Signal`, `SignalKindExactRule`, `UnifiedDedupScore`. Conclusion: **no content of `internal/models` belongs in a platform core**; the only live generic-looking coupling is that `internal/database/embedding_store.go:22` pulls `models` (dedup score) into the storage layer.

## (c) Top 25 packages by in-degree

(Rank 25 has in-degree 11; ties broken alphabetically. In-degree counts non-test importers among the 213 packages.)

| # | package | in-degree | out-degree | non-test LOC | layer | class | server fan-in |
|---|---|---|---|---|---|---|---|
| 1 | `internal/database` | 104 | 16 | 103432 | 2 | mixed | 24 |
| 2 | `internal/logger` | 73 | 0 | 442 | 0 | mixed | 16 |
| 3 | `internal/config` | 49 | 10 | 10506 | 1 | mixed | 11 |
| 4 | `internal/operations/registry` | 38 | 14 | 15137 | 4 | mixed | 16 |
| 5 | `internal/merge` | 25 | 10 | 13113 | 3 | audiobook-specific | 12 |
| 6 | `internal/pathutil` | 25 | 0 | 1040 | 0 | mixed | 1 |
| 7 | `internal/metadata` | 24 | 18 | 12071 | 3 | audiobook-specific | 7 |
| 8 | `internal/serviceregistry` | 24 | 0 | 532 | 0 | mixed | 1 |
| 9 | `internal/util` | 22 | 0 | 1478 | 0 | mixed | 5 |
| 10 | `internal/httputil` | 19 | 2 | 604 | 0 | mixed | 18 |
| 11 | `internal/dedup` | 18 | 21 | 15806 | 3 | audiobook-specific | 8 |
| 12 | `internal/auth` | 17 | 1 | 610 | 3 | generic | 9 |
| 13 | `internal/personname` | 17 | 0 | 1844 | 0 | audiobook-specific | 1 |
| 14 | `internal/metafetch` | 16 | 31 | 21306 | 3 | audiobook-specific | 8 |
| 15 | `internal/versionprimary` | 16 | 4 | 1619 | 3 | audiobook-specific | 3 |
| 16 | `internal/fileops` | 15 | 6 | 1660 | 3 | mixed | 4 |
| 17 | `internal/operations` | 14 | 1 | 379 | 3 | generic | 2 |
| 18 | `internal/logging` | 13 | 0 | 529 | 0 | generic | 5 |
| 19 | `internal/metrics` | 13 | 1 | 840 | 0 | mixed | 4 |
| 20 | `internal/undo` | 13 | 2 | 7214 | 3 | audiobook-specific | 6 |
| 21 | `internal/authorname` | 12 | 1 | 757 | 0 | audiobook-specific | 1 |
| 22 | `internal/fingerprint` | 12 | 1 | 2866 | 0 | audiobook-specific | 1 |
| 23 | `internal/itunes` | 12 | 6 | 8822 | 3 | audiobook-specific | 2 |
| 24 | `internal/tagger` | 12 | 4 | 744 | 3 | audiobook-specific | 2 |
| 25 | `internal/activity` | 11 | 5 | 2526 | 3 | mixed | 7 |

## (d) God packages and cycle near-misses

### God packages

| package | non-test LOC | test LOC | in-degree | out-degree | layer | class |
|---|---|---|---|---|---|---|
| `internal/server` | 46789 | 92403 | 2 | 122 | 5 | mixed |
| `internal/database` | 103432 | 101883 | 104 | 16 | 2 | mixed |
| `internal/plugins/maintenance` | 101319 | 107635 | 2 | 71 | 4 | audiobook-specific |
| `internal/metafetch` | 21306 | 24938 | 16 | 31 | 3 | audiobook-specific |
| `internal/metadata` | 12071 | 12621 | 24 | 18 | 3 | audiobook-specific |
| `internal/audiobooks` | 20631 | 32883 | 8 | 29 | 3 | audiobook-specific |
| `internal/scanner` | 14775 | 23822 | 11 | 27 | 3 | audiobook-specific |
| `internal/operations/registry` | 15137 | 25916 | 38 | 14 | 4 | mixed |
| `internal/maintenance/jobs` | 12366 | 13629 | 1 | 25 | 4 | audiobook-specific |
| `internal/merge` | 13113 | 16863 | 25 | 10 | 3 | audiobook-specific |
| `internal/dedup` | 15806 | 20594 | 18 | 21 | 3 | audiobook-specific |
| `internal/scheduler` | 4249 | 4150 | 5 | 16 | 4 | mixed |
| `internal/config` | 10506 | 13792 | 49 | 10 | 1 | mixed |

- `internal/server` out-degree 122 is the largest in the module; it imports 56 audiobook-specific packages directly. Its only importers are `.` (`main.go:16`) and `cmd`.
- `internal/plugins/maintenance` is the second largest by size (101319 LOC) and imports 71 module packages, 45 of them audiobook-specific per raw/01; it is audiobook code that lives in the "jobs" layer.
- `internal/database`: 103432 non-test LOC, 272 non-test files at the top level plus a 31462-LOC generated mock package (`internal/database/mocks/mock_store.go` has 445 `MockStore` methods including `EXPECT`; CLAUDE.md states 398 Store methods, so treat 398 as UNVERIFIED by this scout; counting exact `Store` method-set size needs go/types, not done).

### Cycle near-misses

- The import graph is **acyclic**: `go list ./...` succeeds and Go rejects import cycles at compile time. No SCC to break.
- Nearest misses are the 12 wrong-way edges in `allowed` (`internal/arch/layering_test.go:290-303`); each is a lower layer reaching a higher one. All 12 were verified to exist and to violate layer order at this commit (see raw/03).
- Decoupling shims that exist because cycles nearly formed (their own package docs say so): `internal/lifecycle/shutdown.go:6` ("process-lifecycle sentinels shared by layers that must not import each other"), `internal/keyedmutex/keyedmutex.go:6` (extracted from deluge), `internal/flight/flight.go:6` (extracted from server), `internal/appdirs/appdirs.go:6` (split from pathutil), `internal/serviceregistry` (DI container, `registry.go`, `graph.go`).
- Reverse-direction risk: `internal/config` is imported by 49 packages and imports `database`; if `database` ever needs a config value it cannot import `config` (that would be a cycle) which is why config reads the store directly (`internal/arch/layering_test.go:294` reason text).

## (e) Everything under `pkg/` and who imports it

| package | non-test LOC | non-test files | imports (in-module) |
|---|---|---|---|
| `pkg/ops` | 3073 | 16 | `internal/auth` (`approval.go:11`, `definition.go:20`), `internal/database` (`writer.go:11`) |
| `pkg/ops/opstest` | 4056 | 6 | `internal/database`, `internal/logger`, `internal/operations/opswriter`, `internal/operations/registry`, `internal/operations/state`, `pkg/ops` |
| `pkg/plugin/sdk` | 519 | 11 | `internal/database` (`iterate.go:12`), `internal/operations/registry` (`operation.go:8`, `reporter.go:8`, `enqueue_options.go:8`, `capability.go:8`, `events.go:8`) |

Importers (file counts are import lines; first site given):

| imported | importer package | non-test import lines | first site | test import lines |
|---|---|---|---|---|
| `pkg/ops` | `internal/operations/registry` | 5 | `internal/operations/registry/finish.go:17` | 8 |
| `pkg/ops` | `internal/operations/opswriter` | 3 | `internal/operations/opswriter/fence.go:21` | 2 |
| `pkg/ops` | `internal/opscatalog` | 2 | `internal/opscatalog/catalog.go:33` | 1 |
| `pkg/ops` | `internal/scheduler` | 2 | `internal/scheduler/v3_schedule.go:21` | 1 |
| `pkg/ops` | `internal/server` | 2 | `internal/server/identification_advance_op.go:31` | 1 |
| `pkg/ops` | `internal/repairs` | 1 | `internal/repairs/writer.go:17` | 0 |
| `pkg/ops` | `pkg/ops/opstest` | 5 | `pkg/ops/opstest/run.go:27` | 4 |
| `pkg/ops` | `internal/audiobooks` | 0 | - | 1 (`internal/audiobooks/revert_opswriter_test.go:19`) |
| `pkg/ops/opstest` | (tests only) `tools/cmd/oplint` | 0 | - | 1 (`tools/cmd/oplint/analyzer_test.go:35`) |
| `pkg/plugin/sdk` | `internal/plugins/maintenance` | 97 | `internal/plugins/maintenance/repair_merged_user_state.go:42` | 32 |
| `pkg/plugin/sdk` | `internal/plugins/dedup` | 33 | `internal/plugins/dedup/llm_review.go:18` | 12 |
| `pkg/plugin/sdk` | `internal/plugins/acoustid` | 9 | `internal/plugins/acoustid/backfill.go:26` | 4 |
| `pkg/plugin/sdk` | `internal/plugins/deluge` | 5 | `internal/plugins/deluge/centralization.go:25` | 1 |
| `pkg/plugin/sdk` | `internal/plugins/metafetch` | 4 | `internal/plugins/metafetch/calibrate_scoring.go:80` | 2 |
| `pkg/plugin/sdk` | `internal/plugins/itunes` | 4 | `internal/plugins/itunes/adapter.go:13` | 1 |
| `pkg/plugin/sdk` | `internal/server` | 2 | `internal/server/duplicates_ops.go:48` | 0 |
| `pkg/plugin/sdk` | `internal/importer` | 1 | `internal/importer/service.go:32` | 2 |
| `pkg/plugin/sdk` | `internal/reconcile` | 1 | `internal/reconcile/itunes_heal.go:34` | 0 |
| `pkg/plugin/sdk` | `internal/scheduler` | 1 | `internal/scheduler/extra_ops.go:28` | 0 |
| `pkg/plugin/sdk` | `tools/cmd/sdkguard` (test only) | 0 | - | 1 (`tools/cmd/sdkguard/main_test.go:19`) |

Notes:
- All `pkg/*` consumers are inside this module; there is no external importer in-tree. `layerOf` places `pkg/ops` at 3, `pkg/ops/opstest` and `pkg/plugin/sdk` at 4 (`internal/arch/layering_test.go:211`, `:232`, `:233`), i.e. `pkg/plugin/sdk` is a layer above storage because it imports the operations registry.
- `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` contains ~12 fixture packages that hard-code this module path and import `pkg/ops` (e.g. `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/clean/clean.go:11`); these are testdata (not in `go list ./...`) but break on a module-path change. `tools/cmd/oplint/analyzer.go:45` also hard-codes `modulePath`, and `tools/cmd/sdkguard/main.go:66` hard-codes the `internal/` prefix.
- Because `pkg/ops` and `pkg/plugin/sdk` import `internal/database`, an out-of-module plugin author can call the SDK but cannot name `database.Book` themselves (Go `internal` rule applies to the importing location); this is an inference from Go's rules, not something tested in-tree.
