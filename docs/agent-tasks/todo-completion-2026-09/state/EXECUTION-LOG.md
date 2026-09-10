<!-- file: docs/agent-tasks/todo-completion-2026-09/state/EXECUTION-LOG.md -->
<!-- version: 1.25.0 -->
<!-- guid: 7a1e4c9d-2b6f-4d38-8e5a-0c3f9b2d6e71 -->
<!-- last-edited: 2026-09-10 -->

# Execution log — burndown 2026-09-10

Coordinator ledger. Owner approved execution at 14:27 EDT 2026-09-10: "start fixing stuff
you're the manager, do the critical one especially any data loss one". CI/CD rows are out
of scope. Rules in force: ≤4 concurrent workers, no forks, tool-call budgets, workers commit
in their own worktree and never push; the coordinator pushes, opens the PR, runs checks,
checks off `TODO.md`, and writes the executive summary. Review-critical PRs (data-loss /
security) are HELD OPEN for the owner — never admin-merged.

## Queue (from FINAL-ANALYSIS §5/§6, app-only, dispatchable)

| Wave | Brief | Risk | Effort | Worker | Status |
|---|---|---|---|---|---|
| 1 | TASK-300 MergeSplitBookCluster RMW lock | data-loss critical | S | go-specialist/sonnet | PR #3181 MERGED by owner 16:3x (`8d63cee95`; held 14:42) |
| 1 | TASK-302 purge-empty-authors guard byte range | data-loss high | S | go-specialist/sonnet | PR #3182 MERGED by owner 16:3x (`7196a2365`; held 14:49) |
| 1 | TASK-303 organize no-op stat (`:141-142` only) | data-loss high | S | go-specialist/sonnet | PR #3180 MERGED by owner 16:3x (`a5be1c9a4`; held 14:41) |
| 1 | TASK-306 backup restore verify | data-loss medium | S | go-specialist/sonnet | first cut REJECTED 14:44 (fail-closed broke default UI restore); reworked; PR #3183 MERGED by owner 16:3x (`a05bbe20f`; held 14:55) |
| 2 | TASK-360 orphan-file hard delete memdb guard | data-loss | S | go-specialist/opus | PR #3185 MERGED by owner 16:3x (`640343ee3`; held 15:24) |
| 2 | TASK-309 scanner AIPhaseSummary discarded | correctness critical | S | go-specialist/sonnet | PR #3186 MERGED 15:41 (rebase, 26/26 green) |
| 2 | TASK-310 ISBN sweep drops provider errors | correctness critical | S | go-specialist/sonnet | PR #3184 MERGED 15:36 (rebase, 26/26 green) |
| 2 | TASK-354 duplicate FilePath in one batch | data-loss | S | go-specialist/opus | PR #3188 MERGED by owner 16:3x (`3e2167421`; held 15:40); L4244 decision surfaced to owner |

**Cap note 15:08:** resuming TASK-309 (finish gate) and TASK-306 (CodeQL rework) while 360/310/354 run made 5 live workers, over the 4 limit. No new dispatch until ≤4.
| 3 | TASK-363 purge-empty-authors file-safety counter | data-loss | M | opus | queued — memdb_reads.go; wait for #3185 (and #3182 same guard family) |
| 3 | TASK-344 MergeBooks audio-route guard | data-loss | M | go-specialist/opus | PR #3187 MERGED by owner 16:3x (`550762858`; held 15:35) |
| 3 | TASK-346 series-normalize trashed-row guard | data-loss | M | go-specialist/sonnet | PR #3189 MERGED by owner 16:3x (`69a95a065`; held 15:44) |
| 3 | TASK-347 series-denumber trashed-row guard | data-loss | M | go-specialist/sonnet | PR #3190 MERGED by owner 16:3x (`316ddd73a`; held 15:48) |
| 3 | TASK-358 series-dedup journaling + scan check | data-loss | M | go-specialist/opus | PR #3191 MERGED by owner 16:3x (`43ed26986`; held 15:57) |
| 3 | TASK-359 series-merge unguarded denominator | data-loss | M | | queued — touches pebble_store.go → wait for #3182/#3185 to merge |
| 4 | TASK-301 bulk journaling helper (reshaped) | data-loss | M | opus | queued — after 300 merges (dedup files) |
| 4 | TASK-361 author-book memdb guard | data-loss | L | opus | queued — pebble_store.go; wait for #3182/#3185 |
| 4 | TASK-338 retire fix-library-states | data-loss | S | go-specialist/opus | PR #3192 MERGED by owner 16:3x (`c02e38a39`; held 16:00); job never run |
| 4 | TASK-362 memdb-lossy-readers headline + 2 defects | data-loss | S | | queued — memdb_reads.go; wait for #3185 |
| 4 | TASK-304 web author-merge popover | data-loss | S | typescript-specialist/sonnet | PR #3193 MERGED by owner 16:3x (`472af5c55`; held 16:00) |
| later | TASK-140 retire cleanup-merged apply path | data-loss | S | go-specialist/sonnet | PR #3194 MERGED by owner 16:3x (`44e254ccb`; held 16:06) |
| later | TASK-337 DELETE /operations/history dry-run | weak data-loss | M | go-specialist/opus | PR #3198 HELD (16:23); delete-by-id half NOT built (recommendation in PR) |
| later | TASK-340 writeback_batcher Stop() join | data-loss | M | go-specialist/opus | PR #3196 MERGED by owner 16:3x (`2ef5f01c5`; held 16:14) |
| security | TASK-308 SSE ACAO wildcard override | security | S | go-specialist/sonnet | PR #3195 MERGED by owner 16:3x (`ed958d4a1`; held 16:06) |
| later | TASK-305 migration record + version unbatched | data-loss (latent) | M | go-specialist/opus | PR #3197 HELD (16:21) |
| security | TASK-348 mask remaining `GET /config` secrets | security | M | go-specialist/opus | PR #3199 HELD (16:29); owner note: nested download-client secrets clear via config file only |
| security | TASK-080 SSRF on cover fetch (fix #645, assess #662) | security | M | go-specialist/opus | dispatched 16:16 (covers.go + cover.go shared hardened client; no dismissals) |
| security | TASK-083 path-injection #1477/#1478 safe_operations.go | security | M | go-specialist/opus | dispatched 16:21 (structural ReadDir/lookup barrier; no dismissals) |
| later | TASK-072 operator-confirmed author merge op | data-loss | M | go-specialist/opus | dispatched 16:24 (new op; dry-run default, ref-count guard, ledger) |
| security | TASK-160 OpenAI key validation server-side (SEC-9) | security | M | general-purpose/opus | dispatched 16:30 (new setup endpoint + WelcomeWizard.tsx) |
| later | TASK-220, 352(prod run), 373, 342, 345, 114, 096; security 335(reshaped), 365(needs owner policy: opt-in vs local-only), 366, 368 | | | | queued in matrix order; 220/114/096/345 touch files of held PRs; 352 is a prod repoint run (banned) |

**16:35 owner merged 15 held PRs** (#3180–#3196 except #3179; #3197/#3198/#3199 still open). Coordinator: 15 worktrees removed + pruned; `TODO.md` check-off PR #3200 (11 lines; L4244 stays open); unblocked queue now dispatchable in order 359 → 363 → 301 → 362 → 361 → 345 as slots free (cap 4).

## Per-task record

### TASK-303 — SF-01 organize same-path no-op stat

- Worktree `.worktrees/organize-303`, branch `agent/organize-303-single-file-organize-no-op-paths-report`, sha `45cf6eedb`.
- Files: `internal/organizer/organizer.go` 1.41.0, `organizer_test.go` 1.9.0, `changelog.d/20260910_organize_303.md`.
- Regression `TestOrganizeBook_NoOpSamePathMissingFile`: failed pre-fix (nil error), passes post-fix.
- Gate exit 0 (build/vet/test, `-race`, staticcheck, itunes package). Rollback: pure code change.
- PR #3180 — HELD for owner. No `TODO.md` line. Side-finding filed: `todo.d/2026-09-10-reorganize-in-place-same-path-no-stat.md` (SF-01b).

### TASK-300 — DA-01 MergeSplitBookCluster lock

- Worktree `.worktrees/dedup-300`, branch `agent/dedup-300-mergesplitbookcluster-performs-an-unguar`, shas `91503d98b` + `c29510ffa`.
- Files: `internal/dedup/split_book_merge.go` 1.6.0, `internal/merge/serialize.go` 1.1.0, new `split_book_merge_concurrent_test.go`, `changelog.d/20260910_dedup_300.md`.
- Regression `TestMergeSplitBookCluster_SharesLockWithMergeService`: pre-fix `maxActive=9, want 1` on 5/5 `-race` runs; post-fix `maxActive=1`.
- Deadlock check done on both callers. Gate exit 0 (dedup + merge `-race`, staticcheck). Rollback: pure code change.
- PR #3181 — HELD for owner. No `TODO.md` line. Side-finding filed: `todo.d/2026-09-10-dedup-books-job-unguarded-merge-rmw.md` (DA-01b).

### TASK-306 — SV-02 backup restore verify

- Worktree `.worktrees/server-handlers-306`, branch `agent/server-handlers-306-post-backup-restore-caller-requested-che`, first sha `7ff85a3bf` (option b, fail-closed).
- REJECTED at review: `web/src/pages/Settings.tsx:195` defaults the verify checkbox to true and `api.ts:3629` defaults `verify=true`, so option (b) turns every UI restore into a 400. Worker re-tasked 14:44 to option (a): `.sha256` sidecar written atomically at create, verified at restore, `ErrChecksumMismatch` on tamper, legacy no-sidecar stays fail-closed with an actionable message, `verified:true` on success.
- Rework sha `44f9c9451`: sidecar write in `CreateBackup` (temp+rename; failure removes the archive), `verifyChecksumSidecar` in `RestoreBackup`, handler 409/400/500 routing, sidecar removed by `DeleteBackup` and retention, listing pinned to ignore `.sha256`. 5 backup + 4 handler tests. Gate exit 0, `-race` clean, staticcheck clean. `web/` untouched.
- 15:07 CodeQL on #3183: 3 NEW alerts on the sidecar code (path-injection backup.go:568/:809, log-injection :810). Worker re-tasked: `RestoreBackupIn`/`DeleteBackupIn`(backupDir, filename) resolve the target from `os.ReadDir` and build every path from the directory entry (the only credited barrier shape in this repo — see memory `reference_codeql_sanitizer_barriers`); handler passes the config dir + sanitized name; sidecar warn logs use the entry name and a sanitized error. No dismissals.
- 15:19 rework sha `e34bfba45` pushed: `RestoreBackupIn`/`DeleteBackupIn` entry-resolution, `verifyChecksumSidecar(archivePath, sidecarPath)`, handler on the `...In` forms, sanitized sidecar logs, 3 new tests. Gate exit 0, `-race` clean, staticcheck clean. CodeQL re-run pending.
- PR #3183 — HELD for owner. Rollback note in the PR: one new metadata file per archive; owner to say if it wants the dry-run protocol. Deploy note: pre-existing backups have no sidecar, so their first verified restore returns 400 until re-created.

### TASK-302 — DB-01 purge-empty-authors guard

- Worker paused after 62 calls with the gate still running in the background; resumed 14:40 with a 20-call budget to finish the gate and report.
- Worktree `.worktrees/database-302`, branch `agent/database-302-purge-empty-authors-delete-guard-book-sc`, sha `1011cdb22`.
- Files: `internal/database/author_bookref.go` 1.5.0, `pebble_store.go` 1.146.0, tests in `author_bookref_test.go` + `author_getter_conformance_test.go`, `changelog.d/20260910_database_302.md`.
- Bounds `["book:0","book:;")` → `["book:","book;")` in pass 2 and in `GetBooksByAuthorIDWithRoleCore`; the latter's `:path:`-only filter upgraded to `strings.Count(key, ":") != 1`.
- Regression: two non-ULID-id tests failed pre-fix (`expected 1, actual 0`; missing `ZZBOUNDS…`), pass post-fix incl. `-race`. Gate exit 0 (`internal/database` 567s). Rollback: pure code change.
- PR #3182 — HELD for owner. No `TODO.md` line. Not run: the brief's live audit for non-digit `book:` keys on prod (worker ban) — owner's call before merge.
- Observed, unfiled: `getBooksByAuthorIDFull` has no `iter.Error()` check; `GetBooksByAuthorIDWithRoleCore` `continue`s on unmarshal error.
- CI `Repo Guards` failed on `gofmt` (comment alignment in `author_bookref_test.go`); fixed by the coordinator in `5a3c31416`. Lesson: every worker prompt now requires `gofmt -l` on changed files before commit.

### TASK-310 — SF-03 ISBN sweep discards provider errors

- Worker paused twice on a background gate; resumed 15:09 foreground-only. Worktree `.worktrees/metadata-310`, branch `agent/metadata-310-isbn-asin-enrichment-sweep-discards-ever`, sha `69143137a`.
- Files: `internal/metafetch/isbn.go` 1.10.0, `service_mock_test.go` 1.11.0, new `isbn_source_errors_test.go`, `changelog.d/20260910_metadata_310.md`.
- Search helpers return errors; `sourceSearchError` (per-source counts, allErrored) wrapped by `EnrichBookISBN`; sampled WARN (1st then every 20th per source); `EnrichMissingISBNs` counts `errored` apart from `checked`, adds per-source totals to the summary, returns `ErrAllSourcesErrored` when every attempted book errored (both op callers already propagate).
- Pre-fix evidence is a compile failure of the new test file (new symbols), not a behavioral assertion — weaker than the other briefs; noted in the PR.
- Gate exit 0 (gofmt, build/vet/test 43.9s, `-race`, staticcheck). Rollback: pure code change.
- PR #3184 — standard lane; merge on green gate. No `TODO.md` line.

### TASK-360 — TODO L5139 orphan-file hard delete fail-open

- Worktree `.worktrees/maintenance-360`, branch `agent/maintenance-360-orphan-files-hard-delete-fail-open-inter`, shas `7855c7160` + coordinator gofmt fix on `mock_store.go`.
- 11 files: guarded twin `GetAllBooksCoreComplete` (memdb + PebbleStore fall-through on `ErrMemdbIncomplete`), `ListSoftDeletedBooks` guarded in place with fall-through, `BookCompletenessReader` on `BookStore`, orphan scan reads the guarded getter and aborts with no delete, mocks regenerated, 5 tests, fragment.
- Worker divergence (accepted): did NOT guard `GetAllBooksCore` in place (~100 call sites incl. request paths; a recorded loss never clears without restart) — same twin split as #2839. Rationale in doc comments and the PR.
- Pre-fix probe: memdb missing two rows → book absent from the membership set → all its files classed orphan. Gate exit 0 (maintenance 52s, full database pkg, `-race`, golangci interfacebloat 0). Rollback: pure guard.
- PR #3185 — HELD for owner. `TODO.md` L5139 to check off on merge. Conflict note: touches `pebble_store.go` like #3182 — second to merge needs a rebase.

### TASK-309 — SF-02 inline AI-parse summary discarded

- Worker paused on a background gate; resumed 15:05 foreground-only (ended at 122 calls, over budget). Worktree `.worktrees/scanner-309`, branch `agent/scanner-309-inline-ai-parse-phase-result-is-discarde`, sha `1cf20a4ed`.
- 10 files: `AIPhaseSummary.ReportTo`, variadic `onAIPhaseWarning` on `ProcessBooksParallel` + `Scanner` interface, `ScanRequest.OnAIPhaseWarning` threaded through `scanFolder`/`processChunk`, `library.scan` and `library.folder-auto-scan` wire it to `reporter.Log(WARN)`; three test files updated for the signature; fragment.
- Regression `TestProcessBooksParallelReportsFailedInlineAIPhase`: pre-fix "Should NOT be empty" (with `.ReportTo` reverted), passes post-fix.
- Gate: gofmt/build/vet exit 0; scanner package has ONE failing test `TestPersistChaptersForBook_MultiFileMP3s_SynthesizesFromTrackTags` — coordinator re-ran it on main `42d187168`: fails identically (pre-existing, already filed at `TODO.md` L2470). Server tests `-run 'Autoscan|AIParse|LibraryScan'` ok (run by the coordinator; worker skipped for budget). staticcheck 0 in touched files.
- PR #3186 — standard lane; merge on green gate. No `TODO.md` line.

### TASK-344 — TODO L2304 MergeBooks audio-route guard

- Worktree `.worktrees/misc-go-344`, branch `agent/misc-go-344-dedup-mergebooks-hard-delete-path-has-no`, sha `a68b3a75a`.
- `guardKeeperAudioRoute` under the merge lock before any write, returns the sibling's `merge.FilelessPrimaryError`; read errors refuse; the one live caller (`itunes_heal.go` `resolveAmbiguousByDB`) already failed closed and now logs the refusal. 3 tests (refuse / all-fileless allowed / FilePath-only keeper allowed). Gate exit 0 (reconcile, dedup+reconcile `-race`, staticcheck 0).
- PR #3187 — HELD for owner. `TODO.md` L2304 to check off on merge.

### TASK-354 — TODO L4241/L4242/L4244 same-FilePath / same-PID rows in one batch

- Worktree `.worktrees/database-354`, branch `agent/database-354-two-rows-with-the-same-filepath-in-one-b`, sha `498c6b515`.
- `BatchUpsertBookFiles`: `stagedByPath` + `stagedByPID` consulted before either committed lookup; later row merges into the earlier staged row (identity from first, content from last = N sequential upserts). 3 tests incl. one pinning lookup order. Full `internal/database` suite 721s exit 0 (needs `-timeout 25m`; default 10m panics — CI already passes 25m/30m).
- L4244 measure-only: existing `maintenance.dedupe-book-file-rows` (dry-run default) counts a FLOOR (groups BookID+FilePath; misses cross-book path dupes and all PID dupes). Owner decision: run dry / run apply / fund an ~80-line read-only PID+cross-book count op. Nothing run on prod.
- Anchor drift: `enforceBookFilePIDUniqueness` no longer exists (`stagePIDTransfer` replaced it); the gap is real.
- PR #3188 — HELD for owner. On merge check off L4241, L4242, L4243 (test exists verbatim); L4244 stays open.

### TASK-346 — TODO L2887 series-normalize trashed-row guard

- Worktree `.worktrees/server-handlers-346`, branch `agent/server-handlers-346-series-normalize-trashed-gap-mergeseries`, sha `a32008be5`.
- `executeSeriesNormalizeCore` reads unfiltered `SeriesRefCounts` once (read failure refuses the pass); `mergeSeriesGroupHelper(store, keep, merges, refCounts) (merged, refused, err)` refuses `DeleteSeries` when `refCounts[from] - moved > 0`, reporting into `errs`. New test + 2 signature updates. Gate exit 0; staticcheck 0 in touched files.
- PR #3189 — HELD for owner. `TODO.md` L2887 to check off on merge.

### TASK-347 — TODO L2901 series-denumber trashed-row guard

- Worktree `.worktrees/maintenance-347`, branch `agent/maintenance-347-series-denumber-trashed-gap-internal-plu`, shas `33da20fff` + `3858528a3` (guard moved above the dry-run branch after an advisor pass).
- `SeriesRefCounts` fetched once per run (fail closed); apply deletes only when `movedAll && refCounts[from] <= len(books)`, held-back count in the summary; dry run previews the same. 3 tests. Gate exit 0, staticcheck 0. Stale TODO note corrected: no `OpsStore` widening needed (`author_purge_empty.go` already does this).
- Worker used `git stash` despite the ban; stash stack verified unchanged (4 pre-existing entries).
- PR #3190 — HELD for owner. `TODO.md` L2901 to check off on merge.

### TASK-358 — TODO L4967 series-dedup undo ledger + scan stand-down

- Worktree `.worktrees/dedup-358`, branch `agent/dedup-358-dedup-series-dedup-s-apply-path-writes-n`, sha `2c3536fa0` (based on `2ed12521b`).
- `series_dedup.go` 1.10.0: `ScanStandDownController` interface (`*server.Server` satisfies it); `DedupSeries(ctx, store, opID, scan, progress, dryRun)` refuses apply without opID / controller / stand-down; one `metadata_update`/`series_id` ledger row per reassigned book and one `series_delete` row per deleted series; failed ledger writes land in `result.Errors`; lease renewed per group, lapse aborts. `duplicates_ops.go` 2.16.0 passes `opID` + server. 6 tests in `series_dedup_undo_ledger_test.go`; 2 test files updated for the signature.
- Gate exit 0 (build/vet/test dedup, `-race`, staticcheck only the pre-existing `server_helpers.go:62`). Rollback YES: dry-run default unchanged; `series_delete` rows are audit-only (undo engine has no case for that type); stand-down does not quiesce a resumed scan; lease-lapse abort skips `dedupCache.InvalidateAll()` (same as the pre-existing cancelled path).
- PR #3191 — HELD for owner. `TODO.md` L4967 to check off on merge.

### TASK-338 — TODO L1294 delete `fix-library-states`

- Worktree `.worktrees/maintenance-338`, branch `agent/maintenance-338-fix-or-unregister-fix-library-states`, sha `d30529cc1`. The job was never run anywhere.
- `fix_library_states.go` deleted (registration goes with it); `fix_library_states_test.go` 2.0.0 is an absence test (`Get` errors AND id absent from `All()`; pre-fix "An error is expected but got nil"); `wantJobCount` 38→37; dispatcher comment corrected from a stale "18 of 34" to the measured 21 of 37; id removed from the `openapi.json` maintenance enum. Vocabulary sweep: no consumer of `present`/`missing` `library_state` anywhere in `internal/`, `cmd/`, `web/src`.
- Gate exit 0 (build/vet/test maintenance + server; staticcheck only the pre-existing `server_helpers.go:62`; JSON valid). Rollback: removes a write path.
- PR #3192 — HELD for owner. `TODO.md` L1294 ("Fix or unregister `fix-library-states`") to check off on merge.

### TASK-304 — WEB-04 author-merge popover error state

- Worktree `.worktrees/web-304`, branch `agent/web-304-author-merge-preview-popover-shows-an-au`, shas `1443157f6` + `4f825a3f7`.
- `api.ts` 2.84.0: `getBooksByAuthor` throws on non-OK (one caller). `DedupAuthorTab.tsx` 1.2.1: `Promise.allSettled`, `failedCount`, "Could not load N of M" banner with a working Retry; "No books found" only on a successful empty fetch. `group.book_count` verified server-supplied. 3 tests (pre-fix "Unable to find an element with the text: /could not load/i").
- Gate: lint 0 errors, tsc exit 0, vitest 1070/1070, build exit 0, prettier clean. Rollback: frontend only.
- PR #3193 — HELD for owner. No `TODO.md` line exists for WEB-04.

### TASK-140 — iTunes P3 cleanup-merged apply path retired

- Worktree `.worktrees/server-140-retire-the-unsafe-cleanup-merged-go-handler-as-a`, branch `agent/server-140-retire-the-unsafe-cleanup-merged-go-handler-as-a`, sha `ebea20fcb`.
- `itl_cleanup.go` 2.0.0: any request without `dry_run=true` returns 410 with `applied:false` before any `.itl` path resolution; `SafeWriteITL` and the `itunesservice` import removed from the file; dry-run preview unchanged. New `itl_cleanup_test.go` (4 refusal subtests, pre-fix the non-empty case reached the writeback and returned 500; preview test). Gate exit 0 (server suite, `-race`, staticcheck only the pre-existing `server_helpers.go:62`). Rollback: removes reachability of a write path. `web/` has zero references to the endpoint.
- PR #3194 — HELD for owner. `TODO.md` line to check off on merge: "**iTunes 2-way-sync P3 (cleanup) — decision: MEASURE-AND-STOP, no removal machinery.**" (~L17244).

### TASK-308 — SV-03 SSE ACAO wildcard

- Worktree `.worktrees/server-handlers-308`, branch `agent/server-handlers-308-sse-handler-unconditionally-overrides-th`, sha `0a5f91905`.
- `events.go` 1.3.0: the `Access-Control-Allow-Origin: *` set in `HandleSSE` deleted; the allowlist from `corsMiddleware` stands. `events_test.go` 1.4.0: `TestHandleSSE_PreservesUpstreamCORSHeader` (pre-fix `got "*"`), and the existing basic-connection test that had asserted `*` corrected. Brief's second anchor range (`:437-449`) was past EOF (file is 334 lines); first anchor sufficed. Only one hardcoded site in `internal/realtime`. Gate exit 0, staticcheck 0.
- PR #3195 — HELD for owner. No `TODO.md` line for SV-03.

### TASK-340 — TODO L1461 writeback batcher Stop() join + single writer

- Worktree `.worktrees/itunes-340`, branch `agent/itunes-340-internal-itunes-service-writeback-batche`, sha `6df50fe40`.
- `writeback_batcher.go` 5.8.0: `sync.WaitGroup` over all three goroutines (Add under `b.mu` after the stopped check); `stopTimerLocked` pairs `timer.Stop()` with `Done` only when the cancel wins; `timerFlush` callback; `flush()` refuses after stop, `drainFlush()` used only by `Stop`; dedicated `flushMu` across parse→diff→`SafeWriteITL` (lock order `flushMu`→`b.mu`; `b.mu` not held across I/O); `resetTimer` no-ops after stop so `reEnqueue` cannot re-arm; `Stop` idempotent, closes `stopCh`, releases `b.mu` before `wg.Wait()`. 4 `-race` tests (pre-fix: goroutine still in `SafeWriteITL` after Stop; `maxActive=2`; `ParseITL called 5 times, want 1`). Whole package `-race -count=2` exit 0.
- Flagged boundaries: unbounded `wg.Wait()` (bounding it would drop the final drain; owner decision); tombstone goroutine joined but not gated on `stopCh` on purpose; `flushMu` is per batcher, other `.itl` writers outside the guarantee.
- PR #3196 — HELD for owner. `TODO.md` L1461 to check off on merge; sibling items (`scanner.go`, `extract_wav_clips.go`) stay open.

### TASK-305 — DB-02 migration bookkeeping atomic + replay guard

- Worktree `.worktrees/database-305`, branch `agent/database-305-migration-effect-migration-record-write`, sha `e63b1c63a`.
- New `migration_bookkeeping.go`: `commitMigrationBookkeeping` writes `migration_<n>` + `db_version` in one `pebble.Sync` batch via unexported `(*PebbleStore).setPreferencesAtomic` (optional interface + `AsPebbleStore`; no `Store`/mock change; compile-time pin that prod takes the batched branch; fallback order record→version is commented). `migrations.go` 1.44.0: `migrationAlreadyRecorded` checked first, a recorded migration advances the version without re-running `Up`. `TestMigrationUpFunctionsAreIdempotent` runs every registered `Up` twice (prospective: no registered `Up` rewrites rows today). Regression `TestMigrationReplayDoesNotRerunRecordedUp` pre-fix `expected 0, actual 1`. Gate exit 0 incl. `ExternalIDMap|Quarantine` tests that run the full registry; staticcheck 2 pre-existing in untouched files. Caveat: `nextID` counter commits outside the batch (can burn an id, harmless). No `TODO.md` line for DB-02.
- PR #3197 — HELD for owner.

### TASK-337 — TODO L1192 dry-run count for DELETE /operations/history

- Worktree `.worktrees/server-handlers-337`, branch `agent/server-handlers-337-add-a-dry-run-count-mode-to-delete-opera`, shas `499f91afb` + `4580f2488`.
- `handler.go` 1.13.0: `?dry_run=true|1` returns `would_delete`/`counts` and deletes nothing; real delete keeps its default and adds the same fields; count error aborts before delete. New `PebbleStore.CountOperationsByStatus` (`pebble_store_operations.go` 1.4.1) on `OperationPruner` + handler interface; `MockStore` + mockery mocks regenerated (`make mocks-check` green). 3 tests; pre-fix captured with a compile-clean probe (unexpected `DeleteOperationsByStatus` call). Gate exit 0 (handlers, full `internal/database` 549 s). v1 `operation:` keyspace is write-dead. `web/` wrapper `deleteOperationHistory` has zero callers. Delete-by-id NOT built; recommendation in the PR body (`DELETE /operations/history/:id` via `DeleteOperationWithLogs`).
- Overlap: `mock_store.go` + generated mocks also touched by #3185 (header/gofmt) → trivial rebase for whichever merges second.
- Worker added a store interface method despite the "STOP and report" instruction; accepted because the method is read-only, on the narrow `OperationPruner`, and the forbidden files were not touched.
- PR #3198 — HELD for owner. `TODO.md` L1192 (wraps to L1196): dry-run half done; owner to check off or split.

### TASK-348 — TODO L3344–L3348 mask remaining `GET /config` secrets

- Worktree `.worktrees/config-348`, branch `agent/config-348-mask-the-remaining-secrets-returned-by-g`, sha `7c3682865`.
- `update_service.go` 3.20.0: six more fields masked in `MaskSecrets`; masked value verified to reach the wire through `withEnvLocks`. Round-trip protection ADDED (the existing `secretFieldKeys` strip only reaches top-level keys): `snapshotRoundTripSecrets`/`restoreRoundTripSecrets` in the `Mutate` window; top-level scalars use `acceptSecretUpdate` (explicit `""` clears), nested download-client credentials use the `restoreMaskedCredentials` precedent (restore on mask and on empty). 7 tests in `mask_remaining_secrets_test.go`; pre-fix all six in cleartext and all six destroyed by an echoed mask on save; call-site mutation check done. `web/` has zero references to the six. Gate exit 0, staticcheck 0.
- Owner note: clearing `deluge.password` / `qbittorrent.password` / `sabnzbd.api_key` now needs a config-file edit rather than a blank field.
- PR #3199 — HELD for owner. On merge check off `TODO.md` L3344, L3345, L3346, L3347, L3348.


### TASK-080 — SEC-CODEQL-BACKLOG SSRF on the cover proxy (#645/#662)

- Worktree `.worktrees/metadata-080-assess-the-2-critical-go-request-forgery-ssrf-co`, branch `agent/metadata-080-assess-the-2-critical-go-request-forgery-ssrf-co`, sha `e0ee6a721` (owner later rebased the PR head to `09d17a803`; diff identical).
- The brief predicted #662 was "plausibly sufficient for a dismissal"; verification refuted it. `internal/covers/covers.go` used `http.DefaultClient` (no address check, redirects followed anywhere); `internal/metadata/cover.go` had a DNS-rebinding TOCTOU (checked the lookup, dialled the hostname), missed `0.0.0.0`/`::`, and checked the scheme on hop 1 only. New `internal/security/safehttp`: one guard for both — blocks loopback/link-local/private/CGNAT/multicast/unspecified/v4-mapped, dials the checked IP literal, re-checks every redirect hop, bounded timeout, fixed error strings. `covers.go` 1.2.1, `metadata/cover.go` 1.8.0 (−60 lines of hand-rolled checking). 13 tests; pre-fix `FetchAndCacheCover("http://127.0.0.1:…") succeeded and cached` and `followed a 302 to "http://127.0.0.1:…"`.
- Gate exit 0 (covers, metadata, safehttp; staticcheck 0; leak scan 0).
- CodeQL triage (subagent, 2026-09-10): the failing check is alerts 1871/1872, the same two findings re-fingerprinted at the moved lines. Both FALSE POSITIVE — the control is a dial-time IP guard outside the dataflow model, and this repo does not credit validators as barriers. No code change can clear them; per-alert dismissal justifications posted as a PR comment. Latent gap surfaced, not fixed: `FetchAndCacheCover` applies only scheme/host validation itself, the host allowlist lives in its one caller.
- PR #3201 — HELD for owner. **Owner action: dismiss 1871/1872 citing `internal/security/safehttp`.** `TODO.md` SEC-CODEQL-BACKLOG entry: partial; check off only once the alert disposition is decided.

### TASK-072 — TODO L10493 merge operator-confirmed duplicate authors

- Worktree `.worktrees/maintenance-072-…` (removed after merge), branch `agent/maintenance-072-new-maintenance-op-merge-an-operator-confirmed-l`, shas `b88c2ae59` + `aa31ba57c`; owner added `60327e022` "renew the scan stand-down lease between merges".
- New op `maintenance.author-duplicate-merge` (`author_duplicate_merge.go` 1.1.0, `plugin.go` 1.31.0): `dry_run` defaults true, `names` allowlist after `util.NormalizeAuthor`, canonical = highest live book count, reuses `mergeAuthorInto`; scan stand-down held and renewed; unfiltered `AuthorRefCounts` guard evaluated before the dry-run branch so preview and apply hold back identical rows; one `CreateOperationChange` per relinked book and per deleted author. 10 tests incl. `-race`. Real finding: `util.NormalizeAuthor` does NOT collapse interior whitespace (L3790 tracks it).
- Gaps recorded for the owner: undo is forensic, not one-click (`internal/undo` has no handler for `author_id` rows or `author_delete`); `merge.LockMergeRMW` not taken (no maintenance op takes it; owner call); stale comment `deps.go:483-485` reported.
- PR #3202 — **MERGED by owner 2026-09-10 20:59 UTC.** `TODO.md` L10493 to check off.

### TASK-160 — SEC-9 OpenAI key validation moved server-side (TODO L6891)

- Worktree `.worktrees/web-160-move-openai-api-key-validation-server-side-curre`, branch `agent/web-160-move-openai-api-key-validation-server-side-curre`, sha `0cdff2a02` (owner rebased to `855c4d3ba`); CodeQL fix `b4367398a` on top.
- New `POST /api/v1/setup/validate-openai-key` (`handlers/openai_validate.go`), registered beside `PUT /config` with `PermSettingsManage`; 2xx → valid, 401/403 only → invalid, 5xx/429/transport/10 s deadline → 502 (never flattened to "invalid key"); key never logged, persisted or echoed; outbound URL a `const`. `WelcomeWizard.tsx` 1.6.0 calls `api.validateOpenAIKey` (`api.ts` 2.85.0); `grep -c 'api\.openai\.com' WelcomeWizard.tsx` → 0. 6 Go tests + 3 vitest (pre-fix `expected "vi.fn()" to be called with … Number of calls: 0`).
- CodeQL triage: one NEW alert, `js/incomplete-url-substring-sanitization` on the test's `includes('api.openai.com')`. Fixed in-branch: the negative assertion now filters on the test key (strictly broader), adds `expect(fetchSpy).not.toHaveBeenCalled()`, and a vacuous `waitFor` around a negative expectation was corrected to wait for the positive call first. tsc 0, vitest 3/3. The Go analysis had not reported at triage time; both new Go files read by hand (constant outbound URL).
- PR #3203 — HELD for owner. `TODO.md` L6891 to check off on merge.

### TASK-361 — AUTHOR-MEMBERSHIP-UNGUARDED (TODO L5163), done by the coordinator

- Worktree `.worktrees/database-361` (removed after merge), branch `agent/database-361-author-membership-unguarded-confirmed-fi`, sha `031f0aa43` (merged as `f77e5f679`).
- `GetBooksByAuthorIDWithRoleCore` — the getter every author merge/delete/dedup path consults before `DeleteAuthor` — dispatched to memdb with no completeness check, 17 days after the series twin got its guard (#2839); TODO.md records the shape firing in prod 2026-08-24 05:00. `memdb_reads.go` 1.26.0: `GetBooksByAuthorIDAllVersions` requires `books` AND `book_authors` complete (a lost junction row is a co-author credit the legacy field cannot recover). `pebble_store.go` 1.147.0: wrapper logs and falls through to the authoritative Pebble scan on `ErrMemdbIncomplete`; the scan and `bookIDsInAuthorJunction` are fail-closed (undecodable row → error naming the key; `iter.Error()` checked). Guard on the wrapper, not the shared listing body. 7 tests in `author_membership_guard_test.go`, 6 failed pre-fix (the healthy-memdb control passed).
- Gate exit 0 (build/vet; `-run 'Author|Memdb|Membership|Series'` 25.2 s; `-race` 3.7 s; staticcheck 0).
- PR #3204 — **MERGED by owner 2026-09-10 21:02 UTC.** `TODO.md` L5163 to check off.

### TASK-363 — AUTHOR-FILE-SAFETY (TODO L5282)

- Worktree `.worktrees/database-363`, branch `agent/database-363-author-file-safety-purge-empty-authors-s`, shas `30e1cb5eb` + `616def981` → rebased onto `25a77e42d` as `1399b0119` + `fc4b164b2`.
- `purge-empty-authors`' "safety that matters" (`require_zero_files`) read `GetAllAuthorFileCounts`, a DISPLAY counter: primary-only, skips trashed, legacy `AuthorID` only, Pebble twin swallowed a `GetBookFilesForIDsCore` error into 0. New `AuthorFileRefStore` capability + `database.AuthorFileRefCounts` (`author_file_refs.go`), resolved via `AsCapability`; walks every `book_authors` row and every book (no primary/trashed filter), dedup per (book, author). `requireTablesComplete` over `book_authors`, `books`, `book_files` with deliberately NO Pebble fall-through (`GetBookFilesForIDsCore` delegates to memdb while warm, so a fall-through would undercount). `author_purge_empty.go` 1.2.0 re-pointed; `deps.go` 1.27.0 drops `GetAllAuthorFileCounts` from `opsAuthorStore`. 3 op-level tests failed pre-fix (`deleted author 2, which has 9 files the display counter cannot see`); DB-level probe recorded the three populations at 0 and was folded into preconditions. Worker's own caveat kept in the PR: the op `continue`s on `refCounts != 0` before the file gate, so the gate cannot fire in prod under that ordering; what the fix buys is a truthful `ZeroBooksWithFiles`, a short `book_files` table now refusing, and three swallowed errors made fatal.
- Gate exit 0 after rebase (build/vet, `PurgeEmptyAuthors`, `AuthorFileRef|Conformance`; worker ran `-race`; staticcheck 0 in touched files).
- PR #3205 — HELD for owner. `TODO.md` L5282 to check off on merge.

### TASK-359 — SERIES-MERGE-UNGUARDED-DENOMINATOR half (1), verified CLOSED at HEAD

- No code. Re-verification found every one of the seven repoint-then-delete sites the entry lists already reading `database.SeriesRefCounts` (counts trashed rows) before `DeleteSeries`, each pinned by a trashed-row test; the phase-1 guard the entry said was still open landed in `c39a43cbc` on 2026-08-30 and the entry's "Half (1) remains OPEN" sentence went stale then. Checked off with the evidence list in `1ca315d53` (on #3200).
- **MERGED 2026-09-10 21:05 UTC** as part of #3200.

### TASK-345 — SERIES-PHANTOM-REPAIR (TODO L2872), done by the coordinator

- Worktree `.worktrees/maintenance-345`, branch `agent/maintenance-345-series-phantom-repair`, sha `e88813f93` (based on `25a77e42d`).
- New op `maintenance.series-phantom-repair` (`series_phantom_repair.go` 1.0.0, `plugin.go` 1.31.0): mode `report` (default) lists every `books.series_id` with no series row — unfiltered ref count, live/trashed/unseen split, sample titles, holder-count histogram, top 200 in the result, full TSV via `report_path`; mode `null` clears the id on every holder; mode `recreate` creates a series from `names["<id>"]` (majority author) and repoints. Both repairs `dry_run` default true, scan stand-down held/renewed, `ApplyRespectingLocks` BEFORE the ledger row (a locked book leaves no row), `metadata_update`/`series_id` ledger row before each write, ledger failure leaves the book as it was, `RunItems` at `NumCPU` over disjoint books. Refuses to run without `SeriesRefCounts`; holders from `GetAllBooksCoreComplete` + `ListSoftDeletedBooks`, anything beyond is "unseen" and never repaired. Nothing deleted in any mode. `undo/engine.go` 1.5.0: `applyFieldRestore` learns `series_id` (pre-fix `book … lost its series_id`).
- 13 tests on a real PebbleStore. Gate exit 0 (build/vet; maintenance 24.7 s + undo; `-race`; staticcheck 0; leak scan 0). Two fixture lessons recorded in the test file: an interface-embedding wrapper hides capabilities (the op refuses before the ledger), and trash is the `MarkedForDeletion` flag, not the timestamp.
- PR #3206 — HELD for owner. `TODO.md` L2872 to check off on merge. Owner decides which repair to run; recommended `report` with `report_path` first.

### Owner merges, wave 2 (2026-09-10 20:39–21:05 UTC)

#3197, #3198, #3199, #3202, #3204, #3200 merged by the owner; main at `1ca315d53`. Worktrees removed: `database-361`, `todo-checkoff-2026-09-10`, `config-348`, `database-305`, `maintenance-072-…`, `server-handlers-337`. Open and held: #3201 (080), #3203 (160), #3205 (363), #3206 (345). In flight: TASK-301 (dedup, `.worktrees/dedup-301`), TASK-220 (maintenance, `.worktrees/maintenance-220`). TODO check-offs owed on merge: L10493 (072), L5163 (361).
