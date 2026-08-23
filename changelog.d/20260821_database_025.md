### Fixed

#### `WipeAllActivity` is now cancellable from its live request path

`WipeAllActivity` — the full activity-log wipe reachable from `handleWipe`, a
real HTTP handler — ran to completion uncancellably even after the client
disconnected. It walked every tier's rows into memory, then deleted them in
500-row batches, with no way for an abandoned request to stop it, the same
defect class already fixed for the unbounded `Query`/`GetDistinctSources`
scans (a prior production outage: 30 abandoned requests held 30.8 GB with zero
connected clients).

`WipeAllActivity` now takes a `context.Context` and checks it before each
tier's scan and before each 500-row delete batch, so an abandoned wipe stops
promptly instead of draining the whole log server-side. The change cascades
through every `ActivityStorer` implementation: `PebbleActivityStore` (both
new checkpoints, plus its already-context-aware `scanTierKVs`),
`NutsActivityStore` (a coarser per-tier check only — it is retired/unwired
and its scan has no per-row ctx plumbing to extend), `DualWriteActivityStore`
(forwards ctx to both backends unchanged), and `InstrumentedActivityStorer`
(forwards ctx into its span and — unlike its sibling methods — now returns
the real partial count on error instead of a hardcoded `0`, since silently
reporting "0 deleted" for a wipe that actually deleted rows would misinform
every caller of the traced wrapper).

On cancellation, `WipeAllActivity` returns the count of rows **actually
deleted so far** — never a fabricated full or zero count — alongside
`ctx.Err()`. Rows not yet reached are left untouched; there is no
partial-tier bookkeeping to resume, so a plain retry finishes the job by
rescanning and deleting whatever remains. `ctx` plumbing does not change
what gets deleted, only whether an abandoned wipe stops early.

Covered by a new regression test that proves cancellation stops the delete
loop mid-tier (500 of 750 seeded rows deleted, not 0 and not all 750), rather
than merely asserting the returned error is non-nil.
