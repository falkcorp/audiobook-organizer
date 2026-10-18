<!-- file: docs/executive-summaries/2026-10-18-dedup-page-retired-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e7b2a91-d3c8-4f56-a0b1-9c2d8e6f1a37 -->
<!-- last-edited: 2026-10-18 -->

# The old Dedup page is gone; Review does all of its jobs

Sixteen changes over several days, ending with this documentation update.
The plan is the "Retire the old /dedup page" proposal in the holistic set.

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
- **Clean-up jobs became Repairs.** Pruning empty series, matching missing
  files to books and finding books split into pieces are now repairs you try
  first and apply afterwards, like the other repairs.
- **Old links and bookmarks still work.** Every old Dedup address redirects to
  the matching place in Review. The list is below.
- **The Version Groups and Duplicate Scan tabs have no replacement.** Seven server requests that only those two tabs used now answer
  "gone" (a 410 reply) for one release, then they are removed.

## What to check

Open each address while signed in. Each should land on the Review page, on the
lane named. Back should not return to the old address.

- `/dedup` goes to the Duplicates lane, exact-match layer.
- `/dedup?tab=books` goes to the Duplicates lane, exact-match layer.
- `/dedup?tab=embedding` goes to the Duplicates lane.
- `/dedup?tab=acoustic` goes to the Duplicates lane, audio-match layer.
- `/dedup?tab=authors` goes to the Authors & series lane.
- `/dedup?tab=series` goes to the Authors & series lane, series view.
- `/dedup?tab=ai` goes to the Authors & series lane, AI scans view.
- `/dedup?tab=reconcile` goes to the Repairs lane with the missing-files
  repair selected.
- `/dedup?tab=split-books` goes to the Repairs lane with the split-book repair
  selected.
- `/dedup?tab=unified`, or any tab name that no longer exists, goes to the
  Duplicates lane.
- `/dedup/labels` goes to the Duplicates lane, Labels view.
- `/authors/dedup` and `/books/dedup` go to the Duplicates lane, exact-match
  layer (the same as `/dedup` with no tab).
- `?book=` and `?band=` on an old address are carried over; every other old
  setting is dropped.

## What did not carry over

- **"Reset all audio fingerprints" has no button in Review.** On the old page it
  wiped every stored fingerprint and then queued a rescan of the whole library.
  The plan said its confirmation should warn that the Mac workers would
  re-fingerprint everything, which takes days. No such button or warning exists
  now: the button went with the old page. The server request behind it is still
  there and still does the wipe-then-rescan, so it should not be called without
  that warning. The server itself never reads audio for this; only the Mac
  workers do.
- **The Library page's own button is untouched.** "Re-fingerprint ALL files"
  there is unchanged.

## Open follow-ups

- Compare the split-book repair with the existing fragment repair and decide
  whether both stay.
- Make "keep recommended" a server-side answer instead of one worked out in the
  browser, so it can apply across pages.
- Move clustering to the server, so the Clusters view is not limited to the
  page of pairs on screen.
- Remove the seven "gone" routes one release after this one.
- Decide whether the reset-fingerprints command returns to Review, with the Mac
  worker warning and only while a worker is connected.
