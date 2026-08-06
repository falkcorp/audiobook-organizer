<!-- file: changelog.d/20260806_150000_delete_book_files_batch.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e5b3db9-c5ac-40b2-88a9-3b387e21cabf -->
<!-- last-edited: 2026-08-06 -->

### Added

- `Store.DeleteBookFilesByIDs(ids []string) error` — deletes many `book_file` rows in
  **one** Pebble batch with one `Sync` commit, **one** go-memdb write transaction, and
  **one** aggregate recompute per affected book, however many rows are being removed.

  **This closes out the question the previous `DeleteBookFile` change left open.** That
  entry noted the per-delete cost was dominated by "fixed overhead (the `Sync` commit
  and the change notification)" and that the real cost of
  `maintenance.dedupe-book-file-rows` was "still unattributed". It is now attributed,
  and it was the change notification. The op cost **~1.35 s per deleted row** of fixed
  overhead, with only ~7 ms of it scaling with the book's remaining file count. Per-row
  deltas stayed flat — 1.85 / 1.94 / 1.42 / 1.66 / 1.54 s — while the book's
  `total_files` fell from 65 to 34, which is what rules out an O(R²) walk and pins the
  cost on per-**book** work being run once per **row**. Across the 2,901 redundant rows
  on production that is roughly 1.3 hours of almost pure waste.

  Per deleted row, `DeleteBookFile`'s `notifyBookFileChange` →
  `RecomputeBookAggregates` → `UpdateBook` chain paid two `pebble.Sync` commits, one
  full copy-on-write `book_ver:<id>:<nanos>` snapshot that re-marshals the *entire* old
  `Book` just to record a changed duration, two go-memdb write transactions (each
  queueing on memdb's single global writer mutex), and two reads of the book's file set
  that both unmarshal `AcoustIDFingerprint` blobs neither one wants. None of that is
  per-row work in principle. The new method hoists all of it out of the loop, following
  the shape of the existing `DeleteBookFilesForBook`; the only real difference is that
  the row set arrives as IDs, so the grouping by owning book has to be derived rather
  than assumed.

  **It is fail-closed.** Resolution happens in a first pass that mutates nothing, and if
  *any* ID fails to resolve, nothing is deleted and the error names the offenders. This
  deliberately diverges from `DeleteBookFile`, which treats an unresolvable ID as
  "already gone" and returns `nil` — defensible for a single row, because nothing else
  rides along and the caller's intent is fully satisfied by doing nothing, but not
  defensible for a batch. An ID that does not resolve means the caller's view of the
  store disagrees with the store, and a disagreement discovered partway through a
  destructive operation is the worst possible moment to press on with the other N−1
  rows. That is this repo's dominant incident shape: write-backs that proceeded on a
  stale view and silently erased fields. Fail-closed is cheap here specifically because
  both callers re-read their row set from the store on every run, so a stale ID is
  simply absent from the next run's list and the deferred work completes then — nothing
  is lost, at most one batch slips by one run.

  Both of `DeleteBookFile`'s lookup paths are preserved (the `book_file_id` index first,
  then the legacy full scan). The scan fallback matters *more* under fail-closed than it
  did per-row: dropping it would turn pre-index rows that are merely slow to delete
  today into rows that abort an entire batch.

  `DeleteBookFile` itself is unchanged — other callers still rely on its per-row notify,
  and a test asserts that its behaviour did not shift.

### Changed

- `maintenance.dedupe-book-file-rows` now accumulates each book's redundant row IDs and
  deletes them in one batched call instead of one `DeleteBookFile` per row. The op
  already ran a single `RecomputeBookAggregates` after its delete loop, so the per-row
  notification was pure duplicated work here.

  **The salvage write stays a separate, earlier commit, and must.** Rescued keeper
  fields are persisted *before* the donor rows they came from are deleted, and a failed
  salvage skips its group with the donors left intact. Folding that write into the
  atomic delete batch would silently remove the escape: the group would commit both or
  neither, and "neither" is indistinguishable from "nothing to do" on the next run, so a
  keeper whose rescue failed could never be repaired from its twins again. Losing a
  duration is recoverable; losing it while also deleting the only other copy is not.
  Accumulating IDs across groups actually strengthens the ordering — every salvage in a
  book commits before any donor in that book is deleted — and a group whose salvage
  failed simply never enters the accumulator.

- `maintenance.orphan-book-files-cleanup` deletes through the same batched method, in
  chunks of 500. Unlike `dedupe-book-file-rows` this path has **no** trailing
  `RecomputeBookAggregates` of its own, so it depends entirely on the batch method
  notifying once per affected book. Chunking bounds the blast radius of a single
  unresolvable ID to one chunk — which the next run picks up anyway, since the orphan
  scan rebuilds its list from scratch — and preserves the cancellation check and
  progress tick that the per-row loop provided for free, the latter being what keeps the
  stuck-op watchdog fed on a multi-thousand-row sweep.

- `PebbleStore.DeleteBookFilesFromMemDB` batches N memdb row deletions into one write
  transaction. The saving is not transaction bookkeeping — it is the contention removed
  from every other writer in the process, since go-memdb serialises all writers behind a
  single global mutex.

  Ten tests cover this: the notification count per affected book (asserted against a
  control test that shows the per-row path still producing one snapshot per row, so the
  "exactly one" figure is anchored rather than vacuous), fail-closed behaviour leaving
  every row intact, secondary-index teardown, duplicate and empty ID handling, the
  salvage-failure ordering rule with a control book that must still collapse, and the
  orphan path's chunking and per-chunk failure isolation.
