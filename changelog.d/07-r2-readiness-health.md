### Added

- `GET /health` now reports readiness alongside liveness: `ready`, `memdb_ready`, `warmup_ms` (once warmup is done) and a `degraded` list of fixed tokens (`memdb_warming` while the in-memory read layer is still warming after a restart). The HTTP status stays 200 while warming, and the endpoint still discloses no versions, counts, paths or error text.
- New `memdb_fallback_reads_total{site}` metric counts reads that fell back to the slow Pebble path because the in-memory layer was not yet published, so a restart's slow window is visible on `/metrics`.
- Operations now wait (up to 300 seconds, cancelable) for the startup warmup before their body starts, with the status message "waiting for startup warmup", instead of running against the slow read path.
- Operations flagged `NoWarmupWait` (the interactive `entities.series-rename` and `entities.author-merge`) skip the startup warmup wait. The wait happens before the run's timeout clock starts, so an operation with a short timeout is no longer cancelled by a warmup that outlasts it, and a cancel or shutdown during the wait ends it without running the body.
