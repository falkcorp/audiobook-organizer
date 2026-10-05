### Fixed

#### Chapter fragments no longer assemble a second copy of a book that already exists

The fragment-consolidation fixer's no-parent path assembled a new book from a
folder's numbered chapter fragments without checking whether that work was
already a live book. On 2026-10-05, 13 of the 19 applied no-parent rows made a
duplicate ("Book 2 - Eldest", 347 fragments, beside "Eldest", 349 files).
Every no-parent group is now compared, by a title key that strips series noise
("Book 2 - ", "02 - ", ", Book 2", "(Unabridged)"), with the live books of
any author. A match becomes an `existing-book` row: when the totals agree
within max(2%, 5 min) the fragments are retired into that book, each keeping
its own file row; otherwise the row is held as `skipped_existing_book`.
Fragments that sit at the chapter positions of an existing book's files with
the same durations and a constant size difference (the Horizon Storms
re-tagged copies) are held as `skipped_retagged_copies` whatever the titles
say. A cut-off join resumes from its plan, and a fresh plan credits the
fragments the earlier run already retired.

#### Every fragment candidate is listed, none silently dropped

Fragments that no chapter group took (fewer than three same-key siblings, a
numbered file left over in a folder of several works, a chapter title with no
chapter key in the file name) used to fall out of the plan with no row and no
reason. Each now has an `unplaced` row with a specific skip kind
(`skipped_lone_chapter`, `skipped_scattered_numbered`,
`skipped_no_chapter_key`), so the Repairs lane and the census count them.

#### Co-owners are decided by the owner's duration rule

A row whose fragment file is also a row of a live book outside it was always
held. Now (owner rule 2026-10-05): a co-owner titled as another work
("Prelude to Foundation" beside "Foundation") still holds the row; a
same-titled co-owner whose total agrees with the fragments' is joined; totals
that disagree let the row proceed beside the co-owner, which keeps its rows;
an unknown total holds. All such rows are review risk.
