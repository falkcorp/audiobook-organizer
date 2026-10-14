### Added

#### Operations v3 SDK, chunk-leasing runner, fenced Writer and v2 adapter (05-PR5)

`pkg/ops` is the new authoring SDK for operations: an op is a `Task`,
`Batch`, `Fixer` or `Pipeline` built from one constructor, with its effects,
permission, exclusive key, schedule and resume policy declared on the
definition. A `Batch` runs on `runtime.NumCPU()` workers by default (one
worker is a choice that must give a reason), is cut into chunks of 256 items
that workers lease, and records each finished chunk in a per-run ledger. A
run interrupted by a deploy resumes under the same op id and re-runs only
the chunks the ledger does not record, so finished work is never redone and
the progress count never goes backwards. Every write goes through a fenced
`Writer` that refuses writes from a canceled or abandoned run, writes an
intent before touching the store, records history only after the write
committed, and has no delete method. An intent is cleared only once its
write and history are recorded, so a failed, panicking or unrecorded write
stays listed as "may be partially applied". A run whose writes start being
refused (it was canceled, or lost its hold on the library scan) stops with
an error instead of counting the refused items as ordinary failures and
ending "completed", even when an item swallows the refusal or makes the
write outside its own context (then the whole run fails, since the write
cannot be pinned on a chunk); a chunk whose
items all finished cleanly when a run was stopped is still kept, so a
resume does not redo it. A crash in an item or in any of the op's other
callbacks is recorded as a failure instead of taking the server down. A run stopped by a server shutdown leaves its
final summary to the resumed run, so a fixer's follow-up step runs once,
over every row applied before and after the restart, including rows applied
in a chunk the restart cut short (recognized only on the resumed attempt,
and only for a row no hold, owner-only flag or guard now stops). (A fixer run that is discarded from
"awaiting decision" instead of retried never runs its follow-up; a planned
follow-up adds a third choice there, "Finish without resuming", which runs
only the follow-up over the rows already applied.) The applied list is stored a row at a time,
so it has no size cap, and a run that starts over drops the list its
abandoned attempt stored. A writing run refuses to start when the store cannot
record intents, the operation journal or metadata history, instead of
writing without them. Nothing in production uses the SDK yet:
the adapter registers a v3 definition on the existing registry (one queue,
one exclusive-key map; `library.scan` keeps its key) and holds unchanged v2
definitions in the same catalog.

### Changed

#### `repairs.Writer` is a wrapper over the new fenced Writer

The fence and stand-down gate every Repairs write passes, the book write that
captures the previous value inside the store's lock, and the metadata-history
recording now live in `internal/operations/opswriter`. `repairs.Writer` keeps
its whole method set (a test pins every signature). Two behaviors are new: a
book write is refused when the Writer has no metadata history recorder
(every Repairs caller passes one), and journaled writes report whether their
history rows were recorded. The run fence moved from the registry to
`opswriter` with it.

### Fixed

#### Late writes can no longer re-create a finished run's per-run keys

The v3 snapshot-page, chunk-offset and intent writers now refuse a run that
does not exist or has already ended, under the same lock as the terminal
transition, and the log writer refuses a run that was discarded. Before, a
write landing after a run ended or was discarded re-created keys that no
cleanup pass ever removed. A log line written as a run finishes is still
kept: the run's log lives until the run is discarded.

#### A v2 progress write no longer rolls back a batch run's ledger progress

Once the chunk ledger owns a run's progress, the v2 reporter's lazy progress
flush and the terminal status write no longer reset the run's done count,
total and current item to the v2 row's older numbers.
