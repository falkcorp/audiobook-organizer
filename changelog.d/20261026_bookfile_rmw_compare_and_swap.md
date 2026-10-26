### Fixed

#### `book_file` read-modify-write commits are now compare-and-swap

Every `book_file` writer that read a row and wrote it back (`SetBookFileHash`,
`ModifyBookFile`, `UpdateBookFile`/`UpsertBookFile`, `PatchBookFileFields`,
`SetBookFileIntro`/`ClearBookFileIntro`, the scan-cache stamp, the batch
upsert and create paths, `MoveBookFilesToBook`, and retire/unretire/rehome)
read the row before taking the owner stripe and committed on a key-exists
check only. A writer that takes no file stripe (the scanner's batch upserts, a
create claiming an iTunes ID, the fingerprint sweeps) could land in that window
and be silently reverted, leaving stale secondary-index keys (`book_file_hash:`,
`book_file_pid:`) behind. The commit now compares the stored bytes with the
bytes the writer read, under the owner stripe; on a mismatch the writer
re-reads, re-applies its own change (`ModifyBookFile` re-runs its callback, so
it may run more than once), and after `maxStageRetries` attempts returns the new
typed `ErrBookFileConflict` (`*BookFileConflictError`). Index maintenance is
derived from the freshly read row. `ClearAllAcoustIDFingerprints` and
`SweepBookFileSegDrop` now commit in chunks under the owner stripes with the same
compare; a row that keeps changing is left as stored and counted
(`SweepBookFileSegDropResult.Conflicts`, an error wrapping `ErrBookFileConflict`
from the fingerprint clear, which then skips the LSH wipe). `dedup.bookfile-seg-drop`
no longer marks itself done when rows were left unswept, and
`backfill-file-hashes` reports rows it skipped because they kept changing.

Follow-up review fixes on the same change:

- The two sweeps now commit 64 rows per chunk (was 256, which could pin most of
  the 256 owner stripes at once), give each row its own retry budget, follow a row
  that moved to another book while the sweep ran instead of counting it as busy,
  and give busy rows one more pass at the end. `ClearAllAcoustIDFingerprints`
  returns `*BookFileSweepConflictError` (still `ErrBookFileConflict`) when rows
  are left, and `acoustid.reset-all` then reports `Reset INCOMPLETE`, keeps the LSH
  index, and tells the operator to run it again instead of failing the op.
- `DeleteBookFile`, `DeleteBookFilesForBook` and `DeleteBookFilesByIDs` now share
  one commit that compares each row's stored bytes under the owner stripe (they
  checked existence at most), so a batch upsert that changed a row's hash or path
  mid-delete no longer leaves index keys pointing at the deleted row. A batch that
  keeps losing the race gives up after `maxBatchStageRounds` with an error
  wrapping `ErrBookFileConflict` and deletes nothing.
- Whole-batch writers (move, retire, unretire, rehome, batch upsert) share a
  bounded re-stage budget, and `tag-backfill` words a row that kept changing as
  "next run picks it up" rather than as a deletion.
- `maintenance.repoint-book-file-rows` journals one row per repoint however many
  times the callback re-runs, and voids it when the write is refused;
  `opswriter.Writer.ModifyBookFile` drops a discarded run's diff when a later run
  skips; `repairs.Writer.RepointBookFile` voids its journal row on
  `ErrBookFileConflict`.
- `UpsertBookFile` re-matches its row when a retry finds the PID it matched on has
  moved to another row, instead of pointing `book_file_pid:` back at the old one.
- `GetBookFileByID`, `GetBookFileByPath` and `GetBookFileByPID` decode straight
  from the Pebble buffer again (no copy of the stored row).
- Removed the dead `ClearITunesPID` and `MarkFileImportedFromDeluge` store
  methods (no production callers; both were whole-row read-modify-writes).

