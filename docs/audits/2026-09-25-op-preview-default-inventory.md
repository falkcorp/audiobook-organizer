<!-- file: docs/audits/2026-09-25-op-preview-default-inventory.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7b3e9d51-4a2c-4f86-b1d7-6e0a8c2f5d93 -->
<!-- last-edited: 2026-09-25 -->

# Operation preview-by-default inventory (2026-09-25)

Owner decision, 2026-09-25: *"Operations make two modes available: preview way and non-preview for
automated jobs ... so preview and dry run by default."* Every operation that writes runs as a PREVIEW
when its request does not state a mode; a LIVE run needs the explicit flag (`dry_run: false`, or the
camelCase alias `dryRun: false`). Automated callers that must stay live pass the flag themselves.

Revised 2026-09-25 after an adversarial review of the first pass (branch `fix/ops-preview-review-2`):
three more ops were live on omission (section 1, last three rows), the maintenance-job family did not
honor `dryRun` or refuse a disagreement (now it does, through `opmode.ResolveDryRunDefault`), and the
guards in section 8 could not fail for most new writing ops (now three more guards).

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
| `dedup.rescore` | LIVE -- pre-filled `RescoreParams{Apply: true}` before decode, so `{}`, `null` and a requeue re-banded every pending candidate (the first pass listed it wrongly under `apply bool` -- omitted = preview) | preview | `internal/plugins/dedup/rescore_op.go` |
| `maintenance.revert-metadata-fetch` | LIVE -- no `dry_run` key advertised, so a request naming `fetch_op_ids` without `dry_run` reverted via `ModifyBook` (the first pass listed it wrongly as having no preview mode) | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/revert_metadata_fetch.go` |
| `maintenance.generate-itl-tests` | LIVE -- no key advertised; a live run `RemoveAll`s its output dir (the first pass listed it wrongly as having no preview mode) | preview (advertises `dry_run:true`) | `internal/maintenance/jobs/generate_itl_tests.go` |

13 ops. Each of the 10 jobs honors `dryRun` in `Run` (checked: every write is behind `!dryRun`).
The only automated caller of `dedup.rescore` (the config-PUT sink, `dedupScoreSink`) already enqueues
`Apply: true`; the two jobs have no automated caller.

## 2. Converted for spelling and the guard: `{}` already previewed

`DryRun bool` pre-filled with true -> `*bool` `dryRun` + `*bool` `dry_run` alias through `opmode.ResolveDryRun`.
Field names unchanged. Behavior change: `dry_run: false` (snake) now applies where it was silently ignored.
No caller sends it to these ops today (grep of `internal/`, `web/src`, `scripts/`).

| Op | Pre-fill that made `{}` safe (proposed-main before this change) |
|---|---|
| `itunes.regroup` | `itunes_regroup.go:61` |
| `itunes.playlist-import` | `itunes_playlist_import.go:96` |
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

`dedup.auto-resolve`, `dedup.bookfile-seg-drop`, `dedup.breakdown-backfill`, `dedup.build-isbn-index`, `dedup.calibrate-composite`, `dedup.cleanup-orphan-author-embeddings`, `dedup.cleanup-orphan-embeddings`, `dedup.dataset-backfill`, `dedup.drain-stale`, `dedup.emb-reencode`, `dedup.mine-gold-labels`, `dedup.purge-legacy-fp-candidates`, `dedup.quarantine-chapter-artifacts`, `dedup.rebuild-gold-labels`, `dedup.reembed-embeddings`, `dedup.rescore-labeled-examples`, `maintenance.author-strip-merge`, `maintenance.build-folder-book-files`, `maintenance.chapters-backfill`, `maintenance.clear-apply-rename-failures`, `maintenance.dedup-exact-triage`, `maintenance.dedupe-book-file-rows`, `maintenance.file-provenance-capture`, `maintenance.file-provenance-export`, `itunes.clone-into-library`, `maintenance.mark-missing-files`, `maintenance.merge-same-path-dupes`, `maintenance.metadata-cache-reap`, `maintenance.missing-file-repair`, `maintenance.missing-file-repoint`, `maintenance.probe-directory-books`, `maintenance.purge-empty-authors`, `maintenance.purge-empty-narrators`, `maintenance.recover-missing-files`, `maintenance.relink-unlinked-books`, `maintenance.repair-junk-titles`, `maintenance.repoint-unrecorded-renames`, `maintenance.review-status-index-repair`, `maintenance.series-denumber`, `maintenance.split-joined-narrators`, `maintenance.title-repair`, `maintenance.version-group-primary-repair`.

42 ops. (`dedup.rescore` was listed here in the first pass; it pre-filled `Apply: true` and is now in
section 1. `TestGuard_NoLiveDefaultPrefill` checks the rest of this list for the same pre-fill and
found none.)

### Maintenance jobs advertising `dry_run:true` before this change

`backfill-book-files`, `bulk-deluge-import`, `cleanup-empty-folders`, `cleanup-organize-mess`, `cleanup-series`,
`dedup-books`, `fix-author-narrator-swap`, `fix-book-file-paths`, `fix-file-modes`, `fix-read-by-narrator`,
`fix-version-groups`, `merge-chapter-groups`, `normalize-primary-flags`, `prune-book-snapshots`,
`purge-unknown-author-duplicates`, `recompute-book-aggregates`, `refetch-missing-authors`,
`relink-missing-to-itunes`, `repair-missing-files`, `repoint-version-primary`, `retention-and-hygiene`,
`scan-composer-tags` (22). With section 1, 32 of 38 jobs now preview on omission.

### HTTP entry resolves the mode before choosing an op

- `metadata.batch-apply-cached` / `metadata.bulk-apply-preview`: `POST .../batch-apply` takes `dry_run *bool`,
  default true, and enqueues the preview op unless `dry_run:false` (`handlers/metadata_cache.go:763`).
- `metadata.candidate-fetch` apply path: `BatchApplyRequest.DryRun *bool`, default true (`metadata_batch_candidates.go:571`).

## 4. Maintenance jobs with no preview mode (not flipped)

Advertising `dry_run:true` for a job whose `Run` ignores it would be worse than false: the UI would offer
a "preview" that writes. Listed in `noPreviewMode` in `internal/maintenance/jobs/preview_default_guard_test.go`.
Every job here has a `Run` that never reads its `dryRun` parameter;
`TestMaintenanceJobs_NoPreviewModeJobsIgnoreDryRun` parses the package and fails if one starts to.

| Job | Advertises | Why |
|---|---|---|
| `relink-report` | `dry_run:false` | read-only report; `Run(..., _ bool)` |
| `scan-duplicate-files` | `dry_run:false` | read-only scan; `Run(..., _ bool)` |
| `scan-duration-mismatch` | `dry_run:false` | read-only scan; `Run(..., _ bool)` |
| `scan-metadata-hash-dups` | `dry_run:false` | read-only scan; `Run(..., _ bool)` |
| `bulk-fetch-metadata` | no key | fetches and applies metadata; `Run` names `dryRun` but never reads it |
| `scan-chapter-groups` | no key | read-only; `Run(..., _ bool)`; its writing twin merge-chapter-groups previews by default |

`generate-itl-tests` and `revert-metadata-fetch` were listed here in the first pass. Both `Run` bodies
branch on `dryRun`, so both had a working preview and defaulted live; they are in section 1 now.

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
| `maintenance.library-optimize` | yes |
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
| `dedup.llm-review` | no |
| `maintenance.external-id-backfill` | yes |
| `maintenance.extract-wav-clips` | no |
| `maintenance.file-integrity-check` | no |
| `maintenance.filepath-collision-report` | no |
| `maintenance.isbn-enrichment` | yes |
| `itunes.heal` | no |
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
| Go test fixtures | `tag_backfill_test.go` built params from the zero `tagBackfillParams{}`, which marshaled to `dryRun:false` (live); 18 sites now pass `DryRun: boolPtr(false)`. `maintenance_dryrun_default_test.go` pinned `backfill-file-hashes` as the live-default real job; now pins it true and `relink-report` false | 2 files |

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

Added by the review pass:

- `TestGuard_NoPlainBoolDryRunParams` now also checks `dryrun`, `dry`, `preview`, `preview_only` and
  `previewOnly`, so a plain bool cannot pass by using another spelling of the preview flag.
- `internal/operations/opmode/guard_test.go` `TestGuard_NoLiveDefaultPrefill`: AST scan for a params value
  pre-filled live before `json.Unmarshal`/`Decode` into it (`Apply`/`Live`/`Commit`/`Execute: true`, or
  `DryRun`/`DryRunCamel`/`DryRunSnake`/`Preview: false` or a `Live()` pointer). Mutation check:
  `rescore_op.go` pre-fill restored -> FAIL naming `rescore_op.go:143 func runRescore pre-fills Apply`;
  reverted -> PASS.
- `internal/maintenance/jobs/preview_default_guard_test.go` `TestMaintenanceJobs_NoPreviewModeJobsIgnoreDryRun`:
  every `noPreviewMode` job's `Run` must not read `dryRun`. Mutation check: `revert-metadata-fetch` re-added
  -> FAIL; removed -> PASS.
- `internal/server/write_op_modes_test.go` `TestWriteOps_EveryWriteOpHasARecordedMode`: every registered op
  that declares `library.write`, `files.write` or `db.migrate` (or no capabilities) must be recorded in
  `internal/server/testdata/write_op_modes.golden` as `preview` or `no-mode`. There is no `live` class, so a
  new writing op fails until its author gives it a preview mode or records it as a known gap in review.
  Maintenance-job ops are left to the jobs guard. Mutation check: `dedup.rescore` line removed -> FAIL; restored -> PASS.
- Maintenance-job family: `TestRunMaintenanceJob_CamelDryRunIsHonored`,
  `TestRunMaintenanceJob_ConflictingDryRunSpellingsAreRefused`, `TestMaintenanceJobOp_CamelDryRunIsHonored`,
  `TestMaintenanceJobOp_ConflictingDryRunSpellingsFail` (`internal/server/maintenance_dryrun_alias_test.go`).

What the guards still cannot prove: that an op recorded as `preview` really previews on `{}` when it has no
params struct the source scans can read. That is a behavioural property; the ledger makes it a reviewed
decision rather than a default.

## 9. Follow-ups

- Move `maintenance.duration-backfill`'s inline resolution onto `opmode.ResolveDryRun` (skipped: another
  agent is editing the duration files on proposed-main).
- Owner decision on the section 7 endpoints.
- Section 5: decide which writing ops without a preview mode need one.
