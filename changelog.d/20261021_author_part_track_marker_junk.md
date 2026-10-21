Author names that end in a part, track or disc marker ("David Copperfield P2
S3", "Great Expectations Pt 4", "Charles Dickens Disc 1", "CD2", "D1T05") or
whose last word is only digits ("Copperfield 07") are now treated as not a
person. `personname.HasPartTrackMarker` holds the rule; author dedup
(`FindDuplicateAuthors`) skips such rows instead of grouping them, and the
junk-author classifier flags them with the new relink-only
`part_track_marker` rule, so the junk-author fixer proposes moving their books
to a real author and never deletes the credit. Real names such as "Malcolm X",
"Pope John Paul II", "Louis XIV" and "50 Cent" are untouched; a band that
truly ends in a bare number ("Maroon 5") is flagged, which is the accepted
cost of the owner-approved rule.
