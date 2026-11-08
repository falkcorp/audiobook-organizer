- [ ] **HNSW live mirror indexes non-primary books; hydrate and reconcile skip them** (R1-15
      follow-up review, open question). `prepBookEmbed` embeds non-primary books on purpose
      (calibration needs their vectors), and `EmbedBook`/`EmbedBooks` then mirror every one
      into the graph through `mirrorBookToChromem`, with `is_primary_version=false` metadata
      (`internal/dedup/engine.go`, the `prepBookEmbed` cache-hit path and the batched upsert).
      `HydrateChromem` and the stale-snapshot reconcile classify the same rows with
      `classifyBookRow`, which skips non-primary books, so the graph a process builds live is
      not the graph a boot builds: after a reconcile those keys are tombstoned (and count
      toward the 10% tombstone share that forces a rebuild), after a clean-stop boot they stay.
      Queries do not see it today: the only book query (`findSimilarBooks`) filters on
      `is_primary_version=true`. What differs is the graph's size and the reconcile's churn,
      and any future unfiltered book query would see books that come and go with the boot path.
      **Stopgap in place (R1-15 round-2 review, S2):** `mirrorCachedBook` marks the graph
      diverged when a cache hit mirrors a row `classifyBookRow` would skip, so that run
      writes no clean-stop marker and the next boot reconciles. The cache hit is the only
      mirror with no Pebble write; the fresh-embed path writes the embedding row, which the
      verdict watch already counts. The cost: any run that re-embeds a cached non-primary
      book (every library-wide embed run does) loses the clean-stop fast boot. The
      recommendation below removes both the mismatch and that cost.
      **Recommendation:** make the mirror follow the hydrate rule: skip the graph upsert (and
      delete a graph entry if present) when `classifyBookRow` would not index the book, while
      still writing the Pebble embedding row. One predicate then decides graph membership for
      all three paths. The other direction (index non-primary books everywhere and rely on the
      metadata filter) would add roughly the 22K non-primary rows production skips today to
      the graph's memory and search cost.
