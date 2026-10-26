- [ ] **TODO-TAGS-KEYWORD-FIELD** Map the search index's `tags` field as a real keyword
      (the keyword tokenizer plus a lower-case filter), bump `bookMappingVersion`
      (`internal/search/bleve_index.go`), and move `tag:` back into the index so it can be exact
      inside an `OR`, an alternation or a `NOT` group. Today `tags` is analysed as words (the
      standard analyzer: lower-cased, English stop words removed), so an index term on it matches
      by word ("to-read" reduces to "read"; "sci-fi" is found in "hard sci-fi"; `-tag:"it"` is the
      whole library). `tag:` is therefore pulled out of the query and applied by `GetBooksByTag`
      (`search.SplitTagTerms`, `search.ResolveTagIDs`), which only works for a term that is a
      conjunct of the whole query, and refuses the other shapes with a 400. The change forces a
      FULL reindex of the library (about 100,000 books). While the index rebuilds, a whole-sent
      query is answered by the substring fallback (`GetAudiobooks` and `searchWithinIDs` in
      `internal/audiobooks/service_query.go`), which matches the literal query text and returns
      about 0 rows for any fielded query, and every collection and playlist reads as
      `members_unavailable`. Plan the window with the owner first, and fix the fallback (or hold
      the old index until the new one is complete) before doing it. After it lands, delete the
      refusal of `tag:` under OR/NOT, `TestTagsIndexFieldMatchesByWord_WhichIsWhyTagIsNotAnIndexTerm`
      will fail and say so, and decide what `tags:` (the raw field name, a word match today) means.
      Left from the PR 4175 re-review (R-B1, decision D46).
