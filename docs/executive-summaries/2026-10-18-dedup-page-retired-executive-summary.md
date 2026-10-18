<!-- file: docs/executive-summaries/2026-10-18-dedup-page-retired-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4e7b2a91-d3c8-4f56-a0b1-9c2d8e6f1a37 -->
<!-- last-edited: 2026-10-18 -->

# The old Dedup page is gone; Review does all of its jobs

PR #4065 (03-PR13, branch `task/03-pr13`), not yet merged; the merge commit
is filled in at merge.
Spec: `docs/proposals/2026-10-holistic/03-dedup-page-retirement.md`. Task
brief: `docs/proposals/2026-10-holistic/tasks/03/03-PR13.md`.

Sixteen changes over several days, ending with this documentation update.

## Executive Summary

- **One place for duplicates.** The old Dedup page had nine tabs. It is
  deleted. Everything it did now happens on the Review page, so there is one
  screen to learn instead of two.
- **The Duplicates lane took over the book work.** It now has filters for how
  each pair was found, buttons to keep the older or the newer record for every
  pair that matches the current filter, a Clusters view that groups pairs into
  sets of books, a CSV export, and two extra views: Labels (past decisions) and
  Suspicious (decisions worth a second look).
- **A new Authors & series lane.** Duplicate authors, duplicate series and the
  AI duplicate scans moved here. It is the fourth of five tabs on the Review
  page: Duplicates, Metadata, Review queue, Authors & series, Repairs.
- **Clean-up jobs became Repairs.** Merging duplicate series and deleting
  empty ones, matching missing files to books and finding books split into
  pieces are now repairs you try first and apply afterwards, like the other
  repairs.
- **Two tabs were retired outright.** Version Groups is covered by the
  exact-match filter plus the Clusters view. Duplicate Scan was a side
  pipeline whose results never reached the review queue, so it was retired.
  The seven server requests behind those tabs now answer "gone" (a 410 reply)
  for one release, then they are removed.
- **Old links and bookmarks still work.** Every old Dedup address redirects to
  the matching place in Review. The list is below.
- **One command was left out on purpose.** "Reset all audio fingerprints" has
  no button in Review, because using it on production would permanently lose
  the audio-match data. See "What did not carry over".

## What it was

The Dedup page was a second, older screen for finding and merging duplicate
books, authors and series. The Review page was the newer one. Both showed
overlapping lists, and some of the old page's tabs read from different data
than Review did.

## Why it mattered

Two screens meant two answers to "is this a duplicate?", and a decision made on
one was not always visible on the other. People had to know which screen held
which job. Some tabs, such as Duplicate Scan, produced results that never
reached the review queue at all, so work done there could not feed the
decisions made elsewhere.

## The fix

Each job was moved into Review, one piece at a time, then the old page and its
tabs were deleted and every old address was pointed at its new home. This
final change records that in the project's documentation: the architecture
reference now describes the Review page and its five lanes, and the two old
planning documents for the port carry a "superseded" notice.

## What to check

Open each address while signed in. Each should land on the Review page, on the
lane named. Back should not return to the old address.

- [`/dedup`](/dedup) goes to the Duplicates lane, exact-match layer.
- [`/dedup?tab=books`](/dedup?tab=books) goes to the Duplicates lane,
  exact-match layer.
- [`/dedup?tab=book-duplicates`](/dedup?tab=book-duplicates) goes to the
  Duplicates lane, exact-match layer.
- [`/dedup?tab=embedding`](/dedup?tab=embedding) goes to the Duplicates lane.
- [`/dedup?tab=acoustic`](/dedup?tab=acoustic) goes to the Duplicates lane,
  audio-match layer.
- [`/dedup?tab=authors`](/dedup?tab=authors) goes to the Authors & series lane.
- [`/dedup?tab=series`](/dedup?tab=series) goes to the Authors & series lane,
  series view.
- [`/dedup?tab=ai`](/dedup?tab=ai) goes to the Authors & series lane, AI scans
  view.
- [`/dedup?tab=reconcile`](/dedup?tab=reconcile) goes to the Repairs lane with
  the missing-files repair selected.
- [`/dedup?tab=split-books`](/dedup?tab=split-books) goes to the Repairs lane
  with the split-book repair selected.
- [`/dedup?tab=unified`](/dedup?tab=unified), or any tab name that no longer
  exists, goes to the Duplicates lane.
- [`/dedup/labels`](/dedup/labels) goes to the Duplicates lane, Labels view.
- [`/authors/dedup`](/authors/dedup) and [`/books/dedup`](/books/dedup) go to
  the Duplicates lane, exact-match layer (the same as `/dedup` with no tab).
- `?book=` and `?band=` on an old address are carried over; every other old
  setting is dropped.

## What did not carry over

- **"Reset all audio fingerprints" was left out on purpose.** On the old page
  it wiped the audio-match fingerprints of every file (all seven stored
  segments) and deleted the audio-match duplicate suggestions still waiting for
  review, then queued a rescan of the whole library. That rescan needs the
  server to read the audio itself, which production refuses to do. The Mac
  workers do not currently write those segment fingerprints. So on production,
  a reset would permanently lose the audio-match data with nothing to rebuild
  it. The owner decided the button should not come back until that is fixed,
  and it was not ported.
- **The server request behind it still exists, and must not be used.** It still
  does the wipe and the rescan. Nobody should call it on production.
- **The Library page's own button is untouched.** "Re-fingerprint ALL files"
  there is unchanged.

## Open follow-ups

- Teach the Mac workers to rebuild the segment fingerprints. Only after that
  can the reset command return to Review, with a warning and a check that a
  worker is connected before it will run.
- Compare the split-book repair with the existing fragment repair and decide
  whether both stay.
- Make "keep recommended" a server-side answer instead of one worked out in the
  browser, so it can apply across pages.
- Move clustering to the server, so the Clusters view is not limited to the
  page of pairs on screen.
- Remove the seven "gone" routes one release after this one.
