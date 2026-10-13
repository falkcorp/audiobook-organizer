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
committed, and has no delete method. Nothing in production uses the SDK yet:
the adapter registers a v3 definition on the existing registry (one queue,
one exclusive-key map; `library.scan` keeps its key) and holds unchanged v2
definitions in the same catalog.

### Changed

#### `repairs.Writer` is a wrapper over the new fenced Writer

The fence and stand-down gate every Repairs write passes, the book write that
captures the previous value inside the store's lock, and the metadata-history
recording now live in `internal/operations/opswriter`. `repairs.Writer` keeps
its whole method set (a test pins every signature) and behaves as before. The
run fence moved from the registry to `opswriter` with it.

### Fixed

#### Late writes can no longer re-create a finished run's per-run keys

The v3 snapshot-page, chunk-offset, intent and log writers now refuse a run
that does not exist or has already ended, under the same lock as the
terminal transition. Before, a write landing after a run ended or was
discarded re-created keys that no cleanup pass ever removed.

#### A v2 progress write no longer rolls back a batch run's ledger progress

Once the chunk ledger owns a run's progress, the v2 reporter's lazy progress
flush and the terminal status write no longer reset the run's done count,
total and current item to the v2 row's older numbers.
