### Changed

#### One run-status table for operations, shared by the server and the web UI

Every operation status (`queued`, `waiting_deps`, `running`, `completed`,
`failed`, `canceled` and the `interrupted*` family) and what it means
(terminal, settled, resumable, retryable, discardable, awaiting a person) is
now defined once in `internal/operations/state`. Every v2 Go classifier that
kept its own list (database, registry, scheduler wait, `childop.Follow`, the
pause handler, the `dedup.run-all` resume check) now asks that table, and the
web pollers, the Retry and Discard predicates and the `OperationV2Status` type
come from `web/src/generated/ops.ts`, generated from the same table
(`make generate-ops`; `make ci` and CI fail when it is stale). No stored
status string changed.

Where the old lists disagreed, the callers now agree: the scheduler's wait,
`childop.Follow` and `dedup.run-all` also treat `interrupted_ask` and the
legacy `interrupted_restart` / bare `interrupted` spellings as interrupted.
This is a consistency change, not a fix: `interrupted_ask` is written only by
the boot sweep on rows from a previous process, and the legacy spellings are
never written to v2 rows, so no running wait could meet one.
