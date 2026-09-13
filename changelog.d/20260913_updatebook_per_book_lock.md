### Fixed

- Two writers updating the same book at once no longer silently drop each
  other's fields. `PebbleStore.UpdateBook` now holds a per-book write lock
  (256 fixed stripes hashed from the book ID) across its read of the stored
  row and its commit, so different books still write in parallel. New
  `Store.ModifyBook(id, fn)` reads and writes under one hold, and
  `SnapshotBook` + `MergeBookChanges` let a caller that does slow work between
  its read and its write merge only the fields it changed onto the fresh row.
  The metadata candidate apply (single and batch), the book-page save
  (`UpdateAudiobook`), `FillBookMediaInfo` and nine in-store read-modify-write
  helpers use it. Callers elsewhere that still do `GetBookByID` then
  `UpdateBook` get a serialized write but can still overwrite a concurrent
  change with their stale copy.
