### Changed

#### Write-back and batch-apply now use real worker pools instead of one book at a time

Four metadata paths that touch every selected book were doing that work
essentially one book at a time. On a library-sized selection that is the
difference between an overnight run and a multi-day one.

- **Bulk write-back** (`library.bulk-write-back`) had a hardcoded pool of 2
  workers, but the pool was not the bottleneck: the *producer* goroutine did the
  per-book database lookup, the protected-path check, the tag/policy read and —
  worst of all — the entire file rename synchronously before handing the book to
  a worker, through a channel buffered at only 4 slots. One slow rename stalled
  both workers. All of that per-book work now runs inside the workers, the
  channel carries bare IDs with a much deeper buffer, and the pool size is
  configurable via `metadata_scoring.write_back_workers` (default 4, was a
  hardcoded 2).

- **Batch save to files** (`metadata.batch-save`) was a plain sequential loop
  over every requested book, doing a tag write and an optional re-organize each.
  It now runs through the shared work-item runner with an explicit worker pool.

- **The two "apply candidates" endpoints** (Metadata Review's *Apply All*, and
  batch-apply from a fetch operation) applied each book's candidate one after
  another while the browser waited on the response. They now apply in parallel.
  The response body is unchanged — same fields, same per-book skip reasons, same
  ordering — so the Metadata Review screen behaves exactly as before, just
  faster.

Two correctness notes that came with the parallelism:

- Concurrent write-back is now serialized **per destination file path** by a
  keyed lock. Three separate situations can put two different book records on
  one file on disk: version-group siblings, a book in a protected path being
  redirected to its library copy, and — subtly — the copy-on-write backup name,
  which is built from a timestamp with only one-second resolution, so two
  writers touching one file inside the same second would generate the same
  backup filename and overwrite each other's backup. The lock is per path, not
  global, so books in different folders still run fully in parallel.

- Cancellation is now checked per book inside each worker rather than only where
  work is handed out. With a deep queue the hand-out loop finishes long before
  the workers do, so previously a canceled bulk write-back could keep writing
  files for the length of the remaining backlog.
