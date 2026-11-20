<!-- file: docs/executive-summaries/2026-11-20-author-index-legacy-rows-executive-summary.md -->
<!-- version: 0.1.0 -->
<!-- guid: 5f9a3c27-8e14-4b6d-a2c0-71d4e9b6f308 -->
<!-- last-edited: 2026-11-20 -->

# Saving a book no longer counts as changing its authors

**Date:** 20 November 2026. Changes how books are saved, plus a cleanup job
that is built and tested but has not been run on your library.

## Executive Summary

- Each book has a list of credited authors, in order. That list is where the
  author comes from. Older parts of the system also keep a single "main author"
  field on the book, and a lookup table copied that field every time a book was
  saved. So every save, even a title fix, also counted as a change to author
  data.
- That mattered for scheduling. Background jobs declare what kinds of data they
  change, and two jobs that both change author data cannot run at the same
  time. Because every book save counted, almost every job that touches books
  looked like an author job. The nightly cleanup of deleted authors would have
  had to wait behind 20 other jobs. It now waits behind 2, the two that really
  change authors.
- Saving a book no longer writes that copy. The author lookup now follows the
  credit lists only.
- The quick author lookup used while the server starts up is now trusted only
  after the server has checked, on that start, that every listed book's main
  author is also in its credit list. If any book fails the check, the lookup
  falls back to the slower full scan, as it worked before the lookup existed.
  The results are the same either way; only the speed differs.
- A new cleanup job deletes the old copied rows. It only reports by default. It
  refuses to delete anything while any book, including trashed ones, has a main
  author missing from its credit list, and it lists those books instead. Those
  rows are the last record of that author for those books.

## What you need to do

Give the go before the cleanup job runs for real on your library. The steps,
the counts to check first and how to undo it are in
`docs/agent-tasks/platform-core/seams.d/F5-18j.md`. Expect the first trial run
to list books that need their credits filled in first.

## What to keep in mind

- Until those books have their credits filled in, author pages open a little
  slower in the first couple of minutes after a restart. Nothing is shown
  wrong.
- Going back to the previous version needs no manual step. It rebuilds its own
  copy of the lookup before trusting it.
