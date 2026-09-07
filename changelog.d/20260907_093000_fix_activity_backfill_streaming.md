### Fixed

#### Pebble→SQLite activity backfill now streams in bounded batches (OOM fix)

The one-time Pebble→SQLite activity backfill (`BackfillPebbleActivityToSQL`)
materialized a whole activity tier into memory via `scanTierKVs` before its first
insert. On prod (2026-09-07) the ~1.3 GiB `change` tier drove RSS to ~30 G and the
kernel OOM-killed the service into a ~14-minute restart loop; the migration was
disabled with `ACTIVITY_BACKEND=pebble` as a stop-gap. No data was lost — reads
never flipped to SQLite (the flip is parity-gated) and Pebble stayed intact.

Added `PebbleActivityStore.streamTierEntries`, a memory-bounded counterpart to
`scanTierKVs` that iterates a tier through a single Pebble snapshot and hands
entries to a callback in batches of at most `sqlBackfillBatch` (500), never
holding the whole tier. The backfill now copies each batch into SQLite and
re-presents that same in-memory batch for parity in one step, so at most 500 rows
are ever live. Per-batch re-presentation (rather than an independent second scan
of Pebble) is also correct under the live dual-write: writes land in Pebble before
SQLite, so an independent re-read could see a row in Pebble not yet in SQLite and
false-fail parity, whereas re-presenting the just-copied batch cannot. Content-key
dedup makes the streamed copy identical in outcome to the old whole-tier pass —
each distinct entry inserts once, repeats collapse — and the parity gate and
sentinel semantics are unchanged.
