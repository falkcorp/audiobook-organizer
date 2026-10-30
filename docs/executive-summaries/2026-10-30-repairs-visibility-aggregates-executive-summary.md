<!-- file: docs/executive-summaries/2026-10-30-repairs-visibility-aggregates-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0c7e41b9-5a62-4d38-b9f4-d1a83e2c6057 -->
<!-- last-edited: 2026-10-30 -->

# Repairs runs no longer show up in totals and overview lists for people who may not see them

**Date:** 30 October 2026. **Branch:** `fix/repairs-visibility-on-overview-and-digests`.

## What was wrong

Some Repairs runs are limited to people who hold particular permissions, because the clean-up they do
touches things like user accounts. The app already hid those runs, their details and their activity
from everyone else. Three places still gave them away:

1. **The pause banner, the stale-operations list and the operations timeline** listed running Repairs
   work by name for any signed-in viewer, so someone without the right permissions could see that a
   restricted clean-up was running.
2. **The daily summaries in the activity feed** hid the individual lines of a restricted run, but still
   stated the day's totals: how many entries of each kind, and the sentence "Daily digest for
   2026-10-01 (14 entries)". Comparing totals between callers, or against the visible lines, said how
   much a hidden run had done.
3. **Cancelling a very old Repairs run that named no clean-up at all** was refused even to an
   administrator, because nobody is allowed to see such a run. A stuck one could not be stopped from
   the newer screens.

## What changed

- The three overview lists now go through one shared filter, and a test walks every operations screen
  the server exposes and fails if a new list is added without a check.
- Each night's summary now records how much of the day belongs to each operation. When someone reads a
  summary, the share of every operation they may not see is taken out of the totals and the sentence.
  Summaries written before this change cannot be split that way, so the totals are left out for anyone
  who might be hidden something, and the Activity page shows a small "counts hidden" label instead of
  numbers. A summary with no details at all, or no date, is treated the same way rather than shown as
  stored.
- Re-deriving old summaries no longer loses what marks a line as belonging to a Repairs run when the
  run's own record has since been deleted.
- An administrator with the settings permission can now cancel an old Repairs run that names no
  clean-up. The run itself stays hidden.

## Who is affected

Everyone using the Activity page sees the same lines as before. People without the restricted
permissions now see smaller day totals (or "counts hidden" on older days). Administrators see no change.
No library data was changed or lost, and no setting needs adjusting.
