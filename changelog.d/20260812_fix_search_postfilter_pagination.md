### Fixed

#### Search past page 1 returned nothing, and the result count was the page length

Searching with any filter active returned rows on page 1 and an empty page for every
page after it. The library UI always sends `is_primary_version=true`, so this was every
user-facing search, not an edge case.

Bleve was asked for one page of `limit` rows, the `is_primary_version` /
`exclude_quarantined` / tag post-filters then deleted most of that already-cut page with
nothing left to refill it, and `paginateFilteredBooks` re-sliced the remainder by the
ORIGINAL offset — out of range for a `<=limit` slice, so it returned zero rows. Measured
on production before the fix, `search=honour&is_primary_version=true&limit=5` gave
1 / 0 / 0 / 0 rows at offsets 0 / 5 / 10 / 20, while the identical query with no filter
paged correctly at 5 / 5 / 5.

The search branch now over-fetches a window of matches when post-filters will run,
filters the whole set, and paginates last. A guard for the same defect already existed
one branch away (`didPushdown`), which is where the shape of the fix came from.

Separately, the reported `count` was `len(page)` rather than the number of matches, so it
tracked the requested limit: the same query reported `count=5` at `limit=5`, `count=3` at
`limit=3` and `count=21` at `limit=250`. A caller could never learn how many matches
existed, so "page 2 of N" was fiction. `GetAudiobooksWithTotal` now returns a real match
total (Bleve's own hit count when it paginates, or the filtered-set size when post-filters
run), and `-1` when no true total is available so the caller keeps its previous behaviour.

Not claimed: the over-fetch window is 10,000 matches. Beyond that the count is an
explicitly-logged lower bound rather than exact, and the warning names the query.
