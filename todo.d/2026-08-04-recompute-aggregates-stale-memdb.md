<!-- file: todo.d/2026-08-04-recompute-aggregates-stale-memdb.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4a29d7e1-83b6-4c50-9f27-1e08b5c3a64d -->
<!-- last-edited: 2026-08-04 -->

- [ ] **Corrected book aggregates are invisible until memdb refreshes.**
      Observed on the first `maintenance.dedupe-book-file-rows` canary
      (2026-08-03): 338 redundant rows were deleted from 10 books and every
      duration was **unchanged** immediately afterwards. `total_file_count` still
      read 50 for a book whose files endpoint already returned 26. A service
      restart surfaced the corrected values — e.g. "Defending the Lost"
      158.00h → **12.15h** — so the data in Pebble was right the whole time and
      only the memdb-backed read was stale.

      Where to look: `DeleteBookFile`
      (`internal/database/pebble_store_bookfiles.go:730`) does the right things in
      the right order — Pebble delete, `DeleteBookFileFromMemDB`, then
      `notifyBookFileChange`. The suspect is
      `RecomputeBookAggregates`
      (`internal/database/pebble_store_book_aggregates.go:131-134`), which
      **early-returns without calling `UpdateBook`** when the recomputed values
      equal the stored ones. `UpdateBook` is what triggers `UpsertBookToMemDB`,
      and that is the call which reloads `book_files` from Pebble
      (`internal/database/memdb_sync.go:53-55`). Skip the write and memdb keeps
      the stale file set.

      Why it matters beyond this op: any caller that deletes book_files and
      relies on the aggregate being visible has the same blind spot, and the
      library list computes duration from the memdb file map, not the stored
      field.

      Until it is fixed, `dedupe-book-file-rows` says so in its completion
      message rather than letting an operator conclude the run did nothing.

- [ ] **Restore the duration on `The Trapped Mind Project`**
      (`01KNDB97CWFSMSEY68P82VDRBF`). The first canary kept a fingerprinted row
      whose `Duration` was 0 and deleted the 129 twins that held the real value,
      leaving the book at 0.00h. The merge fix prevents recurrence but cannot
      undo it — the file itself is intact, so
      `maintenance.duration-reextract` will recover the value from ffprobe.
