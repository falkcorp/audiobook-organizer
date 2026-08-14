### Search follow-ups from the wildcard/phrase fix (2026-08-13)

- [x] **Phrases containing an English stopword still over-match.** Fixed in #2391,
      deployed and verified on production 2026-08-13 22:03 EDT: `"All Jobs"` went from
      **300 results to 3**, all three the intended book. The cause was subtler than
      "the stopword is dropped": the stop filter removes tokens *without renumbering
      the positions of the survivors*, and `MatchPhraseQuery` rebuilds the phrase from
      those positions. So `"All Jobs"` became a **one-slot** phrase (a bare term query)
      while `"Lord of the Rings"` became a **four-slot phrase with two nil slots** —
      wildcards matching "Lord ANY ANY Rings". Text fields now use a
      stopword-preserving analyser, with an index mapping-version marker that triggers
      the rebuild. The re-index ran in ~36 min over 67,824 books, `failed=0` on every
      batch. `TestQuotedPhraseWithLeadingStopword` was replaced by
      `TestQuotedPhraseWithStopword`, which asserts both cases with word-order decoys.
- [ ] **Fuzzy queries (`~`) have the same case-sensitivity defect the wildcard fix just
      addressed.** `bleve_translator.go` builds `NewFuzzyQuery` from the raw term, and
      FuzzyQuery bypasses the analyser exactly as PrefixQuery and WildcardQuery do. Not
      fixed here because the report was specifically about `*` and expanding the change
      silently would make both harder to review. The fix is the same one-line
      `patternTerm()` call already in the file.
