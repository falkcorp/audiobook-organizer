### Added

#### `maintenance.author-strip-merge` — opt-in `delete_unmatched` for numbered rows whose residue names no author

The op already stripped chapter-file numbering off author rows and merged the
residue into an existing author when one matched; rows whose residue matched
nothing were only counted (`stripped-no-target`, 812 on the live library) and
left alone, because renaming them would launder a book title into a plausible
person. Deleting them is a different act and is now available behind
`delete_unmatched=true` (default false). Deletion goes through the same
dangling-`AuthorID`-safe path as the junk deletes: the credit is removed, a
surviving co-author is promoted to the book's primary, and nothing is ever
renamed. The dry run now runs that same code without writing, so its new
`books-left-authorless` figure is the apply's own number rather than an
estimate; report samples name the reason for each delete (`junk` /
`unmatched`).
