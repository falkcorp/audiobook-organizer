<!-- file: changelog.d/20260806_153000_memdb-warmup-write-loss.md -->
<!-- version: 1.0.0 -->
<!-- guid: b7d3e04c-9a51-4f28-8e63-1c05a9f7d2b8 -->
<!-- last-edited: 2026-08-06 -->

### Fixed

- **Books written during startup could stay invisible until the next restart.** The in-memory query
  layer is warmed in the background so the server can start serving immediately — roughly two
  minutes of scanning on a production-sized library. Any book or book file written during the tail
  of that window was saved to the database correctly but never made it into the in-memory snapshot,
  and nothing ever re-warmed it: the row stayed missing from library listings, dedup scans and
  maintenance jobs for the rest of the process lifetime. Deletes had the mirror-image problem —
  a book deleted during the window survived in memory as a phantom row that cleanup and dedup
  would then act on.

  Write-throughs that arrive while the warmup is still running are now buffered and replayed into
  the snapshot immediately before it is published, in the same critical section as the publish, so
  a concurrent write is either replayed into the snapshot or applied to it afterwards — it can no
  longer fall between the two. If that buffer is ever exhausted (a bulk import racing startup), the
  service logs an error and keeps serving reads from the database directly rather than publishing a
  snapshot it knows to be incomplete. Startup is still non-blocking.

  Also fixed an adjacent case of the same problem: `Reset` wipes the keyspace, but an in-flight
  warmup could afterwards publish its pre-wipe snapshot over the top. It is now cancelled instead.
