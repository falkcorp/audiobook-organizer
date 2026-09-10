- [ ] **DUPROW-4** — Build a replay-from-journal tool for `book_file_delete`
      ledger rows. `maintenance.dedupe-book-file-rows` now writes the full
      deleted row as JSON into `OperationChange.OldValue` (change_type
      `book_file_delete`), but nothing can read it back: `internal/undo`'s
      `revertChange` switch handles `file_move` / `organize_rename` /
      `metadata_update` / `db_update` / `dir_create` / `tag_write` and returns
      `unknown change_type` for anything else — so an undo of a dedupe run
      currently reports one failure per journaled row instead of restoring them.
      The ledger is complete enough to replay (id, path, duration, size,
      fingerprint); the reader is what is missing. Until it exists, "replayable"
      means "the data is recoverable by hand", not "undo works".
