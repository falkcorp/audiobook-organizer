<!-- file: docs/executive-summaries/2026-10-24-repairs-visibility-followup-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b9d3e71-c4a2-4f08-8e16-2a7f0d6c9b34 -->
<!-- last-edited: 2026-10-25 -->

# Repairs runs and their activity are private to the people who may use them

## Executive Summary

The Repairs page runs fixers that change the library on someone's behalf, and
some of them handle other people's data, such as their listening progress. An
earlier fix stopped people from reading a fixer's plan, results and change
history unless they were allowed to use that fixer. Other places still showed
the same runs: the newer operations screens, the Activity log, the dashboard,
and the generic "start an operation" routes. This change closes those, and makes
the Audible reading-status fixer administrator-only.

## What changed

- The Audible reading-status fixer now needs the permission to manage users, the
  same as the master-list import, because it writes any user's reading state.
  Editors can no longer plan, apply or read it.
- The newer operations screens leave out a Repairs run from anyone who may not
  use its fixer: the run lists, the counts (so a number never hints that a hidden
  run exists), the timeline, the live feed and the per-run pages. Cancelling such
  a run is refused to anyone but the person who started it. Approving it is
  refused to everyone who may not use the fixer, the starter included.
- The generic start and retry routes now ask for the same permissions as the
  Repairs page. Before, someone allowed to change settings could start the
  Audible reading-status fixer through them without being allowed to manage
  users.
- The Activity log leaves out the log lines of those runs, and its totals and
  pages are counted without them, so paging never shows a gap. The nightly
  summary entries have the hidden runs removed from their list of items.
- The dashboard no longer names those runs in its recent operations, and no
  longer hands out any operation's result data.
- Everything else, including every other kind of operation, looks the same as
  before to a person who only has view access.

## Gaps this change leaves open

- The nightly summary entries still carry overall counts that include the hidden
  runs (how many rows of each kind, and the tag totals), so a number can hint
  that such a run existed. Their items are removed.
- The system status screen (settings permission) still lists recent operations
  with their result data, so someone allowed to change settings but not to
  manage users can still read it there.
- The Activity page's source counts are not filtered, and a few other overview
  lists (stale operations, the pause list) are not yet covered.
- A Repairs run recorded before this change with no fixer named stays hidden
  from everyone and cannot be cancelled by an administrator who did not start it.
- If the record of a Repairs run has been deleted, its Activity lines that name
  the Repairs lane are hidden from everyone, but the engine's own plain log lines
  for it cannot be told apart and stay visible.

These are tracked in the to-do fragment filed with this change.
