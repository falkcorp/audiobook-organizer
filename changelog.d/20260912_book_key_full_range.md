### Fixed

#### Pebble book scans no longer skip books whose ID starts with a letter, `_` or `~`

Thirty-four Pebble scans over book rows used the key range `["book:0", "book:;")`,
and two more (`ListBookIDs`, `GetAllBooksFullFrom`) used `"book:~"` as the upper
bound. `;` is 0x3B, so any book whose ID starts above it (every letter, `_`, `~`)
was silently left out. New IDs are ULIDs and start with a digit, but `CreateBook`
keeps a caller-supplied ID, the seed data mints `seed_<ULID>`, and older rows can't
be ruled out. The most dangerous scan was `getAllBooksCoreFromPebble`, which the
orphan book_file sweep falls back to when memdb is incomplete. It hard-deletes every
file row whose book is missing from that list, so a skipped book lost its files.
`ListSoftDeletedBooks` feeds the same sweep and had the same bug.

All of these scans, and the seven that already used the correct range with their own
inline filter, now go through one iterator in `internal/database/book_row_iter.go`.
It scans the full `["book:", "book;")` range and yields only bare `book:<id>` rows,
jumping over each index subtree (`book:asin:`, `book:path:`, `book:versiongroup:`
and the others) with a single seek. The same fix applies to `GetAllWorks`
(`work:` family, which also accepts caller-supplied IDs) and to the book_file pass
of `GetAllSeriesFileCounts` (`book_file:<bookID>:…`). `getAllBooksCoreFromPebble`
and `ListSoftDeletedBooks` now also return an error instead of a short list when
the iterator fails partway through.
