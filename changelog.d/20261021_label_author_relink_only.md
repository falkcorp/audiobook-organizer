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
