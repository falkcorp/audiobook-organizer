### Fixed

#### Activity-log vacuum no longer reports success while the WAL still holds the space

`VacuumActivity` ends with `PRAGMA wal_checkpoint(TRUNCATE)` so that the space
VACUUM frees actually goes back to the disk (the 2026-09-08 incident: 10.66 GB
freed from the main file and the same amount still held by the `-wal`). It ran
the pragma through `ExecContext` and discarded its result row. A TRUNCATE that
collides with the background checkpointer, which runs every 30 s on its own
connection, returns `busy=1` as an ordinary row rather than an error, so the
vacuum reported success with the WAL untouched. The truncate now goes through
`walCheckpoint` on the checkpoint connection, which reads the row and queues
behind the background loop. A busy result is retried with a bounded backoff
that honours cancellation. If the truncate is still busy after that,
`VacuumActivity` returns its existing "space still held" error. The flaky
`internal/activity` test `TestClampSummaries_VacuumRunsEvenWhenNothingWasClamped`
("WAL 11.19 MB before, 11.57 MB after") had the same cause. It now forces the
collision with a 1 ms checkpointer interval and requires an empty WAL.

#### Test harness: a wait cut short by the package deadline no longer fails finished work

`waitGroupOrFatal` and the other bounded waits in `internal/database` shorten
their bound as the package `-timeout` approaches, down to a 100 ms floor. A
test that ran in that window failed even when its goroutines finished a moment
later. This is how `TestCandidateWritePath_ConcurrentNoRace` failed. Now, if a
shortened or floored bound expires but the work finishes within the grace
period, the wait logs and passes. A full-length bound that expires still fails
the test, and work still running after the grace still exits the binary with
a goroutine dump.

The harness's own child-process self-test also leaked its temp directory,
with its on-disk Pebble store inside it. The `stuck` child exits the binary
before `t.Cleanup` runs, so 78 of these directories had built up in the
system temp dir. The parent now points the child's `TMPDIR` at a directory
it owns, removes it, and asserts that the child's directory is gone.
