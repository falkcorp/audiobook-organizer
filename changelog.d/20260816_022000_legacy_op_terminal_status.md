### Fixed

#### Finished maintenance jobs no longer show as still running

Jobs dispatched through `maintenance.job` and the scheduler create a **v1**
`operations` row and then enqueue a **v2** registry op carrying that row's id in
its params. Nothing ever wrote the v1 row again — its status was effectively
write-only after creation.

The operations UI reads v1. So on 2026-08-14 every maintenance-job row of the
day sat at `"pending"`, including `fix-file-modes` and
`normalize-primary-flags`, both of which had completed with journalled
summaries. A composer scan showed 0% three hours into real work, and a chapters
dry-run showed as an active 1.5-hour task after finishing at 17:57.

A handful of ops already mirrored the status by hand at their own call sites
(`itunes_ops.go`, `diagnostics_ops.go`, `folder_autoscan_op.go`). Everything the
scheduler dispatched did not.

The terminal status is now mirrored centrally in `publishOpTerminal`, which
every terminal path in the registry already funnels through — so a new terminal
path cannot forget it. `completed`, `failed` and `canceled` pass through;
`interrupted_ask` and `interrupted_dropped` collapse to the single
`interrupted` that the v1 vocabulary uses.

Existing progress counters are preserved rather than overwritten with zeros: a
completed job rendering at 0% would trade "stuck at pending" for "finished at
zero", which is no more honest. A completed row that never carried counters is
reported as fully done.

Note that this also unblocks the C510 opstate sweep, which treats unknown
statuses as KEEP — permanently-pending rows pinned their opstate blobs forever.

**Not included:** a backfill repairing rows already stuck from before this
change. Those remain stuck until repaired separately.
