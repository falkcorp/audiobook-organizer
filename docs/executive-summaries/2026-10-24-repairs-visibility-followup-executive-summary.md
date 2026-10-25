<!-- file: docs/executive-summaries/2026-10-24-repairs-visibility-followup-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b9d3e71-c4a2-4f08-8e16-2a7f0d6c9b34 -->
<!-- last-edited: 2026-10-24 -->

# Repairs runs and their activity are private to the people who may use them

## Executive Summary

The Repairs page runs fixers that change the library on someone's behalf, and
some of them handle other people's data, such as their listening progress. An
earlier fix stopped people from reading a fixer's plan, results and change
history unless they were allowed to use that fixer. Two other places still
showed the same runs: the newer operations screens and the Activity log. This
change closes both, and makes the Audible reading-status fixer
administrator-only.

## What changed

- The Audible reading-status fixer now needs the permission to manage users, the
  same as the master-list import, because it writes any user's reading state.
  Editors can no longer plan, apply or read it.
- The newer operations screens leave out a Repairs run from anyone who may not
  use its fixer: the run lists, the counts (so a number never hints that a hidden
  run exists), the timeline, the live feed and the per-run pages. Cancelling or
  approving such a run is refused to anyone but the person who started it.
- The Activity log leaves out the log lines and summaries of those runs, and its
  totals and pages are counted without them, so paging never shows a gap.
- Everything else, including every other kind of operation, looks the same as
  before to a person who only has view access.

## Gaps this change leaves open

- A few overview screens that list recent or stale operations by name are not
  yet covered, and the Activity page's source counts are not filtered.
- If the record of a Repairs run has been deleted, its Activity lines that name
  the Repairs lane are hidden from everyone, but the engine's own plain log lines
  for it cannot be told apart and stay visible.
