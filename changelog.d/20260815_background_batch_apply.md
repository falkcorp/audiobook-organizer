### Changed

- **Applying metadata to many books no longer holds the HTTP request open.**
  `POST /api/v1/audiobooks/metadata/batch-apply-cached` now enqueues a
  `metadata.batch-apply-cached` operation and returns `202` with an `op_id`
  immediately; the UI polls the operation for progress.

  A 250-book apply measured **2m0s** inline on production. Go's HTTP server does
  not kill a handler when the client disconnects, and `ApplyMetadataCandidate`
  took no `context.Context`, so the browser timed out, the UI reported
  "session expired — nothing was applied", and the server kept applying for
  another minute. Every one of those requests returned HTTP 200 and the work
  really happened; the message was wrong on both counts.

  The apply was already parallel and already pushed file work to a pool — the
  problem was never a missing worker pool, it was the request duration.

### Fixed

- The background operation can now be **cancelled**, and reports progress per
  book so the registry stuck-op watchdog can tell a slow run from a wedged one.
  `PerItemTimeout` is set below the watchdog's 5-minute progress timeout so one
  wedged book fails its own item instead of killing the whole batch.
