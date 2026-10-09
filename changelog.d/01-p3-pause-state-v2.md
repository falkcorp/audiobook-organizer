### Fixed

#### `GET /operations/pause` now lists the operations that are running

`running_pausable` and `running_not_pausable` were always empty because the
pause state read the v1 operations keyspace, which has had no writer since
2026-08-23. It now reads the newest v2 operation rows through
`database.ListRecentOperationsV2`, skips terminal rows, and classifies each
running op by its def ID.

### Removed

#### v1 stale-operation reaper and the unrouted `CancelOperation`

The once-a-minute ticker that decoded and sorted the whole v1 `operation:`
keyspace (`failStaleOperations`) is gone; it could never match a row. The
operations handler's `CancelOperation` (no route since the v2 cancel route
replaced it) and the unused `resolveScheduler` are deleted.
`collectStaleOperations` stays: `/operations/stale` still uses it.
