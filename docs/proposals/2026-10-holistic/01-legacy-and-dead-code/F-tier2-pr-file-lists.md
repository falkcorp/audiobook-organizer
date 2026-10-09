<!-- file: docs/proposals/2026-10-holistic/01-legacy-and-dead-code/F-tier2-pr-file-lists.md -->
<!-- version: 1.0.1 -->
<!-- guid: 23309fcd-d3d0-4cd8-b3aa-34aa05fe1388 -->
<!-- last-edited: 2026-10-09 -->

# Appendix F: Tier-2 file lists (superseded grouping: 11 PRs T1 to T11)

> **Superseded grouping (08 §5, freeze F):** the per-package PRs below are shipped as 11 grouped PRs, T1 to T11. The file lists are unchanged; P36 is P73's.

Category D of appendix B: unreachable from every main, not superseded-by-name, not an unwired feature, not dedup, not a test seam, not part of the SQLite activity backend. Each PR deletes the listed functions in these files plus any test that only exercises them, then runs `go build ./... && go vet ./...` and the package tests. Re-check each function for reflection/JSON use before deleting. Size S unless marked.

| PR | package | funcs | lines | files touched (non-test) |
|---|---|---|---|---|
| P10 | `internal/database` | 40 | 505 | `internal/database/ai_scan_store.go`, `internal/database/author_bookref.go`, `internal/database/book_change_log.go`, `internal/database/book_own_folder.go`, `internal/database/book_sort.go`, `internal/database/catalog_entry_store.go`, `internal/database/chromem_embedding_store.go`, `internal/database/credits.go`, `internal/database/embedding_store.go`, `internal/database/fingerprint_window.go`, `internal/database/keyfamilies.go`, `internal/database/memdb_indexers.go`, `internal/database/memdb_search.go`, `internal/database/metadata_fetch_cache.go`, `internal/database/metadata_field_locks.go`, `internal/database/migrations.go`, `internal/database/pebble_file_provenance.go`, `internal/database/pebble_store.go`, `internal/database/pebble_store_atpath_markers.go`, `internal/database/scan_state.go`, `internal/database/settings.go`, `internal/database/storage_format.go`, `internal/database/store.go`, `internal/database/tag_helpers.go` |
| P11 | `internal/metadata` | 18 | 231 | `internal/metadata/assemble.go`, `internal/metadata/audible.go`, `internal/metadata/audnexus.go`, `internal/metadata/book_file_hashes.go`, `internal/metadata/book_name.go`, `internal/metadata/cover.go`, `internal/metadata/custom_tags.go`, `internal/metadata/enhanced.go`, `internal/metadata/folder_parser.go`, `internal/metadata/google_quota.go`, `internal/metadata/googlebooks.go`, `internal/metadata/hardcover.go`, `internal/metadata/metadata.go`, `internal/metadata/throttle.go`, `internal/metadata/wikipedia.go` |
| P12 | `internal/scanner` | 10 | 175 | `internal/scanner/ai_parse_async.go`, `internal/scanner/chapter_consolidator.go`, `internal/scanner/scanner.go` |
| P13 | `internal/server` | 14 | 174 | `internal/server/batch_apply_one.go`, `internal/server/file_io_pool.go`, `internal/server/path_locks.go`, `internal/server/search_index_testing.go`, `internal/server/search_reconciler.go`, `internal/server/server_helpers.go`, `internal/server/server_metadata.go`, `internal/server/server_title_helpers.go`, `internal/server/undo_engine.go` |
| P14 | `internal/errhandling` | 9 | 158 | `internal/errhandling/errhandling.go`, `internal/errhandling/skipcounter.go` |
| P15 | `internal/metafetch` | 12 | 155 | `internal/metafetch/batch.go`, `internal/metafetch/candidate_fallback.go`, `internal/metafetch/file_pipeline.go`, `internal/metafetch/service_normalize.go`, `internal/metafetch/service_scoring.go`, `internal/metafetch/service_writeback.go` |
| P16 | `internal/logger` | 18 | 133 | `internal/logger/logger.go`, `internal/logger/operation.go` |
| P17 | `internal/fileops` | 8 | 130 | `internal/fileops/copy.go`, `internal/fileops/hash.go`, `internal/fileops/safe_operations.go` |
| P18 | `internal/audioutil` | 7 | 89 | `internal/audioutil/drm.go`, `internal/audioutil/duration.go`, `internal/audioutil/mediainfo.go`, `internal/audioutil/timeline.go` |
| P19 | `internal/tagger` | 6 | 88 | `internal/tagger/embed_cover.go`, `internal/tagger/safe_write.go`, `internal/tagger/tagger.go` |
| P20 | `internal/authority` | 9 | 84 | `internal/authority/authority.go`, `internal/authority/lookup.go` |
| P21 | `internal/operations/freshness` | 9 | 75 | `internal/operations/freshness/freshness.go` |
| P22 | `internal/plugins/maintenance` | 4 | 68 | `internal/plugins/maintenance/combined_author_fixer.go`, `internal/plugins/maintenance/duration_backfill.go`, `internal/plugins/maintenance/fragment_consolidation_fixer.go`, `internal/plugins/maintenance/retire_into.go` |
| P23 | `internal/httputil` | 8 | 60 | `internal/httputil/parse.go`, `internal/httputil/rangeserve_gin.go`, `internal/httputil/types.go` |
| P24 | `internal/diagnosis` | 3 | 57 | `internal/diagnosis/probe.go` |
| P25 | `internal/audiobooks` | 5 | 49 | `internal/audiobooks/revert.go`, `internal/audiobooks/service_filtering.go` |
| P26 | `internal/fingerprint` | 5 | 47 | `internal/fingerprint/backfill_utils.go`, `internal/fingerprint/fpcalc.go`, `internal/fingerprint/tool_equivalence.go`, `internal/fingerprint/wholefile.go` |
| P27 | `internal/repairs` | 6 | 47 | `internal/repairs/guards.go`, `internal/repairs/owner.go` |
| P28 | `internal/backup` | 5 | 46 | `internal/backup/backup.go`, `internal/backup/codec.go` |
| P29 | `internal/operations` | 6 | 46 | `internal/operations/state.go`, `internal/operations/trigger_source.go` |
| P30 | `internal/pathutil` | 3 | 46 | `internal/pathutil/abbreviate.go`, `internal/pathutil/commondir.go` |
| P31 | `internal/organizer` | 3 | 42 | `internal/organizer/apply_failure.go`, `internal/organizer/pipeline.go`, `internal/organizer/service.go` |
| P32 | `internal/server/middleware` | 6 | 40 | `internal/server/middleware/absauth.go`, `internal/server/middleware/ratelimit.go` |
| P33 | `internal/util` | 6 | 39 | `internal/util/normalize.go`, `internal/util/perms.go`, `internal/util/pointers.go` |
| P34 | `internal/operations/registry` | 3 | 36 | `internal/operations/registry/live_tracker.go`, `internal/operations/registry/reporter_db.go`, `internal/operations/registry/types.go` |
| P35 | `internal/config` | 3 | 34 | `internal/config/protected_fields.go` |
| P36 | `internal/maintenance/jobs` | 1 | 30 | `internal/maintenance/jobs/dedup_books.go` |
| P37 | `internal/security/pathvalidation` | 1 | 30 | `internal/security/pathvalidation/pathvalidation.go` |
| P38 | `internal/sysinfo` | 1 | 29 | `internal/sysinfo/memory.go` |
| P39 | `internal/metadata/providerhttp` | 3 | 28 | `internal/metadata/providerhttp/providerhttp.go` |
| P40 | `internal/appdirs` | 1 | 22 | `internal/appdirs/appdirs.go` |
| P41 | `internal/mediainfo` | 1 | 22 | `internal/mediainfo/mediainfo.go` |
| P42 | `internal/server/handlers/abs` | 3 | 22 | `internal/server/handlers/abs/browse.go`, `internal/server/handlers/abs/cache_refresh.go`, `internal/server/handlers/abs/play.go` |
| P43 | `internal/openlibrary` | 2 | 20 | `internal/openlibrary/downloader.go`, `internal/openlibrary/types.go` |
| P44 | `internal/ai` | 3 | 19 | `internal/ai/embedding_client.go`, `internal/ai/embedding_scorer.go`, `internal/ai/llm_scorer.go` |
| P45 | `internal/server/handlers/operations` | 1 | 19 | `internal/server/handlers/operations/handler.go` |
| P46 | `internal/undo` | 2 | 19 | `internal/undo/restorable.go`, `internal/undo/revert_plan.go` |
| P47 | `internal/aidispatch` | 6 | 18 | `internal/aidispatch/capabilities.go`, `internal/aidispatch/dispatch.go` |
| P48 | `internal/matcher` | 1 | 18 | `internal/matcher/fuzzy.go` |
| P49 | `internal/realtime` | 2 | 17 | `internal/realtime/events.go` |
| P50 | `internal/server/handlers` | 3 | 17 | `internal/server/handlers/ai.go`, `internal/server/handlers/auth.go`, `internal/server/handlers/playlists.go` |
| P51 | `internal/serviceregistry` | 1 | 17 | `internal/serviceregistry/container.go` |
| P52 | `internal/syncapi/progress` | 1 | 17 | `internal/syncapi/progress/policy.go` |
| P53 | `internal/applygate` | 2 | 14 | `internal/applygate/evidence.go`, `internal/applygate/manual_only.go` |
| P54 | `internal/search` | 1 | 13 | `internal/search/index_builder.go` |
| P55 | `internal/franchise` | 1 | 12 | `internal/franchise/detect.go` |
| P56 | `internal/cache` | 1 | 11 | `internal/cache/registry.go` |
| P57 | `internal/scheduler` | 1 | 9 | `internal/scheduler/daily_at.go` |
| P58 | `internal/security/safepath` | 1 | 9 | `internal/security/safepath/safepath.go` |
| P59 | `internal/server/handlers/aibackends` | 1 | 9 | `internal/server/handlers/aibackends/aibackends.go` |
| P60 | `internal/policy` | 1 | 8 | `internal/policy/policy.go` |
| P61 | `internal/searchcache` | 1 | 8 | `internal/searchcache/cache.go` |
| P62 | `internal/activity` | 1 | 7 | `internal/activity/api.go` |
| P63 | `internal/catalog` | 1 | 7 | `internal/catalog/harvest.go` |
| P64 | `internal/merge` | 1 | 7 | `internal/merge/sync_follow.go` |
| P65 | `internal/personname` | 1 | 7 | `internal/personname/personname.go` |
| P66 | `internal/metabatch` | 1 | 6 | `internal/metabatch/upgrade.go` |
| P67 | `internal/metrics` | 1 | 6 | `internal/metrics/pipeline_metrics.go` |
| P68 | `internal/operations/opmode` | 1 | 5 | `internal/operations/opmode/dryrun.go` |
| P69 | `internal/applycap` | 1 | 2 | `internal/applycap/applycap.go` |
| P70 | `internal/audioext` | 1 | 2 | `internal/audioext/audioext.go` |
| P71 | `internal/versionprimary` | 1 | 2 | `internal/versionprimary/rank.go` |

**Total:** 279 funcs, 3162 lines, 62 packages. `internal/database` and `internal/server` are M (largest file sets) and must also list `internal/database/mock_store.go` / `internal/database/mocks/mock_store.go` when a removed func is a store method.
