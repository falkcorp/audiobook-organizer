### Fixed

- **Search: quoted phrases containing common words are now exact.** `"All Jobs"` returned
  300 rows on production — a set byte-identical to the unquoted query, topped by *Side
  Jobs* and *The Icarus Job*, neither of which contains the phrase. The free-text fields
  were indexed with Bleve's stock English analyser, whose stopword filter deletes tokens
  **without renumbering the positions of the survivors**, and `MatchPhraseQuery`
  reconstructs the phrase from those positions. So `"All Jobs"` analysed to the single
  token `jobs` at position 2 and became a one-term phrase — that is, a plain term query
  with no adjacency constraint at all. Interior stopwords degraded differently but from
  the same cause: `"Lord of the Rings"` became a four-slot phrase with slots 2 and 3
  empty, which Bleve treats as wildcards, matching *Lord of All Rings* just as happily.

  The text fields now use a stopword-preserving analyser (unicode tokenizer, possessive
  stripper, lowercase, Porter stemmer). Ordinary unquoted search is deliberately
  unchanged: stopwords are still dropped from unquoted conjunctions, so
  `shards of oblivion` keeps working exactly as before.

- **Search: the index is rebuilt automatically when its mapping changes.** Bleve stores
  the index mapping inside the index and reuses the stored copy on open, so an analyser
  change would otherwise have no effect on an existing index — silently, and
  indefinitely. The index now records the mapping version that built it and is recreated
  when that no longer matches.

  On recreate the server skips the bulk backfill and seeds the durable dirty set instead.
  That distinction matters: the bulk backfill is not resumable and stops on shutdown,
  which is what left the index missing 24.7% of the library earlier the same day. The
  dirty-set marks are written to disk, so an interrupted rebuild resumes rather than
  leaving a permanent gap.

  **Operational note:** the first start after this change rebuilds the whole search
  index. Search results are incomplete until the reconciler drains — roughly 25 minutes
  for a 68,000-book library. Reverting this change triggers a second rebuild for the same
  reason.
