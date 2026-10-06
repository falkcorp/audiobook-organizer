<!-- file: docs/executive-summaries/2026-10-06-dedup-select-all-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: c0e0f341-7105-4f5e-837e-306dec50ed86 -->
<!-- last-edited: 2026-10-06 -->

# Selecting many duplicates at once

## Executive Summary

- **What was asked.** On the duplicate-review screens the owner wanted to
  tick every row on a page in one click, then every row on every page, and
  to Shift-click to tick a run of rows.
- **What it does now.** Every duplicate list with checkboxes has a tick-box
  at the top that selects the page. Once the page is ticked, a bar offers
  "Select all N matching", which takes in every page of the current filter.
  Shift-click ticks (or unticks) everything between the last row clicked and
  this one. Changing a filter or the page size clears the selection.
- **Acting on everything.** On the Review screen, merging or dismissing
  "all matching" is done by the server, which re-checks the filter itself
  and reports how many it changed and which it skipped (pairs pinned by hand
  are never touched). The screen asks for confirmation and shows the count
  first. On the Acoustic tab the rows are read page by page, up to 5,000 at a
  time.
- **A wrong-merge risk was closed.** On the Version Groups and Series tabs,
  merging one group shifted the tick-boxes and "keep this one" choices of
  every later group onto the group after it. A following merge could keep a
  book or series that belonged to a different group. Groups are now tracked
  by what they contain, not by their position in the list.
- **Errors are visible.** The Acoustic tab used to show "no duplicates
  found" when loading failed; it now says loading failed.
