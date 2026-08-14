- [ ] 🗑️ **`ListBooksByITunesPID` returns soft-deleted books, on both code paths.**
      Found while auditing the memdb/Pebble soft-delete divergence (PR #2392), but
      deliberately **not** fixed there — unlike `GetAllBooksCore`/`CountAllBooks`, the two
      implementations of this method *agree*: neither filters `marked_for_deletion`
      (`internal/database/memdb_reads.go` `ListBooksByITunesPID`, and the Pebble fallback
      at `internal/database/pebble_store.go:1135`). It is therefore a consistent behaviour,
      not a drift, and changing it changes what the iTunes handlers do — out of scope for a
      fix whose whole point was making the two paths agree.

      Whether it is *correct* is a separate question, and the answer is probably no. The
      two callers are `internal/server/handlers/itunes.go:630` (list iTunes-mapped books)
      and `:711` (the writeback preview filter). The second is the one that matters: the
      writeback preview is what decides which metadata gets offered for writing back into
      the iTunes library, and a book the user deleted should almost certainly not be in
      that set. With 3,953 soft-deleted books in prod as of 2026-08-13, any that carry an
      `itunes_persistent_id` are currently eligible.

      Before changing it, check both callers' expectations — the listing endpoint may
      legitimately want to show a trashed-but-still-mapped book so the mapping can be
      cleaned up. If so, the fix is a parameter or a second method, not a blanket filter.
      Note the standing constraint that `books/itunes/**` is read-only; this is about what
      we *offer* to write, and the ITL file, not about touching that tree.

      Regression test to add either way: a soft-deleted book with an iTunes PID must not
      appear in the writeback preview.
