### Added

#### Operations registry — renamed op IDs keep working through aliases (`OperationDef.FormerIDs`)

An op ID is stored in `operations_v2` rows, `op_definitions_v2` and activity-log
attrs, and it is typed into `POST /api/v1/operations/v2 {def_id}`, scripts and
bookmarks. Renaming one used to strand all of those: resume dropped interrupted
rows as "unknown def at startup", enqueues failed, and `?def_id=` filters matched
nothing. A def can now list its old IDs in `FormerIDs`. The registry resolves an
old ID to the canonical def everywhere an ID enters: `EnqueueOp` (HTTP, scheduler,
in-process callers), the dispatcher, startup and quiesced resume, retry, the
isolated-op subprocess handshake, the operations timeline `def_id` filter and the
display-name and notify-level lookups for history rows. Stored rows are never
rewritten, so old history still displays under the renamed op's name. New rows,
including `ResumeRequeue` replacements, are written under the canonical ID.
`RegisterOp` rejects any alias that would shadow a registered ID, in either
registration order. Each use of an old ID increments
`audiobook_organizer_operation_deprecated_def_id_total{alias,entry}`, which is the
signal for when an alias can be removed.

A new guard test (`internal/server/op_id_aliases_test.go`) checks every ID in the
append-only ledger `internal/server/testdata/op_ids.golden` (231 IDs seeded from a
full boot). The test fails if any of those IDs stops resolving, which is what
happens when an op is renamed without an alias. The same test fails if code in
`internal/`, `pkg/`, `cmd/`, `web/src` or `scripts/` still uses an old ID. The
acoustid, dedup, deluge, iTunes and metafetch plugins now expose
`OperationDefs()` so the guard can list their ops without their dependencies
being wired.

### Changed

#### Op-ID namespace drift (naming audit class 8): six op IDs moved

- `maintenance.itunes-regroup` → `itunes.regroup`
- `maintenance.itunes-playlist-import` → `itunes.playlist-import`
- `maintenance.itunes-heal` → `itunes.heal`
- `maintenance.itunes-clone-into-library` → `itunes.clone-into-library`
- `library.optimize` → `maintenance.library-optimize`
- `maintenance.dedup-llm-review` is removed and its ID is now an alias of `dedup.llm-review`

`maintenance.dedup-llm-review` ran the same dedup-engine LLM review as
`dedup.llm-review`, but its description wrongly said "author-dedup candidates".
It also did not declare `CapLibraryWrite`, and its separate ConcurrencyKey meant
the two ops could review the same candidates at the same time. Every old ID still
resolves. The web UI and the docs in `docs/system/` now use the new IDs.
