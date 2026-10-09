<!-- file: docs/proposals/2026-10-holistic/tasks/README.md -->
<!-- version: 1.5.0 -->
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
| Wave 1 | 65 |
| Wave 2 | 36 |
| Wave F | 19 |
| Wave 3 | 5 |
| Wave 4 | 1 |

## How to run one

1. Pick the first brief in the wave whose **`Merge first`** list is fully merged. That list (the first sentence of every brief's `Depends on` cell, and the last column of the table below) is the only authoritative ordering; the prose after it explains why. The `Blocks` cell is generated from the `Merge first` lists by `scripts/check_task_briefs.py --regen-blocks --index` (which also verifies ids, wave order and cycles, and rebuilds this page); it is never hand-edited. Ids named as "not briefed" are wave 3 and 4 roadmap PRs with no brief yet.
2. Before the first brief of a wave starts, bring every brief in that wave up to the current `00-TEMPLATE.md`: a cold-read of all 25 wave-0 briefs on 2026-10-09 found the same seven gaps in nearly every one (no re-verify grep block, no `Already done if` idempotency probe, no `Anti-over-suppression` line, relative worktree path, trailers not spelled out, placeholder changelog names, the `Merge first` id repeated in the prose). The template now carries all seven; a wave-0 brief is the shape to copy. Run the brief's re-verify greps at `origin/main` and fix any drift before handing it out.
3. Start a subagent with the model in its header and this instruction: "Execute `docs/proposals/2026-10-holistic/tasks/<file>` exactly. Read it fully first. Work in a worktree. Report in the brief's Report format."
4. Merge one PR at a time (CI is single-threaded). Hotspot files have a fixed order in `../08-integrated-roadmap.md` §4. Where a §4 chain crosses waves (for example `config.go`: 02 PR 10 is wave 3 but sits mid-chain), the earlier wave merges first and the chain is only a rebase order.
5. Re-plan after 02-PR4 lands: the `ident:` counts replace the dated bucket estimates.

### Two execution notes (2026-10-09)

- The `Claude-Session:` trailer names the **executing** session. The literal URL in a brief's Guardrails is the coordinator's; an agent running in its own session (a cloud session, for example) writes its own URL instead, and that is correct, not drift.
- The TypeScript check is `(cd web && npx tsc --noEmit)`. The old form `npx tsc --noEmit -p web` from the repo root resolves the root `node_modules` tsc (5.7.3), which rejects `web/tsconfig.json` with TS5103; `web/` pins tsc 6.0.3.

### Hub files: expect a rebase conflict, keep both sides

427 same-wave brief pairs with no ordering between them touch the same file. Six files account for most of it. After a sibling merges, `git fetch origin main && git rebase origin/main`; for every conflict in one of these files keep **both** sides' edits, then run the file's own test. Never resolve by dropping the sibling's change, and never stop to ask about a conflict in one of these.

| File | Why it conflicts | After keeping both sides |
|---|---|---|
| `internal/logger/slog_guard_ratchet_test.go` | every deletion brief lowers or removes an entry | `go test ./internal/logger -run 'TestGuard_' -count=1`; delete or lower whatever it reports as stale; never raise a count |
| `internal/server/op_id_aliases_test.go` (`retiredOpIDs`) | every op retirement adds a row | keep the map sorted; `go test ./internal/server -run 'TestOpIDs_' -count=1` |
| `internal/server/testdata/write_op_modes.golden` | hand-maintained list, no `-update` flag | `go test ./internal/server -run TestWriteOps_ -count=1` |
| `internal/server/op_twin_merge_test.go` (created by 04-P5) | 04-P4a to P4f and P14a to P14f each append rows | keep every row; `go test ./internal/server -count=1` |
| `internal/scheduler/tasks.go`, `maintenance.go`, `extra_ops.go`; `internal/server/scheduler_extra_ops.go` | registration lists; siblings delete adjacent lines | keep both deletions; `go build ./... && go test ./internal/scheduler/... ./internal/server/ -count=1` |
| `web/src/components/review/ReviewWorkspace.tsx`, `web/src/services/api.ts`, `internal/config/config.go` | §4 chains; disjoint hunks | keep both; `(cd web && npx tsc --noEmit)` / `go build ./...` |

`changelog.d/` never conflicts: every fragment has its own file name.

## Every brief, in wave order

| Brief | Title | Wave | Model | Size | Merge first |
|---|---|---|---|---|---|
| [01-P1](01/01-P1.md) | Default ActivityBackend to pebble | 0 | sonnet | S | 07-C1 |
| [01-P3](01/01-P3.md) | Delete the v1 stale-operation reaper; pause state reads v2 | 0 | sonnet | S | 07-C1 |
| [01-P4](01/01-P4.md) | Delete the opstate params side table | 0 | sonnet | S | none |
| [01-P5](01/01-P5.md) | Delete validators and the server compat shims | 0 | sonnet | S | none |
| [01-P6](01/01-P6.md) | Delete dead tag-write, retire, scanner and metrics helpers | 0 | sonnet | M | none |
| [01-P79a](01/01-P79a.md) | Honour embed_cover_art and default it on | 0 | sonnet | S | 07-C1 |
| [02-PR1](02/02-PR1.md) | Benchmark the production filter path (compile once), guard against per-row compiles | 0 | sonnet | S | none |
| [02-PR15](02/02-PR15.md) | Review tab heap measurement: procedure, audit template and the figures the owner records | 0 | sonnet | S | none |
| [02-PR16](02/02-PR16.md) | Drop ScoreBreakdown (and CategoryTags if unread) from the review index view | 0 | sonnet | S | 07-C1 |
| [02-PR2](02/02-PR2.md) | Remove per-row waste in the compiled filter path (duration re-parse, by-value Book copies) | 0 | sonnet | S | 02-PR1 |
| [02-PR3](02/02-PR3.md) | One filter grammar, two engines, one shared conformance corpus (Go RE2 and the TypeScript translation) | 0 | sonnet | S | none |
| [02-PR5a](02/02-PR5a.md) | The apply gate and the fetch selection treat a stale candidate row as stale (ships before PR 5b) | 0 | sonnet | S | none |
| [02-PR5b](02/02-PR5b.md) | A retitle marks the candidate row stale instead of deleting it | 0 | sonnet | S | 02-PR5a |
| [04-P1](04/04-P1.md) | Schedule file-integrity-check and orphan-book-files-cleanup (report-only), add settings.manage, add the schedule-has-driver guard | 0 | sonnet | S | 04-P2, 08-X2 |
| [04-P2](04/04-P2.md) | Retire the two ISBN-enrichment stub ops | 0 | sonnet | S | 04-P3a |
| [04-P3a](04/04-P3a.md) | Scheduled backup cleanup honours backup_retention_days (D5), with a prod pre-check | 0 | sonnet | M | 07-C1, 07-C2, 07-C3 |
| [06-P1](06/06-P1.md) | Bump the Go toolchain pin from 1.27.1 to 1.27.2 in all 18 pinned files | 0 | sonnet | S | 07-C1 |
| [06-P2](06/06-P2.md) | Widen the toolchain drift check to Woodpecker, ci_remote.py and the workflow patch level | 0 | sonnet | S | 06-P1, 07-C2, 07-C3 |
| [07-C1](07/07-C1.md) | make `main` green on its four inherited failures, and make the script ratchets fail only on a rise | 0 | sonnet | M | none |
| [07-C2](07/07-C2.md) | path filters so docs-only and web-only PRs do not run the Go jobs | 0 | sonnet | S | 07-C1 |
| [07-C3](07/07-C3.md) | one sharded short-test run instead of two full ones (4 shards, intra-package split) | 0 | opus | M | 07-C2, 07-C1 |
| [07-C4](07/07-C4.md) | measure the first ten merges after C1-C3 (no workflow change) | 0 | sonnet | S | 07-C1, 07-C2, 07-C3 |
| [08-X2](08/08-X2.md) | an operation that declares no `Permissions` requires `settings.manage` on `POST /operations/v2` | 0 | sonnet | S | 07-C1 |
| [08-X3](08/08-X3.md) | refuse in-process audio decoding in three operations unless `allow_server_decode` is set | 0 | sonnet | S | 07-C1 |
| [11-PR1](11/11-PR1.md) | telemetry.Meter helper, histogram views, and the `/metrics` series-name contract golden (69 families) | 0 | opus | M | 01-P6 |
| [01-P2](01/01-P2.md) | Delete the NutsDB activity stack | 1 | sonnet | M | 01-P1 |
| [01-P7](01/01-P7.md) | Delete unused web files, exports and the dead vite test config | 1 | sonnet | M | 07-C1 |
| [01-P77](01/01-P77.md) | Delete the rename preview and apply routes | 1 | sonnet | S | 01-P5, 01-P7, 05-PR1 |
| [01-P79b](01/01-P79b.md) | Honour create_backups on tag writes | 1 | opus | S | 04-P3a, 01-P6, 01-P79a |
| [01-P79c](01/01-P79c.md) | Honour verify_after_write on tag writes | 1 | sonnet | S | 01-P6, 01-P79a, 01-P79b |
| [01-P8](01/01-P8.md) | Route every web request through apiFetch | 1 | sonnet | M | 01-P7 |
| [01-P81a](01/01-P81a.md) | Retire the wipe route (first of the P81 series) | 1 | sonnet | S | 01-P3, 07-R2, 07-R3, 07-R4 |
| [01-P9](01/01-P9.md) | CI ratchet for dead Go code (deadcode) and a knip report | 1 | sonnet | S | 07-C3, 07-C2, 06-P2, 01-P2, 01-P4, 01-P5, 01-P6, 01-P7, 07-C1 |
| [02-PR17](02/02-PR17.md) | Server-side Review query over the in-memory review snapshot: page, count, facets and select-all ids from one evaluation | 1 | opus | M | 02-PR3, 02-PR16, 07-C1 |
| [02-PR18](02/02-PR18.md) | Switch the Review > Metadata lane to server pages behind the review_metadata_server_query flag | 1 | sonnet | M | 02-PR17, 02-PR3, 02-PR15, 01-P77 |
| [02-PR4](02/02-PR4.md) | Identification state index with O(1) counts, the ident: and asin: Library filters, and a compare-and-delete dirty set | 1 | opus | M | 02-PR5b, 07-R2, 02-PR17, 02-PR5a |
| [02-PR4b](02/02-PR4b.md) | `author:` matches every credited author, not only the primary one | 1 | sonnet | S | 02-PR4 |
| [02-PR6](02/02-PR6.md) | Question-keyed provider cache with negative TTLs (P1 answers and P2 product-by-id), pointer rows for legacy per-book keys | 1 | opus | M | 07-C1, 07-C2, 07-C3, 02-PR5b |
| [02-PR7a](02/02-PR7a.md) | Read-only op catalog.coverage-report: how many unidentified books have a harvested author catalog | 1 | sonnet | S | 02-PR4 |
| [02-PR9a](02/02-PR9a.md) | A missing author or narrator on a candidate is not scored as disagreement | 1 | sonnet | S | none |
| [04-P10](04/04-P10.md) | Dedup-on-import always goes through the dedup.check-book op (flag kept for the soak) | 1 | sonnet | M | 04-P13 |
| [04-P11](04/04-P11.md) | Set Concurrency on the five sequential RunItems calls and add the AST lint | 1 | sonnet | S | 04-P3b |
| [04-P12](04/04-P12.md) | Delete maintenance.batch-poller; the inline batch_poller task loop is the only poller (D26) | 1 | sonnet | S | 04-P9, 04-P2, 04-P5 |
| [04-P13](04/04-P13.md) | Fold the per-import-path transcode temp ticker into maintenance.temp-file-cleanup | 1 | sonnet | S | 04-P12, 04-P4a, 04-P7 |
| [04-P14a](04/04-P14a.md) | Near-duplicate C1: maintenance.author-dedup-scan retires, dedup.author-scan survives | 1 | sonnet | S | 04-P11, 04-P4a, 04-P4b, 04-P4c, 04-P4d, 04-P4e, 04-P4f, 04-P3b |
| [04-P14b](04/04-P14b.md) | Near-duplicate C2 (D54): dedup.llm-review survives, scheduler.dedup-llm-review retires | 1 | sonnet | S | 04-P14a |
| [04-P14c](04/04-P14c.md) | Near-duplicate C3: maintenance.reconcile-scan retires, reconcile.scan survives (gates 03 PR 9b) | 1 | sonnet | M | 04-P14b |
| [04-P14d](04/04-P14d.md) | Near-duplicate C5: maintenance.fix-book-file-paths (job) retires, maintenance.mark-missing-files survives | 1 | sonnet | S | 04-P14c |
| [04-P14e](04/04-P14e.md) | Near-duplicate C6: maintenance.repair-missing-files (job) retires; missing-file-repoint and recover-missing-files survive | 1 | sonnet | S | 04-P14d |
| [04-P14f](04/04-P14f.md) | Near-duplicate C7: maintenance.bulk-fetch-metadata (job) retires; library.bulk-metadata-fetch and metadata.candidate-fetch survive | 1 | sonnet | S | 04-P14e |
| [04-P3b](04/04-P3b.md) | One shared .bak sweep helper; retire the cleanup-backups job (C4) | 1 | sonnet | M | 04-P4f |
| [04-P4a](04/04-P4a.md) | Merge four no-body-diff scheduler twins (temp-file-cleanup, trash-cleanup, tombstone-cleanup, db-optimize) into their maintenance.* survivors | 1 | sonnet | M | 04-P5, 04-P1 |
| [04-P4b](04/04-P4b.md) | Merge scheduler.purge-deleted into maintenance.purge-deleted and collapse the two runAutoPurgeSoftDeleted copies | 1 | sonnet | M | 04-P4a |
| [04-P4c](04/04-P4c.md) | Merge scheduler.metadata-refresh into maintenance.metadata-refresh (two runMetadataRefreshScan copies) | 1 | sonnet | M | 04-P4b |
| [04-P4d](04/04-P4d.md) | Merge scheduler.resolve-production-authors into maintenance.resolve-production-authors (inline logic on both sides) | 1 | sonnet | M | 04-P4c |
| [04-P4e](04/04-P4e.md) | Merge scheduler.author-split-scan into maintenance.author-split-scan (about 240 duplicated lines, both carry the ModifyBook fix) | 1 | sonnet | L | 04-P4d |
| [04-P4f](04/04-P4f.md) | Merge scheduler.cleanup-old-backups into maintenance.cleanup-old-backups (retention source already unified by 04-P3a) | 1 | sonnet | M | 04-P3a, 04-P4e |
| [04-P5](04/04-P5.md) | Series twins: dedup.series-prune and dedup.series-normalize survive, maintenance.series-* become aliases | 1 | sonnet | S | 04-P2, 04-P1 |
| [04-P6](04/04-P6.md) | Boot goroutines become ops: external-id, movement-atoms, remux, transcode+quarantine, book_atpath, activity filter-index reconcile | 1 | opus | M | 04-P14f, 07-R2, 07-R3, 07-R4, 01-P81a |
| [04-P7](04/04-P7.md) | opchange_by_book index ensure-mode op replaces the boot goroutine | 1 | opus | M | 04-P6, 07-R2, 07-R3, 07-R4 |
| [04-P9](04/04-P9.md) | Label refinement chain becomes a parent op (dedup.label-refinement) using childop.Follow | 1 | sonnet | S | 04-P7, 04-P1, 04-P4a |
| [05-PR0](05/05-PR0.md) | Docs truth pass for operations (stop documenting code that is gone) | 1 | sonnet | S | none |
| [05-PR1](05/05-PR1.md) | One typed run-status table in Go, generated into TypeScript | 1 | opus | M | 05-PR0 |
| [05-PR2](05/05-PR2.md) | `timed_out` outcome and lifecycle metrics on the OTel meter | 1 | sonnet | S | 05-PR1, 05-PR4, 11-PR4 |
| [05-PR3](05/05-PR3.md) | Write fence, hold the exclusive key until the goroutine exits, per-row cancel in Repairs apply | 1 | opus | M | 05-PR1 |
| [05-PR4](05/05-PR4.md) | `opv3:` keyspace, migration 065, v2 mirror | 1 | opus | L | 05-PR1, 05-PR3 |
| [05-PR5](05/05-PR5.md) | `pkg/ops` SDK, chunk-leasing Batch runner, fenced Writer, v2 adapter | 1 | opus | L | 05-PR3, 05-PR4, 08-X2, 07-R2, 07-R3, 07-R4, 11-PR4 |
| [05-PR6](05/05-PR6.md) | `opstest` harness and `Conformance` (fault injection for chunk leasing) | 1 | opus | M | 05-PR5 |
| [05-PR7](05/05-PR7.md) | One op catalog, a rewritten `oplint`, and a startup ledger check | 1 | opus | M | 05-PR5, 05-PR6, 07-S3, 01-P3, 07-R2, 07-R3, 07-R4, 01-P81a, 04-P6, 04-P7, 04-P13 |
| [05-PR8](05/05-PR8.md) | One schedule declaration (`ops.Schedule`) and a cron evaluator in the TaskScheduler | 1 | sonnet | M | 05-PR5, 05-PR7, 04-P1, 04-P4a, 04-P4b, 04-P4c, 04-P4d, 04-P4e, 04-P4f, 04-P9, 04-P12 |
| [05-PR9](05/05-PR9.md) | `/api/v3/ops/*` census, timeline and SSE endpoints | 1 | sonnet | M | 05-PR4, 05-PR5, 05-PR8, 08-X2 |
| [06-P3](06/06-P3.md) | Ship the pprof tag in the normal deploy build; the committed Makefile owns the build flags | 1 | sonnet | S | 07-C3, 06-P1 |
| [07-G1](07/07-G1.md) | ratchet the flattened `database.Store` method count and the wide-consumer references, as Go tests | 1 | sonnet | S | 07-C1, 07-C3 |
| [07-G2](07/07-G2.md) | layering rule as a Go test with a shrink-only map of known violations | 1 | sonnet | S | 07-C1 |
| [07-G3](07/07-G3.md) | ratchet direct `config.AppConfig` reads as a Go test | 1 | sonnet | S | 07-C1 |
| [07-G4](07/07-G4.md) | one gate manifest, one-way ratchets, and a baseline-lowering bot | 1 | sonnet | M | 07-C1, 07-C3, 07-G1, 07-G2, 07-G3, 06-P3, 01-P9 |
| [07-R2](07/07-R2.md) | readiness in `/health`, a counted memdb fallback, and operations that wait for warmup | 1 | sonnet | M | 07-C1 |
| [07-R3](07/07-R3.md) | classify every startup step as fatal or degraded, in one table | 1 | opus | M | 07-R2 |
| [07-R4](07/07-R4.md) | `Type=notify` readiness and a deploy that waits for `ready` | 1 | opus | M | 07-R2, 07-R3, 06-P3 |
| [07-S1](07/07-S1.md) | `dbtest.NewStore(t, opts...)`, one way to get a test store | 1 | sonnet | S | 07-C1, 07-R2 |
| [07-S3](07/07-S3.md) | drive search indexing from `ChangeObserver` and delete `indexedStore` | 1 | opus | L | 07-R4, 07-G1, 07-S1 |
| [07-S6](07/07-S6.md) | the first-audio-file helper that replaces reads of `Book.FilePath` | 1 | sonnet | S | 07-C1 |
| [10-PR0](10/10-PR0.md) | Deluge client: remove-with-data, ratio, seed time, files | 1 | sonnet | S | none |
| [10-PR1](10/10-PR1.md) | write the torrent-to-book link on import (`BookVersion.TorrentHash`, `BookFile.DelugeHash`, discovery import path) | 1 | sonnet | M | 10-PR0 |
| [10-PR2](10/10-PR2.md) | `deluge.link-backfill` op: link existing book files to their torrents | 1 | opus | M | 10-PR0, 10-PR1 |
| [11-PR2](11/11-PR2.md) | OTLP metric reader behind four config keys (off by default, never fatal, no fallback) | 1 | sonnet | M | 11-PR1, 01-P1, 01-P2 |
| [11-PR3](11/11-PR3.md) | aidispatch migration proof (5 `client_golang` families to OTel, identical Prometheus names) | 1 | sonnet | S | 11-PR1 |
| [11-PR4](11/11-PR4.md) | `internal/opsmetrics` (OTel instruments for ops v3), per-def cardinality fix, alert and recording rules | 1 | opus | M | 11-PR1 |
| [11-PR5](11/11-PR5.md) | `client_golang` constructor ratchet in `make ci` (baseline 69) | 1 | sonnet | S | 11-PR1 |
| [11-PR7](11/11-PR7.md) | AI call metrics and traces (`WithAISpan` wiring at the 9 call sites, exported on `/metrics`) | 1 | sonnet | M | 11-PR1, 11-PR4, 11-PR3 |
| [01-P72](01/01-P72.md) | Drop dead dedup routes and verb aliases | 2 | sonnet | M | 01-P3, 01-P7, 03-PR1 |
| [01-P73](01/01-P73.md) | Delete the dead dedup category C cluster (MergeBooks and friends) | 2 | sonnet | M | 03-PR11, 03-PR12 |
| [01-P75](01/01-P75.md) | Delete the SQLite activity backend | 2 | opus | M | 01-P1, 01-P2, 11-PR2, 05-PR8, 01-P79a, 01-P76, 04-P10, 01-P80, 02-PR19 |
| [01-P76](01/01-P76.md) | Delete internal/download and its config | 2 | sonnet | S | 07-C1, 01-P79a, 10-PR0, 04-P10 |
| [01-P80](01/01-P80.md) | Remove the 11 unread Settings fields and their UI | 2 | sonnet | M | 01-P76, 01-P79a, 04-P10, 01-P79b, 01-P79c |
| [01-P81b](01/01-P81b.md) | Retire the reading-state routes (books/:id aliases and status repair) | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81c](01/01-P81c.md) | Retire the collections and playlist-export routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81d](01/01-P81d.md) | Retire the audiobook alternative-titles, path-history and rescan routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81e](01/01-P81e.md) | Retire the narrator, work-stats and entity-tag routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81f](01/01-P81f.md) | Retire the provider-throttle and metadata-fields routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81g](01/01-P81g.md) | Retire the cache, activity-maintenance, system-log and diagnostics routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81h](01/01-P81h.md) | Retire the merge-journal routes and duplicate verb aliases | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81i](01/01-P81i.md) | Retire the server_lifecycle-owned orphan routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81j](01/01-P81j.md) | Retire the catalog, op-defs, AI-status, tools and review-replay routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [01-P81k](01/01-P81k.md) | Retire the Deluge discovery, iTunes diagnostics, API-key rotate and version-alias routes | 2 | sonnet | S | 01-P81a, 01-P72, 03-PR12, 01-P3 |
| [02-PR14](02/02-PR14.md) | identification.advance: a dirty-set driver in the v3 shape (Pages source, Item, Finish) on the v2 RunItems adapter | 2 | opus | L | 02-PR4, 02-PR6, 04-P1, 04-P4a, 04-P9, 04-P12, 05-PR3 |
| [02-PR19](02/02-PR19.md) | Retire the full-index Review mode: delete the index path, return 410 for view=index for one release, delete the flag | 2 | sonnet | S | 02-PR18, 03-PR10, 01-P80 |
| [02-PR7b](02/02-PR7b.md) | Local author-catalog blocking stage (B1 identifier, B2 exact author block) ahead of the provider fan-out, plus the folder-parse variant | 2 | opus | M | 02-PR7a, 02-PR6, 02-PR4 |
| [02-PR8](02/02-PR8.md) | Window-print inverted index (fpwinidx), SigWindowAcoustID dedup signal and fragment-to-parent containment evidence | 2 | opus | M | 05-PR3, 02-PR9a, 03-PR1 |
| [03-PR1](03/03-PR1.md) | Move the compare-drawer closure under review/compare | 2 | sonnet | S | 01-P7 |
| [03-PR10](03/03-PR10.md) | Redirect /dedup to Review, drop the sidebar entries, repoint the backend announcement | 2 | sonnet | S | 03-PR2, 03-PR5, 03-PR7a, 03-PR7b, 03-PR8, 03-PR9a, 03-PR9b, 03-PR9c |
| [03-PR11](03/03-PR11.md) | Delete the frontend of the old /dedup page and its ported tests | 2 | sonnet | M | 03-PR10, 01-P72, 01-P7 |
| [03-PR12](03/03-PR12.md) | Retire the dead /audiobooks/duplicates routes behind a 410 Gone stub, after proving nothing calls them | 2 | opus | S | 03-PR11, 01-P72 |
| [03-PR13](03/03-PR13.md) | Docs: AI-REFERENCE, port-inventory banners, changelog and executive summary for the retirement | 2 | sonnet | S | 03-PR12, 01-P73 |
| [03-PR2](03/03-PR2.md) | Dupes lane: layer filter, layer chips, bulk keep-older/newer, iTunes and partial-fingerprint chips | 2 | sonnet | M | 03-PR1, 02-PR18 |
| [03-PR3](03/03-PR3.md) | Dupes lane: export duplicates as CSV or JSON | 2 | sonnet | S | 03-PR2, 01-P7 |
| [03-PR4](03/03-PR4.md) | AcoustID key in Settings, online lookup and reset commands, audio-match section in the compare drawer | 2 | sonnet | S | 03-PR1, 03-PR3 |
| [03-PR5](03/03-PR5.md) | Gold Labels as Labels and Suspicious sub-views of the Duplicates lane | 2 | sonnet | M | 03-PR1, 03-PR4 |
| [03-PR6](03/03-PR6.md) | Duplicates lane: Clusters view and cluster verbs | 2 | sonnet | L | 03-PR2 |
| [03-PR7a](03/03-PR7a.md) | New "Authors & series" lane: lane scaffolding and the Authors sub-view | 2 | sonnet | M | 03-PR6 |
| [03-PR7b](03/03-PR7b.md) | Authors & series lane: Series sub-view | 2 | sonnet | S | 03-PR7a |
| [03-PR8](03/03-PR8.md) | Authors & series lane: AI scans sub-view | 2 | sonnet | M | 03-PR7a, 03-PR7b |
| [03-PR9a](03/03-PR9a.md) | Repairs fixer "dedup.series-prune" (merge duplicate series, delete orphan series), written Evaluate-style | 2 | opus | M | 04-P2, 04-P5, 04-P12 |
| [03-PR9b](03/03-PR9b.md) | Repairs fixer "reconcile.missing-files" (view and apply reconcile matches), written Evaluate-style | 2 | opus | M | 04-P14c, 03-PR9a, 03-PR9c |
| [03-PR9c](03/03-PR9c.md) | Repairs fixer "dedup.split-books" (split-book clusters), written Evaluate-style | 2 | opus | M | 03-PR9a |
| [07-F3](07/07-F3.md) | TanStack Query v5 pilot on the Repairs lane, kept only if it removes code | 2 | opus | M | 07-C1, 03-PR10 |
| [01-T1](01/01-T1.md) | Delete unreachable functions in internal/database | F | sonnet | M | 01-P4, 05-PR4, 01-P2, 01-P75, 01-P9 |
| [01-T2](01/01-T2.md) | Delete unreachable functions in internal/server and its handler packages | F | sonnet | M | 01-P3, 01-P72, 03-PR12, 01-P81a, 01-P81b, 01-P81c, 01-P81d, 01-P81e, 01-P81f, 01-P81g, 01-P81h, 01-P81i, 01-P81j, 01-P81k, 01-P5 |
| [01-T3a](01/01-T3a.md) | Delete unreachable functions in metadata, providerhttp, openlibrary and authority | F | sonnet | S | 02-PR6, 01-P6 |
| [01-T3b](01/01-T3b.md) | Delete unreachable functions in metafetch, metabatch, catalog, matcher, personname and franchise | F | sonnet | S | 02-PR7b, 02-PR9a |
| [01-T4a](01/01-T4a.md) | Delete unreachable functions in scanner, organizer, undo and backup | F | sonnet | S | 01-P77, 01-P6 |
| [01-T4b](01/01-T4b.md) | Delete unreachable functions in fileops, audioutil, tagger, mediainfo, audioext and pathutil | F | sonnet | S | 01-P6, 01-P79b, 01-P79c |
| [01-T5a](01/01-T5a.md) | Delete unreachable functions in operations, freshness, registry, opmode and scheduler | F | sonnet | S | 01-P4, 04-P1, 04-P4a, 04-P4b, 04-P4c, 04-P4d, 04-P4e, 04-P4f, 04-P9, 04-P12 |
| [01-T5b](01/01-T5b.md) | Delete unreachable functions in plugins/maintenance, repairs, applygate and applycap | F | sonnet | S | 02-PR8, 02-PR9a, 01-P6 |
| [01-T6a](01/01-T6a.md) | Delete unreachable functions in errhandling, logger, httputil, util, config, security, sysinfo, appdirs and policy | F | sonnet | M | 01-P80, 07-C1 |
| [01-T6b](01/01-T6b.md) | Delete unreachable functions in cache, searchcache, search, realtime, serviceregistry, syncapi/progress, activity, merge, versionprimary and metrics | F | sonnet | S | 01-P75, 01-P73 |
| [01-T7](01/01-T7.md) | Delete unreachable functions in ai, aidispatch, fingerprint, diagnosis and audiobooks | F | sonnet | S | 11-PR3, 02-PR1 |
| [06-P5](06/06-P5.md) | go fix batch 1: any, forvar, minmax, errorsastype, reflecttypefor, stringscut*, stringsbuilder, stditerators, inline | F | sonnet | M | 06-P1, 01-T1, 01-T2, 01-T3a, 01-T3b, 01-T4a, 01-T4b, 01-T5a, 01-T5b, 01-T6a, 01-T6b, 01-T7, 02-PR9a |
| [06-P6a](06/06-P6a.md) | go fix batch 2, slice a: internal/database | F | sonnet | M | 06-P5, 01-T1 |
| [06-P6b](06/06-P6b.md) | go fix batch 2, slice b: internal/server (including handlers, middleware) | F | sonnet | M | 06-P5, 06-P6a, 01-T2, 02-PR16 |
| [06-P6c](06/06-P6c.md) | go fix batch 2, slice c: internal/plugins (51 of 61 files are in plugins/maintenance) | F | sonnet | L | 06-P5, 06-P6b, 01-T5b, 02-PR8, 04-P2, 04-P5, 04-P12, 03-PR9a, 03-PR9b, 03-PR9c |
| [06-P6d](06/06-P6d.md) | go fix batch 2, slice d: metadata, matching and AI packages | F | sonnet | M | 06-P5, 06-P6c, 01-T3a, 01-T3b, 01-T7 |
| [06-P6e](06/06-P6e.md) | go fix batch 2, slice e: scanning, operations, organizing and the remaining small packages | F | sonnet | M | 06-P5, 06-P6d, 01-T4a, 01-T5a, 01-T6a, 01-T6b |
| [06-P7](06/06-P7.md) | go fix batch 3: newexpr (test-only pointer helpers) and delete the helpers it orphans | F | sonnet | M | 06-P6e, 06-P5 |
| [06-P8](06/06-P8.md) | go fix batch 4: waitgroupgo and testingcontext | F | sonnet | M | 06-P7 |
| [06-P11](06/06-P11.md) | Vitest 5, then enable fsModuleCache in a second commit | 3 | sonnet | M | 06-P9 |
| [06-P12](06/06-P12.md) | PGO for deploy builds with a committed, trimpath-built profile | 3 | sonnet | S | 06-P3, 07-C3 |
| [06-P4](06/06-P4.md) | Always-on runtime flight recorder with watchdog and search-stall snapshots | 3 | opus | M | 05-PR5 |
| [06-P9](06/06-P9.md) | Typecheck with TypeScript 7 (tsc), keep TypeScript 6 (tsc6) for typescript-eslint | 3 | sonnet | S | none |
| [07-F1](07/07-F1.md) | generate TypeScript types from Go structs with tygo | 3 | sonnet | M | 07-C3, 07-G4, 01-T2 |
| [07-F4](07/07-F4.md) | delete `docs/api/openapi.json` | 4 | sonnet | S | 07-F1 |

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
