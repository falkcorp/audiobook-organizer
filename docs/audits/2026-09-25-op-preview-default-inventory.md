<!-- file: docs/audits/2026-09-25-op-preview-default-inventory.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7b3e9d51-4a2c-4f86-b1d7-6e0a8c2f5d93 -->
<!-- last-edited: 2026-09-25 -->

# Operation preview-by-default inventory (2026-09-25)

Owner decision, 2026-09-25: *"Operations make two modes available: preview way and non-preview for
automated jobs ... so preview and dry run by default."* Every operation that writes runs as a PREVIEW
when its request does not state a mode; a LIVE run needs the explicit flag (`dry_run: false`, or the
camelCase alias `dryRun: false`). Automated callers that must stay live pass the flag themselves.

Branch `fix/ops-preview-by-default`. Shared resolver: `internal/operations/opmode`
(`ResolveDryRun`, `ParseDryRun`; the generalization of `parseAuthorOpDryRun`).

## How the list was built

- Registered op IDs: `NewServer(mockStore).opRegistry.ActiveDefs()` (190 defs, the same boot
  `TestNewServer_RegistersOpsWithEmptyRootDir` uses), plus every `ID:` literal under `internal/plugins`
  for plugins that the test boot does not enable (acoustid, dedup, deluge, itunes stubs, metafetch).
  Maintenance jobs register as `maintenance.<job-id>` (38 jobs).
- Mode flags: every struct field tagged `dry_run`, `dryRun`, `apply`, `live` in `internal/` and `pkg/`
  (other names searched and not found as op modes: `execute`, `preview_only`, `commit`, `mode` except
  series-phantom-repair, `confirm` which is library.import's circuit-breaker bypass, not a mode).
- "`{}` today" was read from each op's decode + resolution code, not from comments.

## Correction to the naming audit

`docs/audits/2026-09-25-interface-naming-consistency.md` named tag_backfill, itunes_playlist_import,
itunes_regroup, booksig_sidecar_migrate, fs_regroup_xml, booksig_recovery_audit, title_backfill and
rewrite_path_prefix as live on an omitted flag. None of the eight was: the first seven pre-filled
`DryRun: true` before `json.Unmarshal` (which leaves absent keys untouched), and rewrite_path_prefix was
already `*bool` with `== nil ||`. They were still converted (section 2) because they accepted only
`dryRun`, so a caller sending `dry_run: false` got a silent preview, and a plain bool pre-fill is one
refactor from Go's zero value. The ops that really ran live on `{}` are in section 1.

## 1. Converted: `{}` ran LIVE before this change

| Op | Before (`{}`) | After (`{}`) | Where |
|---|---|---|---|
| `itunes.path-repair` | LIVE -- plain `DryRun bool`; rewrites locations in the live iTunes library | preview | `internal/server/itunes_path_ops.go` |
| `dedup.split-book-bulk-merge` | LIVE when `items` sent without `dry_run` -- merges + soft-deletes books (`{}` alone errors: no items) | preview | `internal/dedup/split_book_merge.go`, `internal/plugins/dedup/split_book_bulk_merge.go` |
| `maintenance.backfill-file-hashes` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/backfill_file_hashes.go` |
| `maintenance.backfill-itunes-positions` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/backfill_itunes_positions.go` |
| `maintenance.backfill-metadata-source-hash` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/backfill_metadata_source_hash.go` |
| `maintenance.backfill-sync-ids` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/backfill_sync_ids.go` |
| `maintenance.cleanup-backups` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/cleanup_backups.go` |
| `maintenance.enrich-book-files` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/enrich_book_files.go` |
| `maintenance.recompute-itunes-paths` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/recompute_itunes_paths.go` |
| `maintenance.sweep-pebble-metrics-ttl` | LIVE -- `DefaultParams` advertised `dry_run:false`, which the dispatcher and v2 Run closure apply on omission | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/sweep_pebble_metrics_ttl.go` |

10 ops. Each of the 8 jobs honors `dryRun` in `Run` (checked: every write is behind `!dryRun`).

## 2. Converted for spelling and the guard: `{}` already previewed

`DryRun bool` pre-filled with true -> `*bool` `dryRun` + `*bool` `dry_run` alias through `opmode.ResolveDryRun`.
Field names unchanged. Behavior change: `dry_run: false` (snake) now applies where it was silently ignored.
No caller sends it to these ops today (grep of `internal/`, `web/src`, `scripts/`).

| Op | Pre-fill that made `{}` safe (proposed-main before this change) |
|---|---|
| `maintenance.itunes-regroup` | `itunes_regroup.go:61` |
| `maintenance.itunes-playlist-import` | `itunes_playlist_import.go:96` |
| `maintenance.tag-backfill` | `tag_backfill.go:173` |
| `maintenance.booksig-sidecar-migrate` | `booksig_sidecar_migrate.go:101` |
| `maintenance.fs-regroup-xml` | `fs_regroup_xml.go:200` |
| `maintenance.booksig-recovery-audit` | `booksig_recovery_audit.go:126` |
| `maintenance.title-backfill` | `title_backfill.go:54` |
| `operations.backfill-legacy-status` | `internal/server/legacy_backfill_op.go:57` |

## 3. Already preview by default, unchanged (except where noted)

### `*bool` / `live` resolution

| Op | Mechanism |
|---|---|
| `acoustid.fingerprint-duration-repair` | `live bool` -- omitted = preview |
| `acoustid.window-backfill` | `live bool` -- omitted = preview |
| `dedup.series-dedup` | `*bool`, `== nil ||` (internal/server/duplicates_ops.go:519) |
| `maintenance.activity-reclaim` | `*bool`, `dryRun := true` unless set (activity_reclaim.go:80) |
| `maintenance.author-conjunction-repair` | `*bool`, `== nil ||` (author_conjunction_repair.go:141) |
| `maintenance.author-duplicate-merge` | `*bool`, `== nil ||` (author_duplicate_merge.go:132) |
| `maintenance.author-id-repair` | `*bool` both spellings; now opmode.ResolveDryRun |
| `maintenance.author-path-link` | `*bool` both spellings; now opmode.ResolveDryRun |
| `maintenance.author-split-scan` | opmode.ParseDryRun (was parseAuthorOpDryRun) |
| `maintenance.auto-match-transcribed` | `*bool`, `== nil ||` (auto_match_transcribed.go:77) |
| `maintenance.duration-backfill` | `*bool` both spellings, default true (duration_backfill.go:488); not moved onto opmode (another agent owns the file) |
| `maintenance.intro-migrate-single-file` | `*bool`, `== nil ||` (intro_migrate_single_file.go:128) |
| `maintenance.repair-library-state` | `*bool`, `== nil ||` (library_state_repair.go:113) |
| `maintenance.repair-transcribe-status` | `*bool`, `== nil ||` (repair_transcribe_status.go:222) |
| `maintenance.repoint-missing-to-folder-audio` | `*bool` both spellings; now opmode.ResolveDryRun |
| `maintenance.resolve-production-authors` | opmode.ParseDryRun (was parseAuthorOpDryRun) |
| `maintenance.rewrite-path-prefix` | `*bool`, `== nil ||` (rewrite_path_prefix.go:155) -- the audit listed it as unsafe; it was not |
| `maintenance.series-phantom-repair` | mode defaults to `report`; repair modes `*bool` default true (series_phantom_repair.go:211) |
| `maintenance.transcribe-book-intros` | `*bool`, `== nil ||` (intro_transcribe.go:166) |
| `scheduler.author-split-scan` | opmode.ParseDryRun (was parseSchedulerAuthorDryRun); scheduled run previews by owner decision |
| `scheduler.resolve-production-authors` | opmode.ParseDryRun (was parseSchedulerAuthorDryRun); scheduled run previews by owner decision |

### `apply bool` -- omitted = preview (field name kept)

`dedup.auto-resolve`, `dedup.bookfile-seg-drop`, `dedup.breakdown-backfill`, `dedup.build-isbn-index`, `dedup.calibrate-composite`, `dedup.cleanup-orphan-author-embeddings`, `dedup.cleanup-orphan-embeddings`, `dedup.dataset-backfill`, `dedup.drain-stale`, `dedup.emb-reencode`, `dedup.mine-gold-labels`, `dedup.purge-legacy-fp-candidates`, `dedup.quarantine-chapter-artifacts`, `dedup.rebuild-gold-labels`, `dedup.reembed-embeddings`, `dedup.rescore`, `dedup.rescore-labeled-examples`, `maintenance.author-strip-merge`, `maintenance.build-folder-book-files`, `maintenance.chapters-backfill`, `maintenance.clear-apply-rename-failures`, `maintenance.dedup-exact-triage`, `maintenance.dedupe-book-file-rows`, `maintenance.file-provenance-capture`, `maintenance.file-provenance-export`, `maintenance.itunes-clone-into-library`, `maintenance.mark-missing-files`, `maintenance.merge-same-path-dupes`, `maintenance.metadata-cache-reap`, `maintenance.missing-file-repair`, `maintenance.missing-file-repoint`, `maintenance.probe-directory-books`, `maintenance.purge-empty-authors`, `maintenance.purge-empty-narrators`, `maintenance.recover-missing-files`, `maintenance.relink-unlinked-books`, `maintenance.repair-junk-titles`, `maintenance.repoint-unrecorded-renames`, `maintenance.review-status-index-repair`, `maintenance.series-denumber`, `maintenance.split-joined-narrators`, `maintenance.title-repair`, `maintenance.version-group-primary-repair`.

43 ops.

### Maintenance jobs advertising `dry_run:true` before this change

`backfill-book-files`, `bulk-deluge-import`, `cleanup-empty-folders`, `cleanup-organize-mess`, `cleanup-series`,
`dedup-books`, `fix-author-narrator-swap`, `fix-book-file-paths`, `fix-file-modes`, `fix-read-by-narrator`,
`fix-version-groups`, `merge-chapter-groups`, `normalize-primary-flags`, `prune-book-snapshots`,
`purge-unknown-author-duplicates`, `recompute-book-aggregates`, `refetch-missing-authors`,
`relink-missing-to-itunes`, `repair-missing-files`, `repoint-version-primary`, `retention-and-hygiene`,
`scan-composer-tags` (22). With section 1, 30 of 38 jobs now preview on omission.

### HTTP entry resolves the mode before choosing an op

- `metadata.batch-apply-cached` / `metadata.bulk-apply-preview`: `POST .../batch-apply` takes `dry_run *bool`,
  default true, and enqueues the preview op unless `dry_run:false` (`handlers/metadata_cache.go:763`).
- `metadata.candidate-fetch` apply path: `BatchApplyRequest.DryRun *bool`, default true (`metadata_batch_candidates.go:571`).

## 4. Maintenance jobs with no preview mode (not flipped)

Advertising `dry_run:true` for a job whose `Run` ignores it would be worse than false: the UI would offer
a "preview" that writes. Listed in `noPreviewMode` in `internal/maintenance/jobs/preview_default_guard_test.go`.

| Job | Advertises | Why |
|---|---|---|
| `relink-report` | `dry_run:false` | read-only report; `Run(..., _ bool)` |
| `scan-duplicate-files` | `dry_run:false` | read-only scan; `Run(..., _ bool)` |
| `scan-duration-mismatch` | `dry_run:false` | read-only scan; `Run(..., _ bool)` |
| `scan-metadata-hash-dups` | `dry_run:false` | read-only scan; `Run(..., _ bool)` |
| `bulk-fetch-metadata` | no key | fetches and applies metadata; no preview implemented |
| `generate-itl-tests` | no key | writes test fixtures |
| `revert-metadata-fetch` | no key | reverts only the `fetch_op_ids` it is given |
| `scan-chapter-groups` | no key | read-only; its writing twin merge-chapter-groups previews by default |

## 5. Ops with no mode flag at all

These run their only mode on `{}`. Giving a writing op a preview mode is a feature per op, not a flag
conversion, so none were changed here. **This is the larger remaining gap against the owner's rule.**
`writes` = declares `library.write`; `?` = plugin not enabled in the test boot, so capabilities were not read. The column is the DECLARED capability, not a code audit (for example `maintenance.temp-file-cleanup` deletes temp files and declares no `library.write`).

| Op | writes |
|---|---|
| `acoustid.backfill` | ? |
| `acoustid.fingerprint-rescan` | ? |
| `acoustid.lookup-online` | ? |
| `acoustid.lsh-backfill` | ? |
| `acoustid.reset-all` | ? |
| `acoustid.scan` | ? |
| `ai.author-merge-apply` | yes |
| `ai.author-review` | yes |
| `ai.author-scan` | no |
| `dedup.author-scan` | no |
| `dedup.book-merge` | yes |
| `dedup.book-scan` | no |
| `dedup.book-signature-scan` | ? |
| `dedup.build-candidate-status-index` | ? |
| `dedup.calibrate-embedding-thresholds` | ? |
| `dedup.check-book` | ? |
| `dedup.embed-async` | ? |
| `dedup.embed-scan` | ? |
| `dedup.full-scan` | ? |
| `dedup.llm-review` | ? |
| `dedup.lsh-index-build` | ? |
| `dedup.purge-stale` | ? |
| `dedup.series-merge` | yes |
| `dedup.series-normalize` | yes |
| `dedup.series-prune` | yes |
| `dedup.series-scan` | no |
| `dedup.split-book-scan` | ? |
| `deluge.centralize` | ? |
| `deluge.path-update` | ? |
| `deluge.protected-paths-sync` | ? |
| `diagnostics.ai-analyze` | no |
| `diagnostics.export` | no |
| `entities.author-merge` | yes |
| `entities.resolve-production-author` | yes |
| `entities.series-rename` | yes |
| `itunes.import` | yes |
| `itunes.path-reconcile` | yes |
| `itunes.position-sync` | ? |
| `itunes.sync` | yes |
| `library.ai-parse` | yes |
| `library.bulk-metadata-fetch` | no |
| `library.bulk-write-back` | yes |
| `library.folder-auto-scan` | yes |
| `library.import` | yes |
| `library.optimize` | yes |
| `library.organize` | yes |
| `library.scan` | yes |
| `library.size-refresh` | no |
| `library.transcode` | yes |
| `maintenance.activity-filter-index-backfill` | yes |
| `maintenance.ai-dedup-batch` | yes |
| `maintenance.author-dedup-scan` | no |
| `maintenance.author-title-fragment-scan` | no |
| `maintenance.author-whitespace-collision-report` | no |
| `maintenance.batch-poller` | yes |
| `maintenance.book-atpath-index-backfill` | yes |
| `maintenance.book-atpath-index-verify` | no |
| `maintenance.book-shape-report` | no |
| `maintenance.bulk-write-back` | yes |
| `maintenance.cleanup-activity-log` | yes |
| `maintenance.cleanup-old-backups` | no |
| `maintenance.compact-activity-log` | yes |
| `maintenance.db-optimize` | yes |
| `maintenance.dedup-llm-review` | no |
| `maintenance.external-id-backfill` | yes |
| `maintenance.extract-wav-clips` | no |
| `maintenance.file-integrity-check` | no |
| `maintenance.filepath-collision-report` | no |
| `maintenance.isbn-enrichment` | yes |
| `maintenance.itunes-heal` | no |
| `maintenance.malformed-m4b-remux` | no |
| `maintenance.malformed-m4b-transcode` | no |
| `maintenance.metadata-refresh` | yes |
| `maintenance.metadata-upgrade` | yes |
| `maintenance.missing-file-audit` | no |
| `maintenance.movement-atom-cleanup` | no |
| `maintenance.nightly-compact-activity-log` | yes |
| `maintenance.optimize-activity-db` | yes |
| `maintenance.orphan-book-files-cleanup` | no |
| `maintenance.orphan-book-files-repoint-plan` | no |
| `maintenance.orphan-row-blocking-probe` | yes |
| `maintenance.prune-ai-journal` | yes |
| `maintenance.purge-deleted` | yes |
| `maintenance.purge-old-logs` | yes |
| `maintenance.recompact-activity-digests` | yes |
| `maintenance.reconcile-scan` | no |
| `maintenance.regroup-shattered-ai` | no |
| `maintenance.series-normalize` | yes |
| `maintenance.series-prune` | yes |
| `maintenance.temp-file-cleanup` | no |
| `maintenance.tombstone-cleanup` | yes |
| `maintenance.trash-cleanup` | yes |
| `maintenance.unknown-author-audit` | no |
| `maintenance.version-group-primary-report` | no |
| `maintenance.window` | yes |
| `metadata.batch-save` | yes |
| `metadata.candidate-fetch` | yes |
| `metafetch.calibrate-scoring` | ? |
| `openlibrary.download` | no |
| `openlibrary.import` | yes |
| `reconcile.apply` | yes |
| `reconcile.scan` | no |
| `scheduler.cleanup-old-backups` | yes |
| `scheduler.db-optimize` | yes |
| `scheduler.dedup-llm-review` | yes |
| `scheduler.isbn-enrichment` | yes |
| `scheduler.metadata-refresh` | yes |
| `scheduler.metadata-upgrade` | yes |
| `scheduler.purge-deleted` | yes |
| `scheduler.temp-file-cleanup` | yes |
| `scheduler.tombstone-cleanup` | yes |
| `scheduler.trash-cleanup` | yes |

112 ops. (`maintenance.test-probe-*` are registered only by `internal/server` tests and are excluded.)

## 6. Automated callers

Only callers of ops whose omitted-mode behavior CHANGED need `dry_run:false`. Those ops are the 10 in
section 1 and, for a caller sending snake `dry_run`, the 8 in section 2.

| Caller class | Checked | Changed |
|---|---|---|
| Scheduler (`internal/scheduler/tasks.go`, 31 `EnqueueOp` sites; `maintenance.window` task map in `maintenance.go:174`) | none target a section 1/2 op | 0 |
| Op `Schedule` fields | every section 1/2 op has `Schedule: nil`; maintenance jobs have no schedule | 0 |
| Go enqueues from other ops/services (all 87 non-mock `EnqueueOp` call sites, incl. `optimize.go` children, `lsh_index_build`, `rescore_op`, importer) | none target a section 1/2 op | 0 |
| Startup hooks (`server_lifecycle.go`, `server.go`) | enqueue library.scan / acoustid.backfill only | 0 |
| Direct maintenance job runs | only `maintenance_dispatcher.go` (HTTP) and the v2 Run closure call jobs | 0 |
| Retry / resume | copy `row.Params` verbatim; the dispatcher always persists the RESOLVED `dry_run`, so a pre-deploy live run resumes live | 0 |
| `scripts/` | no script targets a section 1/2 op | 0 |

Deliberately NOT changed: `scheduler.author-split-scan` and `scheduler.resolve-production-authors` are
enqueued with `schedulerExtraOpParams{}` and preview by the owner's earlier decision; adding `dry_run:false`
would reverse it.

### Frontend (`web/src`)

| Call | Sends | Result after this change | Changed |
|---|---|---|---|
| `MaintenanceTab` Manual Fixes "Run" (`runMaintenanceJob(job.id)`) | no `dry_run` | the 8 flipped jobs now take the existing Preview -> confirm -> Apply flow (`advertisesDryRun`) | no |
| `MaintenanceTab` Manual Fixes Apply (`runMaintenanceJob(jobId, false)`) | `dry_run:false` | live, as labeled | no |
| `MaintenanceTab` backfill-file-hashes button (`runMaintenanceJob('backfill-file-hashes', false)`, `backfillFileHashes(false)`) | `dry_run:false` | live, as labeled | no |
| `MaintenanceTab` metadata-hash backfill (`backfillMetadataHashes(false)`) | `dry_run:false` | live, as labeled | no |
| `DedupSplitBookTab` bulk merge -> `POST /dedup/split-book-candidates/bulk-merge` | handler resolves `*bool` default true and now enqueues `&dryRun` | unchanged | no |

Frontend calls changed: 0. No `web/src` code sends snake `dry_run` through the generic
`/operations/trigger` route to a section 2 op.

## 7. Open: HTTP endpoints outside the op registry (owner decision)

The rule was stated for operations. These endpoints write directly (or pick an op) and default LIVE or
near-live on an omitted flag. Flipping them changes what existing UI buttons do, so they are listed,
allowlisted in `internal/operations/opmode/guard_test.go`, and not changed.

| Endpoint | Omitted `dry_run` | Where |
|---|---|---|
| `POST /discovery/import` (Deluge bulk import) | LIVE; bind error also swallowed (`_ = c.ShouldBindJSON`) | `internal/server/deluge_discovery.go:119` |
| `POST /audiobooks/bulk-write-back` | LIVE: enqueues `library.bulk-write-back` | `internal/server/handlers/metadata/handler.go:1448` |
| `POST /itunes/pid-repair` | LIVE unless `?dry_run=true` or body true | `internal/server/itl_pid.go:42` |
| `POST /itunes/rebuild`, `/itunes/rebuild-full` | LIVE unless `?dry_run=true` (query only) | `internal/server/itl_rebuild.go:63` |
| `POST /maintenance/wipe` | preview (pre-filled true; also needs `confirm:"WIPE"`) | `internal/server/maintenance_fixups.go:110` |

## 8. Guards

- `internal/operations/opmode/guard_test.go` `TestGuard_NoPlainBoolDryRunParams`: AST scan of `internal/` and
  `pkg/`; any plain `bool` field tagged `dry_run`/`dryRun` outside an output type (`*Result`, `*Report`, ...),
  a job `DefaultParams`, or the reasoned allowlist fails. Stale allowlist entries fail too.
  Mutation check: `title_backfill.go` field reverted to `DryRun bool `json:"dryRun"`` -> FAIL naming
  `title_backfill.go:26 titleBackfillParams.DryRun`; restored -> PASS.
- `internal/maintenance/jobs/preview_default_guard_test.go` `TestMaintenanceJobs_PreviewByDefault`: iterates
  `maintenance.All()`; every job advertises `dry_run:true` or is in `noPreviewMode` with a reason.
  Mutation check: `cleanup-backups` reverted to `dry_run:false` -> FAIL; restored -> PASS.
- Wiring: `TestOps_DryRunRoutesThroughOpmode` (maintenance plugin, 11 ops), `TestServerOps_DryRunRoutesThroughOpmode`
  (itunes.path-repair, backfill-legacy-status, through the real registered Run), and
  `TestSplitBookBulkMerge_ModeRoutesThroughOpmode`: a body with disagreeing `dry_run`/`dryRun` is refused at the
  mode check, proving both spellings reach the resolver. `opmode` unit tests pin `{}` -> preview.

The registry cannot be iterated for this generically: `OperationDef.Run` takes raw JSON and each op decodes
its own struct, and the test boot does not enable every plugin. The source scan covers every op regardless.

## 9. Follow-ups

- Move `maintenance.duration-backfill`'s inline resolution onto `opmode.ResolveDryRun` (skipped: another
  agent is editing the duration files on proposed-main).
- Owner decision on the section 7 endpoints.
- Section 5: decide which writing ops without a preview mode need one.
