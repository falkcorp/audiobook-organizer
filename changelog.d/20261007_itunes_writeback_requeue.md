### Added

#### iTunes write-back: put the dropped updates back on the queue

Before #3821/#3822 the write-back batcher dropped 4,293 book updates and one
remove, and its log kept no ids. Two endpoints now recover them through the
fixed batcher. Both are gated like `/writeback/held/release` and are dry runs
unless the body sends `"dry_run": false`.

- `POST /api/v1/itunes/writeback/requeue` takes `{"dry_run", "book_ids",
  "kinds"}`. It plans every primary book with the flush's own planner, and
  selects books where a track already in the library differs in metadata or
  location. The dry run returns counts and a 50-book sample. An explicit
  `false` queues the selected book ids. Library tracks no book claims, and DB
  PIDs missing from the library, are only counted. This path never queues an
  add or a remove. `kinds` selects books only: the flush writes every
  difference for a book it takes.
- `POST /api/v1/itunes/writeback/requeue-remove` takes 1 to 5 explicit book ids
  of merged-away or deleted books. It re-queues the remove of a PID only when
  all of these hold:
  - its external-id row is tombstoned;
  - no live book holds the PID;
  - the track is still in the library;
  - the PID is not held.

### Changed

- The per-book diff the flush writes is now one function, `planBookWrite`. The
  flush and the requeue preview share it, and the flush's behavior is
  unchanged.
- New `EnqueueBooks` and `EnqueueRemoveChecked` report when nothing was queued
  (auto write-back off, batcher stopped, remove held). The old
  `Enqueue`/`EnqueueRemove` still return nothing.
