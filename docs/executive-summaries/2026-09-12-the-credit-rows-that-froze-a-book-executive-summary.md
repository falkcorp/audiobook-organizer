<!-- file: docs/executive-summaries/2026-09-12-the-credit-rows-that-froze-a-book-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: a4549af8-0bea-4d8b-844f-c6ea8260655a -->
<!-- last-edited: 2026-09-12 -->

# The credit rows that froze a book

## Executive Summary

The app keeps its library in two places. The database on disk is the record
of truth. A fast in-memory copy answers most of what the web pages ask for,
such as lists, counts, and searches. Every change is written to disk first and
then copied into memory.

Each book has a list of who narrated it and who wrote it. Each entry in those
lists is supposed to say which book it belongs to. Two features could save
entries that left that out:

- the "optimize database" maintenance action, when it split a combined credit
  like "Alice & Bob" into two narrators, and
- the screen or API call that sets a book's narrators, if whatever sent the
  request left the book out.

The disk copy was fine, because the list is filed under the book anyway. The
in-memory copy refuses an entry that does not name its book. From then on,
**every later change to that book failed to reach the in-memory copy**: a new
title, new credits, new files. Pages kept showing the book as it was before
the bad entry was saved. Nothing reported an error, since the disk write had
succeeded. A restart did not fix it, because loading the memory copy at startup
refused the same entry.

The author lists had the same gap on disk. An earlier fix repaired only the
in-memory side, so the next update of such a book failed the same way.

### What changed

- The database now stamps the book onto every narrator and author entry when
  it saves them, whatever the caller sent. If a caller names a different book,
  the database uses the book the list is being saved to, and logs a warning.
- Reading the lists back, and loading the memory copy at startup, also fill
  in the book, so entries saved before this fix no longer cause trouble.
- A one-time startup step repairs entries already on disk. It is safe
  because the correct book is already known for every entry (it is the book
  the list is filed under), and only that one detail changes. It writes a
  count of what it repaired to the log, and running it again changes nothing.

New tests save entries with no book, then confirm the book's next update shows
up in the pages' data. They cover the maintenance action, the set-narrators
call, the startup repair (including a second run), and a restart. The same
tests fail on the code before this fix.

### What this does not do

Nothing was lost on disk, so there is nothing to recover. Books that were
frozen in memory come right on the first restart after this ships. The repair
step is what fixes the stored entries, and loading the memory copy now handles
any it misses.
