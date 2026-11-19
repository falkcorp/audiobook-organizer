<!-- file: docs/executive-summaries/2026-11-19-gated-writer-stamping-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e4c9a21-5b38-4d06-8f17-c2a9d0e63b54 -->
<!-- last-edited: 2026-11-19 -->

# A background job that touches data it never declared is now stopped at the moment it tries

**Date:** 19 November 2026. A safety check in the new background-job engine,
built and tested but not switched on yet.

## Executive Summary

- Every background job has to say up front which kinds of data it is allowed to
  change. The new job engine enforces that when the job actually writes. Until
  now each write carried a single label, so a write that also changed a second
  kind of data (saving a book changes the book, its authors, its search entries
  and its cached lookup results) was checked against the first label only. A job
  that never declared the other kinds slipped through.
- A write now carries every kind of data it touches, and the check refuses it
  before anything is recorded or changed if any one of them was not declared.
  The refusal happens in a trial run as well as a real one.
- The earlier safety net compared the stored data before and after a job and
  could not see three kinds of write: one that rewrites a value with the same
  value, one that deletes something already gone, and one that creates and
  deletes an entry inside the same step. Each is now refused when attempted, and
  each has a test that first shows the old comparison saw nothing.
- The two jobs that fetch book details from outside sources (resolving
  production-company authors and matching transcribed books) were run for real
  against a test library through a writer that enforces what they declare. Both
  ran clean, and a test removes each kind of data they write from their
  declaration in turn to show the check notices.
- Nothing is switched on for users. The engine does not run these jobs yet, so
  library behaviour is unchanged.

## What you need to do

Nothing.

## What to keep in mind

- The wrapper around the metadata fetch covers writes the fetch makes to the
  library store. File work after an apply (cover download, rename, tag writing),
  the activity log and the duplicate check that follows an apply are not behind
  it yet; the seam notes list them for the step that switches the engine on.
- Fifteen of the store's write operations had no entry in the shared table that
  says what each one can change. Their entries are kept beside the new check for
  now, with a test that compares each against what the real store does, and
  belong in the shared table.
