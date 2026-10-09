<!-- file: docs/proposals/2026-10-holistic/tasks/README.md -->
<!-- version: 1.2.0 -->
<!-- guid: 2f8c6a4e-9b1d-4e73-8a5c-6d0f3b2e7c19 -->
<!-- last-edited: 2026-10-09 -->

# Task briefs for the October 2026 holistic roadmap

One brief per PR for waves 0, 1, 2 and freeze window F of `../08-integrated-roadmap.md` §5. Each brief is self-contained for a subagent with no conversation context; the template is `00-TEMPLATE.md`. Wave 3 and 4 PRs have no brief yet: each workstream README lists them with a pointer to the spec section, and they are written when their wave opens, because earlier waves change them.

**Model rule (owner, 2026-10-09):** Sonnet by default; Opus sparingly, only where the brief says so (new runtime invariants, storage migrations, scoring logic, "discover and sweep").

| Count | Value |
|---|---|
| Briefs | 151 |
| Sonnet / Opus | 122 / 29 |
| Wave 0 | 25 |
| Wave 1 | 64 |
| Wave 2 | 36 |
| Wave F | 19 |
| Wave 3 | 6 |
| Wave 4 | 1 |

## How to run one

1. Pick the first brief in the wave whose `Depends on` are all merged.
2. Start a subagent with the model in its header and this instruction: "Execute `docs/proposals/2026-10-holistic/tasks/<file>` exactly. Read it fully first. Work in a worktree. Report in the brief's Report format."
3. Merge one PR at a time (CI is single-threaded). Hotspot files have a fixed order in `../08-integrated-roadmap.md` §4.
4. Re-plan after 02-PR4 lands: the `ident:` counts replace the dated bucket estimates.

## Every brief, in wave order

| Brief | Title | Wave | Model | Size | Depends on |
|---|---|---|---|---|---|
| [01-P1](01/01-P1.md) | Default ActivityBackend to pebble | 0 | sonnet | S | 07 C1 (CI green on main; 08 section 5 row 0.1) |
| [01-P3](01/01-P3.md) | Delete the v1 stale-operation reaper; pause state reads v2 | 0 | sonnet | S | 07 C1 |
| [01-P4](01/01-P4.md) | Delete the opstate params side table | 0 | sonnet | S | none |
| [01-P5](01/01-P5.md) | Delete validators and the server compat shims | 0 | sonnet | S | none |
| [01-P6](01/01-P6.md) | Delete dead tag-write, retire, scanner and metrics helpers | 0 | sonnet | M | none (merge before 01 P79b and P79c, which edit `internal/ta |
| [01-P79a](01/01-P79a.md) | Honour embed_cover_art and default it on | 0 | sonnet | S | 07 C1 |
| [02-PR1](02/02-PR1.md) | Benchmark the production filter path (compile once), guard against per-row compiles | 0 | sonnet | S | none |
| [02-PR15](02/02-PR15.md) | Review tab heap measurement: procedure, audit template and the figures the owner records | 0 | sonnet | S | none (the owner runs the measurement) |
| [02-PR16](02/02-PR16.md) | Drop ScoreBreakdown (and CategoryTags if unread) from the review index view | 0 | sonnet | S | 07 C1 (the metadata_cache.go slog-ratchet entry merges first |
| [02-PR2](02/02-PR2.md) | Remove per-row waste in the compiled filter path (duration re-parse, by-value Book copies) | 0 | sonnet | S | 02-PR1 (its benchmarks are the before/after proof) |
| [02-PR3](02/02-PR3.md) | One filter grammar, two engines, one shared conformance corpus (Go RE2 and the TypeScript translation) | 0 | sonnet | S | none |
| [02-PR5a](02/02-PR5a.md) | The apply gate and the fetch selection treat a stale candidate row as stale (ships before PR 5b) | 0 | sonnet | S | none |
| [02-PR5b](02/02-PR5b.md) | A retitle marks the candidate row stale instead of deleting it | 0 | sonnet | S | 02-PR5a (merged and deployed first) |
| [04-P1](04/04-P1.md) | Schedule file-integrity-check and orphan-book-files-cleanup (report-only), add settings.manage, add the schedule-has-driver guard | 0 | sonnet | S | 04-P2 (merge order on `extra_ops.go`/`plugin.go`); 08 X2 (em |
| [04-P2](04/04-P2.md) | Retire the two ISBN-enrichment stub ops | 0 | sonnet | S | 04-P3a (both edit `internal/scheduler/extra_ops.go`; merge o |
| [04-P3a](04/04-P3a.md) | Scheduled backup cleanup honours backup_retention_days (D5), with a prod pre-check | 0 | sonnet | M | none in code. Owner pre-check (Step 1) gates the merge. 07 C |
| [06-P1](06/06-P1.md) | Bump the Go toolchain pin from 1.27.1 to 1.27.2 in all 18 pinned files | 0 | sonnet | S | 07 C1 (main green at attempt 1). Needs the `golang:1.27.2-al |
| [06-P2](06/06-P2.md) | Widen the toolchain drift check to Woodpecker, ci_remote.py and the workflow patch level | 0 | sonnet | S | 06-P1 (the pin must already be 1.27.2). On `.github/workflow |
| [07-C1](07/07-C1.md) | make `main` green on its four inherited failures, and make the script ratchets fail only on a rise | 0 | sonnet | M | none |
| [07-C2](07/07-C2.md) | path filters so docs-only and web-only PRs do not run the Go jobs | 0 | sonnet | S | 07-C1 |
| [07-C3](07/07-C3.md) | one sharded short-test run instead of two full ones (4 shards, intra-package split) | 0 | opus | M | 07-C2 (same file). The upstream input request can be opened  |
| [07-C4](07/07-C4.md) | measure the first ten merges after C1-C3 (no workflow change) | 0 | sonnet | S | 07-C1, 07-C2, 07-C3 merged, plus ten further merges to `main |
| [08-X2](08/08-X2.md) | an operation that declares no `Permissions` requires `settings.manage` on `POST /operations/v2` | 0 | sonnet | S | 07-C1 (so the PR is judged on a green `main`) |
| [08-X3](08/08-X3.md) | refuse in-process audio decoding in three operations unless `allow_server_decode` is set | 0 | sonnet | S | 07-C1 |
| [11-PR1](11/11-PR1.md) | telemetry.Meter helper, histogram views, and the `/metrics` series-name contract golden (69 families) | 0 | opus | M | none |
| [01-P2](01/01-P2.md) | Delete the NutsDB activity stack | 1 | sonnet | M | 01 P1 (merged) |
| [01-P7](01/01-P7.md) | Delete unused web files, exports and the dead vite test config | 1 | sonnet | M | 07 C1 |
| [01-P77](01/01-P77.md) | Delete the rename preview and apply routes | 1 | sonnet | S | 01 P5 (both edit `wire_handlers.go` / `audiobooks_compat.go` |
| [01-P79b](01/01-P79b.md) | Honour create_backups on tag writes | 1 | opus | S | 04 P3a (the scheduled cleanup must read `backup_retention_da |
| [01-P79c](01/01-P79c.md) | Honour verify_after_write on tag writes | 1 | sonnet | S | 01 P6 (edits `tagger/safe_write.go` first); 01 P79a; 01 P79b |
| [01-P8](01/01-P8.md) | Route every web request through apiFetch | 1 | sonnet | M | 01 P7 (both touch `web/src/services/playlistApi.ts`) |
| [01-P81a](01/01-P81a.md) | Retire the wipe route (first of the P81 series) | 1 | sonnet | S | 01 P3 and 07 R4 (server_lifecycle.go order: 01 P3 -> 07 R2 - |
| [01-P9](01/01-P9.md) | CI ratchet for dead Go code (deadcode) and a knip report | 1 | sonnet | S | 07 C3 (ci.yml order: C2 -> C3 -> 06 P2 -> 01 P9 -> 07 G4); t |
| [02-PR17](02/02-PR17.md) | Server-side Review query over the in-memory review snapshot: page, count, facets and select-all ids from one evaluation | 1 | opus | M | 02-PR3 (grammar corpus), 02-PR16 (slim index view; same file |
| [02-PR18](02/02-PR18.md) | Switch the Review > Metadata lane to server pages behind the review_metadata_server_query flag | 1 | sonnet | M | 02-PR17 (server query), 02-PR3 (corpus), 02-PR15 findings (r |
| [02-PR4](02/02-PR4.md) | Identification state index with O(1) counts, the ident: and asin: Library filters, and a compare-and-delete dirty set | 1 | opus | M | 02-PR5b (merge order on pebble_store.go: 5b, then 07 R2, the |
| [02-PR4b](02/02-PR4b.md) | `author:` matches every credited author, not only the primary one | 1 | sonnet | S | none (independent of 02-PR4; touches different cases of the  |
| [02-PR6](02/02-PR6.md) | Question-keyed provider cache with negative TTLs (P1 answers and P2 product-by-id), pointer rows for legacy per-book keys | 1 | opus | M | 07 C1-C3 (CI), 02-PR5b is not required |
| [02-PR7a](02/02-PR7a.md) | Read-only op catalog.coverage-report: how many unidentified books have a harvested author catalog | 1 | sonnet | S | 02-PR4 (the ident states select the books) |
| [02-PR9a](02/02-PR9a.md) | A missing author or narrator on a candidate is not scored as disagreement | 1 | sonnet | S | none |
| [04-P10](04/04-P10.md) | Dedup-on-import always goes through the dedup.check-book op (flag kept for the soak) | 1 | sonnet | M | 04-P13 (order); the 2-week soak decision (D27) before the fl |
| [04-P11](04/04-P11.md) | Set Concurrency on the five sequential RunItems calls and add the AST lint | 1 | sonnet | S | 04-P3b (order only; no shared files) |
| [04-P12](04/04-P12.md) | Delete maintenance.batch-poller; the inline batch_poller task loop is the only poller (D26) | 1 | sonnet | S | 04-P9 (tasks.go order); 04-P2 and 04-P5 (plugin.go order) |
| [04-P13](04/04-P13.md) | Fold the per-import-path transcode temp ticker into maintenance.temp-file-cleanup | 1 | sonnet | S | 04-P12 (order); 04-P4a (the survivor op, with its `FormerIDs |
| [04-P14a](04/04-P14a.md) | Near-duplicate C1: maintenance.author-dedup-scan retires, dedup.author-scan survives | 1 | sonnet | S | 04-P11 (and all of 04-P4a to P4f, 04-P3b: D27 says P14 follo |
| [04-P14b](04/04-P14b.md) | Near-duplicate C2 (D54): dedup.llm-review survives, scheduler.dedup-llm-review retires | 1 | sonnet | S | 04-P14a |
| [04-P14c](04/04-P14c.md) | Near-duplicate C3: maintenance.reconcile-scan retires, reconcile.scan survives (gates 03 PR 9b) | 1 | sonnet | M | 04-P14b |
| [04-P14d](04/04-P14d.md) | Near-duplicate C5: maintenance.fix-book-file-paths (job) retires, maintenance.mark-missing-files survives | 1 | sonnet | S | 04-P14c |
| [04-P14e](04/04-P14e.md) | Near-duplicate C6: maintenance.repair-missing-files (job) retires; missing-file-repoint and recover-missing-files survive | 1 | sonnet | S | 04-P14d |
| [04-P14f](04/04-P14f.md) | Near-duplicate C7: maintenance.bulk-fetch-metadata (job) retires; library.bulk-metadata-fetch and metadata.candidate-fetch survive | 1 | sonnet | S | 04-P14e |
| [04-P3b](04/04-P3b.md) | One shared .bak sweep helper; retire the cleanup-backups job (C4) | 1 | sonnet | M | 04-P4f (so `extra_ops.go` no longer holds a copy) |
| [04-P4a](04/04-P4a.md) | Merge four no-body-diff scheduler twins (temp-file-cleanup, trash-cleanup, tombstone-cleanup, db-optimize) into their maintenance.* survivors | 1 | sonnet | M | 04-P5 (creates `op_twin_merge_test.go`), 04-P1 (tasks.go ord |
| [04-P4b](04/04-P4b.md) | Merge scheduler.purge-deleted into maintenance.purge-deleted and collapse the two runAutoPurgeSoftDeleted copies | 1 | sonnet | M | 04-P4a |
| [04-P4c](04/04-P4c.md) | Merge scheduler.metadata-refresh into maintenance.metadata-refresh (two runMetadataRefreshScan copies) | 1 | sonnet | M | 04-P4b |
| [04-P4d](04/04-P4d.md) | Merge scheduler.resolve-production-authors into maintenance.resolve-production-authors (inline logic on both sides) | 1 | sonnet | M | 04-P4c |
| [04-P4e](04/04-P4e.md) | Merge scheduler.author-split-scan into maintenance.author-split-scan (about 240 duplicated lines, both carry the ModifyBook fix) | 1 | sonnet | L | 04-P4d |
| [04-P4f](04/04-P4f.md) | Merge scheduler.cleanup-old-backups into maintenance.cleanup-old-backups (retention source already unified by 04-P3a) | 1 | sonnet | M | 04-P3a (retention source), 04-P4e |
| [04-P5](04/04-P5.md) | Series twins: dedup.series-prune and dedup.series-normalize survive, maintenance.series-* become aliases | 1 | sonnet | S | 04-P2, 04-P1 (plugin.go and tasks.go merge order P2, P5) |
| [04-P6](04/04-P6.md) | Boot goroutines become ops: external-id, movement-atoms, remux, transcode+quarantine, book_atpath, activity filter-index reconcile | 1 | opus | M | 04-P14f (order); 07 R2 at minimum (readiness), 07 R3/R4 pref |
| [04-P7](04/04-P7.md) | opchange_by_book index ensure-mode op replaces the boot goroutine | 1 | opus | M | 04-P6 (shares `server_lifecycle.go` and the warmup dep; 07 R |
| [04-P9](04/04-P9.md) | Label refinement chain becomes a parent op (dedup.label-refinement) using childop.Follow | 1 | sonnet | S | 04-P7 (tasks.go merge order P1, P4a, P9, P12) |
| [05-PR0](05/05-PR0.md) | Docs truth pass for operations (stop documenting code that is gone) | 1 | sonnet | S | none |
| [05-PR1](05/05-PR1.md) | One typed run-status table in Go, generated into TypeScript | 1 | opus | M | 05-PR0 |
| [05-PR2](05/05-PR2.md) | `timed_out` outcome and lifecycle metrics on the OTel meter | 1 | sonnet | S | 05-PR1, 05-PR4 (the v3 `timed_out` state and its v2 mirror), |
| [05-PR3](05/05-PR3.md) | Write fence, hold the exclusive key until the goroutine exits, per-row cancel in Repairs apply | 1 | opus | M | none (independent; 08 section 5 lists it after 05-PR1 only t |
| [05-PR4](05/05-PR4.md) | `opv3:` keyspace, migration 065, v2 mirror | 1 | opus | L | 05-PR1 (the `state` package), 05-PR3 (the zombie handle that |
| [05-PR5](05/05-PR5.md) | `pkg/ops` SDK, chunk-leasing Batch runner, fenced Writer, v2 adapter | 1 | opus | L | 05-PR3 (fence), 05-PR4 (`OpsV3Store`, `state.V3`), **08 PR X |
| [05-PR6](05/05-PR6.md) | `opstest` harness and `Conformance` (fault injection for chunk leasing) | 1 | opus | M | 05-PR5 |
| [05-PR7](05/05-PR7.md) | One op catalog, a rewritten `oplint`, and a startup ledger check | 1 | opus | M | 05-PR5, 05-PR6 (the catalog tests use `opstest`), 07 S3 (`Ch |
| [05-PR8](05/05-PR8.md) | One schedule declaration (`ops.Schedule`) and a cron evaluator in the TaskScheduler | 1 | sonnet | M | 05-PR5 (`ops.Schedule`, `Definition`), 05-PR7 (the catalog s |
| [05-PR9](05/05-PR9.md) | `/api/v3/ops/*` census, timeline and SSE endpoints | 1 | sonnet | M | 05-PR4 (`OpsV3Store`, `RunRecord`, state machine), 05-PR5 (r |
| [07-G1](07/07-G1.md) | ratchet the flattened `database.Store` method count and the wide-consumer references, as Go tests | 1 | sonnet | S | 07-C1 (green `main`). 07-C3 is not required: these are `go t |
| [07-G2](07/07-G2.md) | layering rule as a Go test with a shrink-only map of known violations | 1 | sonnet | S | 07-C1 |
| [07-G3](07/07-G3.md) | ratchet direct `config.AppConfig` reads as a Go test | 1 | sonnet | S | 07-C1 |
| [07-G4](07/07-G4.md) | one gate manifest, one-way ratchets, and a baseline-lowering bot | 1 | sonnet | M | 07-C1, 07-C3, 07-G1, 07-G2, 07-G3 |
| [07-R2](07/07-R2.md) | readiness in `/health`, a counted memdb fallback, and operations that wait for warmup | 1 | sonnet | M | 07-C1 |
| [07-R3](07/07-R3.md) | classify every startup step as fatal or degraded, in one table | 1 | opus | M | 07-R2 (`degraded[]` in `/health`) |
| [07-R4](07/07-R4.md) | `Type=notify` readiness and a deploy that waits for `ready` | 1 | opus | M | 07-R2, 07-R3. Merge order: **06-P3 before R4** (both edit th |
| [07-S1](07/07-S1.md) | `dbtest.NewStore(t, opts...)`, one way to get a test store | 1 | sonnet | S | 07-C1, 07-R2 (the helper waits for warmup using `WaitForWarm |
| [07-S3](07/07-S3.md) | drive search indexing from `ChangeObserver` and delete `indexedStore` | 1 | opus | L | 07-R4 (lifecycle file order), 07-G1 (S3 lowers the reference |
| [07-S6](07/07-S6.md) | the first-audio-file helper that replaces reads of `Book.FilePath` | 1 | sonnet | S | 07-C1 |
| [10-PR0](10/10-PR0.md) | Deluge client: remove-with-data, ratio, seed time, files | 1 | sonnet | S | none |
| [10-PR1](10/10-PR1.md) | write the torrent-to-book link on import (`BookVersion.TorrentHash`, `BookFile.DelugeHash`, discovery import path) | 1 | sonnet | M | 10-PR0 (uses `deluge.NormalizeTorrentID`, `GetTorrentDetail` |
| [10-PR2](10/10-PR2.md) | `deluge.link-backfill` op: link existing book files to their torrents | 1 | opus | M | 10-PR0 (`ListTorrentDetailsByLabel`, `NormalizeTorrentID`),  |
| [11-PR2](11/11-PR2.md) | OTLP metric reader behind four config keys (off by default, never fatal, no fallback) | 1 | sonnet | M | 11-PR1 |
| [11-PR3](11/11-PR3.md) | aidispatch migration proof (5 `client_golang` families to OTel, identical Prometheus names) | 1 | sonnet | S | 11-PR1 |
| [11-PR4](11/11-PR4.md) | `internal/opsmetrics` (OTel instruments for ops v3), per-def cardinality fix, alert and recording rules | 1 | opus | M | 11-PR1 |
| [11-PR5](11/11-PR5.md) | `client_golang` constructor ratchet in `make ci` (baseline 69) | 1 | sonnet | S | 11-PR1 |
| [11-PR7](11/11-PR7.md) | AI call metrics and traces (`WithAISpan` wiring at the 9 call sites, exported on `/metrics`) | 1 | sonnet | M | 11-PR1, 11-PR4 (shared files); 11-PR3 for the dispatch-serie |
| [01-P72](01/01-P72.md) | Drop dead dedup routes and verb aliases | 2 | sonnet | M | 01 P3 (handler tests), 01 P7 (the api.ts wrappers for these  |
| [01-P73](01/01-P73.md) | Delete the dead dedup category C cluster (MergeBooks and friends) | 2 | sonnet | M | 03 PR 11 (both touch dedup tests); 02 PR 11/12 rebase on thi |
| [01-P75](01/01-P75.md) | Delete the SQLite activity backend | 2 | opus | M | 01 P1 shipped for at least one release (D11); 01 P2 (go.mod  |
| [01-P76](01/01-P76.md) | Delete internal/download and its config | 2 | sonnet | S | 07 C1; 01 P79a (config.go order: P79a -> P76); 10 PR 0–2 mer |
| [01-P80](01/01-P80.md) | Remove the 11 unread Settings fields and their UI | 2 | sonnet | M | 01 P76 and 01 P79a (config.go order: P79a -> P76 -> 04 P10 - |
| [01-P81b](01/01-P81b.md) | Retire the reading-state routes (books/:id aliases and status repair) | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81c](01/01-P81c.md) | Retire the collections and playlist-export routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81d](01/01-P81d.md) | Retire the audiobook alternative-titles, path-history and rescan routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81e](01/01-P81e.md) | Retire the narrator, work-stats and entity-tag routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81f](01/01-P81f.md) | Retire the provider-throttle and metadata-fields routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81g](01/01-P81g.md) | Retire the cache, activity-maintenance, system-log and diagnostics routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81h](01/01-P81h.md) | Retire the merge-journal routes and duplicate verb aliases | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81i](01/01-P81i.md) | Retire the server_lifecycle-owned orphan routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81j](01/01-P81j.md) | Retire the catalog, op-defs, AI-status, tools and review-replay routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [01-P81k](01/01-P81k.md) | Retire the Deluge discovery, iTunes diagnostics, API-key rotate and version-alias routes | 2 | sonnet | S | 01 P81a (creates `gone()`), 01 P72, 03 PR 12 (same wiring fi |
| [02-PR14](02/02-PR14.md) | identification.advance: a dirty-set driver in the v3 shape (Pages source, Item, Finish) on the v2 RunItems adapter | 2 | opus | L | 02-PR4 (ident index and dirty-set API), 02-PR6 (question-key |
| [02-PR19](02/02-PR19.md) | Retire the full-index Review mode: delete the index path, return 410 for view=index for one release, delete the flag | 2 | sonnet | S | 02-PR18 (one release with the flag on), 03 PR 10 (ReviewWork |
| [02-PR7b](02/02-PR7b.md) | Local author-catalog blocking stage (B1 identifier, B2 exact author block) ahead of the provider fan-out, plus the folder-parse variant | 2 | opus | M | 02-PR7a (its number is reviewed by the owner first), 02-PR6  |
| [02-PR8](02/02-PR8.md) | Window-print inverted index (fpwinidx), SigWindowAcoustID dedup signal and fragment-to-parent containment evidence | 2 | opus | M | 05 PR 3 (fence and per-row cancel live, for the index-build  |
| [03-PR1](03/03-PR1.md) | Move the compare-drawer closure under review/compare | 2 | sonnet | S | none inside 03 (01 P7 deletes `components/dedup/BulkActionBa |
| [03-PR10](03/03-PR10.md) | Redirect /dedup to Review, drop the sidebar entries, repoint the backend announcement | 2 | sonnet | S | 03-PR2, 03-PR5, 03-PR7a, 03-PR7b, 03-PR8, 03-PR9a, 03-PR9b,  |
| [03-PR11](03/03-PR11.md) | Delete the frontend of the old /dedup page and its ported tests | 2 | sonnet | M | 03-PR10 live in production for at least one release (redirec |
| [03-PR12](03/03-PR12.md) | Retire the dead /audiobooks/duplicates routes behind a 410 Gone stub, after proving nothing calls them | 2 | opus | S | 03-PR11 (the frontend callers are gone); 01 P72 (disjoint ro |
| [03-PR13](03/03-PR13.md) | Docs: AI-REFERENCE, port-inventory banners, changelog and executive summary for the retirement | 2 | sonnet | S | 03-PR12 (and 01 P73, which lands between PR 12 and this PR i |
| [03-PR2](03/03-PR2.md) | Dupes lane: layer filter, layer chips, bulk keep-older/newer, iTunes and partial-fingerprint chips | 2 | sonnet | M | 03-PR1; 02 PR 18 (wave 1, `ReviewWorkspace.tsx` order R24) |
| [03-PR3](03/03-PR3.md) | Dupes lane: export duplicates as CSV or JSON | 2 | sonnet | S | 03-PR2; 01 P7 (first in the `api.ts` order R16, so its four  |
| [03-PR4](03/03-PR4.md) | AcoustID key in Settings, online lookup and reset commands, audio-match section in the compare drawer | 2 | sonnet | S | 03-PR1, 03-PR3 (serial on `ReviewWorkspace.tsx`) |
| [03-PR5](03/03-PR5.md) | Gold Labels as Labels and Suspicious sub-views of the Duplicates lane | 2 | sonnet | M | 03-PR1, 03-PR4 (serial on `ReviewWorkspace.tsx`) |
| [03-PR6](03/03-PR6.md) | Duplicates lane: Clusters view and cluster verbs | 2 | sonnet | L | 03-PR2 |
| [03-PR7a](03/03-PR7a.md) | New "Authors & series" lane: lane scaffolding and the Authors sub-view | 2 | sonnet | M | 03-PR6 (serial on `ReviewWorkspace.tsx`; PR 6 is the last 03 |
| [03-PR7b](03/03-PR7b.md) | Authors & series lane: Series sub-view | 2 | sonnet | S | 03-PR7a |
| [03-PR8](03/03-PR8.md) | Authors & series lane: AI scans sub-view | 2 | sonnet | M | 03-PR7a (and 03-PR7b for the segmented-control order) |
| [03-PR9a](03/03-PR9a.md) | Repairs fixer "dedup.series-prune" (merge duplicate series, delete orphan series), written Evaluate-style | 2 | opus | M | 04 P2, P5, P12 (same-file order on `internal/plugins/mainten |
| [03-PR9b](03/03-PR9b.md) | Repairs fixer "reconcile.missing-files" (view and apply reconcile matches), written Evaluate-style | 2 | opus | M | 04 P14c (the D27 reconcile pair: the surviving op must save  |
| [03-PR9c](03/03-PR9c.md) | Repairs fixer "dedup.split-books" (split-book clusters), written Evaluate-style | 2 | opus | M | 03-PR9a (merge order on `plugin.go`: 9a, then 9c) |
| [07-F3](07/07-F3.md) | TanStack Query v5 pilot on the Repairs lane, kept only if it removes code | 2 | opus | M | 07-C1; sequence: after 03 PR 10 and before 05 PR 10 and 05 P |
| [01-T1](01/01-T1.md) | Delete unreachable functions in internal/database | F | sonnet | M | 01 P4 (opstate params), 05 PR 4, 01 P2 and 01 P75 where they |
| [01-T2](01/01-T2.md) | Delete unreachable functions in internal/server and its handler packages | F | sonnet | M | 01 P3, 01 P72, 03 PR 12; all 01 P81 groups that edit the wir |
| [01-T3a](01/01-T3a.md) | Delete unreachable functions in metadata, providerhttp, openlibrary and authority | F | sonnet | S | 02 PR 6 (question-keyed provider cache); 01 P6 (enhanced.go) |
| [01-T3b](01/01-T3b.md) | Delete unreachable functions in metafetch, metabatch, catalog, matcher, personname and franchise | F | sonnet | S | 02 PR 7b and 02 PR 9a |
| [01-T4a](01/01-T4a.md) | Delete unreachable functions in scanner, organizer, undo and backup | F | sonnet | S | 01 P77 (rename service code), 01 P6 (scanner.go) |
| [01-T4b](01/01-T4b.md) | Delete unreachable functions in fileops, audioutil, tagger, mediainfo, audioext and pathutil | F | sonnet | S | 01 P6, 01 P79b and 01 P79c (`safe_write.go`, `write_tags_saf |
| [01-T5a](01/01-T5a.md) | Delete unreachable functions in operations, freshness, registry, opmode and scheduler | F | sonnet | S | 01 P4 and the 04 scheduler PRs (04 P1, P4a-P4f, P9, P12) |
| [01-T5b](01/01-T5b.md) | Delete unreachable functions in plugins/maintenance, repairs, applygate and applycap | F | sonnet | S | 02 PR 8, 02 PR 9a |
| [01-T6a](01/01-T6a.md) | Delete unreachable functions in errhandling, logger, httputil, util, config, security, sysinfo, appdirs and policy | F | sonnet | M | 01 P80 (`internal/config`), 07 C1 (the `logger` slog ratchet |
| [01-T6b](01/01-T6b.md) | Delete unreachable functions in cache, searchcache, search, realtime, serviceregistry, syncapi/progress, activity, merge, versionprimary and metrics | F | sonnet | S | 01 P75 (`internal/activity`); 01 P73 (`merge.FollowMergeWith |
| [01-T7](01/01-T7.md) | Delete unreachable functions in ai, aidispatch, fingerprint, diagnosis and audiobooks | F | sonnet | S | 11 PR 3 (`aidispatch` instruments), 02 PR 1 (`audiobooks` be |
| [06-P5](06/06-P5.md) | go fix batch 1: any, forvar, minmax, errorsastype, reflecttypefor, stringscut*, stringsbuilder, stditerators, inline | F | sonnet | M | 06-P1 (1.27.2 fixes `go fix`); the whole 01 tier-2 deletion  |
| [06-P6a](06/06-P6a.md) | go fix batch 2, slice a: internal/database | F | sonnet | M | 06-P5 merged; 01 T1 (`internal/database` tier-2 deletions) m |
| [06-P6b](06/06-P6b.md) | go fix batch 2, slice b: internal/server (including handlers, middleware) | F | sonnet | M | 06-P5 merged and, in slice order, 06-P6a merged; 01 T2 merge |
| [06-P6c](06/06-P6c.md) | go fix batch 2, slice c: internal/plugins (51 of 61 files are in plugins/maintenance) | F | sonnet | L | 06-P5 merged and, in slice order, 06-P6b merged; 01 T5b, 02  |
| [06-P6d](06/06-P6d.md) | go fix batch 2, slice d: metadata, matching and AI packages | F | sonnet | M | 06-P5 merged and, in slice order, 06-P6c merged; 01 T3a, T3b |
| [06-P6e](06/06-P6e.md) | go fix batch 2, slice e: scanning, operations, organizing and the remaining small packages | F | sonnet | M | 06-P5 merged and, in slice order, 06-P6d merged; 01 T4a, T5a |
| [06-P7](06/06-P7.md) | go fix batch 3: newexpr (test-only pointer helpers) and delete the helpers it orphans | F | sonnet | M | 06-P6e merged (all of batch 2); 06-P5 merged. |
| [06-P8](06/06-P8.md) | go fix batch 4: waitgroupgo and testingcontext | F | sonnet | M | 06-P7 merged (go fix PRs are strictly sequential). |
| [06-P11](06/06-P11.md) | Vitest 5, then enable fsModuleCache in a second commit | 3 | sonnet | M | 06-P9 (serializes `web/package.json` and the lockfile). If 0 |
| [06-P12](06/06-P12.md) | PGO for deploy builds with a committed, trimpath-built profile | 3 | sonnet | S | 06-P3 merged and deployed (needs the pprof tag and `-trimpat |
| [06-P3](06/06-P3.md) | Ship the pprof tag in the normal deploy build; the committed Makefile owns the build flags | 3 | sonnet | S | 07 C3 (Makefile merge order is 07 C3 -> 06 P3/P12 -> 07 G4/U |
| [06-P4](06/06-P4.md) | Always-on runtime flight recorder with watchdog and search-stall snapshots | 3 | opus | M | none hard. If ops v3 (05 PR 5) has already replaced `interna |
| [06-P9](06/06-P9.md) | Typecheck with TypeScript 7 (tsc), keep TypeScript 6 (tsc6) for typescript-eslint | 3 | sonnet | S | none hard (the roadmap orders it first in the frontend chain |
| [07-F1](07/07-F1.md) | generate TypeScript types from Go structs with tygo | 3 | sonnet | M | 07-C3, 07-G4 (the freshness check is added to the gate manif |
| [07-F4](07/07-F4.md) | delete `docs/api/openapi.json` | 4 | sonnet | S | 07-F1 (the generated types replace the hand-maintained contr |

## Per-workstream indexes

- [01](01/01-README.md)
- [02](02/02-README.md)
- [03](03/03-README.md)
- [04](04/04-README.md)
- [05](05/05-README.md)
- [06](06/06-README.md)
- [07](07/07-README.md)
- [10](10/10-README.md)
- [11](11/11-README.md)
- 08: `08/08-X2.md`, `08/08-X3.md` (coordinator PRs)
