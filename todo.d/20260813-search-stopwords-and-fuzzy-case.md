### Search follow-ups from the wildcard/phrase fix (2026-08-13)

- [ ] **Phrases containing an English stopword still over-match.** `"All Jobs"` returns
      three books because `all` is dropped by the analyser before matching, reducing the
      phrase to the single term `jobs`. The phrase machinery itself is correct —
      `"Side Jobs"` and `"Jobs on the Side"` now resolve to exactly one book each, and
      no longer match each other. Fixing the stopword case means indexing the text
      fields with an analyser that keeps stopwords, which changes the index mapping and
      requires a full re-index of the library. That re-index is now cheap and proven:
      the coverage reconciler drained all 67,824 books in ~25 minutes with zero
      failures. `TestQuotedPhraseWithLeadingStopword` is a characterization test that
      will FAIL when this is fixed — that failure is the signal to delete it and fold
      the case into `TestQuotedPhraseIsAPhrase`.
- [ ] **Fuzzy queries (`~`) have the same case-sensitivity defect the wildcard fix just
      addressed.** `bleve_translator.go` builds `NewFuzzyQuery` from the raw term, and
      FuzzyQuery bypasses the analyser exactly as PrefixQuery and WildcardQuery do. Not
      fixed here because the report was specifically about `*` and expanding the change
      silently would make both harder to review. The fix is the same one-line
      `patternTerm()` call already in the file.
