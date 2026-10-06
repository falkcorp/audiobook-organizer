<!-- file: docs/executive-summaries/2026-10-06-version-twin-metadata-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: f49cb6f9-becc-4504-ad16-031c9cc17089 -->
<!-- last-edited: 2026-10-06 -->

# Copying metadata to the version that the library shows

PR: https://github.com/falkcorp/audiobook-organizer/pull/3797 (not yet merged)

## Executive Summary

- **What was missing.** When the library holds two copies of one audiobook
  (a "version group"), only one of them, the primary, is the copy listening
  apps see. A count on 2026-10-05 found about 859 primaries counted as
  "metadata missing", even though the other copy of the same book already
  had its metadata matched, or had search results waiting for review.
  Nothing carried that work over to the primary.
- **What it does now.** A new repair on the Review page's Repairs tab lists
  those groups. For a copy that was already matched, it applies the same
  record to the primary, the same way a normal metadata apply does. Both
  "undo last apply" and reverting the repair's run on the Operations page
  take it back. For a copy that only has search results, it copies those
  results to the primary so the primary shows up for review. Nothing is
  applied in that case, and reverting the run removes the copied results.
  The narrator, ASIN, ISBN and running time are copied only when the
  running times (or the narrator) show the primary is the same edition.
- **What it refuses to do.** It never changes which copy is the primary,
  and it writes no book but the primary. Doing otherwise can hide a book
  from the listening apps. It never touches iTunes-linked books (checked
  again at the moment of writing), Doctor Who, Big Finish or Torchwood, books with
  hand-locked fields, or books the listening apps do not list. It also
  holds a group when the two copies look like different editions (running
  time more than 5% apart, abridged against unabridged, or a different
  narrator), when the copies disagree, or when the titles or authors do not
  match. Each held row says why.
- **Outcome.** The owner reviews a plan and applies chosen rows. How many of
  the ~859 qualify will be known from the first plan on the live library.
  Copies matched before 2026-10-05 may no longer have the search result they
  were matched from. Those rows are held with that reason and need a fresh
  search first.

## The repair

- **What it was.** Matching metadata was done per copy. The copy the
  listening apps show could stay "missing" while its twin was finished.
- **Why it mattered.** It inflated the missing-metadata count and showed
  unfinished entries in the listening apps for books that were in fact
  identified.
- **The fix.** A reviewable repair that copies the twin's finished match, or
  its search results, onto the primary. It re-checks each book at the moment
  of writing, keeps a record of every change, and refuses anything that
  changed since the plan.
