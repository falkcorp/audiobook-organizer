### Fixed

`ListWaitingDepsOps` no longer decodes every operation row to find the parked
ones. A new `opv2:wait:` index, staged by `stageOpRow` in the same batch as the
operation row, lets the dependency scheduler's sweep tick, its per-subject
re-evaluation and its startup index rebuild read only the rows that are
waiting on dependencies. That scan was about 7 GB of allocation per 25 minutes
on the production heap. Like the timeline index it is untrusted at every boot:
the reads use the full scan until the boot reconcile has built and repaired the
index, and a read that finds a key for a row that is gone or no longer waiting
deletes the key and skips it.
