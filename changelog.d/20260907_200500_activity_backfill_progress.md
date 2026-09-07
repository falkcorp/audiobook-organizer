### Fixed

- **The Pebble→SQLite activity backfill was unreadable while it ran.** It logged
  `processing tier` once per tier and then nothing until the final summary, so a run that
  had been copying the `change` tier for three hours looked exactly like a wedged one —
  on production the only way to tell them apart was to diff row counts out of the SQLite
  file by hand. Each tier now emits a heartbeat (`scanned`, `copied`, `rows_per_sec`,
  `elapsed`) and a terminal `tier complete` line with its own totals. The heartbeat is
  triggered on wall-clock rather than every-N-batches on purpose: a batch of 500 iTunes
  ITL rows carries ~9.6 MB of `details` each while a batch of ordinary events carries a
  few hundred bytes, so any fixed N gives either an hour of silence or a log flood
  depending only on which rows a tier happens to hold.

- **A resumed backfill could not be distinguished from a stalled one.** The result now
  reports `PerTierCopied` alongside `PerTierScanned`; the two differ by exactly the
  idempotent content-key skips, so a tier where `scanned` ≫ `copied` is unambiguously a
  re-run re-streaming history it already copied.

- Dry runs are no longer silent. `processing tier` was suppressed under `dryRun`, which
  meant the mode used specifically to estimate how long a real run takes produced no
  output for its entire duration. Progress and completion lines are emitted in both
  modes, tagged with `dry_run` so a reader cannot mistake one for a real copy.
