### Fixed

#### Chapter fragments are checked against the library before a book is assembled from them

The fragment-consolidation fixer's no-parent path assembled a new book from a
folder's numbered chapter fragments without checking whether that work was
already a live book. On 2026-10-05, 13 of the 19 applied no-parent rows made a
duplicate ("Book 2 - Eldest", 347 fragments, beside "Eldest", 349 files).
Every no-parent group is now compared with the live books by a title key: a
leading position ("Book 2 - ", "02 - ", "Inheritance Cycle 02 - ") is taken
off but kept, so "Book 2 - Eldest" matches "Eldest" and never "Book 3 -
Eldest"; a volume or book number elsewhere stays in the key, so "Saga, Vol 3"
is not "Saga, Vol 1" and "Dragon Born, Book 3" is not "Dragon Born". The
group's title comes from its folder, its chapter key or its members' titles;
a group with none of these is held (`skipped_no_title_key`) instead of being
assembled unchecked. A match becomes an `existing-book` row: when the totals
agree within max(2%, 5 min) and the authors do not differ, the fragments are
retired into that book, each keeping its own file row; otherwise the row is
held as `skipped_existing_book`. The row shows the source folder, title and
author beside the book it joins.
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
held. Now (owner rule 2026-10-05) the co-owner is compared with the row's
work (a moved or copy row's whole parent, a no-parent row's chapters): a
co-owner titled as another work ("Prelude to Foundation" beside
"Foundation"), by another author, of unknown duration, or same-titled with a
total that disagrees still holds the row; a single same-titled co-owner whose
total agrees is joined; only junk-titled co-owners ("", "c5") whose totals
disagree let the row proceed, and they keep their rows. All such rows are
review risk.
