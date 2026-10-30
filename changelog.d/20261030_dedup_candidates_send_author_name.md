### Fixed

- The Duplicates lane's candidate list (`include_books`) and the candidate comparison drawer now send `author_name` and `series_name` on `book_a` / `book_b`. They returned the bare book record, so every Dupes card, cluster list and comparison panel read an author of `undefined` and showed no author line. The names are resolved by the same `database.ResolveBookNames` the audiobooks endpoints use.
