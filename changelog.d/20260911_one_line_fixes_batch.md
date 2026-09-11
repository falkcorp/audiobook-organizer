### Fixed

#### Eight small-impact one-line fixes from the burndown's easy quadrant

- **ABS permissions (TASK-143 / N-3):** `defaultPermissions()` no longer advertises `delete` and `update`; the ABS surface has no item-edit or item-delete route, so Absorb/AudioBooth stop rendering affordances this server cannot service. The conformance allow-list records the deviation from the oracle capture.
- **NutsDB digest compaction (DB-04):** the delete of the previous day's digest row now propagates a real I/O error instead of being discarded, so a failed delete can no longer leave two digests for one day.
- **`audiobook_organizer_books_total` (TASK-131):** the metric's help text now says it counts PRIMARY books (one per version group), not the total row count; the name is unchanged so existing dashboards keep working.
- **Activity batcher (DA-04):** `isBatchable` documents why warn/error lines of a batchable type deliberately bypass the batcher (they must stay individually visible).
- **`prodSchedulerStore` (TASK-117):** now carries the full store and implements `database.StoreUnwrapper`, so capability lookups can walk past it instead of reporting the Pebble store as unsupported.

### Removed

- **Tracked `mtls-bridge` binary (TASK-014):** the 9 MB arm64 build artifact is untracked and both `mtls-bridge` and `mtls-bridge.exe` are gitignored (forward-only hygiene per the 2026-07-10 repo-size plan).
- **`internal/operations/mocks` (TASK-118):** the generated `ProgressReporter` mock, its `.mockery.yaml` entry and the dead `mocks`-tagged `server_import_file_mocks_test.go` that was its only referencer.
- **`Importer.linkAsVersion` (DEAD-1 residue):** the dead production method and the two tests that were its only callers.
