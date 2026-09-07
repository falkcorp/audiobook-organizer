### Delete the v1 startup resume sweep — the registry already owns restart resume

`Server.resumeInterruptedOperations()` (`internal/server/server_lifecycle.go:48`)
and the ~270 lines it drives — `resumeV2Op`, `resumeLegacyOp`, `countLegacyV1Ops` —
are a **superseded duplicate** of `Registry.resumeAfterStartup`
(`internal/operations/registry/resume.go:39`), which `Start()` runs before the
dispatcher begins.

**Why it can no longer act.** Its only candidate source is
`store.GetInterruptedOperations()`, which iterates the bound
`[]byte("operation:") .. []byte("operation:~")`
(`internal/database/pebble_store_operations.go:474-495`) — the v1 keyspace, whose
minter was retired 2026-08-23. Note the consequence for the branch *inside* it:
`resumeV2Op` dispatches on a def's `ResumePolicy`, but it is only ever reached for
a **v1 row whose `Type` happens to match a registered v2 def name**. A v2-native op
interrupted by a restart is not in `operation:` at all, so this sweep never sees it.

**This is not a gap — the registry covers it, and covers it better.**
`resumeAfterStartup` reads `ListResumableOperationsV2`, applies the full policy set
(`ResumeRestart` / `ResumeRequeue` / `ResumeDrop` / `ResumeAsk`, unknown ⇒ drop),
supersedes stale `interrupted_quiesced` rows, merges checkpoint state into params,
and performs the scan stand-down boot-consult. The v1 sweep does none of that.

**Production evidence, 2026-09-07 (two consecutive boots):**

| Boot | v2 registry | v1 sweep |
|---|---|---|
| 11:43:04 | `resumeAfterStartup: processing resumable ops count=1`, then `re-queued restart op def_id=library.scan resume_count_new=3` | silent |
| 16:23:49 | `resumeAfterStartup: no resumable ops` | silent |

The v1 sweep emitted neither its per-op line (`"Resuming interrupted operation"`)
nor its telemetry line (`"legacy v1 op rows pending resume"`) at either boot, while
the registry did real work at the first. Restart resume is demonstrably live and is
demonstrably not this code.

**The file names its own exit condition, and it is now met.** The `countLegacyV1Ops`
gate says it "lays the groundwork for a future task that deletes `resumeLegacyOp`
once this count is verified to stay at zero in production across a full release
cycle." Two boots is not a full release cycle, so treat the table above as the
beginning of that record rather than the end of it — but the count cannot become
non-zero by any route that still exists, because nothing mints a v1 row.

**Scope of the deletion**, all in `internal/server/server_lifecycle.go`:

1. `resumeInterruptedOperations` and its call site in server startup.
2. `resumeV2Op`, `resumeLegacyOp`, `countLegacyV1Ops`.
3. The `operations.LoadParams[operations.BulkWriteBackParams]` read at `:212` —
   the only production reader of the `opstate:<id>:params` side table outside the
   `op.Legacy`-gated history bridge.

   **That side table already has zero matched writer→reader pairs.** Verified
   2026-09-07 across `internal/` excluding tests and mocks:

   | direction | site | type | counterpart |
   |---|---|---|---|
   | write | `organizer/service.go:234` | `operations.OrganizeParams` | **no loader anywhere** |
   | write | `itunes/service/importer.go:397` | `operations.ITunesImportParams` | **no loader anywhere** |
   | read | `server_lifecycle.go:212` | `operations.BulkWriteBackParams` | **no writer anywhere** — the only other reference is the struct decl at `operations/state.go:48`; the pre-UOS handler that wrote it is gone |
   | read | `metabatch/fetch_ops_index.go:162` | bare `[]string`, gated `if op.Legacy` | written by the retired v1 fetch handler |

   So the `:212` branch can only ever take its own `params == nil` path
   (`"no saved params, cannot resume"`), and the two live writers persist blobs
   nothing will ever load. Five maintenance jobs already carry comments recording
   their own migration off `GetOperationParams` (`bulk_deluge_import.go:47`,
   `bulk_fetch_metadata.go:49`, `prune_book_snapshots.go:28`,
   `revert_metadata_fetch.go:40`, `scan_composer_tags.go:49`) — this is the tail
   of that migration, not a new decision. The params pair
   (`SaveOperationParams` / `GetOperationParams` / all four helpers in
   `operations/state.go`) can therefore be deleted outright; it needs no v2
   successor, because nothing is reading what it stores.

   ⚠️ **Do not delete `opstate:` state generally.** The `opstate:<id>` *state*
   pair is a separate, live concern — `operations.SaveCheckpoint` is in active
   use; see the retention sweep fix of 2026-09-07 (#3108). Only the `:params`
   half is dead.
4. `GetInterruptedOperations` itself, with the rest of the `operation:` methods in
   the final v1 phase.

Filed rather than done: deleting a resume path wired into server startup is a
production call — it is the kind of change that looks inert right up until a boot
proves otherwise, so it wants a deliberate merge and a watched restart rather than
a drive-by. Same shape as the stale-operation reaper filed the same day.

Note that this is a **deletion, not a migration**. There is nothing to repoint the
sweep at: `resumeAfterStartup` already does the job, and does more of it. Any plan
entry that reads "repoint the startup resume sweep to v2" is describing work that
does not exist.
