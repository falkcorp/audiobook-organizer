<!-- file: docs/agent-tasks/todo-completion-2026-09/state/EXECUTION-LOG.md -->
<!-- version: 1.14.0 -->
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
| 1 | TASK-300 MergeSplitBookCluster RMW lock | data-loss critical | S | go-specialist/sonnet | PR #3181 HELD (14:42) |
| 1 | TASK-302 purge-empty-authors guard byte range | data-loss high | S | go-specialist/sonnet | PR #3182 HELD (14:49) |
| 1 | TASK-303 organize no-op stat (`:141-142` only) | data-loss high | S | go-specialist/sonnet | PR #3180 HELD (14:41) |
| 1 | TASK-306 backup restore verify | data-loss medium | S | go-specialist/sonnet | first cut REJECTED 14:44 (fail-closed broke default UI restore); reworked; PR #3183 HELD (14:55) |
| 2 | TASK-360 orphan-file hard delete memdb guard | data-loss | S | go-specialist/opus | PR #3185 HELD (15:24) |
| 2 | TASK-309 scanner AIPhaseSummary discarded | correctness critical | S | go-specialist/sonnet | PR #3186 MERGED 15:41 (rebase, 26/26 green) |
| 2 | TASK-310 ISBN sweep drops provider errors | correctness critical | S | go-specialist/sonnet | PR #3184 MERGED 15:36 (rebase, 26/26 green) |
| 2 | TASK-354 duplicate FilePath in one batch | data-loss | S | go-specialist/opus | PR #3188 HELD (15:40); L4244 decision surfaced to owner |

**Cap note 15:08:** resuming TASK-309 (finish gate) and TASK-306 (CodeQL rework) while 360/310/354 run made 5 live workers, over the 4 limit. No new dispatch until ≤4.
| 3 | TASK-363 purge-empty-authors file-safety counter | data-loss | M | opus | queued (after 302 merges — same guard family) |
| 3 | TASK-344 MergeBooks audio-route guard | data-loss | M | go-specialist/opus | PR #3187 HELD (15:35) |
| 3 | TASK-346 series-normalize trashed-row guard | data-loss | M | go-specialist/sonnet | PR #3189 HELD (15:44) |
| 3 | TASK-347 series-denumber trashed-row guard | data-loss | M | go-specialist/sonnet | dispatched 15:27 (series_denumber_op.go) |
| 3 | TASK-358 series-dedup journaling + scan check | data-loss | M | go-specialist/opus | dispatched 15:36 (series_dedup.go) |
| 3 | TASK-359 series-merge unguarded denominator | data-loss | M | | queued — touches pebble_store.go → wait for #3182/#3185 to merge |
| 4 | TASK-301 bulk journaling helper (reshaped) | data-loss | M | opus | queued — after 300 merges (dedup files) |
| 4 | TASK-361 author-book memdb guard | data-loss | L | opus | queued |
| 4 | TASK-338 retire fix-library-states | data-loss | S | go-specialist/opus | dispatched 15:43 (delete the job + absence test; never run it) |
| 4 | TASK-362 memdb-lossy-readers headline + 2 defects | data-loss | S | | queued |
| 4 | TASK-304 web author-merge popover | data-loss | S | typescript-specialist | queued |
| later | TASK-140, 072, 220, 337, 340, 352, 305, 373, 342, 345, 114, 096; security 308, 080, 083, 160, 335(reshaped), 365, 366, 368, 348 | | | | queued in matrix order |

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

