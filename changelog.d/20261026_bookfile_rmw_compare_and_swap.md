### Fixed

#### `book_file` read-modify-write commits are now compare-and-swap

Every `book_file` writer that read a row and wrote it back (`SetBookFileHash`,
`ModifyBookFile`, `UpdateBookFile`/`UpsertBookFile`, `PatchBookFileFields`,
`SetBookFileIntro`/`ClearBookFileIntro`, the scan-cache stamp, the batch
upsert and create paths, `MoveBookFilesToBook`, and retire/unretire/rehome)
read the row before taking the owner stripe and committed on a key-exists
check only. A writer that takes no file stripe (a create claiming an iTunes ID,
the bulk moves, the deletes, the fingerprint sweeps, the scan-cache stamp) could
land in that window and be silently reverted, leaving stale secondary-index keys
(`book_file_hash:`, `book_file_pid:`) behind. (`BatchUpsertBookFiles` now takes
the file stripes of the rows it rewrites, so it no longer races the single-row
writers; the compare still covers it against the writers above.) The commit now
compares the stored bytes with the
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
`backfill-file-hashes` files a row whose write the store gave up on after other writers kept changing it (`ErrBookFileConflict`) under the report's `busy` bucket, retried next run, not as a write error.

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
  checked existence at most), so a writer that changed a row's hash or path
  mid-delete (a bulk move, a PID transfer) no longer leaves index keys pointing at the deleted row. A batch that
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

Second round of review fixes:

- `DeleteBookFilesByIDs` now takes `[]BookFileRef` (book and file id) and returns
  the rows it deleted. A row that now belongs to another book than the caller
  judged it under, or that is listed under two books, is refused with the new
  `ErrBookFileOwnerChanged` (nothing deleted, not retried as busy). The dedupe
  delete journal rewrites a row whose pre-image differs from the bytes actually
  deleted, and voids every journal row when the delete deleted nothing.
- A batch whose rows keep changing past the re-stage budget now refuses the
  stale rows (`*BookFileRowsRefusedError`) and commits the rest, instead of
  abandoning the whole batch; the hard backstop at twice the budget still gives
  up with `ErrBookFileConflict`. Between rounds the batch backs off with jitter
  and re-reads only the rows that changed.
- A combine whose absorbed files are busy now answers as a refusal before moving
  any file (the error wraps `ErrBookFileConflict`) instead of leaving a half-done
  combine, and `merge.IsRetryLater` now recognises it, so the combine operation
  ends as "retry later" (`BookCombineOpResult.RetryLater`) rather than failed. (A review approve whose
  bulk move is busy is held as `REVIEW_APPLY_PARTIAL`; `REVIEW_RETRY_LATER` is the
  library-index-still-loading case.)
- A row stored under a key that names another book than its own `BookID` now
  fails the deletes with the new `ErrBookFileKeyMismatch` instead of reading as
  vanished and ending as a busy row no retry can clear. The two sweeps rewrite
  such a row in place, at the key it is stored at, as they always did.
- A sweep chunk refused over one changed row re-stages only that row.
- `maintenance.repoint-book-file-rows` voids a repoint's journal row only when
  its write provably did not happen, the rule `repairs.Writer.RepointBookFile`
  already follows.
- The incomplete-reset message of `acoustid.reset-all` no longer tells the
  operator the rescan was not queued (the reset endpoint queues it either way).
- `DeleteBookFilesByIDs` commits in chunks of 64 rows, so the stored bytes it holds
  to compare at a commit stay bounded (about a gigabyte for a 2,000-row call of
  fingerprinted rows before). Refusals are checked once at the start of the call,
  not held for its whole length; a later chunk that fails after earlier ones
  committed returns the rows that are gone together with the error, and the dedupe
  delete journal keeps those rows' journal and voids only the others.

Third round of review fixes:

- The dedupe and cross-folder row cleanups now report a chunked delete that
  committed some chunks and then failed as partial: the rows the store reports as
  deleted are counted as deleted and their books recomputed, the rest are marked
  failed, and the run summary says so.
- The combine operation's retry-later message (`COMBINE_RETRY_LATER_MESSAGE`, and
  the server's matching result message) now says the library is still loading
  or other writers are changing the files the combine moves, and that no files
  were moved from the other books, instead of naming only the loading library;
  the review lane's `REVIEW_RETRY_LATER` toast no longer claims the index is the only cause.
