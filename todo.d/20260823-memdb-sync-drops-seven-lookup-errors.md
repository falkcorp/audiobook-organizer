- [ ] **MEMDB-SYNC-DROPPED-ERRORS: seven delete helpers in `memdb_sync.go` treat a
      lookup error as "row absent" and commit.** Each does `if err != nil || obj == nil
      { continue }` (or `err == nil && obj != nil`) around a `txn.First`, so a real
      lookup failure is indistinguishable from a missing row and is neither logged nor
      recorded. Every one of them fails CLOSED for the reference counters — memdb
      retains a row Pebble deleted, which over-counts — which is why this is not urgent.
      But it is seven unlogged error drops in the single file that owns the
      memdb/Pebble invariant, and the next divergence will arrive through one of them.
      Split the conditions: `obj == nil` continues, `err != nil` logs and calls
      `recordLostRows`. Related: `loadBookFilesForBookID` drops undecodable rows with
      `if err := json.Unmarshal(...); err == nil` and returns a nil error, and
      `UpsertBookToMemDB` then uses that short list to REPLACE memdb's book_files for
      the book — a silent, unrecorded divergence feeding `GetAllAuthorFileCounts`.
      Found by review on #2787.
