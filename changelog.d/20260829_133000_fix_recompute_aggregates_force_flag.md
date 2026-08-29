### Fixed

- **`recompute-book-aggregates` could never be re-run, and the escape hatch it printed to
  the operator did nothing.** The job's `Force` flag was declared in `DefaultParams()` and
  read nowhere. It was dropped at three separate layers: `POST /maintenance/jobs/:job_id`
  bound only `dry_run` from the request body, `maintenanceJobOpParams` had no `force`
  field, and `MaintenanceJob.Run`'s signature carries only `dryRun` — so a submitted
  `{"force": true}` was discarded before it reached any job code. Meanwhile two
  operator-facing messages, one logged and one shown in the operation log, both said "Use
  Force=true to override". Net effect: one clean non-dry run set the
  `book_aggregates_v1_done` sentinel and disabled the job **permanently**, while telling
  the operator there was a way out.

  This mattered beyond the job itself. `notifyBookFileChange` swallows aggregate-recompute
  errors on the stated grounds that "the backfill job acts as a safety net for any
  misses"; an unrunnable job is not a safety net. That comment has been corrected to say
  what is now true — the remedy exists, but nothing invokes it automatically.

  `Force` is now wired end to end: the dispatcher binds `force`, `maintenanceJobOpParams`
  carries it, and the sentinel gate reads it. An operator can rebuild every book's
  aggregates with `POST /api/v1/maintenance/jobs/recompute-book-aggregates`
  `{"dry_run": false, "force": true}`. The web UI's Run button still sends no `force` key,
  which is unchanged behaviour and the safe default.

### Added

- `maintenance.WithRawParams` / `maintenance.RawParamsFromCtx` — the live channel by which
  a maintenance job reads its own run parameters. `MaintenanceJob.Run` takes only
  `dryRun`, and the previously documented route (`store.GetOperationParams(opID)`, reading
  `opstate:<opID>:params`) lost its writer when the v1 op minter was retired: that key is
  now written only by `internal/organizer` and `internal/itunes`, never on the maintenance
  path. Params now travel on the context alongside the operation id, taken verbatim from
  the v2 operations row — which both resume paths already preserve, so a restarted run
  still sees the operator's actual choice.

  Known issue, reported but **not** fixed here: four other maintenance jobs still read
  parameters through that same dead `store.GetOperationParams(opID)` path and therefore
  receive nothing —
  `revert-metadata-fetch` (whose `fetch_op_ids` is required, so the job always errors),
  `bulk-fetch-metadata`, `bulk-deluge-import` and `scan-composer-tags`. They are a
  separate call path from the fix above and were left untouched; each needs its params
  struct added to `maintenanceJobOpParams` (or its own decode off `RawParamsFromCtx`)
  before its parameters can arrive.
