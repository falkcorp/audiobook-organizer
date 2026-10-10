### Added

- `GET /health` now reports readiness alongside liveness: `ready`, `memdb_ready`, `warmup_ms` (once warmup is done) and a `degraded` list of fixed tokens (`memdb_warming` while the in-memory read layer is still warming after a restart). The HTTP status stays 200 while warming, and the endpoint still discloses no versions, counts, paths or error text.
- New `memdb_fallback_reads_total{site}` metric counts reads that fell back to the slow Pebble path because the in-memory layer was not yet published, so a restart's slow window is visible on `/metrics`.
- Operations are held in the dispatcher, before they take a worker slot or start any timeout, while the store warms after a restart (up to 300 seconds); a held operation stays queued showing "waiting for startup warmup" and can still be cancelled. User-triggered operations that finish in seconds (`entities.series-rename`, `entities.author-merge`, `dedup.purge-stale`, `dedup.purge-legacy-fp-candidates`, `library.import`) are exempt via `NoWarmupWait`.
- `memdb_fallback_reads_total` carries an `outcome` label: `fallback` for reads served from Pebble and `refused` for reads that have no Pebble path and returned an error.
