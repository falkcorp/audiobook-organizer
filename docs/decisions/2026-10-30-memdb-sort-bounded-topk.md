<!-- file: docs/decisions/2026-10-30-memdb-sort-bounded-topk.md -->
<!-- version: 1.0.0 -->
<!-- guid: fcbdc00c-ffe7-4b21-ae42-6b05fd326c16 -->
<!-- last-edited: 2026-10-30 -->

# memdb unindexed sort: bounded top-k over pointers

## Problem

TODO entry ABS-CREATED-AT-SORT-INDEX. `MemStore.GetBookSummaries` served any sort
key without an enabled memdb index (everything except `title`, and `year` while
it is the default index) by copying every matching `Book` (976 B each) into a
slice, hydrating names, stable-sorting the whole slice and cutting the page. The
ABS Recently Added shelf and the Library tab's `sort=addedAt&desc=1` hit that
path on every home-page load.

## Decision log

- **Enabling the `created_at` index: not touched.** It trades about 19 MB of
  memory and slower inserts for read speed, which the TODO entry reserves for the
  owner. `enabled_sort_indexes` and its default are unchanged. The entry stays
  open for that decision.
- **Chosen: bounded top-(offset+limit) heap over `*Book` pointers**
  (`memdb_summaries_topk.go`, `container/heap`). memdb objects are read-only, so
  the walk keeps pointers, evicts against the heap root, and `bookToSummary`
  projects only the final page. Rejected: collecting pointers and sorting all of
  them (still O(n log n) over every match, and keeps n pointers where the heap
  keeps offset+limit); a quickselect (more code for no measured gain at k=40).
- **One ordering authority.** `SortBooks` and the heap both rank with the new
  `bookSortLess(field, ascending)` in `book_sort.go`: comparator, then book ID,
  the whole result negated for descending. The ID tiebreak makes the order total
  over distinct IDs, so a stable sort of everything and a top-k selection return
  the same rows in the same order, ties included. Rejected: a second copy of the
  comparison logic in the heap (the drift problem `book_sort.go` documents).
- **Dispatch is explicit.** `sortHydratesRows` names the sort keys for which
  `hydrateSortNames` writes onto rows (`author`, `series`). Those keep the
  copying path, because writing through a memdb pointer would corrupt rows shared
  with concurrent readers. Every other key takes the heap. A test
  (`TestSortHydratesRowsMatchesHydrateSortNames`) fails if the two lists drift.
  Rejected: narrowing the copy to hydrated sorts by cloning only the final page
  (the author/series order depends on names resolved for every row, so the whole
  set must be hydrated).
- **Callers.** `GetBookSummaries` returns `[]BookSummary`, built fresh per row; its
  only callers are the two PebbleStore wrappers in `pebble_store.go`. Pointer fields
  inside a summary alias the memdb row, as they did when it was built from a
  shallow copy and as the streamed index path already does. No caller sees a
  `Book`, so nothing mutates a returned row and no extra copy is needed.
- **Out of scope.** The Pebble fallback twin (`hydrateSortNamesPebble`) reads rows
  from disk, not shared memdb objects, and is untouched.

## Evidence

Property test `TestMemdbSummariesTopKMatchesCopyPath`: 3000 random books drawn
from small value pools (heavy ties, about 1 in 6 values missing), every key in
`SortableBookFields()` (23), ascending and descending, 13 offset/limit windows,
compared against an oracle that re-implements the old copy-sort-cut path. A
mutation check (dropping the ID tiebreak from `bookSortLess`) fails
`TestBookTopKMatchesSortBooks` at the first tied row.

Benchmark, 60k-row synthetic store, no `created_at` index, `created_at` descending,
limit 40, ~15k visible primary rows (`BenchmarkSortRecentlyAddedDesc`,
`-benchmem -benchtime 2x`, GOMAXPROCS=2 on a busy CI Mac):

| | time/op | B/op | allocs/op |
|---|---|---|---|
| before (origin/main) | 57.4 ms | 78,548,664 | 39 |
| after | 3.2 ms | 20,528 | 19 |

Bytes drop about 3,800x. The allocation count was already low before (the copy
slice grows by doubling), so the win is in bytes and GC pressure, not count.
Control, `BenchmarkSortAuthorAscCopyPath` (unchanged path): 82,784,544 B/op and
255,954 allocs/op both before and after.

The remaining cost, 3.2 ms, is the walk plus one comparison per visible row; an
index would turn that into a bounded reverse walk (~7 us per the TODO entry).
