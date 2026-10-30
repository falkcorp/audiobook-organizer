### Fixed

- `GetBooksByTitleInDir`, which the scanner calls once for every newly imported
  book to find same-title siblings for version linking, no longer walks and
  JSON-decodes every book row. MemStore now keeps a `(normalized title, parent
  directory)` index and `PebbleStore.GetBooksByTitleInDir` answers from it once
  memdb is published, so the lookup costs the number of siblings found instead
  of the size of the library. The Pebble scan remains as the fallback for the
  warmup window and for a memdb known to be missing book rows
  (`ErrMemdbIncomplete`); it now logs, rather than silently skipping, a row it
  cannot decode. Results are identical to the old scan (soft-deleted,
  merged-away and non-primary rows are still returned, ID order), except that
  memdb rows carry the stripped book projection.
