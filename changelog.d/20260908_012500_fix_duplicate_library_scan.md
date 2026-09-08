### Fixed

- **A second library scan no longer queues behind a running one.** The Active
  Operations panel could show one scan running and another queued; the second was
  not a double-run (the dispatcher's ConcurrencyKey gate serializes them) but it
  would start the moment the first finished, scanning the whole library again for
  no reason.
- Root cause: the enqueue-time dedupe reuses an already-active op only when the
  incoming params are **byte-identical** to the active row's, and `library.scan`'s
  params cannot stay identical. It is `ResumeRestart`, and resuming merges the
  saved checkpoint (`resume_folder_idx` / `resume_item_offset`) into the row's
  params — so once a scan has resumed even once, every later trigger compares
  unequal, logs "params differ — queueing a second run", and stacks a duplicate.
  Seen on production 2026-09-08: a `library.scan` running at `resume_count=2`
  with a second queued behind it.
- `library.scan` now sets `DedupeQueuedRuns`, which skips the params comparison
  for this def. The flag already existed but no def had ever opted in. It is
  correct **here specifically** because a library scan walks the whole root — a
  second scan is the same work, not a different selection. The byte-comparison
  default stays as-is for everything else, because for a set-parameterized op
  like `metadata.batch-apply-cached` the params *are* the work list and deduping
  a request would silently discard books.
