## The review lane still downloads the entire candidate set (2026-09-09)

`web/src/components/review/lanes/useMetadataLane.ts:543` calls
`api.getCachedReviewResults(0, 0)`. The first argument is `limit`, and `0` means
"return all rows" — the same convention that made `Resume Review` pull 40,485
rows before #3156. So the review lane fetches the whole matching set on open.

This is the *other* endpoint (`GET /audiobooks/metadata/cache/review-results`),
not the one #3154/#3156 fixed, and it is more expensive per row than the one that
was fixed: `ListCachedCandidates` returns six scalar fields per row, whereas the
review-results handler fans `GetCachedCandidates` out over every prepared row
through an errgroup before it can answer. Prod had 37,852 pending rows on
2026-09-09 and the count was climbing ~100 per minute while `batch-apply-cached`
ran.

Note the circularity to break, in `internal/server/handlers/metadata_cache.go`
around line 373: the comment justifies doing that fan-out over all rows rather
than one page with "both callers pass limit=0, so the page has always BEEN every
row." That is *true today* — it is true precisely because the only caller asks
for everything. It is a description of the current caller, not an argument that
the full fan-out is cheap, and it should not be read as one when this is picked
up.

**Decide before implementing** (this is why it is filed rather than done):

- Does the review lane actually need every row up front, or does it need a page
  plus `total_count`? It has client-side filtering/grouping across lanes, so the
  answer is not obviously "page it" — check what `useMetadataLane` does with the
  full array before assuming.
- If it does need everything, the fix is not paging but making the whole-set path
  cheaper (or streaming it), and the handler comment above should say so
  explicitly instead of resting on the caller's current argument.

Measured cost is unknown from outside: prod is running a build older than #3154,
so `limit` is inert there and the two shapes cannot be compared by curl against
production. Comparing them needs a deploy, or a local run against a realistic
dataset.
