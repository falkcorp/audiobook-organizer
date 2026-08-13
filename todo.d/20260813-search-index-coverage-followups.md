- [ ] **Decide whether to force a search-index rebuild on prod.** The boot-time
      coverage check (`internal/server/search_coverage.go`) repairs the gap on the
      next restart by marking ~40K books dirty and letting the reconciler drain
      them (~5,000/tick, 30s ticks). That is a large background operation on a
      live server. Owner call: let it happen on the next natural restart, or
      schedule it. Measured gap 2026-08-13: books created 2026-08 were 2%
      searchable (1 found / 50 missing in sample), 2026-04 were 97% (38/1).
- [ ] **`all` and `and` are stopwords and are silently dropped from queries.**
      `dropStopwordOnlyConjuncts` (`internal/search/bleve_translator.go:150`)
      strips conjuncts that analyse to zero tokens — it exists to fix "shards of
      oblivion" returning nothing. Measured in the query JSON emitted by
      `TestReproAllJobsAndClasses`: `All Jobs and Classes` searches only
      `Jobs AND Classes`, and `all jobs` searches only `jobs`. The user is given
      no indication half the query was discarded. Independent of the index-coverage
      bug fixed on 2026-08-13; needs its own change.
- [ ] **Quoted phrases do not produce a `MatchPhraseQuery`.** The server-side
      parser never strips the quote characters, so `"All Jobs and Classes"`
      becomes the terms `All` and `Classes"` — closing quote glued to the final
      token. Confirmed in the same emitted query JSON. The translator's
      `n.Quoted` branch (`bleve_translator.go:317`) works; it simply never fires.
      It *appears* to work only because the English analyzer discards the quote as
      punctuation. Phrase search is not doing what the UI help text implies.
- [ ] **`SearchIndexDroppedCount` is not actually exposed on `/metrics`.** The
      comment in `internal/server/search_reconciler.go` says it is "Exposed for
      the metrics endpoint and for tests", but a live scrape of prod `/metrics`
      on 2026-08-13 returned 100 metric families and none matching
      `search`/`dirty`. Same declared-but-not-registered shape as the
      `maintenanceOrder` defect (#2360). Add the drop counter and the dirty-set
      backlog so the next divergence is visible without grepping journald.
- [ ] **A one-book version group can have no primary member.**
      `01KXXVBGQGH6PEP9WE0ZWHBJ50` ("All Jobs and Classes! Book II") is the sole
      member of `vg-01KXXVBGMHPATT8X1X3DV5AW2Q` and has
      `is_primary_version=false`, so it is invisible in the default Library view
      (which filters to primary versions) no matter what the search index says.
      Worth a sweep for other headless groups plus a repair, since a group with
      no primary is unreachable by design rather than by accident.
