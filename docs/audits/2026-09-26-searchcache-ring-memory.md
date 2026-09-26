<!-- file: docs/audits/2026-09-26-searchcache-ring-memory.md -->
<!-- version: 1.1.0 -->
<!-- guid: 51b02350-8472-4bea-b151-3e430c2c0b06 -->
<!-- last-edited: 2026-09-26 -->

# Search-cache change ring: what a bigger ring costs (2026-09-26)

The owner asked what raising the search-result cache's change ring would cost in
memory. This document measures that cost and describes what a bigger ring changes
in behaviour. **The size was not changed.**

## Current value

`internal/searchcache/changelog.go`: `DefaultRingSize = 65536` on main (68ce6cf3c).
It was raised from 8,192 by the owner decision of 2026-09-25. The server builds
exactly one `ChangeLog` per process.

## Memory, measured

Measured with `BenchmarkChangeLogRingMemory` in
`internal/searchcache/changelog_memory_test.go`. The benchmark takes the live heap
after two `runtime.GC()` calls, before and after each step. Platform: darwin/arm64,
M1 Max, Go 1.27.

```
go test ./internal/searchcache -run '^$' -bench ChangeLogRingMemory -benchtime 3x
```

| Ring size | Ring slice (always paid) | ID strings kept alive by a full ring | Full ring total | Per entry |
|---|---|---|---|---|
| 8,192 | 196,688 B (192 KiB) | 262,144 B (256 KiB) | 448 KiB | 24 B slice + 32 B ID = 56 B |
| **65,536 (current)** | 1,572,928 B (1.5 MiB) | 2,097,152 B (2.0 MiB) | **3.5 MiB** | 56 B |
| 262,144 | 6,291,632 B (6.0 MiB) | 8,388,944 B (8.0 MiB) | 14 MiB | 56 B |

- **Per record:** `unsafe.Sizeof(changeRecord{})` is 24 bytes: a `uint64` generation
  plus a 16-byte string header. `NewChangeLog` allocates the whole slice up front,
  so this part is paid at startup even if nothing is ever recorded.
- **ID strings:** a 26-byte ULID book ID lands in the 32-byte size class. The ring
  pays for an ID only when it is the last holder of that string. An ID that memdb
  or a cached result list still holds is shared, so 32 B per entry is the upper
  bound, reached when every record names a book the process has not otherwise
  kept.
- **Summary:** memory is linear in the ring size at 56 B per entry. At 262,144 the
  total is 14 MiB, compared with 3.5 MiB today. Either figure is small next to the
  128 MiB `DefaultMaxBytes` result-cache cap.

## CPU and lock cost: this, not memory, is the real cost of a bigger ring

`ChangedSince` walks every live record while holding the `ChangeLog` mutex. It
does not stop early. `Record` takes the same mutex. Store writes call `Record`
synchronously on the writer's goroutine: `PebbleStore` defers
`notifyBooksChanged` in its book write paths (`memdb_sync.go`), which calls
`searchChangeObserver.BooksChanged`. Bleve commits call it too, through
`recordIndexCommit`. So a book write, or an index commit, waits for as long as
a walk takes.

Two benchmarks cover this:

- `BenchmarkChangedSinceFullRing`: the ring is full and the cache entry is only one
  generation old, so every record is skipped.
- `BenchmarkChangedSinceWholeRing`: the entry is as old as the oldest record, so
  every record is returned. This is the case during a big backfill.

| Ring size | Walk, entry 1 gen old | Walk, entry as old as the ring | Garbage per whole-ring walk |
|---|---|---|---|
| 8,192 | 7.6 µs | 0.69 ms | 1.36 MB (96 allocs) |
| **65,536 (current)** | 60 µs | **7.3 ms** | **12.5 MB** (556 allocs) |
| 262,144 | 253 µs | 29.4 ms | 50.5 MB (2,100 allocs) |

The whole-ring case is wasted work whenever the ring holds more than 2,048
distinct books. `ChangedSince` builds the full deduped set, and then `patch()`
throws it away because the set is larger than `MaxPatchChanged` (2,048). The work
grows with the ring, not with the cap. At 262,144 records, one stale lookup holds
the mutex for about 29 ms, blocks every book write for that time, and allocates
about 50 MB of garbage.

**Possible fix (not made, outside this task's scope):** give `ChangedSince` a
`limit` argument and have it return "too many" once it has seen
`MaxPatchChanged+1` distinct IDs. Every existing call site is in `cache.go`. The
fix would put a bound on both the lock hold and the garbage for any ring size,
and it would make a bigger ring much cheaper.

## What a bigger ring changes in behaviour

The ring is sized in **records**. The patch cap is sized in **distinct books**,
and `ChangedSince` dedupes. That gives two independent reasons for a cache entry
to be rebuilt rather than patched:

1. **Ring overflow:** since the entry was built, more records were written than
   the ring holds. `ChangedSince` then returns `ok=false`.
2. **Patch cap:** the entry has more than 2,048 distinct changed books.
3. **PatchLimit:** more than 64 of the changed books still match the query
   (`DefaultPatchLimit`). The cache checks this after re-evaluating the changed
   set, at every ring size.

A burst of D distinct books, each written R times, overflows a ring of size S
when D×R > S. It hits the cap when D > 2,048, whatever S is. So a deeper ring
avoids rebuilds only for bursts that **repeat the same books**:

- The ring decides the outcome only when R > S / 2,048. That means more than 4
  writes per book at 8,192, more than 32 at 65,536, and more than 128 at 262,144.
- A big backfill that touches more than 2,048 distinct books between two reads of
  one cache entry rebuilds at every ring size. A bigger ring does not help.
- A backfill that rewrites a smaller set of books many times does benefit: for
  example, a per-file pass over one author's catalogue, or a store write followed
  by its index commit, which can produce more than one record per book write. It
  goes stale (and rebuilds) less often.

`TestChangeLogRingPatchWindow` pins these cases:

- 1,000 books × 10 writes overflows 8,192 but patches at 65,536.
- 2,000 books × 40 writes overflows 65,536 but patches at 262,144.

The window an entry can be patched across is therefore the smallest of three
limits:

- the ring size divided by the writes per book;
- 2,048 distinct books;
- 64 divided by the fraction of changed books the query matches.

For a broad query, where nearly every changed book matches, PatchLimit binds at
about 64 distinct books. A deeper ring then buys nothing. The ring cases in
`TestChangeLogRingPatchWindow` only prove that `ChangedSince` can still answer.
They do not prove that the cache patches, because PatchLimit is applied after
`ChangedSince`.

**Effect of raising the ring:** fewer stale results and fewer rebuild events
during bursts that are heavy on repeats. There is no change for bursts of many
distinct books, because the cap forces a rebuild there.

## Knowing which limit binds in production

The new counter `audiobook_organizer_search_cache_patch_cap_rebuilds_total` (also
`Stats.PatchCapRebuilds`) counts rebuilds forced by the 2,048 cap, and only those.
Rebuilds forced by ring overflow or by `PatchLimit` are not counted.

The cache's total rebuild count (`Stats().Rebuilds`) is not exported anywhere
today: no metric, and no endpoint. Only tests read it. So the comparison below
needs a follow-up that exports it, for example as a sibling counter. Once both
are exported, compare them during a backfill:

- If most rebuilds are cap rebuilds, a deeper ring buys nothing.
- If cap rebuilds are few while rebuilds climb, the ring (or `PatchLimit`) is what
  binds.

## Recommendation

Keep 65,536 unless the counters show that ring overflow dominates. Before any
further raise, bound `ChangedSince` at `MaxPatchChanged+1`. Without that bound, a
4× ring means 4× longer write stalls on every stale lookup (about 29 ms at
262,144), and that costs more than the 10.5 MiB of extra memory.
