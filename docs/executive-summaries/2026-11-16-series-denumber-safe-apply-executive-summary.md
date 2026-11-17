<!-- file: docs/executive-summaries/2026-11-16-series-denumber-safe-apply-executive-summary.md -->
<!-- version: 2.0.0 -->
<!-- guid: 977a5f90-535a-436e-afa1-f26225c95156 -->
<!-- last-edited: 2026-11-17 -->

# The series clean-up can now be undone, and applies only what the library itself backs up

**Date:** 16-17 November 2026. The clean-up (the operation that folds numbered
series such as "Name 05" into one "Name" series) had never been applied to the
real library. A dry run against the real library showed its most confident group
was not safe, so this change makes it safe before anyone runs it for real. A
review of the first version found it still trusted a list of known-bad shapes, so
the second version turns that around.

## Executive Summary

- The dry run listed about 3,500 plans in its highest-confidence group. About
  nine in ten covered series that show no books at all; the other 300 or so
  carried about 350 books between them. Several of those were wrong: parts of
  one audio file read as volumes of a series, three-digit chapter numbers read as
  volume numbers, and series with no author that would all have merged into one.
- The operation no longer tries to list what is wrong. It applies a plan only
  when the series name has one plain shape (a name, a separator, an optional
  "Book" or "Vol" marker and a number from 1 to 99) and the library backs the
  number up: the book already sits at that position, or the same author has at
  least two more series of the same name with books, which together make a run
  of three consecutive numbers. Everything else is held, and the report says why.
  A rough re-count of the dry run against the new rules leaves about 170 plans,
  carrying about 160 books, that could apply; the rest are held.
- The operation respects the owner's locks on a book's series or position, and
  stays out of the way of a running library scan. Right before it deletes an
  emptied numbered series it checks once more that nothing points at it.
- It never creates a second series of the same name under another author row,
  and the medium-confidence group is now only ever reported, never applied.
- Nothing the operation does is permanent. Each book it moves, each series it
  creates and each numbered series it deletes is recorded first, and undoing the
  operation puts the numbered series back under its own identity with its books
  and their positions as they were. A write that fails is marked so an undo never
  acts on it, and a run with failed writes ends as a failure, not a success.
- It no longer creates an empty "Name" series for a plan that moves nothing,
  and it never puts a book on a position another book already holds. Books that
  would collide are left where they are and listed.
- A first run can be limited to one series, or to the series with the most
  books, so the result can be looked at before the rest follow. A run that
  changes anything needs a report file, so there is always a record.
- Not done: showing the held plans in the Repairs tab (filed as a task), and
  removing the empty numbered series (the existing empty-series clean-up does
  that).
