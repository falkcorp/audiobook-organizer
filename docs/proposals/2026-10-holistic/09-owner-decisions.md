<!-- file: docs/proposals/2026-10-holistic/09-owner-decisions.md -->
<!-- version: 1.5.0 -->
<!-- guid: 3b9e6f2a-71c4-4d0e-a8b5-9f1c2e7d4a60 -->
<!-- last-edited: 2026-10-09 -->

# 09: Owner decisions on the holistic roadmap

These are the owner's answers to the decisions in `08-integrated-roadmap.md` §7, recorded 2026-10-08. "Recommended" means the owner accepted the recommended answer from §7 as written.

## Wave 0–1 blockers (22)

| # | Answer |
|---|---|
| D1 | Recommended: an undeclared permission means `settings.manage`. Editors lose Library Optimize. |
| D2 | Recommended: the server decoders are guarded, and Fingerprint Books goes to the Mac workers. |
| D4 | Recommended: `maintenance.*` survives, and the `scheduler.*` names become FormerIDs. |
| D5 | Recommended: use `backup_retention_days`, after comparing the two prod values first. |
| D6 | Recommended: hold the zombie's key until its goroutine exits, with an alert at 10 minutes. |
| D8 | Recommended: scheduled writers must declare `.Live()`. |
| D10 | Recommended: flip the activity default to pebble. |
| D16 | Recommended: repoint the pause `running_*` fields to v2. |
| D18 | Recommended: add a deadcode ratchet in CI. |
| D23 | Recommended: schedule `file-integrity-check` and `orphan-book-files-cleanup`, both report-only. |
| D24 | Recommended: the other declared crons stay off and are listed for the owner. |
| D26 | Recommended: delete `maintenance.batch-poller` and keep the inline loop. |
| D27 | Recommended: keep the newer op in each near-duplicate pair, with a 2-week soak before the flag is retired. |
| D28 | Yes to all five SDK defaults, **amended by D28a**. The owner's requirement: ops are parallel by design from the start, the SDK hides that complexity, and status is always visible. |
| D28a | **New, owner-approved.** The Batch runner uses **chunk leasing**: the source is split into chunks (about 256 items) and the chunks are leased to parallel workers. Each finished chunk is recorded in a per-run bitmap or completed-range ledger, so a resume re-runs only chunks that were unfinished or in flight, never finished work above a prefix watermark. Status comes from the same ledger: chunks done/total, items done, each worker's current chunk, and the rate. Op authors write only the per-item function. Parallelism, partitioning (`PartitionBy` keeps same-key items on one worker), checkpoints and status belong to the runner. This replaces the contiguous-prefix watermark in `05-operations-v3/sdk-api.md` §5. |
| D29 | Recommended: a retitle marks candidates stale and does not delete them. |
| D30 | Recommended: Audible and Audnexus 14 days, Open Library 30, Google 7, plus a per-book Search again. |
| D32 | Recommended: `author:` matches any credited author. |
| D36 | **Server-side, measured** (appendix `02-filter-identification-pipeline/D-review-filtering-memory.md`). Review → Metadata moves to server-side filter, count and page queries over the in-memory review cache, which measured 0.2–9 ms at 40k rows. The browser holds the visible page plus the ID list for "select all N", under 2 MB, down from about 140 MB steady and 240 MB peak. PRs: R0 Chrome heap snapshot to find the reported 16 GB (this lane explains only 0.1–0.25 GB), R1 drop the score breakdown from the list payload, R2 `metadata_cache_query.go`, R3 frontend switch behind a flag, R4 retire full-load mode. |
| D43 | Recommended: index from `ChangeObserver` and delete `indexedStore`. |
| D44 | Recommended: `Type=notify`, readiness in `/health`, and ops wait for warmup. The owner installs the unit. |
| D47 | Recommended: ratchets fail only on a rise, and a bot lowers the baseline; one gate list for all runners. |
| D51 | Recommended: phased `Book.FilePath` migration. |

## Remaining decisions (29)

| # | Answer |
|---|---|
| D9 | Recommended. The owner marks the routes their scripts use; the rest are retired per handler group with a 410 stub for one release. The wipe route is retired. |
| D11 | Recommended. Delete the SQLite activity backend one release after the pebble default, with a restore tag. |
| D12 | Recommended. Delete `internal/download`. |
| D13 | Recommended. Delete the rename preview and apply routes. |
| D14a | Keep the windowed fingerprint similarity and wire it as a supporting dedup and fragment signal (same as D34). |
| D14b | Keep intro-transcript classification (`TitleAgreement`, `IsLikelyMisfiled`) and wire it as identification and review evidence. |
| D14c | **Do NOT delete version swap or the Deluge notify code.** The owner needs Deluge integration; see D52. Re-evaluate `RunVersionSwap` when D52 is designed. |
| D14d | **Metrics strategy (the owner wants both OTel and Prometheus, built so they won't need a major refactor later).** Today the OTel MeterProvider (`internal/telemetry/telemetry.go:173`) exports into the default Prometheus registry, and the live `/metrics` route (`server_lifecycle.go:1309`, `promhttp.Handler()`) serves both the OTel instruments and the about 25 `client_golang` series. Decision: (1) all NEW instrumentation, including the ops v3 metrics (05 R15, PR 2 and PR 11), uses the OTel metric API; (2) add an optional OTLP metric exporter as a second reader, enabled by config, alongside the Prometheus reader; (3) existing `client_golang` metrics stay and move to OTel when their code is next touched, with no big-bang rewrite; (4) delete `telemetry.MetricsHandler`, an unused placeholder that serves stub text. |
| D14e | Wire `embed_cover_art` using `tagger.EmbedCoverArt`. Do not delete the cover-embed pair. |
| D15 | Wire `create_backups`, `verify_after_write` and `embed_cover_art`. Remove the other 11 unread Config fields and their Settings UI. |
| D17 | Recommended. Retire the v1 `operation:` keyspace from the UI after 05 PR 4. The rows stay on disk. |
| D19 | Recommended. A new "Authors & series" Review lane. |
| D20 | Recommended. Gold Labels becomes a sub-view of the Dupes lane. |
| D21 | Recommended. Port split-book as a fixer, measure it against the fragment fixer, then decide. |
| D22 | Recommended. A per-page cluster view now; a server-side keep "recommended" later. |
| D25 | Recommended. Dual-write `opv2:` for 30 days after the last wave. |
| D31 | Recommended. Measure catalog coverage of the missing books first, then match catalog-first as review-only candidates. |
| D33 | Recommended. A FormulaVersion rescore runs in a window with no scan. |
| D34 | Yes, as D14a. |
| D35 | Recommended. Two weeks in shadow mode, then τ_auto = 0.98 and τ_review = the lowest P with precision ≥ 0.5. |
| D37 | Recommended. The pprof tag in every deploy with the listener off by default; drop `-N -l`; move the generic build flags into the committed Makefile. |
| D38 | Recommended. A committed PGO profile with `-trimpath`; the flight recorder at 32 MiB with 10 files. |
| D39 | Recommended. TS 7 for typechecking now, side by side with TS 6. |
| D40 | Recommended. No `omitzero` in the sweeps. |
| D41 | Recommended. `repair-library-state` stays frozen and API-only, and is never scheduled. |
| D42 | Recommended. A version-group record holds the primary, in a storage cut-over window. |
| D45 | Recommended. Persist only the config keys that changed. |
| D46 | Recommended. Delete `openapi.json` and generate the TS types. |
| D48 | Recommended. Pilot a server-state library on the Repairs lane; keep it only if it removes code. |
| D49 | Recommended. Split maintenance by domain inside the 05 port waves. |
| D50 | Recommended. Run the go fix batches only in freeze window F, regenerated, after 01 tier 2. |

## New items raised by the owner

| # | Item |
|---|---|
| D52 | **Deluge cleanup after organize (new feature).** Once every file in a torrent has a verified matching `book_file` in the library (same size and a content hash or fingerprint match; never path alone), remove the torrent **and its downloaded data** from Deluge, but only after seeding reaches **ratio 1.0 or 14 days, whichever comes first**. Both thresholds are settings. It is built as a Repairs-style fixer (trial → approve → apply) on the ops v3 Fixer kind, with a declared `Deletes` effect and a journal. It never touches library files or `book_file` rows. It needs its own design doc before implementation; reuse the existing Deluge client and evaluate `versions/swap.go`'s `NotifyDelugeAfterVersionSwap`. |

## Round-2 open decisions (D53–D72)

Appended by the coordinator on 2026-10-09 from `08-integrated-roadmap.md` §7 (v1.1.0). Answered by the owner on 2026-10-09; the third column is the owner's answer (the reviewers' recommendation unless it says otherwise). Nothing above this heading was changed. (One note on the record above: D14d says "about 25 `client_golang` series"; 11 measured 69 families, and its ratchet baseline is 69.)

| # | Question (source) | Recommended answer | Status |
|---|---|---|---|
| D53 | 05 r3. Wrap the four frozen ops (`library.bulk-write-back`, `maintenance.bulk-write-back`, `operations.backfill-legacy-status` until 01 P74, `maintenance.repair-library-state`) as native v3 Tasks whose `Run` calls the untouched v2 body, so `v2compat` can be deleted in 05 PR 15? | **Yes.** Wrap the four frozen ops as native v3 Tasks over untouched bodies; `v2compat` is deleted in 05 PR 15. | **answered 2026-10-09** |
| D54 | 04 Q5a. Pair C2: `scheduler.dedup-llm-review` is three days newer than `dedup.llm-review`, but D4 retires `scheduler.*`. Which survives? | **`dedup.llm-review` survives**; `scheduler.dedup-llm-review` becomes its FormerIDs alias. | **answered 2026-10-09** |
| D55 | 01 Q10. The three category-B files with no decision: `internal/deluge/importer_adapter.go`, `internal/plugins/deluge/import.go`, `internal/ai/telemetry.go`. | **Keep the two Deluge files until 10 PR 2. Do NOT delete `ai/telemetry.go`: the owner wants AI metrics.** New 11 PR 7 adds OTel instruments per provider/model/task (requests, latency histogram, tokens in/out, failures by reason, parse-accepted ratio) on `/metrics`, and wires the dormant `WithOpenAISpan` helper so AI calls also trace. Owner: "should we add metrics around ai and have this data put somewhere?" | **answered 2026-10-09** |
| D56 | 01 P79a. Flip the `embed_cover_art` default to `true` in the same PR that gates the live cover embed on it? | **Yes.** Flip `embed_cover_art` to `true` in the same PR as the gate. | **answered 2026-10-09** |
| D57 | 10 Q6. Copy model (the cleanup fixer) or seed-from-library model (`deluge_move_enabled`)? | **Copy model.** `deluge_move_enabled` stays false and is marked deprecated in Settings; the fixer refuses torrents whose files sit in the library. | **answered 2026-10-09** |
| D58 | 10 Q7. Add `deluge_cleanup_min_age_hours` (default 24) beyond the ratio and age thresholds? | **Yes**, `deluge_cleanup_min_age_hours` default 24. | **answered 2026-10-09** |
| D59 | 07 F3 / appendix C. Adopt TanStack Query v5 for the Repairs pilot, with the bar: at least 150 of 215 server-state lines removed, 22 tests green in at most 3.5 s, bundle at most 650 KB, compiler bailouts not higher; revert on a miss? | **Yes, TanStack Query v5** for the Repairs pilot with the stated bar (≥150 of 215 lines removed, 22 tests green, no new compiler bailouts); revert on a miss. | **answered 2026-10-09** |
| D60 | 07 Q9. Put 07 C1–C3 (CI throughput) at the very front of wave 0, ahead of the Go 1.27.2 bump, and request the `run-go-tests` input from `falkcorp/github-common`? | **Yes.** 07 C1–C3 go to the very front of wave 0; request the `run-go-tests` input, inline fallback. | **answered 2026-10-09** |
| D61 | 11 §3.3. OTLP metric exporter behind new keys (`otel_metrics_otlp_endpoint`, `_interval`, `_insecure`, `telemetry_environment`), off by default, never fatal, no fallback to the trace endpoint? | **Yes.** OTLP metric keys off by default, never fatal, no fallback to the trace endpoint. | **answered 2026-10-09** |
| D62 | 10 Q1 + Q3. Eligibility: only audio files must be verified (sidecars are "discarded with the data"); "in the library" means every file is under `RootDir`, not the `organized` state flag? | **Yes to both.** Audio files only; "in the library" = every file under RootDir. | **answered 2026-10-09** |
| D63 | 10 Q2. Scope only torrents with the discovery label by default (empty = all)? | **The discovery label by default**; empty means all. | **answered 2026-10-09** |
| D64 | 10 Q4 + Q8. The app never deletes a directory Deluge left behind (`orphan_data` rows are held); a Mac-only fingerprint lane is a later v2 only if the `unverifiable` count stays large after the hash backfill? | **Yes to both.** No app-side directory deletes; the Mac fingerprint lane only if `unverifiable` stays large. | **answered 2026-10-09** |
| D65 | 10 Q5. Build the fixer on v2 `repairs.Fixer` now, or wait for 05 PR 5–7? | **Wait for 05 PR 5–7.** 10 PR 0–2 ship now. | **answered 2026-10-09** |
| D66 | 11 Q1–Q3. Scope labels off (`WithoutScopeInfo`), gRPC only, cumulative temporality? | **Yes to all three.** No scope labels, gRPC only, cumulative. **Owner, 2026-10-09: Prometheus compatibility is a hard requirement and stays; the `/metrics` scrape endpoint is the primary surface, OTLP push is an optional second reader.** Every new OTel instrument must appear on `/metrics` with a Prometheus-conventional name; the series-name contract test in 11 PR 1 guards that. | **answered 2026-10-09** |
| D67 | 11 Q5–Q7. Pebble collector stays on `client_golang`; the five `ai_dispatch_*` names stay unprefixed; `internal/metrics` is deleted only when its last family has moved on touch? | **Yes to all three.** Pebble collector stays; `ai_dispatch_*` names stay bare; `internal/metrics` deleted when its last family has moved. | **answered 2026-10-09** |
| D68 | 01 P76 (task brief). Deleting `internal/download` leaves three live readers of `download_client.torrent.deluge` (`internal/deluge/integration.go`, `internal/server/deluge_integration.go`, `internal/maintenance/jobs/bulk_deluge_import.go`): drop the fallback, keep a Deluge-only struct, or defer? | **Defer 01 P76 to wave 2**, after 10 PR 0–2 decide the Deluge config shape; the fallbacks stay until then. Recommended was keep-with-Deluge-only-struct. | **answered 2026-10-09** |
| D5 (note) | 04 P3a. `backup_retention_days` does not exist today; the brief introduces it (default = the current hard-coded retention). | **Approved**: add the setting with today's default. | **answered 2026-10-09** |
| D69 | 01 P79b (task brief). With `create_backups` honoured, do the two bulk write-back ops keep a `.bak-*` sibling per file? `SafeWriteDeps` is a package singleton, so the opt-out must be a context value. | **Bulk skips backups** via `tagger.WithoutBackup(ctx)` at the three bulk entry points; single-book edits keep them when the setting is on. | **answered 2026-10-09** |
| D70 | 01 P72 Q6. Do any owner scripts or curl habits call the 20 dead dedup/verb-alias/ai-review routes? | **No.** P72 proceeds; handlers that lose their last route are deleted. | **answered 2026-10-09** |
| D71 | 01 P81b. Does anything Mac-side sync listening position through `/books/:id/position`? | **No.** Retire the `/books/` alias behind `gone()`; `/audiobooks/:id/position` stays. | **answered 2026-10-09** |
| D72 | 06 P2 decision flag. Pin workflow `go-version:` to the full patch (14 literals, 8 workflows, checker flips to full-pin equality) or keep floating minors? | **Full patch pin everywhere.** | **answered 2026-10-09** |
