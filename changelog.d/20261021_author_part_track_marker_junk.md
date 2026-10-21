### Fixed

#### Author names with part/track/disc markers are no longer treated as people

Author names that end in a part, track or disc marker ("David Copperfield P2
S3", "Charles Dickens Disc1", "CD2", "D1T05", "S01E02", "Pt.4") or whose last
word is only digits ("Copperfield 07") are now treated as not a person.
`personname.HasPartTrackMarker` holds the rule (an edition suffix such as
"(Unabridged)" is ignored first). Author dedup (`FindDuplicateAuthors`) skips
such rows instead of grouping them. The junk-author classifier flags the glued
markers (P, S, Pt, CD, D1T05 and the like) with the new relink-only
`part_track_marker` rule, so the junk-author fixer proposes moving their books
to a real author and never deletes the credit. Numeric and spaced markers keep
the verdicts the classifier already gave them: "Copperfield 07" and "Great
Expectations Pt 4" stay `number_word` (relink-only), while "Charles Dickens
Part 2" stays `structural_word`, which can unlink the credit. Real names such
as "Malcolm X", "Pope John Paul II", "Louis XIV", "50 Cent", "Agent K9" and
"R2 D2" are untouched; a band that truly ends in a bare number ("Maroon 5") is
flagged, which is the accepted cost of the owner-approved rule.

Names that are only a part/track/book label ("Book 2 (Unabridged)", "[Track
003] 2", "Vol. 01", "10 Chapter 10") are also skipped by author dedup
(`personname.IsLoneMarkerPhrase`). The junk-author classifier is unchanged for
them: it already flags them as `structural_word` or `author_creation_gate`.
