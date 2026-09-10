<!-- file: docs/agent-tasks/todo-completion-2026-09/state/EXECUTION-LOG.md -->
<!-- version: 1.3.0 -->
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
| 2 | TASK-360 orphan-file hard delete memdb guard | data-loss | S | go-specialist/opus | dispatched 14:46 |
| 2 | TASK-309 scanner AIPhaseSummary discarded | correctness critical | S | go-specialist/sonnet | dispatched 14:46 |
| 2 | TASK-310 ISBN sweep drops provider errors | correctness critical | S | go-specialist/sonnet | dispatched 14:49 |
| 2 | TASK-354 duplicate FilePath in one batch | data-loss | S | go-specialist/opus | dispatched 14:56 (L4241/4242 code; L4244 measure-only) |
| 3 | TASK-363 purge-empty-authors file-safety counter | data-loss | M | opus | queued (after 302 merges — same guard family) |
| 3 | TASK-344 MergeBooks audio-route guard | data-loss | M | | queued |
| 3 | TASK-346 / 347 / 358 / 359 series trashed-row guards | data-loss | M | | queued — check shared files before pairing |
| 4 | TASK-301 bulk journaling helper (reshaped) | data-loss | M | opus | queued — after 300 merges (dedup files) |
| 4 | TASK-361 author-book memdb guard | data-loss | L | opus | queued |
| 4 | TASK-338 retire fix-library-states | data-loss | S | | queued |
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
- PR #3183 — HELD for owner. Rollback note in the PR: one new metadata file per archive; owner to say if it wants the dry-run protocol. Deploy note: pre-existing backups have no sidecar, so their first verified restore returns 400 until re-created.

### TASK-302 — DB-01 purge-empty-authors guard

- Worker paused after 62 calls with the gate still running in the background; resumed 14:40 with a 20-call budget to finish the gate and report.
- Worktree `.worktrees/database-302`, branch `agent/database-302-purge-empty-authors-delete-guard-book-sc`, sha `1011cdb22`.
- Files: `internal/database/author_bookref.go` 1.5.0, `pebble_store.go` 1.146.0, tests in `author_bookref_test.go` + `author_getter_conformance_test.go`, `changelog.d/20260910_database_302.md`.
- Bounds `["book:0","book:;")` → `["book:","book;")` in pass 2 and in `GetBooksByAuthorIDWithRoleCore`; the latter's `:path:`-only filter upgraded to `strings.Count(key, ":") != 1`.
- Regression: two non-ULID-id tests failed pre-fix (`expected 1, actual 0`; missing `ZZBOUNDS…`), pass post-fix incl. `-race`. Gate exit 0 (`internal/database` 567s). Rollback: pure code change.
- PR #3182 — HELD for owner. No `TODO.md` line. Not run: the brief's live audit for non-digit `book:` keys on prod (worker ban) — owner's call before merge.
- Observed, unfiled: `getBooksByAuthorIDFull` has no `iter.Error()` check; `GetBooksByAuthorIDWithRoleCore` `continue`s on unmarshal error.
