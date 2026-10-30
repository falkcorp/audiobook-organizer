<!-- file: docs/decisions/2026-10-30-journal-direct-writers.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2d7b6f41-8a93-4c5e-b017-e4f3a96d1c28 -->
<!-- last-edited: 2026-10-30 -->

# Journal direct writers and the classification check

Task: TODO-JOURNAL-DIRECT-WRITERS. `repairs.Writer` refused an unclassified
change type; other writers called `CreateOperationChange` directly and did not.

## Findings

The TODO named five writers. The real list was 20 non-test files and 48 call
sites outside `internal/database` and `internal/repairs` (the five, plus the
organizer, dedup series cores, narrator/author maintenance ops, credits backfill,
reconcile, merge journal pointers, entity reclassify and two server helpers).
Several wrote types nobody had classified, `book_file_repoint` among them
(`database.ChangeTypeBookFileRepoint` is the different string
`book_file_repoint_location`).

## Decisions

1. Do not route the direct writers through `repairs.Writer`. Rejected: Writer
   assigns a fresh ULID, stamps `Source`, beats the lease, and dedupes identical
   rows within an op. Several writers pre-assign an id and rewrite the same row
   to void it (repoint, authormerge, credits backfill, merge pointers, reclassify,
   dedupe crossfolder), and the organizer and dedup cores have no Writer. Routing
   them would change ids, `Source` and dedupe behaviour. The recommended
   fallback applies to all of them: one shared check-then-create function.
2. The check lives in `internal/database` (`change_type_class.go`):
   `CheckChangeTypeClassified`, `RecordOperationChange`, `RecordOperationChanges`.
   `internal/undo` imports `internal/merge`, and merge writes journal rows, so
   the table could not stay in undo. No new leaf package: database is already
   imported by every writer. `undo.ClassifyChangeType` was removed (no production caller left) and
   `undo.IsUserDataChangeType` delegates; `repairs.Writer` calls the same
   `CheckChangeTypeClassified` early (before its lease beat) and
   `RecordOperationChange(s)` for the write, and
   `repairs.ErrUnclassifiedChangeType` is now `database.ErrUnclassifiedChangeType`.
3. The table keys became string values (with the undo constant named in a
   comment) because database cannot name undo's constants. the drift test moved to `internal/database` (an external test package, which may import undo) and now
   compares constant values with the table in both directions.
4. New literals classified as library data (false): `book_file_repoint`,
   `book_file_delete`, `narrator_split`, `narrator_orphan_unlink`,
   `narrator_delete`, `author_delete`, `author_link`, `file_path_update`,
   `book_create`, `organize`, `organize_summary`, `organize_skipped`,
   `organize_failed`, `series_prune_summary`. All describe paths, names and ids
   of library records, no user's listening state.
5. Guard allowlist (`internal/arch/journal_direct_writers_test.go`): the whole
   `internal/database` package, plus `internal/operations/opswriter/writer.go`
   and `pkg/ops/opstest/world.go`. Rejected: routing opswriter through the check.
   It records history after the write committed, so refusing would drop history
   rather than prevent a write, and its `effect:<name>` types are dynamic; it
   needs its own decision (prefix rule) and is left as a follow-up.
   opstest only forwards the store method it implements.

## Behaviour change to be aware of

A direct writer given a type outside the table now gets
`ErrUnclassifiedChangeType` from `RecordOperationChange`. Sites that used to
discard the journal error (`_ =`: nine in the organizer, four in the series
merge) now go through `Service.recordChange` / `recordSeriesChange`, which log a
Warn naming the operation id and change type (sanitized) and still do not stop
the run, because the write the row describes has already happened. Sites that
already returned or counted the error are unchanged. Production types were
enumerated by hand from every call site; tests in each touched package pass.
