<!-- file: docs/agent-tasks/platform-core/F7-09-f7-flip-integrations.md -->
<!-- version: 1.1.6 -->
<!-- guid: 92d3d884-998e-42de-94e1-451dd63836fb -->
<!-- last-edited: 2026-11-05 -->

# F7-09 — F7 flip, one PR: integrations, AI jobs, provider gates, library roots and notify wired, M6 registered, internal/plugin and the webhook plugin deleted, deliveries in core's format, the AI families renamed with their dashboards, the late-owner holds and poll on their native bodies

| Field | Value |
|---|---|
| ID | F7-09 |
| Release | R2, flip F7 (the flip PR) |
| Tier | **T4** (Opus / xhigh–max): every flip PR is T4; this one moves about fifteen packages and carries M6 |
| Wave | W37 (alone: the only PR of its wave; it owns `main.go`, `internal/server/`, books' registration and the kernel and module wiring files in W37). Runs inside an announced **move window** |
| Depends on | F6-03, F7-02, F7-03, F7-04, F7-05, F7-06, F7-07, F7-08, F7-10 |
| Owns gate tests | `TestBooksIndexRebuiltAtCutover` (the search oracle; F7-08 landed its dark half) |
| Spec | 09 §9.4 R2 flips table row F7, §9.6 rows 5, 15 and 16, §9.2.3 (M6), §9.2.7 (the AI renames); 03 §3.8 F7; 05-notify §10; 05-ai §8, §10; 05-library §5L.10; 05-metadata M1.10, M2.10, M3.10; 05-download §10; 05-storage §5S.3.8; 05-metrics §13 item 3; 05-tasks §5T.8.2 (the late-owner kinds `core.integrations.release-holds` and `core.ai.batch-poll`); 05-http-api §8.1 (F7 row: `/api/v1/plugins` deleted); 04 §8.1 F7, §8.3 (the `internal/plugin` deletion gate); 06 §6.4.10 F7 row |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **One atomic PR** (09 §9.2.1): it wires the
subsystems, registers M6, deletes what they replace and switches every first-party caller in the same PR. It merges
alone in W37. The R2 freeze is on: do not touch `.github/workflows/prerelease.yml`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-09-flip" -b agent/platform-core-f7-09-flip origin/main
cd "$REPO/.worktrees/pc-f7-09-flip"
git rebase origin/main
npm ci --prefix web
```

Announce the move window one working day ahead, naming every 04 row listed under "04 keys" below.

## Goal (09 §9.4 R2 flips table, row F7; 06 §6.4.10 F7 row)

1. **Wire** in the kernel and books: add the F7 members to `core/module/manifest.go`, `registrar.go` and `deps.go`
   (`Manifest.Requires`, `Manifest.Library`; `Registrar.Adapter`, `Registrar.AICapability`, `Registrar.DerivedIndex`;
   `Deps.Storage.Indexes`, `Deps.Storage.Derived`, `Deps.Library`, `Deps.HTTP`, `Deps.Integrations`, `Deps.Downloads`,
   `Deps.AI`, `Deps.Notify`) and their boot calls in `core/kernel/boot.go` and `kernel.go`: the derived-index P7
   validation (06 `ProblemDerivedIndex`) and the version/generation check, `Load` and the rebuild enqueue at Run start
   step (3a) over F7-07's `storage.Derived`/`DerivedIndex` types (05-storage §5S.3.9 "When the hooks run"); the
   `storage/index` `Opener` into `StorageHandles`; `app.Options.Adapters` in ABO's `main` (`deluge.Descriptor()`,
   `notifymail.Descriptor()`, `webhook.Descriptor()`, the `core.openai_compat` adapter); books' adapters `books.<id>`
   (metadata sources, whisper) and its 13 AI capabilities with the capability-ID map.
   **Mount F7-10's instance routes** (`core/internal/integrationhttp`), handing its `HoldReleaser` the
   `providerhttp.Registry`, and regenerate `core/openapi/core.json` and the client under
   `core/web/packages/api-client/src/generated/`. Un-skip the permission and 401 cells of `integrationhttp`
   (`core/internal/integrationhttp/routes_test.go`: delete each `t.Skip("needs stage 13: F4-08")`), which run now that
   F4-08's stage 13 is live and the package is mounted.
   **Service registry** (06 §6.11.1 step 4): delete from `internal/books/services.go` the `ServiceDefs` of eventbus,
   filesystem, searchindex, the ten AI services, batchpoller and the three metafetch services, their core packages now
   wired by the kernel.
2. **Register M6** in `internal/books/cutover/steps.go`, passing F7-08's constructor this flip's books level constant as
   `RaisesLevel` and its new books level step as the `After` ref (F7-08's constructor takes both from the caller). In
   `internal/books/cutover/stepids.go`, add this flip's books level constant and level-step ID (and core's, when core's
   level changes) beside the earlier flips' and replace the M8 ID; never edit an earlier flip's constant or ID (F3-06).
   Then apply the per-flip level mechanism (order M2, stated in
   F3-04 and F3-06): **add** a new level step for each owner whose level this flip raises (books'
   `books.level-<n>`; core's `core.level-<n>` when core's level changes), named for the raised target level, at that
   owner's next contiguous version, numbered immediately before M6 (which runs `After` it); the earlier flips' level
   steps stay registered unchanged (05-storage §5S.3.5). **Replace** the M8 entry with a newly named one whose step ID
   names M6 as the last step it compacts after, so exactly one M8 is registered and it runs last; bump the raised
   owners' declared `Spec.Level` in the same PR. On a store F5-18's `main` migrated, the earlier level steps are
   applied and the new M8 ignores the previous M8's `done` record and `applied` journal (03 §3.8; 05-storage
   §5S.3.13; IMPL-13).
3. **Delete** `internal/plugin` with no importer left (04 §8.3), the webhook plugin `internal/plugins/webhook`, and the
   unauthenticated `/api/v1/plugins` mount (05-http-api §8.1 F7 row); books publishes its 12 event types through
   `Deps.Notify` in the same batch as the change (05-notify §10's topic map); deliveries are in core's signed format
   (09 §9.6 row 5).
4. **AI**: `internal/aidispatch` and `internal/ai/aijobs` move into `core/integrations/ai`; `internal/ai`'s client half
   into the `openai` adapter (its feature methods stay books'); HEAD's `ai.*`/`ai_dispatch.*` families deleted and
   replaced by `<namespace>_ai_*` with every reference in `deploy/grafana/dashboards/*.json` and
   `deploy/prometheus/{alert-rules,recording-rules,alert-rules.test}.yml` updated in this PR (05-ai §8; 09 §9.2.7);
   `/api/v1/ai/capabilities`, `/ai/endpoints/status`, `/ai/test-connection` deleted, the SPA on the core routes.
5. **Provider gates**: HEAD's `internal/metadata/providerhttp`, `dailyquota`, `throttle*`, `circuitbreaker` replaced by
   core `providerhttp`; the metadata chain on `integrations/metadata` (the `switch src.ID` deleted, 05-metadata M3.10);
   `/api/v1/metadata/providers/throttles` deleted, the SPA and scripts on instance health and `DELETE …/hold`.
6. **Library roots**: books reads roots through `library.Scope`; `ImportPathStore` deleted; scanner, autoscan and the
   filesystem handlers write `rootscan:<root ID>`; the browse route under `core:library.browse` mounted, HEAD's browse
   API and the `fileops` `init()` registration deleted; `internal/watcher` → `Manager.Watch`.
7. **Search**: books' index on `storage/index` at `<db parent>/index/books/books.bleve`, rebuilt at cutover;
   `internal/search` moves to `core/internal/bleveindex` and `core/storage/index` (books keeps document building and
   query parsing); memdb, hnsw and chromem registered as `DerivedIndex`.
8. **Late-owner kinds** (05-tasks §5T.8.2): `integrations/providerhttp` and `integrations/ai` extend the registrations of
   `core.integrations.release-holds` and `core.ai.batch-poll` with their native bodies; `providerhttp.InterimHolds`,
   `ai.InterimPoll`, books' two interim backends and `InterimBackends`' holds and poll fields deleted (06 §6.4.10).
9. **Config**: the class-`i` `/api/v1/config` handlers and HEAD's test-connection handlers
   (`handleDelugeTestConnection`, `/ai/test-connection`) deleted in this PR, as F7-10's instance routes replace them
   (05-metadata M1.3 "When the routes land"); the SPA's integration settings and first-party callers switched to
   `/api/core/v1/integrations/...`; no alias (D10).

04 keys this PR moves: `F7 move internal/ai/aijobs -> mediacore/integrations/ai`; `F7 move internal/aidispatch ->
mediacore/integrations/ai`; `F7 move internal/config -> mediacore/integrations/ai`; `F7 move internal/metadata ->
mediacore/integrations/providerhttp`; `F7 move internal/metadata/providerhttp -> mediacore/integrations/providerhttp`;
`F7 move internal/ai -> mediacore/integrations/ai/openai`; `F7 move internal/deluge ->
mediacore/integrations/download/deluge`; `F7 move internal/plugins/webhook -> mediacore/integrations/notify/webhook`;
`F7 move internal/search -> mediacore/internal/bleveindex`; `F7 move internal/search -> mediacore/storage/index`;
`F7 move internal/server -> mediacore/storage/index`; `F7 move internal/fileops -> mediacore/library`;
`F7 move internal/pathutil -> mediacore/library`; `F7 move internal/watcher -> mediacore/library`;
`F7 delete internal/plugin`.

## Invariants

- **I1.** The search oracle `TestBooksIndexRebuiltAtCutover`, owned here, passes end to end: F7-08's dark half (the
  dirty set dropped, `library.bleve` untouched) plus the full rebuild at the native path, search served degraded
  meanwhile, and the corpus' answers equal the R0 binary's (05-storage §5S.10).
- **I2.** The webhook delivery test (`TestWebhook_JSONBodyGolden`, `TestWebhook_SignatureWithTimestamp`) and
  `TestWebhookMigration_SecretByValue` pass: a receiver keeps its secret and verifies core's signature.
- **I3.** `TestRootsCutover_FromHEADFixture`, `TestIntegrationsMigration_EveryInstanceAndID`,
  `TestAIJobMigration_EveryJobAndState` pass on the registered step; the R2 cutover suite (F2-15's count, ID,
  reference and byte-identity checks) is green with M6 registered.
- **I4.** Every series a shipped dashboard or rule names is served (the dashboard-reference test) and `promtool test
  rules` passes after the AI renames; a plain gather has no duplicate family (`TestABOMetricsGatherNoDuplicate`) and
  only `<namespace>_ai_*` AI families plus books' `ai.parse_results` (`TestNoHEADAIFamilies`).
- **I5.** `internal/plugin` is deleted with no importer left; nothing serves `/api/v1/plugins`.
- **I6.** `TestCutoverRefusedByOlderBuilds` still refuses with the pre-freeze binary on a store migrated through M6;
  the crash matrix covers M6 and the new M8. A store migrated by F5-18's `main` and booted by this `main` has its
  levels raised before M6, runs exactly one M8, and F5-18's binary then exits 78 on it (this flip's cell in F2-15's
  harness).
- **I7.** Every first-party caller of a deleted route is switched in this PR (SPA, `scripts/`, skills, docs).
- **I8.** A derived index declared with no hooks, hooks for an undeclared index, or a build kind not named
  `<module>.derived.<name>.build` fails boot; `Load` never runs in `app.New` or `Describe`
  (`TestDerivedIndexRebuildTriggers`).

## Files

| Group | Paths |
|---|---|
| wiring (this PR owns them in W37) | `main.go`, `internal/books/module.go`, `internal/books/services.go`, `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go`, `core/app/options.go` |
| generated | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/` |
| cutover | `internal/books/cutover/steps.go` (M6, this flip's level step, the new M8), `internal/books/cutover/stepids.go` (add this flip's level constant and level-step ID; replace the M8 ID), `internal/books/cutover_suite_test.go` (this flip's `TestCutoverFlipUpgrade` cell, F2-15's harness), `internal/books/store/` |
| moved or deleted packages | `internal/plugin/`, `internal/plugins/webhook/`, `internal/server/`, `internal/ai/`, `internal/aidispatch/`, `internal/deluge/`, `internal/search/`, `internal/scanner/`, `internal/fileops/`, `internal/pathutil/`, `internal/watcher/`, `internal/metadata/`, `internal/config/` |
| importers outside those packages (132 files at the plan's base `f7ac5b11e`, listed in full below the table) | `cmd/dedup_bench_batch.go`, `cmd/dedup_bench_pass2.go`; files under `internal/aiscan/`, `internal/appdirs/`, `internal/audiobooks/`, `internal/catalog/`, `internal/dedup/`, `internal/fingerprint/workerclient/`, `internal/importer/`, `internal/itunes/`, `internal/itunes/service/`, `internal/maintenance/jobs/`, `internal/merge/`, `internal/metafetch/`, `internal/organizer/`, `internal/playlist/`, `internal/plugins/acoustid/`, `internal/plugins/dedup/`, `internal/plugins/deluge/`, `internal/plugins/maintenance/`, `internal/quarantine/`, `internal/reconcile/`, `internal/remux/`, `internal/sweep/`, `internal/tagger/`, `internal/telemetrypolicy/`, `internal/testutil/`, `internal/transcode/`, `internal/transcribe/`, `internal/versionprimary/`, `internal/versions/` |
| SPA, deploy files, move bookkeeping | `web/src/`, `deploy/grafana/dashboards/`, `deploy/prometheus/`, `.mockery.yaml`, `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json` |
| F7-10's skipped cells | `core/internal/integrationhttp/routes_test.go` (un-skip only: remove the `t.Skip("needs stage 13: F4-08")` calls; no other change) |

The 132 importer files at `f7ac5b11e`, by directory:

```
cmd/: dedup_bench_batch.go dedup_bench_pass2.go
internal/aiscan/: pipeline.go pipeline_review2_test.go pipeline_review_test.go
internal/appdirs/: appdirs.go appdirs_test.go database_dir_test.go
internal/audiobooks/: filter_date.go service_mutation.go service_query.go service_query_search_whole_test.go service_single.go
internal/catalog/: harvest_test.go
internal/dedup/: apply_verdicts_stale_test.go embed_async_gate_test.go embed_concurrency_test.go embed_model_cutover_test.go engine.go engine_primary_gate_test.go engine_test.go hydrate_chromem_test.go lifecycle.go
internal/fingerprint/workerclient/: config.go gate.go worker.go
internal/importer/: service.go
internal/itunes/: import.go
internal/itunes/service/: importer.go service.go validate.go
internal/maintenance/jobs/: app_dir_guard_test.go backfill_file_hashes.go bulk_deluge_import.go cleanup_empty_folders.go cleanup_organize_mess.go relink_missing_to_itunes.go relink_report.go
internal/merge/: itunes_guard.go service.go
internal/metafetch/: apply_timings.go batch_verdict_test.go browse_search.go cache_no_source_answered_test.go candidate_fallback_test.go catalog_stage_test.go chain_throttle.go export_limits_test.go helpers.go provider_limits_test.go reliability_test.go search_fanout.go search_fanout_pcache_test.go search_fanout_probe_test.go search_fanout_test.go search_title_variants_test.go service_files.go service_search.go service_writeback.go service_writeback_forcedwrite_test.go service_writeback_realfile_test.go source_chain_walk.go source_chain_walk_test.go write_file_tags_verify_test.go writeback_backup_ctx_test.go writeback_protected_test.go
internal/organizer/: folder_scope.go inplace_collision.go inplace_multifile.go organizer.go preview.go service.go
internal/playlist/: evaluator.go evaluator_tag_terms_test.go evaluator_viewer_states_test.go
internal/plugins/acoustid/: window_backfill.go worker_hub.go worker_hub_calibration_roots_test.go
internal/plugins/dedup/: embed_scan_test.go
internal/plugins/deluge/: centralization.go centralization_test.go link_backfill.go link_backfill_budget_test.go link_backfill_test.go path_update_preview_test.go plugin.go plugin_test.go protected_paths_sync_test.go register.go
internal/plugins/maintenance/: app_dir_guard_test.go cover_ops.go file_provenance_capture.go folder_books_fixer.go itunes_clone_into_library.go itunes_clone_into_library_test.go itunes_regroup.go library_state_repair.go recover_missing_files.go relink_pre_fix_imports_fixer.go repoint_missing_to_folder_audio.go repoint_unrecorded_renames.go rewrite_path_prefix.go throttle_reset.go throttle_reset_test.go
internal/quarantine/: register.go service.go service_test.go
internal/reconcile/: app_dir_guard_test.go itunes_heal.go reconcile.go
internal/remux/: app_dir_guard_test.go remux.go transcode.go
internal/sweep/: app_dir_guard_test.go backup_cleanup.go backup_cleanup_test.go temp_cleanup.go temp_cleanup_test.go
internal/tagger/: safe_write_verify_test.go verify.go
internal/telemetrypolicy/: series_contract_test.go
internal/testutil/: integration.go
internal/transcode/: app_dir_guard_test.go transcode.go transcode_coverage_test.go
internal/transcribe/: canary_cooldown_probe_test.go dispatcher.go inflight.go workerfault_requeue_test.go
internal/versionprimary/: signals.go
internal/versions/: ingest.go serve_guard.go
```

Plus one `changelog.d/` fragment. The importer list was computed at the plan's base; F5-18 and F6-03 run earlier and
may have moved or removed some (F5-18 deletes `internal/plugins/`). Grep 2 below decides: every importer of a moved
or deleted package at your base is this PR's file, and the PR does not build while any remains (04 §8.3).

## Re-verify before editing (expected results at your base)

Evidence counts are at HEAD `f7ac5b11e`; regenerate them and record both in the PR.

```bash
M=github.com/falkcorp/audiobook-organizer
# 1. internal/plugin importers (the deletion gate, 04 §8.3). [evidence: 20 non-test files in 10 packages, two of them
#    the generated mocks in internal/server/handlers/mocks]
git grep -l "\"$M/internal/plugin\"" -- '*.go' | grep -v _test.go
# 2. Importers of every moved package outside this row's paths. [evidence:
#    internal/plugin: internal/dedup, internal/itunes/service, internal/quarantine;
#    internal/ai/aijobs: internal/dedup; internal/aidispatch: internal/dedup, internal/telemetry/contract, internal/transcribe;
#    internal/deluge: internal/maintenance/jobs, internal/plugins/deluge; internal/search: internal/audiobooks, internal/playlist;
#    internal/metadata/providerhttp: internal/catalog, internal/metafetch; internal/metadata/dailyquota: internal/metafetch;
#    internal/fileops and internal/pathutil: about 25 packages, of which only the moved symbols' callers change]
for p in internal/plugin internal/ai/aijobs internal/aidispatch internal/deluge internal/search internal/metadata/providerhttp internal/metadata/dailyquota internal/fileops internal/pathutil internal/watcher; do
  echo "== $p"; git grep -l "\"$M/$p\"" -- '*.go' | grep -v "^$p/" | xargs -n1 dirname 2>/dev/null | sort -u; done
# 3. HEAD's AI metric families and the files that name them. [evidence: 19 non-test Go references;
#    deploy/grafana/dashboards/audiobook-organizer-ai.json, deploy/prometheus/alert-rules.yml, alert-rules.test.yml]
grep -rn 'ai_dispatch\|"ai\.\(requests\|failures\|tokens\|request\.duration\)' --include='*.go' internal/ | grep -v _test | wc -l
grep -l 'ai_dispatch\|ai_requests' deploy/grafana/dashboards/*.json deploy/prometheus/*.yml
# 4. Routes this PR deletes. [evidence: wire_metadata_routes.go:58-60 throttles; wire_media_routes.go:50 ai/test-connection;
#    handlers/aibackends ai/capabilities and ai/endpoints/status; handlers/plugins.go /api/v1/plugins*]
grep -rn '"/metadata/providers/throttles\|"/ai/test-connection\|"/ai/capabilities\|"/ai/endpoints/status\|plugins' \
  --include='*.go' internal/server/wire_*.go | grep -v _test
# 5. The dark packages and the M6 step. Expected: each present; M6 not yet in steps.go
ls core/integrations/integrations.go core/integrations/ai/openai/ core/integrations/download/deluge/ \
   core/integrations/notify/webhook/ core/library/transfer.go core/storage/index/ core/storage/derived.go \
   core/internal/integrationhttp/ internal/books/cutover/m6/
grep -n 'type HoldReleaser interface' core/internal/integrationhttp/*.go        # 1 (F7-10)
grep -c 'needs stage 13: F4-08' core/internal/integrationhttp/routes_test.go     # >= 2 (F7-10's skipped permission tables)
grep -n 'func (r \*Registry) ReleaseHold' core/integrations/providerhttp/*.go  # 1 (F7-02)
grep -n 'Derived ' core/storage/*.go | grep -v _test                           # Spec.Derived (F7-07 or add it here)
grep -c 'm6' internal/books/cutover/steps.go
# 6. The interim late-owner backends to delete. Expected: InterimHolds, InterimPoll in core and ABO's main
grep -rn 'InterimHolds\|InterimPoll' --include='*.go' core/ internal/ main.go | grep -v _test
```

STOP if any dark package or M6 is missing (grep 5), or if grep 2 finds an importer you cannot re-point inside this PR
(report it; the PR must leave no importer of a deleted package).

## Steps

1. Run the re-verify block; paste grep 1 and 2 into the PR description as the importer checklist. If
   `storage.Spec` still has no `Derived []DerivedIndex` field (F7-07's planner note), add it here in the file that
   declares `Spec` and name the file in the PR.
2. Write or extend the tests first (below).
3. Wire the module members, the kernel boot calls (derived P7 and (3a), the index `Opener`), F7-10's routes and
   books' registrations (Goal 1); ABO's `main` sets `app.Options.Adapters`; delete the listed `ServiceDefs` from
   `internal/books/services.go`; regenerate `core/openapi/core.json` and the client.
4. Register M6, add this flip's level steps and replace the M8 entry with a newly named one, bumping the raised
   owners' `Spec.Level` (Goal 2);
   extend the crash matrix with the new M8 and `TestCutoverRefusedByOlderBuilds`'s cells; add this flip's cell to
   F2-15's harness.
5. Move or delete each package of the 04 keys with `git mv` where code moves; re-point every importer; delete
   `internal/plugin` last, once grep 1 is empty; regenerate or delete `internal/server/handlers/mocks` and update
   `.mockery.yaml`.
6. AI renames with the dashboards and rules (Goal 4); `promtool test rules`.
7. Switch the SPA and first-party scripts off every deleted route (Goals 4, 5, 6, 9), the integration settings and
   test buttons onto F7-10's routes.
8. Delete the interim late-owner backends (Goal 8).
9. Layering keys, shard weights, headers, fragment; `go build ./... && (cd core && go build ./...)`; acceptance.

## Code contract

No new public API beyond F7-01 … F7-08's. ABO's adapter list (05-metadata M1.3):

```go
app.Options{
	Adapters: []integrations.Descriptor{deluge.Descriptor(), notifymail.Descriptor(), webhook.Descriptor() /* , the openai_compat descriptor */},
}
```

## Tests (write first; spec names copied exactly)

- `TestBooksIndexRebuiltAtCutover` end to end (this row owns it: extend F7-08's dark half with the rebuild at the
  native path, the degraded search meanwhile and the answers equal to R0's).
- `TestDerivedIndexRebuildTriggers` (05-storage §5S.10; the kernel's P7 validation and the (3a) enqueue on a version
  change, a `Load` refusal and after a restore).
- F7-10's `TestIntegrationHTTP_*` route tests run on ABO's mounted routes, the un-skipped cells of
  `TestIntegrationHTTP_ReadNeedsIntegrationsRead` and `TestIntegrationHTTP_WritesNeedIntegrationsManage` among them
  (403 and 401 enforced by stage 13); a route-level test that HEAD's
  `POST /api/v1/deluge/test-connection` and `/api/v1/ai/test-connection` are gone (name coined here:
  `TestHEADTestConnectionRoutesGone`).
- `TestNoHEADAIFamilies` (05-ai §9).
- The dashboard-reference test and `promtool test rules` after the AI renames.
- `TestABOMetricsGatherNoDuplicate` (unchanged).
- `TestCutoverRefusedByOlderBuilds` and the R2 cutover suite with M6 registered.
- `TestCutoverFlipUpgrade`: this flip's cell in F2-15's cutover-suite harness, `internal/books/cutover_suite_test.go`
  (order M2; the cell shape is F3-04's): a store migrated by F5-18's
  `main`, booted by this `main`, has its levels raised, runs exactly one M8, and F5-18's binary then exits 78 on it.
- F3-06's registered-list test, green with M6 registered: the levels are raised to the build's target before the
  first step of each flip, versions are contiguous per owner, and exactly one M8 is registered, last.
- `TestDerivedIndexHooksAfterRegister` (06 §6.10): `Load` runs at Run start step (3a), after the module's start hooks
  and before `Bus.Open`; the rebuild runs as the module's `<ID>.derived.<name>.build` definition with its own Effects,
  Timeout and Parallelism; a wrong kind name or a declaration without hooks fails boot.
- Extend `TestDepsScoping` (06 §6.10; R1-20's, grown at F4-08 and F6-03) with the F7 cells: `library.Scope` writes
  are attributed to the module, and a notify to a topic outside the module's catalog entries is refused through
  `Deps.Notify`.
- Extend `TestCoreRoutesReplaceGin` (05-http-api §12; F4-08's) with the F7 cell: no gin route remains at a path F7's
  native routes replace.
- `TestInternalPluginDeleted` (name coined here): `go list ./...` contains no `internal/plugin` package and the router
  answers 404 for `GET /api/v1/plugins`.

## Acceptance (commands and expected output)

```bash
M=github.com/falkcorp/audiobook-organizer
# internal/plugin gone with no importer. Expected: nothing / 0
ls internal/plugin internal/plugins/webhook 2>/dev/null
git grep -l "\"$M/internal/plugin\"" -- '*.go' | wc -l

# HEAD's AI families gone from code and deploy files. Expected: 0 and 0
grep -rn 'ai_dispatch\|"ai\.\(requests\|failures\|tokens\|request\.duration\)' --include='*.go' internal/ core/ | grep -v _test | wc -l
grep -l 'ai_dispatch' deploy/grafana/dashboards/*.json deploy/prometheus/*.yml | wc -l
promtool test rules deploy/prometheus/alert-rules.test.yml

# Interim backends for holds and poll gone. Expected: 0
grep -rn 'InterimHolds\|InterimPoll' --include='*.go' core/ internal/ main.go | wc -l

# F7-10's permission cells un-skipped and green. Expected: 0, then ok
grep -c 'needs stage 13: F4-08' core/internal/integrationhttp/routes_test.go
(cd core && go test -count=1 -race ./internal/integrationhttp/...)

# Gate tests. Expected: ok
go test -count=1 -run 'TestBooksIndexRebuiltAtCutover|TestDerivedIndexRebuildTriggers|TestHEADTestConnectionRoutesGone|TestRootsCutover_FromHEADFixture|TestWebhookMigration_InstancesAndRule|TestIntegrationsMigration_EveryInstanceAndID|TestAIJobMigration_EveryJobAndState|TestNoHEADAIFamilies|TestABOMetricsGatherNoDuplicate|TestCutoverRefusedByOlderBuilds|TestDeployReferencedSeriesServed|TestDerivedIndexHooksAfterRegister|TestDepsScoping|TestCoreRoutesReplaceGin|TestCutoverFlipUpgrade' ./...
(cd core && go test -count=1 -run 'TestWebhook_JSONBodyGolden|TestWebhook_SignatureWithTimestamp' ./integrations/notify/...)

# Builds, frontend and e2e. Expected: exit 0; green
go build ./... && (cd core && go build ./...)
make web-build && make web-test && make test-e2e

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

The required checks of 09 §9.2.1, the R2 cutover suite and `unit-lifecycle` included (this PR changes `main.go`),
must be green on the PR.

## Out of scope

- Moving organizer, versions, quarantine, Deluge import and iTunes transfer onto `library.Transfer` (after R2).
- The download phase 2, the AI usage ledger, the notify inbox (deferred-build).
- Backup, health, the updater and the kernel `/metrics` mount (F8-04).

## Rollback

Before R2 ships: revert the PR (atomic). After R2 ships: `make rollback-restore` (09 §9.2.1).

## Repo rules

- Worktree off fresh `origin/main`; `npm ci --prefix web`; headers bumped on every file; moved files keep their guid.
- The PR description lists every `old ==> new` package path (09 §9.2.10) and the importer checklist.
- One PR per rename: the AI series, their dashboards, rules and rule tests change together (09 §9.2.7).
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
  never squash; never `--admin` past a required check. No release or flip trailer.
- Push workflow changes, if any, with `git push`.

