### Fixed

#### Activity log writes now batch their fsyncs, not just their rows

`ActivityBatcher` and `Writer.drain` both exist to amortize activity writes —
collect entries for up to 15 seconds (60 second cap), flush early at 500 items,
and hand the whole flush to the store at once. They amortized **rows**. They did
not amortize **fsyncs**.

`Writer.writeBatch` received the flush and then looped, calling
`store.Record(e)` once per entry, and `PebbleActivityStore.Record` commits every
entry with `batch.Commit(pebble.Sync)`. So a "batch" of 100 entries was 100
separate durable commits — 100 fsyncs. There was no batched write path in the
store at all.

Measured on this repo (`BenchmarkActivityRecordPerEntry` /
`BenchmarkActivityRecordBatch`, 5,000 rows per iteration, `-count=5`, medians)
at identical durability — `pebble.Sync` either way:

| write path | rows/sec (median) | range across 5 runs |
| --- | --- | --- |
| one `Record` per entry (what shipped) | 101 | 76 – 116 |
| batched, 500 entries per commit | 29,530 | 27,440 – 30,336 |

The per-entry samples rise monotonically across the five repetitions (76, 87,
101, 107, 116) — the slow path warms up, so its median is a mid-range estimate
rather than a stable figure; the batched samples are flat. The gap is two orders
of magnitude either way, so the warm-up does not change the conclusion, but the
honest number for the old path is a range.

~290x, with no migration and no weakening of the durability guarantee. The
multiplier tracks how many entries share a commit: a `drain` flush of 100 goes
from 100 fsyncs to 1, and a full 500-item early flush from 500 to 1. The fsync,
not the row, was the unit of cost.

**What changed**

- `PebbleActivityStore.RecordBatch` stages every entry — the primary row and its
  `act:op:` / `act:bk:` index entries — into one `pebble.Batch` and commits once
  with `pebble.Sync`.
- `Record` and `RecordBatch` now share one `prepareEntry` helper that normalizes
  the entry, marshals it, and derives its keys. Neither path builds a key of its
  own any more. This matters because Pebble's delete of a missing key succeeds
  *silently*: an index key written in a slightly different format would be
  undeletable forever and nothing would report an error — which is exactly the
  defect that left ~0.783 GiB of orphaned `act:op:` rows on production. The
  index keys come from `pactIndexKeysFor`, the same derivation `pactDeleteEntry`
  uses to remove them, so "what this writes is what `Prune` deletes" is now true
  by construction.
- `Writer.writeBatch` uses the batched path when the store offers it, via a
  narrow `batchRecorder` interface and a runtime type assertion (the
  `backup.Checkpointable` shape) rather than a new method on
  `database.ActivityStorer`. A store without the capability keeps the per-entry
  loop and is still correct, just slower. Which path is live is logged at
  startup, so a missed assertion is never silent. `Writer.Flush` had the same
  one-fsync-per-entry shape and now goes through `writeBatch` too.
- Commits are capped at 500 entries each, and a larger flush is split across
  several commits rather than staged into one unbounded batch — this service has
  already OOMed once on an unbounded activity path.

**Error semantics** are deliberate and documented in the code. An entry whose
JSON will not marshal is the only failure attributable to a single row: it is
dropped, the rest of the flush still commits, and the returned count and error
name the loss. A commit failure is not attributable to any entry and loses the
whole commit; it deliberately does *not* retry per-entry, because the realistic
causes (a full or failing disk) would fail the retries too and turn one logged
loss into up to 500 more fsyncs against a disk that just failed one. Either way
the loss is reported — to stdout rather than through `slog`, because this writer
*is* what the log system tees into, so logging a failed flush would enqueue an
entry whose own flush fails and logs again.
