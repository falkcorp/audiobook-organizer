- [ ] **`GET /system/logs` returns nothing and has for a long time — point it at the activity store.**
      `sysinfo.CollectSystemLogs` walks recent operation ids and calls
      `GetOperationLogs(op.ID)`, which reads the `operationlog:` Pebble keyspace.
      That keyspace has **no writer**: `AddOperationLog` is called only from
      `logger.OperationLogger`, which is only constructed by `logger.ForOperation`,
      and `ForOperation` has **zero production callers**. Operations log through the
      activity store instead — `internal/server/batch_save_op.go:99` already spells
      out that the two are different keyspaces.
      Verified against production 2026-09-07:
      `GET /api/v1/system/logs?limit=5` → `{"logs":null,"offset":0,"total":0}`.
      The System Logs page is empty on screen and nothing reports an error.

      This is **not** a v1-vs-v2 keyspace problem and repointing the id source to v2
      does not fix it (deliberately left on `GetRecentOperations` in the sysinfo v2
      repoint, with a comment saying why) — fresh v2 ids have no `operationlog:`
      rows either. The fix is to source the endpoint from the activity store, which
      means deciding what "system logs" means now that per-operation logs, activity
      entries and the `journalctl` stream are three different things.

      Also decide the fate of `operationlog:`, `AddOperationLog`,
      `GetOperationLogs`, `logger.ForOperation` and `logger.OperationLogger` — a
      seventh write-dead keyspace found during the v1 operations purge, and the
      only one whose deadness is currently user-visible.
