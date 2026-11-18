<!-- file: docs/executive-summaries/2026-11-18-series-writers-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: f3b8d246-5e19-4a70-8c2d-b41e7a96d058 -->
<!-- last-edited: 2026-11-18 -->

# Three ways a book could lose its series by accident are closed

**Date:** 18 November 2026. This follows a bulk cleanup that removed a very large
number of empty series from the live library, and the duplicate-series groups
now waiting in Repairs for review. Both run
the same two steps for each series: check that nothing uses it, then delete it.
The three fixes below are about what happens between those two steps and around
the undo.

## Executive Summary

- **A book can no longer be saved pointing at a series that was just deleted.**
  The delete is careful: it looks at every book and removes the series only if
  none uses it. But a book edit that had already decided "this book goes in
  series 12" could land a moment after series 12 was deleted, and the book
  would then name a series that no longer exists. Every list then showed that
  book with no series, and nothing said why. Now every save that links a book
  to a series checks the series is still there, at the moment it saves, and
  refuses if not. Books already in that state from older builds stay editable,
  and removing the link is always allowed. Copying a book (splitting a version,
  splitting a file, the audio conversion record) quietly drops a series that was
  deleted instead of failing the whole copy. So do the other places that copy one
  book's details onto another: refreshing a library copy from its original,
  merging a duplicate into a book, filling in a split book, and restoring an old
  version of a book. Each now leaves out the deleted series and carries over
  everything else. Refreshing a library copy used to fail quietly and fail again
  on every metadata update; it now reports that the copy is out of date. A
  metadata import or an edit that names a deleted series is refused for that book
  alone, with a message that names the series, and the rest still go through.
- **The Repairs "prune series" fixer now uses that careful delete too.** It used
  to count the books in a series and then delete it as two separate steps, so a
  book linked in between was stranded. It now deletes only when nothing uses the
  series at that moment, and counts a series that changed since the plan as
  "changed since plan" instead of deleting it. If the library cannot say whether
  a series is in use, the fixer stops before it moves a single book, so a row is
  never left half done.
- **Undoing an operation no longer runs beside other series changes.** The undo
  button is a web request, not a queued job, so the rule that keeps two jobs from
  rewriting the same series at the same time did not apply to it. Undoing a
  series clean-up could therefore run while a series merge or prune was running.
  The undo now takes the same reservation the jobs take: it is refused, with a
  message naming what is running, while an overlapping job runs, and while it
  runs the overlapping jobs wait their turn.

## What this does not cover

- Five places in the code still delete a series with the older, unchecked delete
  (two in the duplicate-series tools, one in the merge helpers, two behind the
  Series page's delete buttons). They are filed to be moved over one by one.
- Undoing does not pause the library scan. The scan and web requests coordinate
  book by book by design, and whether an undo of a whole clean-up should also
  pause the scan is filed as a question.

## Why it mattered

The library holds a very large number of series, and the owner is working
through them in bulk. A book that quietly loses its series is easy to miss and
hard to trace, and the bulk tools are the code most likely to hit the narrow
window. These changes make the check happen at the moment of the write, which is
the only place it cannot be raced.
