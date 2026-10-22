<!-- file: docs/executive-summaries/2026-10-22-retired-versions-preview-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 86d93130-4e35-4fb9-a614-b81c80b79bc9 -->
<!-- last-edited: 2026-10-22 -->

# A safe preview for cleaning up trashed duplicate books

## Executive Summary

The library holds tens of thousands of books that were merged into another copy
and left in the trash. Moving their file records onto the copy that was kept is
the last step before the trash can be purged, but it touches every one of those
books, so it needs a careful look first. This work adds a **preview only**
version of that clean-up in the Review screen. It changes nothing: it sorts
every trashed book into a plain-language group and says what would happen to
it, so the owner can review the groups before any real clean-up is built.

## What you will see

- Each trashed book lands in exactly one group, such as "would be retired",
  "left alone, the owner rejected this pairing", "needs the owner's approval
  because a file is in the iTunes folder", or "too recent to touch".
- **Doctor Who, Big Finish and Torchwood books are never proposed for
  automatic clean-up.** They get their own group ("owner applies by hand"),
  whether the trashed book or the copy it would join is one of them.
- A book that was removed by a combine that can still be undone is flagged, so
  that a later clean-up can be made reversible by undoing the combine. The plan
  summary counts these separately.
- The screen shows a "Preview only" notice and has no Apply button for this
  tool. The server also refuses any attempt to apply it.

## What changed underneath, and what to do after the update

- **Every Review repair plan is now stored differently.** A plan used to be one
  very large record (about 39 MB for this one); it is now a small summary plus
  one record per row, so opening a plan reads only the page of rows on screen.
  The old plan records are not converted. **After this update is deployed, each
  repair plan has to be run once more.** Until then, opening an old plan shows
  the message "run the plan again". Nothing is lost by this; plans are only
  previews.
- Old plan records that are already stored stay in the database (the very
  large ones are tens of megabytes) until they are deleted.
- The checks that decide whether a file sits under the iTunes folder can no
  longer hang a run on a dead network drive. A path that gets no answer in 15
  seconds counts as "could not tell", and the book is skipped rather than
  cleared on a guess.

## What did not change

No book, file record or file on disk is modified by this tool.
