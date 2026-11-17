<!-- file: docs/executive-summaries/2026-11-16-series-denumber-safe-apply-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 977a5f90-535a-436e-afa1-f26225c95156 -->
<!-- last-edited: 2026-11-16 -->

# The series clean-up can now be undone, and no longer applies plans a dry run showed were wrong

**Date:** 16 November 2026. The clean-up (the operation that folds numbered series
such as "Name 05" into one "Name" series) had never been applied to the real
library. A dry run against the real library showed its most confident group was
not safe, so this change makes it safe before anyone runs it for real.

## Executive Summary

- The dry run listed about 3,500 plans in its highest-confidence group. About
  nine in ten covered series that show no books at all; the other 300 or so
  carried about 350 books between them. Several of those were wrong: parts of
  one audio file read as volumes of a series, three-digit chapter numbers read as
  volume numbers, and series with no author that would all have merged into one.
- Every one of those shapes is now held out of the group the operation applies
  by itself, and the report says why for each.
- Nothing the operation does is permanent any more. Each book it moves, each
  series it creates and each numbered series it deletes is recorded first, and
  undoing the operation puts the numbered series back under its own identity
  with its books and their positions as they were. The operation refuses to
  apply at all if it cannot record a change.
- It no longer creates an empty "Name" series for a plan that moves nothing,
  and it never puts a book on a position another book already holds. Books that
  would collide are left where they are and listed.
- A first run can be limited to one series, or to the series with the most
  books, so the result can be looked at before the rest follow.
- Not done: showing the held plans in the Repairs tab (filed as a task), and
  removing the empty numbered series (the existing empty-series clean-up does
  that).
