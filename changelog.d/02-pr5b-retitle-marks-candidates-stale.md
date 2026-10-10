### Fixed

- Retitling a book, or changing its author to a differently named one, no longer deletes its cached metadata candidates. The row is kept and marked stale with the question it was fetched for: every apply path refuses it (identity_stale) and the next candidate fetch refetches the book, so a retitle can no longer silently lose candidate rows. A stale row is also no longer copied to another book.
