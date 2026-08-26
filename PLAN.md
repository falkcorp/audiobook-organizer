<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d177ce3-b54d-4b88-8b1f-37c31cf1cb96 -->
<!-- last-edited: 2026-08-25 -->

# Reviewable BookFile Backfill and Import Canary

## Goal

Make `maintenance.backfill-book-files` return an auditable, structured summary
for both dry-run and apply modes, then deploy it.  Use the summary to decide
whether to apply the BookFile-row repair, and run one narrowly scoped production
import canary with auto-organize enabled.

## Affected files

- `internal/maintenance/jobs/backfill_book_files.go` — count books examined,
  eligible books, candidate rows, rows created, skipped directory/path cases,
  and row-creation errors; emit the summary through the operation reporter.
- `internal/maintenance/jobs/backfill_book_files_test.go` — add outcome tests
  proving dry-run reports candidates without writes and apply reports successful
  writes plus creation errors.
- `PLAN.md` — retain the approved execution, verification, and rollback plan.

## Steps

1. Establish a focused baseline for the maintenance-job tests and inspect the
   reporter contract used by v2 operation summaries.
2. Add failing tests for dry-run and apply summaries.  Tests must assert the
   persisted-row effect separately from the returned counts.
3. Add the minimal typed summary/reporting implementation.  Preserve existing
   scan semantics: create only missing `book_files`; never delete or alter audio
   files; fail the operation if a row creation error occurs rather than silently
   hiding it.
4. Run focused and package test suites, commit the fix, and deploy through the
   existing production process.
5. Queue a dry-run in production and inspect its structured operation result.
   Apply only if the result has zero errors and the candidate count is credible.
6. Attempt one identified import canary with organize enabled; verify exactly
   one intended Book, the expected BookFile rows, resolvable audio paths, and
   the organize operation outcome.  Do not start a full scan from this plan.
7. Record each production outcome in `docs/CURRENT-STATUS.md` on its audit
   branch and commit it.

## Test strategy

- `go test ./internal/maintenance/jobs -run 'TestBackfillBookFiles'`
- `go test ./internal/maintenance/jobs`
- `go test ./internal/server -run 'TestMaintenanceJob'`
- Production: inspect the completed v2 operation result and logs for counts,
  then query the canary Book and BookFile records by their returned IDs.

## Rollback

- Code: revert the feature commit and redeploy the previous image if the job
  fails validation.
- Backfill: dry-run is write-free.  Do not apply on any nonzero error.  If an
  apply writes an incorrect row, remove only the explicitly identified rows via
  the maintenance/repair path after taking a fresh backup; never delete audio.
- Canary: auto-organize can be set back to false through the merge-style config
  endpoint.  An out-of-root organized copy can be reviewed/reverted via its
  recorded organize operation; an approved in-root rename uses the operation's
  recorded change set.
