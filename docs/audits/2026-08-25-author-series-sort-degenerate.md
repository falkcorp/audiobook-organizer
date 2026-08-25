<!-- file: docs/audits/2026-08-25-author-series-sort-degenerate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d3f6b28-4c17-4e85-a0b9-7f2e5c1d8a64 -->
<!-- last-edited: 2026-08-25 -->

# Sorting by author or series has never ordered anything

**Status:** measured 2026-08-25, fix in progress.
**Severity:** user-facing sort silently returns an arbitrary order; the
supporting index spends memory to store a constant.

## What was measured

A probe seeded a `MemStore` with three books carrying distinct `AuthorID`s and
three matching `Author` rows, in the production shape (`AuthorID` set,
`Book.Author` pointer nil), then asked for `SortBy: "author"`:

```
fixture b1 AuthorID=3 Author=<nil> sortValue=""
fixture b2 AuthorID=1 Author=<nil> sortValue=""
fixture b3 AuthorID=2 Author=<nil> sortValue=""
sort_by=author ascending -> [b1 b2 b3]
```

Expected by author name (Aaron/Mabel/Zoe) was `[b2 b3 b1]`. The result is plain
ID order. Every book produced the same empty sort key, so the comparator ranked
them all equal and the primary-key tiebreak decided the page.

## Why

`bookAuthorSortValue` (and `bookSeriesSortValue`) read a pointer that is never
populated on the path that builds the index:

```go
func bookAuthorSortValue(b *Book) string {
    if b.Author != nil { return b.Author.Name }
    return ""
}
```

Three independent reasons that pointer is nil:

1. **It is never persisted.** `Book.Author` is declared
   `Author *Author \`json:"author,omitempty" db:"-"\`` — `db:"-"`. There is no
   author-name column on `Book`; the only stored linkage is `AuthorID *int`.
2. **memdb explicitly clears it.** Both insert paths go through
   `stripBookForMemdb`, which does `cp.Author = nil` / `cp.Series = nil`,
   documented as "hydrated separately via authorsMap/seriesMap in the service
   layer".
3. **The service sorts before that hydration.** `applySorting` runs at the end
   of `GetAudiobooks`; `EnrichAudiobooksWithNamesAndFiles` is a separate
   function the handler calls afterwards.

So the value is empty on the index path, on the Pebble path, and at the point
the service sorts.

## Why no test caught it

`TestSortIndexOrderMatchesComparator` enumerates
`narrator, duration, file_size, bitrate, year, created_at, updated_at`.
**`author` and `series` are not in the list**, and its fixture never sets
`Author:` on any book.

The deeper reason a test would not have caught it even if added carelessly:
`seedMemStore` inserts books with `txn.Insert(memTableBooks, &b)` **directly**,
bypassing `stripBookForMemdb`. A fixture that set `Author:` would therefore be
indexed with a name that production strips — the test would pass on a shape
production never produces.

Note also that the cross-path conformance test cannot see this either: it
compares the index against `SortBooks`, and both read the same nil pointer, so
they agree — on garbage. Two implementations agreeing is not evidence when both
consult the same absent input.

## Structural consequence

A memdb indexer receives only the `*Book`. It cannot reach the authors table.
So an index on author *name* is not implementable under the current schema
without denormalising the name onto `Book`. The entries

```go
"author": memIdxSortAuthor,
"series": memIdxSortSeries,
```

in `sortIndexForField` are therefore claims the schema cannot honour, and
`CanPushDownSort` repeats them to the query planner. `author` was added to the
enabled set on 2026-08-24, which allocated a real index (the schema comment
budgets ~146 MB per key at prod scale) whose every entry is the same empty
string.

## Related

Same family as the marker/ordering defect fixed in #2892: a declared capability
outrunning the implementation, with no error surface — the page comes back 200
OK, the right rows, in an order the caller did not ask for.
