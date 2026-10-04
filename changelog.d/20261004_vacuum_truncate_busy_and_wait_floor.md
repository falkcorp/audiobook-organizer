### Fixed

#### Activity-log vacuum no longer reports success while the WAL still holds the space

`VacuumActivity` ends with `PRAGMA wal_checkpoint(TRUNCATE)` so that the space
VACUUM frees actually goes back to the disk (the 2026-09-08 incident: 10.66 GB
freed from the main file and the same amount still held by the `-wal`). It ran
the pragma through `ExecContext` and discarded its result row. A TRUNCATE that
collides with the background checkpointer, which runs every 30 s on its own
connection, returns `busy=1` as an ordinary row rather than an error, so the
vacuum reported success with the WAL untouched.

The truncate now goes through `walCheckpoint` on the checkpoint connection,
which reads the row and queues behind the background loop. Each attempt runs a
PASSIVE checkpoint, which copies frames without blocking writers, and issues
the TRUNCATE only when that PASSIVE copied everything. The TRUNCATE then just
resets the file. This is the same gate the background loop uses. Without the
gate, a TRUNCATE holds the WAL write lock while it copies the rebuilt database
or waits for a reader, and foreground activity writes fail with `SQLITE_BUSY`
after their 10 s busy timeout. Attempts are retried up to 8 times with backoff.
The worst case is about 9 s if every TRUNCATE has to wait its 1 s for readers.
When a reader pins the WAL, no TRUNCATE is issued and the vacuum gives up after
about 1 s of backoff. In either case `VacuumActivity` returns its existing
"space still held" error, and the background checkpointer resets the WAL on its
next idle tick once the reader is gone.

`POST /api/v1/activity/clamp-summaries` no longer answers 500 when the clamp
committed but the vacuum or WAL reset failed. It returns 200 with the clamp
counts, `space_still_held: true` and a fixed `vacuum_error` message. The
underlying error is logged as a warning and is not sent to the client. Prod
currently runs the Pebble activity backend (`ACTIVITY_BACKEND=pebble`), so
none of this has run there yet.

The flaky `internal/activity` test
`TestClampSummaries_VacuumRunsEvenWhenNothingWasClamped` ("WAL 11.19 MB before,
11.57 MB after") had the same cause. It now holds the background checkpointer
off, never skips, and requires an empty WAL after the vacuum.

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
