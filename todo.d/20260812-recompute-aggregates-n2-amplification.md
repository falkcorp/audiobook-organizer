## 🔴 `RecomputeBookAggregates` is O(N²) on one write path and never runs on the other

Measured on production 2026-08-11, 06:30–10:46 (the window of a 4h15m library scan).

### The numbers

| metric | value |
|---|---|
| `RecomputeBookAggregates updated` log lines | **126,928** |
| distinct books touched | **5,595** |
| most recomputes for a single book | **1,189** |
| next three | 616, 416, 400 |
| file-record reads implied (Σ N(N+1)/2) | **5,430,858** |
| file-record reads if coalesced (1 per book) | 126,928 |
| **read amplification** | **42.8×** |
| book-row writes | 126,928 vs 5,595 → **22.7×** |

Book-size distribution of the touched set: 711 books with 1 file, 1,824 with 2–9,
2,901 with 10–99, 157 with 100–499, 2 with 500+.

### Why it is quadratic

`RecomputeBookAggregates` reads **every** file of a book (`GetBookFiles`) and rewrites
the book row on each call. Five write methods each trigger one via
`notifyBookFileChange`:

- `CreateBookFile`, `UpdateBookFile`, `DeleteBookFile`, `DeleteBookFilesForBook`,
  `DeleteBookFilesByIDs`

So inserting N files for one book one at a time costs 1+2+…+N file reads and N book-row
writes. For the 1,189-file book that is ~706,000 reads to insert 1,189 rows.

### The other half: the batch path never recomputes at all

`BatchUpsertBookFiles` does **not** call `notifyBookFileChange` anywhere. It refreshes
memdb and invalidates library stats, but the book's `Duration` and `FileSize` aggregates
are simply left stale after a batch write.

| path | recomputes per book |
|---|---|
| `CreateBookFile` / `UpsertBookFile` | **N** (O(N²) reads) |
| `BatchUpsertBookFiles` | **0** (stale aggregates) |

Two paths, opposite failure modes, neither correct. The right answer is **exactly one
recompute per affected book** on both.

### ⚠️ Attribution is NOT established — do not repeat this mistake

These 126,928 calls were initially attributed to the scan writing book files. **That is
wrong.** The scanner writes via `BatchUpsertBookFiles`
(`internal/scanner/scanner.go:1544`), which never triggers a recompute. The claim was
made from co-occurrence in a time window, and corrected on PR #2355.

`RecomputeBookAggregates updated` lines carry **no `opID` — 0 of 126,928** — so the log
cannot say who caused them. Co-occurring in the same window: 10,369 re-organizes, and
27,018 `book_file PID uniqueness: transferred to new row` (which is emitted from the
`CreateBookFile` mint path, and therefore *is* a per-file `notifyBookFileChange`
trigger). Neither is traced.

**First task is attribution, not the fix**: add the operation ID to the store's log line,
or instrument `notifyBookFileChange` with a caller tag. Fixing before knowing the
workload means the fix cannot be measured.

### Fix direction, once attribution is known

A coalescing scope on the store:

```go
flush := store.BeginAggregateBatch()   // notifyBookFileChange records book IDs
defer flush()                          // one RecomputeBookAggregates per touched book
```

- Depth-counted and mutex-guarded — the scanner runs worker pools, so concurrent
  `CreateBookFile` calls for different books must be safe.
- **Scope it per book, not per scan.** A scan-wide scope leaves aggregates stale for the
  whole 4h15m run, which is a correctness regression traded for speed.
- Apply the same coalescing inside `BatchUpsertBookFiles` — that closes the staleness gap
  in the same change.
