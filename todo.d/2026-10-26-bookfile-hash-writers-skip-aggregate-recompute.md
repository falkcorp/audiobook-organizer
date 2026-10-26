- [ ] **TODO-BF-HASH-WRITERS-RECOMPUTE** `SetBookFileHash` and `UpdateBookFileHashes`
      skip the per-book `RecomputeBookAggregates` because their comment says
      "Hashes feed no book aggregate" (`internal/database/pebble_store_bookfiles.go`,
      both `updateBookFileOver(..., false)` calls). That is no longer true:
      `internal/database/book_runtime.go` reads `FileHash` and `OriginalFileHash`
      to drop a missing row that repoints a present duplicate from the stored
      aggregate, and the backfill-file-hashes job's own `storeHash` comment says
      so and recomputes each book once per chunk. Callers that store a hash
      through these two methods (extract-wav-clips, write_tags_safe,
      `book_file_hash_recorder`) can leave `Book.Duration` stale until something
      else recomputes the book. Decide per caller: recompute in the store, or keep
      the skip and have the batch caller recompute once (the backfill job's
      pattern). Found in the review of #4190; pre-existing on main, not changed there.
