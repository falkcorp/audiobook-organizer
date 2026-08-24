### Added

#### The library now does a full re-read once a week, on a schedule that survives restarts

Until now the only automatic scan was the incremental one every six hours, which
skips any file whose size and modification time are unchanged. That is the right
default, but it means a file whose contents changed without its timestamp moving —
or one whose metadata was never read correctly the first time — could stay stale
indefinitely.

There is now a weekly **full sweep** that re-reads and re-hashes everything,
including the organized library root. It is on by default and both knobs are
configurable (`scheduled.library_scan_full.period_hours`, default 168).

The scheduling deliberately does not use a seven-day timer. Timers here live only
in memory, so a timer set for seven days is reset every time the service restarts.
Production restarted **146 times in the preceding 30 days** — a mean uptime of
roughly five hours — so a weekly timer would have been reset long before it ever
elapsed and would have run **exactly zero sweeps**, while logging a perfectly
healthy-looking schedule the entire time. Instead the sweep records when it last
ran and checks hourly whether a week has passed, which is unaffected by restarts.

Deploying this does not start a sweep. With no recorded history the first check
writes down the current time and waits a full period, so upgrading never kicks off
an unannounced multi-hour re-read of the whole library.

### Fixed

#### Files that were being re-read and re-hashed on every single scan are now visible

The scan records "I have read this file, at this size and timestamp" so the next
scan can skip it. That bookkeeping step could be abandoned three different ways —
the file could not be examined, the database lookup failed, or there was simply no
library entry for that path — and **none of the three were recorded anywhere**. All
three looked exactly like success.

The consequence was permanent rather than transient. A file with no recorded scan
state is treated as never scanned, so it is re-read and re-hashed on *every*
subsequent scan, forever, reporting nothing. The third case is the troubling one:
it happens structurally for files that duplicate an already-linked copy, which
means the waste concentrates on precisely the files that are most expensive to
process, and it can never resolve on its own.

Each cause is now counted separately and reported in the scan summary, so the size
of the problem can finally be measured rather than guessed at. This change makes
the waste visible; it does not yet eliminate it.
