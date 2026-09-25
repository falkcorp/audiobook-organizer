### Added

- New maintenance op `maintenance.build-folder-book-files` builds `book_file` rows for books ABS lists (primary + organized) that own none while their folder holds audio. It creates one row per audio file in natural filename order, fills each file's size and duration, and recomputes the book's totals, so these books stop showing a duration of 0. Files another live book already references are skipped and reported, the iTunes tree is never touched, and the op is a dry run unless `{"apply": true}`. It takes optional `book_ids` and `limit`.
- The ABS item filter (primary, organized, not quarantined) is now `database.ABSLibraryFilter`, shared by the ABS handler and the new op.
