<!-- file: docs/proposals/2026-10-holistic/05-operations-v3/implementation-briefs.md -->
<!-- version: 1.3.0 -->
<!-- guid: 21e3c9de-3b45-456a-87b0-5f7485a01b3f -->
<!-- last-edited: 2026-10-08 -->

# Operations v3 — implementation briefs

One brief per PR in [`../05-operations-v3.md`](../05-operations-v3.md) §4. Every brief is
written so an implementer with no context can start: goal, exact files, steps, tests,
rollback, size, done-when. All paths are repo-relative; "(new)" marks a file that does not
exist at HEAD `f7211eb39`. Every changed file gets its version header bumped; every PR adds a
`changelog.d/` fragment (no header in fragments).

Standing rules for every PR below: work in a worktree; no `go work init`; never touch
`internal/writeback/`; never change the `library.scan` exclusive key string; never add a
delete path for `book_file` rows; no audio decoding on the server.

---

## PR 0 — Docs truth pass for operations (S)

**Goal.** Stop the docs from describing code that is gone (F25).
**Files.** `docs/AI-REFERENCE.md` (§"internal/operations — Background job queue (legacy v1)"
lines 103-105 describe a `queue.go` that does not exist; replace with the registry + v3 plan
pointer); `internal/operations/registry/types.go` (header comment cites
`docs/superpowers/specs/2026-05-04-unified-operations-system.md`, absent at HEAD — point at
`docs/development/writing-a-plugin.md`); `docs/development/writing-a-plugin.md` (add a
"v3 is coming" note linking this proposal).
**Tests.** None (docs/comments). `make fmt-check`.
**Rollback.** Revert.
**Done when.** `grep -n 'queue.go' docs/AI-REFERENCE.md` is empty and the types.go header
names a file that exists.

## PR 1 — Typed run state table + generated TypeScript (M)

**Goal.** One definition of run states and their properties (F2).
**Files.**
- `internal/operations/state/state.go` (new) — `type State string`; the v2 status strings as
  constants for now; `Props{Terminal, HoldsSlot, Resumable, Retryable, Discardable}`;
  `func Classify(s string) Props` with prefix handling for `interrupted*`.
- `internal/operations/state/state_test.go` (new) — table test pinning every v2 status.
- `internal/database/pebble_store_ops_v2.go` — `isTerminalV2Status` delegates to `state`.
- `internal/operations/registry/registry.go` — `isTerminalStatus` delegates.
- `internal/operations/registry/legacy_op_status.go` — `IsTerminalStatus` delegates; the
  disagreement is resolved explicitly (see decision below).
- `internal/operations/registry/retry.go` — `IsInterruptedStatus` delegates.
- `tools/cmd/opsgen/main.go` (new) — writes `web/src/generated/ops.ts`.
- `web/src/generated/ops.ts` (new, generated).
- `web/src/services/api.ts` — `OperationV2Status` and `isOperationTerminal` re-exported from
  the generated file; add `waiting_deps`; drop `interrupting`/`interrupted` only if the
  generator says the backend never mints them (it accepts them on read).
- `Makefile` — `generate-ops` target; `ci` checks the generated file is current.
**Decision to encode.** `interrupted_quiesced` and `interrupted_ask` are **not** terminal
(the backend resumes or waits on them). Pollers that today stop on them
(`web/src/services/api.ts:556`) must instead stop on `Terminal || AwaitingDecision`; the
generated `isSettled()` provides that, and `pollOperation` uses it, so no poller spins on a
quiesced scan.
**Tests.** `go test ./internal/operations/... ./internal/database/...`; vitest for
`api.ts` pollers with every status; a test that fails if `ops.ts` is stale.
**Rollback.** Revert; no persisted data changes.
**Done when.** `grep -rn 'func isTerminal\|func IsTerminal\|func IsInterrupted' internal` shows
only delegations to `state`.

## PR 2 — `timed_out` and lifecycle metrics (S)

**Goal.** A timeout is not a cancel (F3); count interrupted outcomes (F23).
**Files.** `internal/operations/registry/worker.go` (`finalStatusForCanceledRun` returns
`timed_out` for `DeadlineExceeded`; `recordRunMetrics` counts it as failed and adds
interrupted/dropped); `internal/operations/state/state.go` (add `timed_out`, terminal);
`internal/metrics/metrics.go` (`ops_runs_total{def,outcome}`, keep old counters as aliases for
one release); `deploy/prometheus/alert-rules.yml` (include `timed_out`); regenerate
`web/src/generated/ops.ts`.
**Tests.** Registry test: a def with a 10ms timeout ends `timed_out`; metric increments.
**Rollback.** Revert. Rows already written `timed_out` read as terminal via the state table
on the old binary? No — the old binary does not know it. Mitigation: the old binary's
`isTerminalV2Status` allowlist treats unknown as live; Clear Stale would offer them. Accept,
or keep writing `canceled` + message until PR 4's mirror exists. **Recommended:** land PR 2
after PR 4 so the mirror writes `canceled` for v2 readers.
**Done when.** `operations_failed_total` (or `ops_runs_total{outcome="timed_out"}`) moves on a
forced timeout.

## PR 3 — Fence; hold the key until exit; per-row cancel in Repairs (M)

**Goal.** No writes after a stop; no overlap with a zombie (F4, F5, F13).
**Files.**
- `internal/operations/registry/worker.go` — `runHandle.fence atomic.Uint64`; revoke on
  cancel, timeout, watchdog, quiesce, abandonment; on abandonment keep `concurrencyKey` and
  `writes` held (do not `releaseRunHandle`) until the goroutine's deferred exit; still free
  the worker slot so the pool does not shrink.
- `internal/operations/registry/registry.go` — `Cancel` revokes the fence before canceling
  ctx; `FenceFromContext(ctx) Fence` exported for writers.
- `internal/operations/registry/reporter_db.go` — reporter carries the fence.
- `internal/operations/registry/scan_standdown.go` — a lost lease revokes the holder's fence.
- `internal/repairs/writer.go` — every write method checks the fence first (`ErrFenced`).
- `internal/repairs/engine.go` — `ctx.Err()` check before each row inside the partition loop
  (`:668`).
**Tests.** Registry: an op that ignores ctx and writes in a loop; after `Cancel`, the fake
writer records zero writes; a second enqueue of the same def stays queued until the zombie
returns. Repairs: cancel during a 10-row partition stops after the in-flight row. Run with
`-race`.
**Rollback.** Revert. No persisted change.
**Done when.** The 2026-10-01 shape (apply keeps retiring after cancel) is reproduced in a
test and fails before / passes after.

## PR 4 — `opv3:` keyspace, migration 065, v2 mirror (L)

**Goal.** v3 run records with exact census indexes; v2 kept readable for rollback (R18).
**Files.** `internal/database/iface_ops_v3.go` (new: `OpsV3Store`), `internal/database/pebble_store_ops_v3.go`
(new: keys per `state-and-persistence.md` §2, CAS transitions, `meta:counts`, mirror writer),
`internal/database/pebble_store_ops_v3_test.go` (new), `internal/database/migrations.go`
(migration 065 Up/Down), `internal/database/migration065_test.go` (new),
`internal/database/keyfamilies.go` (register `opv3:` families),
`internal/database/pebble_store_ops_v2.go` (export `stageOpRow` for the mirror),
`internal/operations/registry/registry.go` + `worker.go` + `resume.go` (write through
`OpsV3Store`; read v2 rows only via migration).
**Steps.** 1) store + tests; 2) migration with the mapping table and its counts log; 3) switch
the registry's writes to v3 + mirror in one batch; 4) census of `opv2:` rows added to
`maintenance.db-census-exact` output first (so the migration's size is known).
**Tests.** Migration idempotence (run twice, same result); every v2 status mapped; mirror
round-trip (write v3, read via v2 API, same status family); `ix:state` counts equal a full
scan after 10k random transitions.
**Rollback.** Deploy previous binary: it reads mirrored `opv2:` rows; `opv3:` inert.
Migration 065 Down resets the version only.
**Done when.** `GET` of a run via the old v2 endpoint and via the store's v3 read agree for
every state.

## PR 5 — `pkg/ops` SDK, Batch runner, Writer, adapter (L)

**Goal.** The authoring surface of `sdk-api.md`, runnable on the existing executor.
**Files.** `pkg/ops/{definition,task,batch,fixer,pipeline,rc,progress,schedule,effects,approval,lanes,errors}.go`
(new); `internal/operations/registry/v3_adapter.go` (new: runs an `ops.Definition` as a
registry def, and wraps a v2 `OperationDef` as an `ops.Definition` for the catalog);
`internal/operations/registry/batch_runner.go` (new: generalizes `run_items.go` — snapshot,
watermark, partitions, atomic counters, cancel per item, pause gate, stand-down renewal);
`internal/operations/opswriter/{writer,intent,history,fence}.go` (new: the core of
`internal/repairs/writer*.go`); `internal/repairs/writer.go` (becomes a wrapper over
`opswriter`, same method set, test pinning the method set kept).
**Also.** Dispatcher readiness wait (`NeedsReady`, `sdk-api.md` §11.1) on 07's readiness state; `ValidateCatalog` rejects an empty `Permission` **on native v3 defs only** (coordinator: 147 adapted plugin defs declare none; they rely on 08 PR X2's default until ported); `oplint` rule against `database.Store` in `Deps`.
**Tests.** Unit tests per kind using a minimal in-package fake; a run queued before readiness waits and does not time out; Writer ordering test
(intent → write → history, previous captured in callback); preview refuses and records.
**Rollback.** Revert; nothing uses `pkg/ops` yet except tests.
**Done when.** Example 1 and Example 2 from `examples.md` compile and pass in-package tests.

## PR 6 — `opstest` harness and Conformance (M)

**Files.** `pkg/ops/opstest/{world,run,faults,conformance,fakes}.go` (new) and tests.
**Tests.** Conformance passes for the three example defs; each fault injector has a
deliberately broken def that it catches (mutation-style: a def that writes after cancel, a
def with an `AppendOnly` lie, a def that records history before the write).
**Rollback.** Revert.
**Done when.** `go test -race ./pkg/ops/...` green with ≥4 workers.

## PR 7 — One catalog, rewritten oplint, startup ledger check (M)

**Files.** `internal/opscatalog/{catalog,tombstones,validate}.go` (new) +
`catalog_link_test.go` (new, replaces `internal/plugins/plugins_wiring_test.go`);
`internal/plugins/plugins.go` (folded into the catalog, then deleted);
`internal/server/server.go` (lines 745-775: the maintenance inline registration and the
`opRegistrars` loop call the catalog); `internal/server/op_registrars.go` (kept for adapted v2
registrars until wave ports finish); `internal/server/server_lifecycle.go`
(`opRegistrationGate` also runs `ValidateCatalog` incl. the embedded ledger);
`internal/server/testdata/op_ids.golden` (embedded, still append-only);
`tools/cmd/oplint/main.go` (rewritten as `go/analysis`; old walled-garden rule dropped);
`Makefile` (`oplint` target fixed and added to `ci`).
**Tests.** A def defined but not bundled fails oplint (testdata package); a ledger ID without
def/alias/tombstone fails startup (unit test on `ValidateCatalog`).
**Rollback.** Revert; registration falls back to the five v2 paths.
**Done when.** `make ci` runs oplint and is green.

## PR 8 — Single scheduler source + cron evaluator (M)

**Files.** `internal/scheduler/scheduler.go` (build tasks from v3 `Schedule` for catalog defs;
keep `TaskDefinition` for un-ported ops), `internal/scheduler/cron.go` (new: parse/evaluate;
dependency choice in the PR body — a small parser or `github.com/robfig/cron/v3`'s parser
only), `internal/scheduler/maintenance.go` (`taskV2DefIDs` shrinks as defs port),
`internal/scheduler/tasks.go` (unchanged entries for un-ported ops), `go.mod`/`go.sum` if a
dependency is added, `internal/database/pebble_store_ops_v3.go` (`opv3:sched:`).
**Rule.** A def whose v2 `Schedule` cron has no `TaskDefinition` today is registered with
`Schedule` **disabled** (shown in the def list as "declared, not active") until the owner
answers Q2.
**Tests.** Cron evaluation table; coalescing into a running run; missed-fire counting; a
disabled schedule never fires.
**Rollback.** Revert; TaskScheduler keeps running every v2 task.
**Done when.** `GET /api/v3/ops/defs` (PR 9) or a unit test lists next-fire times equal to
today's cadence for every scheduled op.

## PR 9 — `/api/v3/ops/*`, census endpoints, v3 SSE (M)

**Files.** `internal/server/handlers/opsv3/{defs,runs,timeline,counts,events,rows}.go` (new) and
tests; `internal/server/wire_ops_v3_routes.go` (new); `internal/server/server.go` (route
wiring); `internal/server/handlers/operations_v2.go` (v1/v2 endpoints served from v3 records,
mapped to v2 vocabulary).
**Permissions.** Reads: `PermLibraryView`. Runs: the def's `Permission`. Approval: the def's
`Approval.Approver` + verified auth method from the request's auth context.
**Tests.** Census `total` equals the number of runs inserted across 3 pages; timeline
`complete=false` when the window is truncated; params rejected by schema return 400 before
anything is queued; an approval with an unverified auth method is refused.
**Rollback.** Revert; v1 endpoints unaffected.

## PR 10 — Frontend on v3 (L)

**Files.** `web/src/services/api.ts`, `web/src/stores/useOperationsStore.ts`,
`web/src/stores/operationGrouping.ts`, `web/src/components/OperationActivityPanel.tsx`,
`web/src/pages/ActivityLog.tsx`, `web/src/components/review/**` (Repairs lane on
`/api/v3/ops/defs?kind=fixer` and `/api/v3/ops/runs/:id/rows`),
`web/src/components/ops/OpForm.tsx` (new), `web/src/components/ops/ProgressView.tsx` (new),
the 27 `.tsx` files that render progress from `progress_current`
(`grep -rln 'progress_current\|ProgressBar\|LinearProgress' web/src --include='*.tsx' | grep -v test`).
**Tests.** Vitest per component; Playwright: run a preview, approve two rows, apply, see the
revert button; a zombie renders as "stopping".
**Rollback.** Revert; backend still serves v1 shapes.

## PR 11 — Grafana dashboard and alerts (S)

**Files.** `deploy/grafana/dashboards/operations.json` (new), `deploy/prometheus/alert-rules.yml`
(zombie > 10m, fenced writes > 0, intents unresolved > 0 for 1h, schedule missed),
`deploy/grafana/README.md`.
**Rollback.** Revert.

## PR 12A-H — Port waves

Each wave is one PR per row group below (split further if a PR exceeds ~1,500 lines). Before
cutting a wave, re-read `04-operations-census/ops-census.csv`: skip `prune`, fold `merge`
(applied at census v1.1.0, see `migration-guide.md` §3). When 07 splits
`internal/plugins/maintenance` by domain, each op moves into its domain package in the same
PR that ports it, so no op is moved twice.
Every ported def follows `migration-guide.md` §1.3. **Measured split** (census v1.1.0, 234 defs; script: the brief file lists matched against the CSV `loc` column, overlaps in `duplicates_ops.go`, `extra_ops.go`, `book_atpath_index.go` and `apply_when_scanned_op.go` resolved by ID): 12A 12, 12B 22, 12C 37, 12D 2, 12E 115, 12F 16, 12G 3, 12H 9 = 216 ported; 13 pruned (aliases/tombstones); 5 frozen (PR 15). The family assignment below was computed
from the census CSV (first draft); file lists are the defs' `loc` files.

**12A — read-only reports (12 defs).** `internal/plugins/maintenance/{author_whitespace_collision_report,book_atpath_index,book_shape_report,booksig_recovery_audit,credit_census,db_census_exact,file_provenance_export,filepath_collision_report,missing_file_audit,unknown_author_audit,version_group_primary_report}.go`, `internal/server/diagnostics_ops.go`. Note `book_atpath_index.go` also holds a writer (`book-atpath-index-backfill`, wave 12E).

**12B — housekeeping (22 defs after census; precondition: census PRs P4a-P4i merged).** `internal/plugins/dedup/{cleanup_orphan_author_embeddings,cleanup_orphan_embeddings,purge_legacy_fp,purge_stale}.go`, `internal/plugins/maintenance/{activity_reclaim,ai_journal_prune,author_purge_empty,cleanup,compact_activity_log,db,narrator_purge_empty,nightly_compact_activity_log,orphan_book_files,recompact_activity_digests,series}.go`, `internal/scheduler/extra_ops.go`, `internal/server/duplicates_ops.go` (series-prune only), `internal/scheduler/tasks.go` (remove ported `TaskDefinition`s). The activity-compaction defs must declare `Unordered` sources wherever they read tiers with backdated keys (F11).

**12C — `internal/maintenance` jobs (37 defs).** `internal/maintenance/jobs/*.go` (37 files listed in the census), `internal/server/maintenance_job_op.go`, `internal/server/maintenance_dispatcher.go`. Replace `maintenance.OperationIDFromCtx` / `RawParamsFromCtx` with `rc`. Keep each job's ID.

**12D — Repairs fixers (19 fixers, plus the 3 that 03 PR 9 adds first as v2 fixers: `series_prune_fixer.go`, `reconcile_fixer.go`, `split_books_fixer.go` (coordinator); plus the 2 defs `repairs.plan`/`repairs.apply` that run them until PR 14).** `internal/plugins/maintenance/{audible_read_status,author_named_series,combined_author,consolidation_leftovers,duplicate_copies,folder_books,fragment_consolidation,itunes_stale_path,junk_author,junk_title,letter_l_ordinal,lost_candidates,relink_stale_series,reparse_folder_names,scan_title_revert,swapped_title_author,tag_franchise,version_group_primary,version_twin_metadata}_fixer.go`, `internal/plugins/maintenance/plugin.go` (`Repairs()` → bundle), `internal/repairs/guards.go` (→ framework Guards). The iTunes stale-path fixer keeps its database-only rule; it must not gain any file or iTunes write.

**12E — batch writers (115 defs; split into 6 PRs: 11 + 25 + 18 + 34 + 13 + 14).**
- 12E1 acoustid + deluge: `internal/plugins/acoustid/{backfill,duration_backfill,fingerprint_rescan,lsh_backfill,online_lookup,reset_all,scan,window_backfill}.go`, `internal/plugins/deluge/{centralization,path_update,protected_paths}.go`. Fingerprinting stays on `LaneMac`.
- 12E2 dedup: the 30 `internal/plugins/dedup/*.go` files in the census list (`auto_resolve` gains concurrency here by partitioning by group, F28).
- 12E3 book_file row writers (*coordinator: "never a delete" is not true of every body at HEAD. `dedupe_book_file_rows_crossfolder.go:230` and `itunes_clone_into_library.go:1274` call `DeleteBookFilesByIDs`: the first deletes a journaled exact duplicate, the second undoes a clone. Port those bodies unchanged under a declared `Deletes(ResBookFiles)` effect that `oplint` flags for owner review. Add no new delete. The admin-only `POST /maintenance/wipe` (`maintenance_fixups.go:354`) also deletes every `book_file` row; 01 Q6 should retire it.*) `internal/plugins/maintenance/{build_folder_book_files,dedupe_book_file_rows,mark_missing_files,merge_same_path_dupes,missing_file_repair,missing_file_repoint,move_book_file_rows,orphan_book_files_repoint_plan,probe_directory_books,recover_missing_files,relink_unlinked,repoint_book_file_rows,repoint_missing_to_folder_audio,repoint_unrecorded_renames,rewrite_path_prefix,book_atpath_index,opchange_book_index,file_provenance_capture}.go`. Never a delete; repoint only.
- 12E4 author/series/title/tag: `internal/plugins/maintenance/{author,author_conjunction_repair,author_duplicate_merge,author_id_repair,author_path_link,author_strip_merge,author_title_fragment_report,authority_build,narrator_split_joined,series,series_denumber_op,series_phantom_repair,tag_backfill,title_backfill,title_repair,version_group_primary_repair,chapters_backfill,duration_backfill,cover_ops,dedup_ops,dedup_triage,auto_match_transcribed,clear_apply_rename_failures,repair_merged_user_state,repair_transcribe_status,review_status_index_repair,activity_filter_index_backfill,booksig_sidecar_migrate,backfill}.go`. `maintenance.repair-library-state` (`library_state_repair.go`) is **not** ported unless the census and the owner keep it, given the standing rule against running library-state rewrites.
- 12E5 iTunes and audio-file ops (bodies untouched; adapter or thin native wrapper only): `internal/plugins/itunes/{import,position_sync}.go`, `internal/plugins/maintenance/{itunes_clone_into_library,itunes_playlist_import,itunes_regroup,fs_regroup_xml,intro_transcribe,intro_migrate_single_file,extract_wav_clips,regroup_shattered_ai,integrity_check,reconcile}.go` (`write_back.go` and `batch_poller.go` are on the frozen list, PR 15). Transcription and WAV extraction keep running only where they run today; no new server-side decode. *Coordinator: "where they run today" includes in-process ffmpeg and fpcalc on the server, in `intro_transcribe.go`, `extract_wav_clips.go` and `acoustid/fingerprint_rescan.go`. Only `window_backfill.go` has a remote-only guard (`allow_server_decode`). These defs port with `Lane: LaneMac` and no in-process fallback, or stay frozen. Until then, 08 PR X3 adds the same refusal guard to the v2 bodies.*
- 12E6 server ops (incl. the 7 `dedup.*` defs in `duplicates_ops.go` other than `dedup.series-prune`, which is 12B): `internal/server/{catalog_harvest_op,diagnostics_ai_ops,duplicates_ops,entities_ops,legacy_backfill_op,reconcile_ops,series_rename_ops}.go`. `operations.backfill-legacy-status` stays on the adapter until workstream 01 removes the v1 keyspace (census: conditional prune).

**12F — AI and metadata (16 defs, incl. `scheduler.metadata-upgrade`, `scheduler.dedup-llm-review` and `metadata.apply-when-scanned`; `maintenance.isbn-enrichment` is tombstoned, not ported).** `internal/plugins/maintenance/{metadata,metadata_cache_reap}.go`, `internal/plugins/metafetch/{asin_backfill,calibrate_scoring}.go`, `internal/scheduler/extra_ops.go` (metadata/isbn ops), `internal/server/{ai_ops,aiscan_op,apply_when_scanned_op,batch_apply_op,batch_save_op,bulk_apply_preview,metadata_candidate_op,openlibrary_ops}.go`, `internal/applygate/*` (exposed as a Guard, logic unchanged). Bulk apply gets `Approval{PlanRequired}` per Q9.

**12G — pipelines (3 defs).** `internal/plugins/dedup/run_all.go`, `internal/plugins/maintenance/optimize.go`, `internal/server/scheduler_maintenance_window_op.go`, `internal/operations/childop/follow.go` (absorbed into the Pipeline runner, then deleted).

**12H — library (9 defs, incl. `library.organize-when-scanned`), last.** `internal/server/{library_core_ops,library_ai_parse_op,library_size_refresh_op,folder_autoscan_op,apply_when_scanned_op,metadata_ops}.go` (`library_writeback_op.go` is on the frozen list, PR 15). `library.scan` keeps its exclusive key, its checkpoint format (ship the `LegacyResume` decoder for `resume_folder_idx` / `resume_item_offset`), its stand-down behavior and its pickup gate; the PR includes a soak checklist: one scheduled scan, one quiesce by a Fixer apply, one deploy mid-scan.

## PR 13 — Retire the `internal/maintenance` job framework (M)

**Files.** `internal/maintenance/{job,progress,registry,result}.go`, `internal/maintenance/jobs/` (now thin or empty), `internal/server/maintenance_job_op.go`, `internal/server/maintenance_dispatcher.go`, `GET /api/v1/maintenance/jobs` route (adapter onto `/api/v3/ops/defs` until the frontend stops calling it).
**Rollback.** Revert (wave 12C kept every ID).

## PR 14 — Retire `repairs.plan` / `repairs.apply` and `/api/v1/repairs` (M)

**Files.** `internal/plugins/maintenance/repairs_ops.go`, `internal/server/wire_repairs_routes.go`, `internal/server/handlers/repairs/**`, `internal/repairs/engine.go` (logic now in the Fixer runner), settings keys `setting:repairs_last_plan_op:` / `setting:repairs_last_apply_op:` (read by v3 for one release, then dropped).
**Rollback.** Revert; stored plans are run results under the same op ids.

## PR 15 — Delete the adapter and v2 authoring surface, except a frozen allowlist (L)

**Precondition.** Every def is either native, pruned, or on the frozen allowlist below.
**Frozen allowlist** (`internal/operations/registry/v2compat/allowlist.go`, new; `oplint`
fails any other ID using `v2compat`):
- `library.bulk-write-back`, `maintenance.bulk-write-back`: census OUT OF SCOPE (write-back ban); registration and bodies untouched until the owner lifts it;
- `maintenance.batch-poller`: until census Q3 picks prune or adopt;
- `operations.backfill-legacy-status`: until workstream 01 removes the v1 keyspace;
- `maintenance.repair-library-state`: not ported, no schedule, until the owner decides (standing rule against library-state rewrites).

`v2compat` keeps only what those five use (`OperationDef` subset, `Reporter`, `RegisterOp`), runs them on the same executor, and maps their status into the v3 state table.
**Files.** `internal/operations/registry/v3_adapter.go` (→ `v2compat/`), `internal/operations/registry/{run_items,types,reporter}.go` (v2 parts not used by the allowlist), `pkg/plugin/sdk/**`, `tools/cmd/sdkguard/**`, `internal/scheduler/tasks.go` (all `TaskDefinition`s gone), `internal/scheduler/maintenance.go` (`taskV2DefIDs`), `internal/server/op_registrars.go`, `internal/operations/registry/subprocess.go` and `cmd/child_mode.go` (Q10), `internal/operations/opmode/` (mode is a framework field), `internal/operations/trigger_source.go`.
**Tests.** Allowlist test: exactly the five IDs register through `v2compat`; each still runs in a smoke test.
**Rollback.** Revert.
**Done when.** `grep -rn 'registry.OperationDef\|sdk.OperationDef' internal pkg` matches only `v2compat/` and the five allowlisted files.

## PR 16 — Remove the `opv2:` mirror; ship `ops export-v2` (S)

**Files.** `internal/database/pebble_store_ops_v3.go` (mirror off), `cmd/ops_export.go` (new subcommand), `docs/` runbook entry for downgrade after this point.
**Precondition.** Owner-chosen soak (Q4) has elapsed since PR 15.
**Rollback.** Re-enable the mirror (one flag in the store) and run `ops export-v2` for runs made while it was off.
