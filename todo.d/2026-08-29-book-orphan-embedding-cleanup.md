- [ ] **DEDUP-ORPHAN-BOOK-EMB** Act on `HydrateChromem`'s new `books_orphaned`
      counter. The hydrate now reports, per restart, how many `emb:v:book:*`
      rows point at a book ID that `GetBookByID` no longer resolves — dead
      weight that no re-embed can ever reach, since the entity is gone. Two
      follow-ups: (1) read the count off a production restart and record it
      next to the 2026-08-29 baseline (39,658 book rows read, 17,706 indexed,
      21,952 skipped, of which only the stale-model bucket was previously
      visible); (2) if it is material, add the book-side counterpart of
      `dedup.cleanup-orphan-author-embeddings` — a dry-run-by-default op that
      reports orphaned vs. live rows and deletes only the ones it can prove
      orphaned. Note the book case is the EASY one: unlike authors, PebbleDB
      does not tombstone-redirect book IDs, so `GetBookByID` returning
      `(nil, nil)` is already the sound orphan signal. Also worth checking why
      `books_lookup_error` is nonzero if it is — that bucket means a LIVE book
      fell out of dedup and is an incident, not a cleanup candidate.
