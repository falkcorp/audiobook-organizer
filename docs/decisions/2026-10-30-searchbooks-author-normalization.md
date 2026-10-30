<!-- file: docs/decisions/2026-10-30-searchbooks-author-normalization.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b6645e0-cca3-4bc7-a747-26a12488900c -->
<!-- last-edited: 2026-10-30 -->

# SearchBooks author matching: normalize the query like the stored side

Decided 2026-10-30 (autonomous window, owner reviews later). Source: TODO.md
section "SearchBooks compares a NORMALIZED author name against a raw lower-cased
query".

## What the TODO got wrong

The TODO says `util.NormalizeAuthor` strips punctuation and reorders
`Last, First`. It does not. At this commit it is `CollapseSpaces(ToLower(s))`: lower
case plus every unicode whitespace run (tab, newline, NBSP, U+2000..U+200A,
U+3000) collapsed to one ASCII space, ends trimmed. `internal/personname` is not
involved. So the real mismatch is whitespace only, not punctuation or name order.
The predicate (`SubstringSearchRank`) already ran `searchFold` over both sides,
which collapses `_` and runs of ASCII spaces, so only non-ASCII whitespace and
edge spaces in the QUERY could miss. The most plausible real case is a query
pasted with an NBSP.

## Decision

In `internal/database/search_rank.go`, the author branch now calls
`authorMatchesQuery`: it matches if the stored name contains the query as typed
(lower-cased, the fallback) OR the query run through `util.NormalizeAuthor` then
`searchFold`. Matches only widen; nothing that matched before stops matching.
A query that normalizes to empty (whitespace only) matches no author.

Rejected: lower-casing the stored side only (would diverge from the name-index
key function and lose the collapse the stored side relies on); replacing the
as-typed match with the normalized one (drops the "query typed in stored form"
guarantee for no gain).

## Callers and semantic changes

All substring paths share `SubstringSearchRank`, so one edit covers the memdb
scan, the Pebble disk scan, `SearchBooksFiltered`/`SearchBookIDsFiltered`, and
`RankSubstringMatches` (scoped fallback). `SearchBooks` callers:

- `internal/audiobooks/service_query.go` (three substring fallbacks when the
  search index is nil/rebuilding or DSL parse/translate fails)
- `internal/server/handlers/itunes.go` ListBooks search (overfetch window)
- ABS handlers via `SearchBooksFiltered` (browse, search cache)

Behavioural change for every caller: author-only matches for queries containing
non-ASCII whitespace now appear, and a trailing/leading space on an author query
is now trimmed (`"wayne "` finds `bruce wayne`; before it did not). Title and
narrator matching are unchanged, so a trailing space still narrows a title match.
The result-cache key (`SearchQueryKey`) is unchanged; distinct keys may share
results, which is safe.

## Measurement

The "how many authors normalize differently" checkbox needs the production
database. The unit test (`TestSubstringSearchRank_AuthorQueryNormalizedLikeStoredSide`)
holds a table of Batman-named cases with a `legacyHit` column recording which
rows missed before the fix. Follow-up: count authors whose
`NormalizeAuthor(name) != ToLower(name)` on prod (a read-only scan) to size how
many names carry non-ASCII or doubled whitespace.
