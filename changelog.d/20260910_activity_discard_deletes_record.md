### Fixed

- The Activity page's Discard button now works on the rows it is shown for. It called the cancel endpoint, and for a run the server had already finished — every `interrupted_dropped` row a restart leaves behind — cancel has nothing to do and answers 404, which the page swallowed; on 2026-09-10 fifteen presses on two rows changed nothing. Discard now calls the new `DELETE /api/v1/operations/v2/:id/record`, which deletes the run's record (row, indexes, checkpoint, logs, errors) so it leaves the list and the startup resume sweep for good. Cancel and Discard both show the server's reason in the toast when refused instead of logging it to the console only.

### Added

- `DELETE /api/v1/operations/v2/:id/record` discards a persisted operation that nothing is executing: 204 on success, 404 for an unknown id, 409 while the row is still queued, running or waiting on dependencies (cancel it first). Same permission as cancel. Backed by `OpsV2Store.DeleteOperationV2` and `Registry.Discard`.
