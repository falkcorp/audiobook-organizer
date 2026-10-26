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
