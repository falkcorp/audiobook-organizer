## Search: "keyword" fields are not exact-match at all

`bookIndexMapping`'s `keyword()` helper (`internal/search/bleve_index.go`) sets
`f.Analyzer = standard.Name`. Bleve's *standard* analyser is not a keyword
analyser — it tokenizes on unicode boundaries and **carries a stopword filter**.
So every field intended as exact-match is being tokenized and stopword-stripped:

- `genre`, `language`, `library_state`, `format`
- `tags` (array)
- `isbn10`, `isbn13`, `asin`
- `_type` — which is the document-type discriminator

Consequences to measure before fixing:

- A genre like `"Science Fiction"` is indexed as two terms, so a filter for it
  can match `"Fiction"` alone.
- Identifier fields (`isbn*`, `asin`) are case-folded and tokenized rather than
  stored verbatim; whether that breaks lookups depends on the query path, which
  has not been checked.
- `_type` is used for document routing. Worth confirming it still resolves
  correctly before changing anything.

The fix is `keyword.Name` (`analysis/analyzer/keyword`), which emits the input as
a single unanalyzed term.

**This requires a full re-index**, same as the stopword change — bump
`bookMappingVersion` and the existing recreate path handles it.

Deliberately NOT bundled with the stopword fix (2026-08-13): moving a second
axis of the mapping in the same rebuild would have made the mutation test for
the phrase behaviour ambiguous, and these fields need their own before/after
measurement on production rather than being carried along silently.
