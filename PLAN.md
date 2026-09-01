<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4c9e2b71-8a35-4d60-9f12-6e0a7b3d5c84 -->
<!-- last-edited: 2026-09-01 -->

# Plan — push the regroup review search down to the server

Branch `feat/review-items-server-search`, worktree `.worktrees/review-search`.

Written while the user is AFK, on "fix everything you can". No approval gate was
available; the plan is recorded here so the reasoning is reviewable after the fact.

## The defect, stated precisely

`GET /api/v1/review/items` supports exactly four filters:

```go
type ReviewFilter struct {
    Status string
    Kind   string
    Limit  int
    Offset int
}
```

The regroup lane fetches `limit=REGROUP_FETCH_LIMIT` (500) and then filters
**client-side** with `searchTextFor`. So on a queue larger than 500 the search box
answers a question nobody asked: not "which holds match?" but "which of the 500
holds that happened to load match?".

The lane is already honest about this — it derives a `truncated` flag and warns —
but honesty about a wrong answer is not a right answer. This is the same class as
the `sort` that was silently unordered: a 200, the right shape, arbitrary content.

## Why this is nearly free on the Go side

`ListReviewItems` already loads **every** matching item into memory, applies `Kind`
in Go, sorts, and only then slices the page:

```go
all, err = p.listReviewItemsByStatusIndex(f.Status)   // ALL of them
if f.Kind != "" { /* filter in Go */ }
sort.Slice(all, ...)
return all[start:end], total, nil
```

A search term is one more pass in exactly the place `Kind` already occupies, before
the sort and before the page is cut. `total` then means "matches", which is what the
UI has always claimed it means.

## The parity problem, and the decision taken

The client builds its haystack from `summary, folder_ref, dedup_key, id,
labelForKind(kind), kind, payload.folder, payload.survivorTitle,
payload.derived_title, payload.title, payload.files[]`.

The server has all of those EXCEPT `labelForKind`, which is a frontend display map
(`REVIEW_KIND_LABELS`) with no Go counterpart:

| kind | label |
|---|---|
| `regroup.multidisc` | Multi-disc groups |
| `regroup.version-group` | Abridged / Unabridged editions |
| `regroup.anthology` | Anthologies / collections |
| `regroup.ambiguous` | Ambiguous folders |

Three of the four labels share a word with their kind, so a free-text search for
them keeps working. `regroup.version-group` does not: today "abridged" matches it,
after this change it will not.

**Decision: do not port the label map to Go.** Copying a display string table into
the backend to preserve one substring match creates precisely the two-copies-that-
diverge defect this codebase has spent five PRs deleting. The kind dropdown already
selects that bucket directly, and it is a *better* control for it than free text.
The loss is stated in the PR rather than hidden.

**Decision: the server becomes the single source of truth for `q`.** The client-side
search predicate is DELETED, not kept as a second narrowing pass. Two predicates
over one query is the "one string, two meanings" failure: the row count and the
`total` beside it would disagree and neither would be wrong.

## Files to change

1. `internal/database/review_store.go` — `ReviewFilter.Search`; one filter pass in
   `ListReviewItems` beside the `Kind` pass; a `reviewSearchHaystack` helper.
2. `internal/database/review_store_test.go` — cover match, non-match, case
   folding, combined with Kind, and that `total` reflects the search.
3. `internal/server/handlers/review/handler.go` — read `q`; document it.
4. `internal/server/handlers/review/*_test.go` — the param reaches the filter.
5. `web/src/services/api.ts` — `search` on the filter type → `q` param.
6. `web/src/components/review/lanes/useRegroupLane.ts` — send the debounced query;
   delete the client-side predicate; rewrite the counts doc block, which currently
   documents `visible <= loaded` as a client narrowing.
7. `web/src/components/review/lanes/useRegroupLane.test.ts` — the query is sent,
   debounced, and a stale response cannot overwrite a newer one (the request-token
   guard now has a third writer to order).
8. `changelog.d/` fragment — headerless.

## Ordered steps

1. Go store + tests. `go build ./... && go test ./internal/database/ -run Review`.
2. Handler + tests.
3. Verify against prod READ-ONLY: `GET /review/items?q=...` returns a `total` that
   changes with the term. No writes, no scan.
4. Frontend: api.ts, then the lane, then its tests.
5. `npx tsc --noEmit`, `npx vitest run`, `npm run lint`.
6. Mutation-check the new Go filter (invert the match, drop the case fold) and the
   new lane tests (drop the debounce, drop the token bump) at final HEAD.

## Test strategy

- The Go filter gets a table test whose fixture contains a row that matches ONLY in
  the payload and a row that matches ONLY in the summary — a fixture where every row
  matches everywhere cannot observe a dropped field.
- `total` is asserted separately from `len(items)`, since the whole point is that
  the count now describes matches rather than the page.
- Mutation, not coverage: each new assertion is checked by breaking what it covers.

## Rollback

Additive on the wire — an absent `q` is the current behaviour exactly. Revert the
merge; no migration, no config, no flag.

## Explicitly NOT in scope

- Porting `REVIEW_KIND_LABELS` to Go (see above).
- The metadata lane. It fetches its whole set with `getCachedReviewResults(0, 0)`
  and filters in memory, so its filters are already whole-set correct; measured at
  23 ms for the 11-filter chain over 2,000 rows. Nothing to push down.
- The dupes lane, which already has four server-side filters.
- Sorting. The endpoint sorts newest-first only, and the lane's sort control is
  client-side over the page — the same defect shape as search, but a separate
  change with its own index question.
