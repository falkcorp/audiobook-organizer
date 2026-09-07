### Nothing ever deletes a v2 operation row — `opv2:op:` grows without bound

The `retention-and-hygiene` job's phase (1) is the only operation-retention sweep
in the codebase. It lists via `ListOperations` and deletes via
`DeleteOperationWithLogs`, both of which are the **v1** `operation:` keyspace.

Nothing has minted a v1 row since the v1 id minter was retired on 2026-08-23. So
phase (1) now operates on a fixed, shrinking set of pre-retirement rows and will
reach empty — at which point the job named "Retention & Dead-Prefix Hygiene"
retains nothing at all.

Meanwhile there is **no v2 equivalent**. Verified on `origin/main` 2026-09-07:

```
git grep -n "DeleteOperationV2\|DeleteOpV2\|PurgeOperationsV2\|DeleteOperationsV2" -- 'internal/*.go'
(no matches)
```

The only v2 deletions anywhere are `DeleteOpStateV2` in the registry's resume
paths, which drop a consumed *checkpoint* and never the operation row. So every
operation the system has run since 2026-08-23 is still in `opv2:op:`, along with
its `opv2:act:` activity rows, and always will be.

This matters now rather than eventually because Pebble growth is already a live
problem (30 GB, `book_ver:` CoW at 7.65 GB, compaction never running — see
`project_prod_disk_and_pebble_growth`).

**Not a mechanical fix — it needs a decision first:**

- What retention policy do v2 rows get? `OperationLogRetentionDays` (default 90)
  is the v1 knob; reusing it is the obvious answer but should be deliberate.
- Deleting a v2 op row orphans its `opv2:act:` and `opv2:q:` entries and any
  `operation_results` rows. Whatever sweep gets written has to delete the whole
  set, the way `DeleteOperationWithLogs` does for v1.
- Terminal-only, and which terminal? `IsTerminalV2Status` excludes
  `interrupted_quiesced` / `_ask` / `_restart` for good reason — a retention
  sweep must not delete a row the resume path still owns, however old it is.

Found while fixing the same job's phase (3), which had the mirror-image bug: a
v1-only *liveness* check that read every v2 operation as deleted (PR #3108).
