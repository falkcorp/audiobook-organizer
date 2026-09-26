### Added

- `maintenance.author-strip-merge`: new `relink_title_as_author` flag credits every book held by a title-as-author row ("Arcane Chef 2") or its numbered twin ("01 Arcane Chef 2") to its real author, when the library's own evidence names exactly one: the stored provider author, another version of the book, the files' artist / album-artist tags, or another book in the series. The author is created through the creation gate when no row exists. Conflicting or missing evidence leaves the book alone and reports it; books under `books/itunes/**` and Doctor Who / Big Finish / Torchwood are never touched. `apply=false` with the flag set is the preview: one log line per book with the junk author, chosen author and sources. With `delete_title_as_author` as well, a twin whose books were all relinked is deleted. Every author creation and credit move writes an undo-journal row first (the book's full credit list in order, co-authors included, plus its primary author); a failed journal write skips the book. Duplicate author rows for the chosen name are reported as `ambiguous` and not written, and `limit` caps the relinks.

### Fixed

- `maintenance.author-strip-merge`: a numbered twin is never merged into a title-as-author row, whatever `delete_title_as_author` says. It is reported as `target-is-junk`; a target whose credits cannot be read is reported as `target-unverified` and not merged.
- Copy clusters with no row in the book's own folder now keep a non-iTunes row over a `books/itunes/**` twin, so `zero_rows_only` duration backfill no longer skips such a book as iTunes.

### Changed

- The frozen iTunes tree rule (`books/itunes/**`) now lives in one helper, `pathutil.UnderFrozenITunesTree`, used by the copy-keeper choice, the duration backfill, the merge guard, config validation and the maintenance ops. It matches case-insensitively on path segments (`itunes` under a segment ending in `books`), so `Books/iTunes/` and `audiobooks/itunes/` both count. The copies in `internal/config` and `author-path-link` are removed.
- `mergeAuthorInto` and the new relink share one per-book credit move (`relinkBookCredit`).
