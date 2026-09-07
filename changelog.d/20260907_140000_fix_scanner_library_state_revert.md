### Fixed

#### A rescan no longer reverts `library_state`, so organized books stop vanishing from the ABS layer

Every rescan reset a book's `library_state` back to `imported`, undoing the
`organized` stamp that the organizer had written. Because the scan root and the
organized tree are the same directory, this affected every organized book on
every scan — and because the Audiobookshelf-compatible layer serves only books
whose state is `organized`, those books silently disappeared from author pages,
series listings and counts as a scan advanced. Measured on production: six of one
author's nine books read `imported` while still carrying `last_organized_at`, and
only the three most recently organized were visible to an ABS client. The
underlying data — author links, series links, file paths — was correct
throughout; only the state field had been reverted.

The overlay was unguarded because `library_state` was listed among the fields
"read off the file itself, [where] the scanner IS authoritative". That rationale
was false for this one field: unlike its neighbours (`FileHash`, `FileSize`,
`Duration`, …) it is not read from the file at all — it is a creation default,
overridden only when a scan genuinely derives a state such as `suspicious`. The
organizer writes the same column and claims ownership of it, so two writers
conflicted and the scanner won simply by running more often.

A scan-derived state still wins, and a row that has no state yet still receives
the default; what a scan can no longer do is overwrite an existing state with its
own default. The behaviour is now pinned in both directions by tests, which the
field previously had none of.
