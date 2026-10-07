<!-- file: docs/plans/2026-10-07-itunes-writeback-requeue.md -->
<!-- version: 1.2.0 -->
<!-- guid: 4187f421-1b4d-445f-a098-917d3feef234 -->
<!-- last-edited: 2026-10-07 -->

# Plan: re-enqueue the dropped iTunes write-back updates

Branch `feat/itunes-writeback-requeue`, worktree `aorg-wb-requeue`, based on
`origin/main` 7bf98d864 (the batcher fix from #3821/#3822 is in).

## Goal

Recovery step 3 of `2026-10-07-itunes-writeback-drops.md` says "re-enqueue the
changed books through the fixed batcher". There is no way to do that. The batcher
dropped 4,293 book updates and one remove before the fix, and the drop log has
no ids. This adds two owner endpoints:

- `POST /api/v1/itunes/writeback/requeue` puts every book whose iTunes tracks
  differ from the DB back into the batcher queue. It queues updates only, never
  adds or removes.
- `POST /api/v1/itunes/writeback/requeue-remove` re-queues the remove for a few
  explicit merged-away loser book ids whose PID was tombstoned but never removed.

Both default to `dry_run: true`. They write only when the request sends
`"dry_run": false`. An empty body means all defaults.

## Decisions

1. **Use the batcher's diff, not `ComputeITLDiff`.** The rebuild diff and the
   batcher disagree on what each book should look like:

   | | rebuild diff | batcher flush |
   |---|---|---|
   | PIDs | `book.ITunesPersistentID` only | every `book_file` PID |
   | location | current `FilePath`, canonicalized | `f.ITunesPath`, normalized |
   | track name | `book.Title` | `f.Title` |

   Selecting books with the rebuild diff would miss books with file-level PIDs.
   It would also preview counts (3357 metadata / 2867 location on prod) that
   are not what the batcher writes. The per-book part of `drainFlush` moves into
   one planner, `planBookWrite`. The flush and the requeue preview both call it,
   so the preview reports exactly what the queue will try to write. The
   flush's behavior does not change.
2. **Updates only.** A book is selected only when at least one of its PIDs is a
   track in the library and that track differs. PIDs missing from the library
   (rebuild "adds") are counted as `ignored_add_tracks`. On a full scan, library
   tracks that no scanned book claims (rebuild "removes", 94,471 on prod) are
   counted as `ignored_remove_tracks`. Neither is ever queued.
3. **`limit` and `after_id` chunk a large requeue.** A call skips books at
   or before `after_id`, then books already pending, and then queues at most
   `limit`. It returns `skipped_pending` and `next_after_id`. Skipping
   pending books matters: in dry-run mode, or for a diff that never
   converges, the batcher keeps a chunk queued. Without the skip, a rerun
   would pick the same N again. A full requeue goes to the batcher as one pending set. If the
   contract's 20% mhoh cap refuses that set, the batch is kept and retried
   until it is split. By estimate the cap is far off (~6k tracks × ~6 mhoh vs
   98k tracks), but chunking is the way out if it is hit.
4. **`kinds` only picks which books are selected.** The batcher queues book ids,
   not single changes. Once a book is queued, the flush writes every difference
   it finds for that book. The response says so.
5. **Enqueue reports what it did.** `Enqueue` and `EnqueueRemove` return nothing
   when write-back is off or the batcher is stopped, and `EnqueueRemove` skips a
   held PID with only a log line. New `EnqueueBooks` and `EnqueueRemoveChecked`
   return counts and errors. The old methods wrap them, so the
   `Enqueuer`/`EnqueueRemove` interfaces stay the same. `dry_run:false` with
   auto write-back off returns 409, never "enqueued N".
6. **The remove's PID is not on the loser's external-id rows.** The drops plan
   says it is, but merge step (b) `ReassignExternalIDs` moves the loser's
   mappings to the winner before step (c) queues the remove. The PID is found
   on the loser's own book row and `book_file` rows. It is queued only when all
   of these hold:
   - the loser is soft-deleted or non-primary;
   - the PID's external-id row is tombstoned;
   - no other live book holds the PID, at book level (`ListBooksByITunesPID`)
     or on ANY `book_file` row (`GetAllBookFilesCore`, because the
     `book_file_pid` index keeps one row per PID and duplicates exist);
   - the track is still in the library;
   - the PID is not on the held list.

   Requests are capped at 5 book ids and at `MaxRemovesPerFlush` PIDs.
7. **Concurrency.** The full-library scan reads book files and authors for each
   book. It runs on an `errgroup` limited to `runtime.NumCPU()`. Results are
   merged under a mutex and sorted by book id, so the 50-book sample is
   deterministic.
8. **Permission:** `PermLibraryEditMetadata`, the same as
   `/writeback/held/release`. The route lines sit next to it, so the owner gate
   being added on `fix/apikey-expiry-and-privilege` can cover all three.

## Files

- `internal/itunes/service/writeback_batcher.go`: extract `planBookWrite`, and
  add `EnqueueBooks`, `EnqueueRemoveChecked`, `IsHeld` and `WriteTarget`.
- `internal/itunes/service/writeback_requeue.go` (new): `PlanRequeue` and
  `PlanRemoveRequeue`.
- `internal/server/itunes_writeback_requeue.go` (new): the two handlers.
- `internal/server/server_lifecycle.go`: routes.
- Tests next to each file. Changelog fragment.

## Tests

- Batcher: the existing flush tests guard the extraction. New tests cover
  `EnqueueBooks` and `EnqueueRemoveChecked` (disabled, stopped, held).
- Planner (synthetic `ITLLibrary` built in Go, in-memory store): changed vs
  unchanged books, adds and removes ignored, `kinds` filter, subset ids.
- Handler: dry run is the default, including with no body; explicit `false`
  queues; adds and removes are never queued; subset filtering works; remove
  re-queue works only for an eligible explicit id; the request cap is enforced.

## Rollback

Revert the PR. The endpoints write nothing but queue entries. While prod has
`write_back_dry_run=true`, queued books stay in the durable queue, unscheduled,
until dry-run is turned off. Then the flush re-diffs each one, and a book that
no longer differs completes as a no-op. No endpoint withdraws a queued remove,
which is why the remove path is explicit-id only and dry run by default. Queued
items show in `/itunes/writeback/status`. Nothing in this change clears the
queue.
