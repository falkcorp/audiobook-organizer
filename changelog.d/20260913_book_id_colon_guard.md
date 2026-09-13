### Fixed

- `CreateBook` now rejects a caller-supplied book ID that contains `:` and
  returns `database.ErrInvalidBookID`. Before this, the ID went into the
  `book:<id>` Pebble key verbatim. Every book-keyed scan assumes the ID is a
  single key segment. The version-group backfill, for example, skipped such a
  row with no error. There is one create path, `PebbleStore.CreateBook`, and
  the `indexedStore` decorator delegates to it
  (PEBBLE-KEY-BOUND-CENSUS, #2896).
