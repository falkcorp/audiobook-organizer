<!-- file: docs/proposals/2026-10-holistic/09-owner-decisions.md -->
<!-- version: 1.1.0 -->
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
