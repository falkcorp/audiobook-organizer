<!-- file: docs/decisions/2026-10-30-search-or-over-per-user-terms.md -->
<!-- version: 1.0.1 -->
<!-- guid: 932e5775-5fc6-4cea-881e-77138eb9ab56 -->
<!-- last-edited: 2026-10-30 -->

# OR and grouped NOT over per-user terms

Task: TODO.md entry OR-WITH-PER-USER-ANDED (`title:Alpha OR read_status:finished` was
evaluated as an AND).

## Finding that contradicts the brief

On origin/main (fc3e9f5d3) the mix was no longer silently ANDed: `search.CheckPerUserBoolean`
already refused a per-user term under an OR with a sibling, or under a NOT over a group
(`ErrPerUserBoolean`: HTTP 400 on collection and playlist create/update and on the library
list, `members_unavailable` for a stored row). The entry's second option (refuse the mix with a
clear error) was therefore already shipped, and the entry was stale on that point. It was also
stale on the hourly refresh: `RefreshRelativeDynamic` skips per-viewer collections
(`isRelativeRefreshCandidate`), which are evaluated per viewer on every read. This change
implements the entry's first option (evaluate the mix) and is also the backend core of
TODO-PERUSER-BOOLEAN.

## Decision log

- Chosen: turn each per-user leaf into an exact index query over the viewer's own state rows
  (`search.MixedQuery`), and let Bleve evaluate the whole boolean tree. A leaf a row-less book
  fails (finished, in_progress, `>0`, ...) becomes a DocID query over the matching rows (match-none
  when there are none); a leaf a row-less book satisfies (unstarted, `<50`, ...) becomes match-all
  minus a DocID query over the failing rows. NOT over a group is the index's own negation.
  Scoring, paging and the exact total stay the index's.
- Rejected: a Go-side evaluator that fetches candidates from Bleve and combines per-leaf id
  sets (the entry's first wording). It loses relevance order, paging and the exact total, and a
  NOT or an OR cannot be answered from a candidate list at all (the candidates of an OR are the
  union, which is the whole library for a per-user side).
- Rejected: keeping the refusal. It is correct but leaves a collection or playlist query a person
  can reasonably write unusable.
- Chosen: also lift the grouped-NOT refusal. It has the same root cause (per-user terms were a
  flat AND list) and goes through the same code, so leaving it would mean two code paths for one
  defect.
- Chosen: only the shapes that need it take the new path (`perUserNeedsTree`). Plain conjunctions,
  a term negated on its own and one field's alternation keep the flat TranslateAt +
  PerUserFilter + MatchPerUserFilters path unchanged; a differential test compares the two routes.
- Chosen: `TranslateAt` returns `errPerUserNeedsRows` for a tree shape instead of a list that
  would be wrong. Callers that can take rows use `TranslateQuery`, which returns `Translation.Mixed`.
- Chosen: no viewer. Playlists and collections return no members (the existing leak guard).
  Library search judges the terms as the zero-value state (finished matches nothing, unstarted
  matches everything), also when `DisablePerUserSearchFilters` is set, with a warning log: there is
  no neutral value a per-user term inside an OR or under a NOT can be dropped to, which is what the
  flat path does for an AND.
- Chosen: a store that can list a viewer's rows (`UserBookStateLister`, the Pebble store) is read
  with one scan; a scan failure fails the search (the rows decide the whole expression, so a
  missing read changes which books match). A store that cannot list is read by point reads of every
  indexed id over a pool of `runtime.NumCPU()` workers; no production store takes that path.
- Chosen: `ErrPerUserBoolean` and `CheckPerUserBoolean` are deleted (cut over, no compatibility).
  The value checks (`CheckPerUserValues`) stay and apply under an OR too. `tag:` under an OR is
  still refused (separate rule, unchanged).
- Chosen: the two substring fallbacks (index nil or rebuilding) refuse a query that needs the tree
  (`search.CheckPerUserNeedsIndex`, a `*PerUserValueError`, so a 400 like every other refused
  per-user term). On main they refused it through the shape check; narrowing that check to values
  would have turned the 400 into a silent substring answer, so this restores the refusal where, and
  only where, it is still correct. `SearchPerUserProblem` (also run by the HTTP handler before any
  engine is chosen) does not refuse it, so the index path is not affected.
- Not changed: the fallbacks still never evaluate a flat per-user term (`read_status:finished`
  alone is not applied while the index is down). Pre-existing, not touched here.
- Checked: every non-test consumer of `TranslateQuery`/`TranslateAt` (the playlist evaluator,
  `searchWithBleve`, `validateCollectionQuery`, which only needs the error). The search result
  cache, the list cache and the tag-facet cache are gated by `SearchHasPerUserFilters`, which reads
  field names from the AST, so a mixed query (empty `PerUser` list) is still never cached; a
  two-viewer test through the real service with the cache wired pins it.
- Ordering note: relevance for a mixed query is the index's; DocID leaves score a constant, so a
  per-user-only OR side does not influence ranking beyond matching.
