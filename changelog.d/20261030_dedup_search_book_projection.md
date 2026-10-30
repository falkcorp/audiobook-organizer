### Changed

- The Dupes panel's text search no longer reads every book in full to find matches.
  It asks the store for a four-field projection (id, title, file path, author name,
  joined by the store) via the new `BookBulkReader.ListBookSearchRows`, implemented on
  `MemStore` and on `PebbleStore` (whose Pebble fallback decodes only those fields).
  Match behaviour is unchanged; a parity test compares the old and new ID sets over a
  300-book fixture on both store paths. `DedupStore` now needs that one method in place of
  `GetAllBooksCore` and `GetAllAuthors`.
