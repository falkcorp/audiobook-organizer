### Fixed

- Cancelling an operation that was interrupted by a restart now sticks. Before, the cancel button (`DELETE /api/v1/operations/v2/:id`) only knew how to stop a run that was queued or actively running; a Library Scan sitting at `interrupted_quiesced` after a deploy answered 404, its row was never touched, and every restart picked it back up from the resume pile. The registry now moves any non-running, unfinished row (`interrupted_quiesced`, `waiting_deps`, `interrupted_ask`, or a stale `running` row with no live worker) to `canceled` through the store's normal status path, so the startup resume sweep never sees it again. Already-finished rows still answer 404.

### Added

- `POST /api/v1/operations/v2/:id/retry` re-runs a finished operation (completed, failed, canceled, or any `interrupted_*` status) as a new run with the same definition and the same parameters, through the same enqueue path the "start operation" endpoint uses. It answers `202` with `{"data": {"id", "def_id", "status"}}` for the new run, `404` for an unknown id, and `409` when the row is still queued, running or waiting on dependencies. Same permission as cancel (`settings.manage`).
