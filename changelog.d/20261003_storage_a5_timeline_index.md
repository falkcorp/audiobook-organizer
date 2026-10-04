### Changed

#### Operations timeline reads open/done indexes instead of every operation row

`ListOperationsV2Since` (the Activity timeline and `/operations/timeline`)
decoded every `opv2:op:` row on every call, 5.46-5.95 s per call in
production and growing by about 1,780 operations a day. It now reads two
small empty-valued indexes, `opv2:open:<op>` and
`opv2:done:<completed_nanos>:<op>`, which every operation-row writer stages
in the row's own batch (enforced by a module-wide CI test). The index is
untrusted at every boot: the timeline uses the full scan until that boot's
background reconcile (after memdb warmup) has checked and repaired it,
so a rollback to an older binary and a roll-forward again need no
operator step. The `opsv2_timeline_index_trusted` gauge shows
which path is in use. Results are identical to the scan for every
window and limit (property test over 200 random histories plus
window-boundary checks); the changes are a deterministic id-descending
tie-break where `started_at` and `queued_at` are both equal, and rows with
no id (hollow shells) are no longer listed by either path.
Benchmark on 50,000 operations (2,000 in the last 24 h), Apple M1 Max:
24 h window 255 ms (scan) to 10.8 ms (index); 90 d window 266 ms (scan)
and 271 ms (index), unchanged because every row in the window still has to
be read until operation retention exists.

#### Boot reconcile cost and write guards

The boot reconcile reads one snapshot: one walk of the operation rows plus
one key-only walk of each index family, with no per-row lookups. Measured
(in-memory, 50k / 200k operations), steady boot: 0.49 s / 1.96 s before,
0.12 s / 0.51 s after; first boot: 1.01 s / 5.42 s before, 1.11 s / 4.29 s
after. The cost grows with the operation count until operation retention
lands. `SetRaw`, `DeleteRaw` and `DeleteRawBatch` now refuse `opv2:op:`,
`opv2:open:` and `opv2:done:` keys, and a module-wide CI test allowlists
every function that can touch an operation row key, with role and
key-flow checks.

#### Operation log tail read

`GetOpLogsV2(op, limit)` now walks back from the last log key and stops
after `limit` rows, instead of decoding every log row of the operation (one
`library.scan` has about 300,000) and keeping the last `limit`.
