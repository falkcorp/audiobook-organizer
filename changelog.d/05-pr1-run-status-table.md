### Changed

#### One run-status table for operations, shared by the server and the web UI

Every operation status (`queued`, `waiting_deps`, `running`, `completed`,
`failed`, `canceled` and the `interrupted*` family) and what it means
(terminal, settled, resumable, retryable, discardable, awaiting a person) is
now defined once in `internal/operations/state`. The seven Go classifiers that
each kept their own list now ask that table, and the web pollers, the Retry
and Discard predicates and the `OperationV2Status` type come from
`web/src/generated/ops.ts`, generated from the same table
(`make generate-ops`; `make ci` and CI fail when it is stale). No stored
status string changed.

### Fixed

#### Scheduler windows no longer wait forever on an `interrupted_ask` child

The scheduler's wait for a child operation ended on `interrupted_quiesced`
and `interrupted_dropped` but not on `interrupted_ask` (or the legacy
`interrupted_restart` / `interrupted` spellings), none of which moves again
in-session. It now ends on every settled status, so the window moves on to
its remaining tasks.
