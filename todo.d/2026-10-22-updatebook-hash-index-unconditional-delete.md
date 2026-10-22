- [ ] **UpdateBook hash-index delete is unconditional.** `updateHashIndex` in
      `internal/database/pebble_store.go` (about lines 3490-3540) deletes the old
      `book:hash:`, `book:originalhash:` and `book:organizedhash:` rows whenever a
      book's hash changes, even when another live book owns the row (the same
      class `DeleteBook` was fixed for on 2026-10-22). Make it compare-and-delete
      like `deleteKeyIfValueIs`, add a test with two books sharing one hash, then
      run `maintenance.book-hash-index-repair` (preview first) on prod.
