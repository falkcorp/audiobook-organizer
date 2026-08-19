### Changed

#### The PebbleStore split plan's "shared mutable core" argument was measured, and half of it was wrong

`docs/plans/2026-08-19-split-the-pebblestore-surface.md` argued the struct cannot
be partitioned by domain because its fields — one `*pebble.DB`, the memdb layer,
**five mutexes**, a generation counter and a warmup lifecycle — are shared across
every domain. That section was written from reading the struct declaration rather
than measuring usage.

Measured across the 48 non-test files declaring `*PebbleStore` methods, resolving
each file's actual receiver name: `db` is used by 45/48 and the memdb accessor by
10/48, but **every mutex is used by exactly one domain file** — `counterMu` by
authors, `opsMu`/`opsLogSeq` by ops-v2, `reviewMu` by review, and so on. The
mutexes were offered as proof the state cannot be partitioned; they are the part
that partitions most cleanly.

The conclusion survives its broken premise: `db` and the memdb layer really are
cross-cutting, so a split still yields facades over a shared core. What changes is
that the core is ~6 fields rather than 18, and the split is therefore a
cost/benefit decision rather than a design impossibility — a decision that belongs
to a human, since it is 558 methods across 48 files against production.

Recorded with its own limits: the counts are file-level, not method-level, and two
earlier passes at these figures were wrong (18/48 and 61 files) before the receiver
name was resolved per file.
