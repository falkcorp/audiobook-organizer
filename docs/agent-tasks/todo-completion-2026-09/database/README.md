<!-- file: docs/agent-tasks/todo-completion-2026-09/database/README.md -->
<!-- version: 1.7.0 -->
<!-- guid: eb72dde0-73f3-422b-9dfc-562dd6405d0e -->
<!-- last-edited: 2026-09-10 -->

# Workstream — database (todo-completion-2026-09)

18 tasks: 7 carried forward from the 2026-08-21 package (ids kept), 11 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-023](TASK-023-investigate-then-evict-dirty-flag-merged-away-bo.md) | carried | correctness | P2 | L | Investigate then evict/dirty-flag merged-away book/file IDs from every read cach | grep -rn 'IndexBook/bleve/Invalidate/listCache' internal/merge internal/dedup --include='* |
| [TASK-035](TASK-035-add-deletenarrator-to-the-store-crud-building-bl.md) | carried | hygiene | P2 | S | Add DeleteNarrator to the store (CRUD building block only) | grep -rn DeleteNarrator internal/database/ = 0 hits at HEAD. |
| [TASK-037](TASK-037-omnibus-anthology-book-type-field-part-1-of-the-.md) | carried | correctness | P2 | L | Omnibus/anthology book_type field -- Part 1 of the omnibus-detection-and-dedup s | grep -rn 'BookType/book_type' internal/database/ --include='*.go' (excluding tests) = 0 hi |
| [TASK-038](TASK-038-filter-system-sourced-tags-out-of-the-browse-by-.md) | carried | hygiene | P2 | S | Filter system-sourced tags out of the Browse-by-Tag cloud | internal/audiobooks/service_tags.go:16-19 ListAllUserTags is still `return svc.store.ListA |
| [TASK-039](TASK-039-add-transcribe-status-to-the-book-summary-list-p.md) | carried | hygiene | P2 | M | Add transcribe_status to the book-summary list projection and a frontend quality | internal/database/store.go: BookSummary struct (starts L394) carries TranscribedTitle (L42 |
| [TASK-177](TASK-177-add-a-per-test-deadline-context-withtimeout-to-i.md) | carried | hygiene | P2 | S | Add a per-test deadline (context.WithTimeout) to internal/database's riskiest un | grep 't.Context()' internal/database/*.go = 0 hits; grep 'context.WithTimeout' internal/da |
| [TASK-179](TASK-179-database-store-40-build-the-ast-go-types-ci-gate.md) | carried | hygiene | P2 | M | database.Store (40) -- build the AST/go-types CI gate that makes it unreachable  | tools/cmd/ contains dedup-dataset-audit, itunes-group-preview, merge-split-books, oplint,  |
| [TASK-302](TASK-302-purge-empty-authors-delete-guard-book-scan-uses.md) | new-finding | data-loss | P1 | S | purge-empty-authors delete-guard book scan uses a narrower byte-range bound than | internal/database/author_bookref.go:325 |
| [TASK-305](TASK-305-migration-effect-migration-record-write-and-sche.md) | new-finding | data-loss | P2 | M | Migration effect, migration-record write, and schema-version write are three sep | internal/database/migrations.go:451 |
| [TASK-315](TASK-315-the-real-pebbledb-corrupted-organize-path-repair.md) | new-finding | correctness | P2 | S | The real PebbleDB corrupted-organize-path repair (migration014UpPebble) is writt | internal/database/migrations.go:632 |
| [TASK-322](TASK-322-no-persisted-author-books-secondary-index-pre-me.md) | new-finding | perf | P1 | M | No persisted author->books secondary index; pre-memdb-warmup fallback does two f | internal/database/pebble_store.go:2254 |
| [TASK-326](TASK-326-dual-write-activity-migration-the-secondary-sqli.md) | new-finding | perf | P2 | S | Dual-write activity migration: the secondary (SQLite) backend receives every wri | internal/database/sql_activity_migrating_store.go:161 |
| [TASK-331](TASK-331-deletebook-never-deletes-the-book-authors-book-n.md) | new-finding | hygiene | P2 | S | DeleteBook never deletes the book_authors:/book_narrators: sidecar rows it creat | internal/database/pebble_store.go:3112 |
| [TASK-334](TASK-334-digest-compaction-swallows-the-delete-error-for.md) | new-finding | hygiene | P3 | S | Digest compaction swallows the delete error for the pre-existing digest row, ris | internal/database/nuts_activity_store.go:614 |
| [TASK-354](TASK-354-two-rows-with-the-same-filepath-in-one-batch-now.md) | new-todo | data-loss | P1 | S | 🟠 Two rows with the same FilePath in one batch now corrupt Book.Duration › Fix | TODO.md lines 4241, 4242, 4244 |
| [TASK-359](TASK-359-series-merge-unguarded-denominator-was-trashed-r.md) | new-todo | data-loss | P1 | M | SERIES-MERGE-UNGUARDED-DENOMINATOR — (was `…-TRASHED-ROWS-RESIDUAL` | TODO.md lines 5018 |
| [TASK-361](TASK-361-author-membership-unguarded-confirmed-fired-in-p.md) | new-todo | data-loss | P1 | L | AUTHOR-MEMBERSHIP-UNGUARDED — CONFIRMED FIRED IN PROD 2026-08-24 05:00 UTC, not  | TODO.md lines 5163 |
| [TASK-363](TASK-363-author-file-safety-purge-empty-authors-safety-th.md) | new-todo | data-loss | P1 | M | AUTHOR-FILE-SAFETY: `purge-empty-authors`' "safety that matters" is itself a fil | TODO.md lines 5282 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
