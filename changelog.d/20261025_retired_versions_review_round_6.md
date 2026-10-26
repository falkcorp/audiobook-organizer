### Fixed

#### Retired-version readers: the sixth re-review findings

A combine that needs the complete list of the rows holding a file, to decide
whether it may take another book's row, now refuses before it writes anything
while the in-memory index is still warming up after a restart. It used to warn
and go on, which retired the absorbed book without its audio on the survivor, a
lasting result of a condition that clears within a couple of minutes (the
ordinary single-holder case included). The refusal is an error that says to retry
later, and the same combine goes through once the index serves. A combine that
loses the list while it runs still finishes with a warning, as before. A file held
by several books, or by a book that no longer exists, is still a warning.

The regroup apply (multi-disc collapse and duplicate-of merge) now reports what
its combine left undone: a file that could not be attached to the survivor is put
in the approve response's `note` (bulk approve: `notes`, one entry per item) and
logged, instead of only reaching the undo journal. A review-apply function adds
such lines with `database.AddApplyNotice`; the item is still recorded as applied,
because the combine committed.

The review page now shows that note. A single approve toasts it as a warning, and a
bulk approve lists each item's note in the kind's report panel until it is
dismissed. The held-holds lists (applying, needs attention) show it under the list
after a Finish or an Approve. The same note also carries the existing "apply is
off, nothing ran" and "finished an interrupted apply" texts, which the page used
to drop on a single approve. The web client's `approveReviewItem` now returns
`{item, note}` instead of the item alone.

A file row that fails to write during a combine (a failed move of another book's
row, a failed create) is now a warning on the combine's result and its undo
journal, not a log line only, and the journal no longer keeps the "took the row
from trashed book" note for a row that never moved.

Undoing a combine whose reattached row came from a book that has since been
purged now gives the row to the absorbed book whose file it is, with a warning,
instead of leaving it on the survivor, whose duration and track list then
included audio it never had. A row taken for the survivor's own file stays on the
survivor.

A combine refused while the library index is still loading now says so where the
user sees it: the queued manual combine operation now ends completed with a
recorded `retry_later` outcome (the toast is a warning, "The library is still
loading; try the combine again shortly", with no Undo, because nothing was
journaled or changed), and a single review approve answers 503
`REVIEW_RETRY_LATER` (the hold stays pending; a bulk approve lists it under
`skipped`). Both carry the reason, which used to reach only the server log behind
a generic 500. The message
no longer promises "a minute": it says to try again once the index has finished
loading, and that a server restart is needed if it never does.

A file row written during a combine whose fsync failed (applied, durability
unknown) is now treated as attached, like the bulk move already was: the journal
keeps its record so undo puts the row back, and the result carries a "durability
unknown" warning instead of "nothing changed". The review page no longer toasts the
routine "needs no apply step" note of a `separate` approve. A junk-titled book whose
only file is held by a retired version is now listed as needing a person, not as a
fragment for the consolidation fixer, which does not act on retired rows.
