### Removed

#### `DELETE /operations/history` — retired; it deleted from the dead v1 keyspace and reported success (SV-01)

The endpoint iterated the legacy `operation:` keyspace by status and answered 200 with a plausible `deleted` count, but the v1 minter was retired on 2026-08-23 and the Activity page reads v2 rows, so a "clear history" call cleared nothing a user could see. It had no caller in the web client. The route, handler, its client function, and the store-level `DeleteOperationsByStatus` / `CountOperationsByStatus` that existed only to back it are removed; the path now answers 404, and finished runs are removed per row with `DELETE /operations/v2/:id/record`.
