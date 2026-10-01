- [ ] **ALTQUERY-CACHE** An alt-query metadata search (custom title, no
      `refresh`) can be answered from the book row's own cached provider
      results: the per-source fetch-cache identity comes from the row, not
      the query. Key the cache on the effective query.
- [ ] **RIPTITLE-CACHE-KEY** The bulk candidate path keys the fetch cache on
      the cleaned rip-detail title, while `searchMetadataForBook` keys it on
      the raw row title, so the two paths never share cache entries for the
      same book. Use one key builder for both.
- [ ] **APPLY-METADATA-404** `POST /audiobooks/:id/apply-metadata` returns
      500 for a book that does not exist; it should be 404.
- [ ] **FRAGMENT-SHAPES** The part-row resolver (`internal/metabatch/
      part_rows.go`) still lets these fragment shapes through to a whole-book
      search: "NN - Title" ("32 - Leveling Up The World 3"), "N-M Author"
      ("248-299 Kevin J Anderson"), bare `copyN` stems ("Cobra_copy124"),
      and "Part NN" rows searched under their folder name. The bulk-apply
      runtime gate catches them today; the resolver should not search them.
      Also measure rip-titled box sets ("Harry Potter 1-7 [64k]") before
      adding a duration check to the rip branch.
- [ ] **DATA-EDGE-OF-VICTORY** Book "Edge of Victory 1 - Conquest" has its
      narrator (Alexander Adams) as author instead of Greg Keyes, so the
      author-gated search variants miss it. Look for other narrator-as-author
      rows of the same shape.
