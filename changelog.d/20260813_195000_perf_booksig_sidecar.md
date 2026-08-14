### Changed

- **Startup no longer reads 580 MB of book signature it throws away.** The five
  `BookSig*` fields now live under their own `book_sig:<id>` key instead of
  inline in the `book:<id>` row. Measured on production 2026-08-13: the `books`
  warmup phase read 729 MB, of which 580 MB (80%) was a signature that the
  in-memory projection nils out the instant it finishes decoding. The warmup
  scan is bounded `["book:", "book;")` and `_` sorts above `;`, so the sidecar
  falls outside the range and those bytes stop being read at all.

  Every signature consumer sees exactly what it saw before — dedup's signature
  scan and AcoustID-conflict veto, the dataset builder, the op-dependency field
  predicate, the AcoustID backfill, and the book-signature recovery audit all
  reach their books through `GetBookByID` or `GetAllBooksFullFrom`, and both
  hydrate the sidecar. Books already on disk are untouched: when no sidecar key
  exists the read falls back to the inline value, so the 67,824 existing
  signatures keep working with nothing migrated.

  This also closes a data-loss shape rather than only a performance one.
  Previously the sole thing preventing a memdb-projection round-trip from wiping
  every signature was a hand-maintained preserve-guard in `UpdateBook` — a guard
  that exists because that wipe already happened once, and whose own comment asks
  the next author to keep it in sync. A book that arrives carrying no signature
  now writes no sidecar key and deletes nothing, so not-wiping is the default
  behaviour instead of something someone has to remember.

  Note this reduces what startup READS, not what the database stores: the
  copy-on-write `book_ver:` snapshots deliberately keep the full inline
  signature, because the recovery audit recovers from them.
