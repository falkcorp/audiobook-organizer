### Fixed

- **Maintenance jobs now actually receive their custom parameters.** Five jobs read
  their non-`dry_run` parameters from a code path that had lost its writer, so every
  one of them silently ran with defaults no matter what the operator sent:

  - `revert-metadata-fetch` — `fetch_op_ids` is REQUIRED, so this job could only ever
    return the error `fetch_op_ids required: ...`. It was 100% non-functional.
  - `bulk-fetch-metadata` — `prefer_audible` and `skip_cached` were pinned to `false`.
  - `bulk-deluge-import` — `max_books` was pinned to `0` (unlimited).
  - `scan-composer-tags` — `fix_mode` was pinned to `set_narrator`, so an operator
    asking for `clear` silently got the opposite behaviour.
  - `prune-book-snapshots` — `keep_count` was pinned to its default of 10.

  Two independent layers were broken. The jobs read
  `store.GetOperationParams(opID)`, a Pebble key whose only writer
  (`operations.SaveParams`) lost its last maintenance-path caller when the v1
  operation minter was retired — the read outlived its writer. And the run route
  itself bound only `dry_run` and enqueued a fixed three-field struct, dropping every
  other key before it could reach the params blob at all. Fixing either alone changes
  nothing; the jobs now read `maintenance.RawParamsFromCtx`, and
  `POST /api/v1/maintenance/jobs/:job_id/run` now forwards every key the operator
  sent.

  The catalogue route was the tell throughout: `GET /api/v1/maintenance/jobs`
  advertises each job's `default_params`, so the API published `fetch_op_ids` and
  `fix_mode` to clients while the run route threw them away.

### Removed

- `GetOperationParams` is removed from the `maintenance.JobStore` interface. It had no
  writer on the maintenance path, so any job calling it received nothing, silently and
  forever. Taking the method away means a future job cannot reach for it — the mistake
  stops compiling instead of returning empty results.
