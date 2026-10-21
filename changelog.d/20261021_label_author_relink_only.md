### Fixed

#### Never leave a book without an author when repairing junk author credits

The junk-author repair and `maintenance.author-strip-merge` no longer remove a
credit that is a book's only author credit (owner decision 2026-10-21). A sole
junk credit is held (`held_sole_credit` in the repair; `books-held-sole-credit`
and `rows-held-sole-credit` in the strip-merge summary, which keeps the junk
record while any book depends on it), and is swapped only when a real author is
found. Names that are only a part, track or book label ("Book 2 (Unabridged)",
"Part 2", "Disc 1", "Vol. 01", "[Track 003] 2", "1-02") also get the relink-only
`label_only` rule, which needs the provider or two independent kinds of
evidence. `personname.IsLoneMarkerPhrase` now covers ranges, decimals, letter
labels, prologue/epilogue/introduction words and number-only codes.

Two more places no longer strand a book without an author. Reclassifying an
author as a narrator falls back to the unknown-author record when no author
credit would survive, and a failed rollback of an organized copy no longer
clears the author credits of a book it then cannot delete. The strip-merge
treats a narrator-only or junk co-credit as no author at all, and labels such
as "Part 1 of 3", "Book #2" and "Track 1/12" are label-only. HTML entity
shrapnel such as "&#169" is not read as a label.
