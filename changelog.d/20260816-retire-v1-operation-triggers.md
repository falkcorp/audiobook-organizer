### Fixed

- **Starting a scan reported failure while the scan was running.** The legacy
  trigger routes answered `202 {"op_id":..., "id":...}` unwrapped, while the web
  client read `.data.id`. `.data` was `undefined` on that shape, so `op.id` threw a
  `TypeError`, the caller's `catch` swallowed it, and the UI showed "Failed to start
  scan" — for a scan that had started. Same for organize, transcode and optimize.

### Changed

- **Retired the v1 operation trigger routes** `POST /api/v1/operations/{scan,
  organize,transcode,optimize}`. All four were pure shims whose entire body forwarded
  to the same `registry.EnqueueOp` that `POST /api/v1/operations/v2` calls, so they
  contributed no behaviour and one wrong response shape. Callers now post
  `{def_id, params}` to `/api/v1/operations/v2`.

  One behaviour change worth knowing: `POST /operations/transcode` rejected a missing
  `book_id` with a synchronous `400`. The generic trigger cannot validate per-op
  params, so the same mistake now enqueues an operation that fails immediately with
  `book_id is required`. The guard still exists — it reports asynchronously.
