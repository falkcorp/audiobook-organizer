<!-- file: docs/executive-summaries/2026-11-16-series-denumber-safe-apply-executive-summary.md -->
<!-- version: 3.5.0 -->
<!-- guid: 977a5f90-535a-436e-afa1-f26225c95156 -->
<!-- last-edited: 2026-11-18 -->

# The series clean-up can now be undone, and applies only what the library itself backs up

**Date:** 16-17 November 2026. The clean-up (the operation that folds numbered
series such as "Name 05" into one "Name" series) had never been applied to the
real library. A dry run against the real library showed its most confident group
was not safe, so this change makes it safe before anyone runs it for real. Two
reviews of the first versions found the evidence it trusted was still too easy to
fool, so the rule below is the third and strictest one.

## Executive Summary

- The dry run listed about 3,500 plans in its highest-confidence group. About
  nine in ten covered series that show no books at all; the other 300 or so
  carried about 350 books between them. Several of those were wrong: parts of
  one audio file read as volumes of a series, three-digit chapter numbers read as
  volume numbers, and series with no author that would all have merged into one.
- A plan now applies only when the series name has one plain shape (a name, a
  separator, an optional "Book" or "Vol" marker and a number from 1 to 99) and the
  library backs the number up in one way only: **eligibility now requires a run of
  single-book series with distinct titles.** That means at least three series of
  the same author and base name, numbered one after another, each holding exactly
  one book, each book with a title of its own, and no two sharing an identifier.
  A book's own stored position no longer counts, because the importer copies it
  from the name. One failing member holds the whole run, because a run with one
  torn-apart book in it is evidence about the run, not about positions. A
  numbered series that holds several different books is held whole, never split
  by position.
- A rough re-count of the dry run against these rules leaves about 110 plans,
  carrying about 110 books, that could apply, down from about 170 plans and 160
  books under the previous rule. The count is an upper bound: the dry run records
  no book titles, identifiers or author names, so those three checks could not be
  re-run offline and can only remove more.
- The operation refuses a name that is a part of the series' own author's name
  (a surname alone, a given name alone), treats a lone numbered name as held and
  counted rather than dropped, and its summary counts every reason it held a book.
- The operation respects the owner's locks on a book's series or position, and
  stays out of the way of a running library scan; with no scan control it refuses
  to apply. Right before it deletes an emptied numbered series it reads the
  series again and checks that its name, author and holds are what the plan saw.
  The delete itself is one step in the store that checks the series still has
  that name and author, checks nothing points at it, and deletes it while no book
  can be saved: a book added to the series at the last moment keeps it, and so
  does a rename, a new author or a hold placed at the last moment, and the report
  says which.
- Before the first write into a base series it reads that series' books again,
  including trashed ones, so a book that took the position since planning stops
  the move. A book's versions are never split across two series.
- Nothing the operation does is permanent. Each change is recorded before it is
  made, in this order: the new base series is recorded after it is created and
  before any book moves into it; each book's series link and position are recorded
  before the book is written; an emptied numbered series is recorded before it is
  deleted. A record whose write then failed is marked so an undo never acts on it,
  and a run with failed writes ends as a failure, not a success. Undoing the
  operation puts the numbered series back under its own identity with its books
  and their positions as they were. The undo is as careful as the operation: when
  it removes a base series the operation created, it checks that nothing points at
  it and deletes it in one step, so a book added at the last moment keeps the
  series, and a series that was renamed, held or is already gone is left alone and
  listed in the undo's report instead of passing without a word. If someone else
  had already deleted an emptied numbered series before the operation reached it,
  the undo record for it is kept, so the undo can bring that series back and put
  the books in it.
- The operation now registers the books and series it writes with the scheduler,
  so the other series clean-ups (empty-series removal, merge, rename, normalize,
  the repairs apply) do not run at the same time. Its dry run says how many emptied
  series it would delete, and the apply's summary accounts for each one: deleted,
  kept at the last moment, already deleted by someone else, or not emptied.
- A first run can be limited to one series, or to the series with the most
  books, so the result can be looked at before the rest follow. A run that
  changes anything needs a report file, so there is always a record.
- Not done: showing the held plans in the Repairs tab (filed as a task); removing
  the empty numbered series (the existing empty-series clean-up does that); and
  two narrow windows around the delete (filed as a task): a book saved by a writer
  that looked at the series before the delete and saves after it, and another
  program using the same database at the same time, or a database that cannot do
  the one-step delete (the operation refuses to start on one). The undo itself is
  started from the web page and is not yet held back by the scheduler's
  one-at-a-time rule (filed as a task); its delete is safe on its own. The
  library's maintenance jobs, the empty-series clean-up among them, are also not
  held back by that rule (filed as a task); the clean-up was made to skip a book
  another process has just moved, instead of undoing the move, and to keep a
  series a book was moved into after the clean-up looked, instead of deleting it
  (it now deletes a series only through the same one-step delete). The two should
  still not be run at the same time: the scheduler cannot hold one back for the
  other, and a writer that looked at a series before the clean-up's delete and
  saves after it can still leave its book on a deleted series (the same narrow
  window as above). An undo that could
  not put one of its own books back now stays unfinished, so a retry removes the
  series it had created instead of leaving it behind.
