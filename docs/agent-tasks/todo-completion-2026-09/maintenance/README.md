<!-- file: docs/agent-tasks/todo-completion-2026-09/maintenance/README.md -->
<!-- version: 1.7.0 -->
<!-- guid: 660e23b9-9815-471e-94a9-5eb8f9306231 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — maintenance (todo-completion-2026-09)

21 tasks: 13 carried forward from the 2026-08-21 package (ids kept), 8 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-066](TASK-066-wire-a-durable-freshness-stamp-for-maintenance-c.md) | carried | correctness | P2 | M | Wire a durable freshness stamp for maintenance.chapters-backfill before it is ev | grep -rln "freshness.Stamp/freshness.ClearStamps" --include='*.go' . / grep -v _test -> 0  |
| [TASK-068](TASK-068-build-a-report-only-counter-for-book-filepath-co.md) | carried | hygiene | P2 | S | Build a REPORT-ONLY counter for Book.FilePath collisions | grep -rn 'FilePathCollision/CollisionCount/filepath_collision' --include='*.go' . -> 0 hit |
| [TASK-070](TASK-070-add-a-user-configurable-activity-log-retention-w.md) | carried | correctness | P2 | M | Add a user-configurable activity-log retention window (default 7 days, 0=never) | grep -n activity_log_retention_days internal/config/config.go -> 0 hits. server_maintenanc |
| [TASK-071](TASK-071-build-a-detection-only-report-of-other-title-fra.md) | carried | hygiene | P2 | M | Build a detection-only report of other title-fragment author rows | grep -rn 'TitleFragmentAuthor/title-fragment-author/author.title.fragment.report' internal |
| [TASK-072](TASK-072-new-maintenance-op-merge-an-operator-confirmed-l.md) | carried | data-loss | P1 | M | New maintenance op: merge an operator-confirmed list of duplicate real-author ro | grep -rn 'author-duplicate-merge/author-merge/merge-author/MergeAuthors\b' internal/plugin |
| [TASK-073](TASK-073-read-through-audit-of-the-8-ctxopid-consumer-cal.md) | carried | correctness | P2 | M | Read-through audit of the 8 ctxOpID consumer call sites now that op IDs actually | ctxOpID call sites re-counted at HEAD: series.go:82, cleanup.go:50+122, write_back.go:59,  |
| [TASK-074](TASK-074-build-a-report-only-census-of-books-with-a-place.md) | carried | hygiene | P2 | M | Build a report-only census of books with a placeholder author already baked into | grep -rn 'unknown-author-audit/UnknownAuthorAudit' internal/ -> 0 hits; no unknown_author_ |
| [TASK-075](TASK-075-extend-purge-empty-authors-report-to-categorize-.md) | carried | hygiene | P2 | S | Extend purge-empty-authors' report to categorize the 822 zero-book-but-has-files | grep -n HeldBackSample internal/plugins/maintenance/author_purge_empty.go -> 0 hits. ZeroB |
| [TASK-076](TASK-076-author-narrator-swap-repair-routed-through-the-r.md) | carried | correctness | P2 | L | Author-narrator swap repair, routed through the review queue | grep -rn 'swap-shaped/AuthorNarratorSwapCandidate' internal/ -> 0 hits; no author_narrator |
| [TASK-077](TASK-077-narrow-the-3-remaining-maintenance-jobs-callees-.md) | carried | hygiene | P2 | M | Narrow the 3 remaining maintenance-jobs callees off maintenance.JobStore | All 3 functions still take the wide `store maintenance.JobStore` parameter at HEAD: vgFixA |
| [TASK-195](TASK-195-add-a-zero-size-bucket-to-maintenance-missing-fi.md) | carried | hygiene | P2 | S | Add a zero-size bucket to maintenance.missing-file-audit | grep -n '.Size()/fileZeroSize/ZeroSize' internal/plugins/maintenance/missing_file_audit.go |
| [TASK-219](TASK-219-add-a-per-book-tsv-report-artifact-to-the-existi.md) | carried | hygiene | P2 | M | Add a per-book TSV report artifact to the EXISTING dedupe-book-file-rows dry run | grep -n 'ReportPath/writeDupeRow/.tsv' internal/plugins/maintenance/dedupe_book_file_rows. |
| [TASK-220](TASK-220-journal-every-duplicate-row-deletion-to-the-undo.md) | carried | data-loss | P1 | M | Journal every duplicate-row deletion to the undo ledger and refuse to apply whil | grep -n 'CreateOperationChange/OperationQueueStore/ListActiveOperationsV2' internal/plugin |
| [TASK-338](TASK-338-fix-or-unregister-fix-library-states.md) | new-todo | data-loss | P1 | S | Fix or unregister `fix-library-states` | TODO.md lines 1294 |
| [TASK-342](TASK-342-duplicate-placeholder-book-records-priority-3-of.md) | new-todo | data-loss | P1 | L | Duplicate & placeholder book records — PRIORITY 3 of the 2026-09-05 audit cleanu | TODO.md lines 2088 |
| [TASK-343](TASK-343-author-numbering-cleanup-follow-ups-from-the-202.md) | new-todo | correctness | P1 | M | Author-numbering cleanup follow-ups (from the 2026-09-05 production runs) | TODO.md lines 2218, 2221 |
| [TASK-347](TASK-347-series-denumber-trashed-gap-internal-plugins-mai.md) | new-todo | data-loss | P1 | M | SERIES-DENUMBER-TRASHED-GAP — `internal/plugins/maintenance/series_denumber_op.g | TODO.md lines 2901 |
| [TASK-353](TASK-353-decide-how-to-repair-the-duplicate-author-rows-t.md) | new-todo | data-loss | P1 | M | Decide how to repair the duplicate author rows that already exist | TODO.md lines 4019 |
| [TASK-356](TASK-356-decide-how-to-merge-the-duplicate-author-rows-al.md) | new-todo | data-loss | P1 | M | Decide how to merge the duplicate author rows already present, and whether book  | TODO.md lines 4630 |
| [TASK-360](TASK-360-orphan-files-hard-delete-fail-open-internal-plug.md) | new-todo | data-loss | P1 | S | ORPHAN-FILES-HARD-DELETE-FAIL-OPEN — `internal/plugins/maintenance/orphan_book_f | TODO.md lines 5139 |
| [TASK-362](TASK-362-memdb-lossy-readers-headline-is-stale-correct-it.md) | new-todo | data-loss | P1 | S | MEMDB-LOSSY-READERS headline is STALE — correct it before acting on it | TODO.md lines 5246 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
