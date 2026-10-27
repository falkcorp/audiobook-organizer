### Added

#### Retired-version guards on purge, trash, restore and auto-promote

The fourth stacked change for retired book versions. A retired version is one a
live book keeps out of its own file list. Until now a version with no rows left
in it could still be trashed, restored or purged, and a trashed book that only
held retired versions was reported as an ordinary book that owns files. The
version lifecycle (trash, restore, purge now, the nightly cleanup) now refuses a
retired version with its own error (`versions.ErrRetiredVersion`), the version
routes answer 409 for it, and auto-promote never picks one.

Purging trashed books now tells a holder of retired versions apart from a book
with files of its own. `database.CheckBookOwnsNoFiles` wraps the new
`database.ErrBookHoldsRetiredVersions` (as well as `ErrBookOwnsFiles`) when every
row that blocks the delete is retired. The purge, its preview and the carry
precheck report such a book under the `holds_retired_versions` reason
(`skipped_holds_retired_versions` and its id list in the result, in the
summary, in the capped operation result and in the decided count), and the
hard delete, discard-progress and purge-now routes answer 409 with the
`HOLDS_RETIRED_VERSIONS` code instead of `OWNS_FILES`. The trash listing's
breakdown still counts such a book under `owns_files`; it has no separate
figure yet.

Restoring a trashed holder keeps reading only its live files to decide whether
it is organized, so a retired version stored outside the library folder does not
demote it to imported, and its retired versions come back untouched. Restoring a
trashed book that is the shell of a retired version still holding its files is
refused with a 409 saying the book stays in the trash and its files are safe,
instead of bringing back a book with no files. The version restore route also
answers 409 (not 400) for a retired version. Purging a trashed version (the
Purge now route and the nightly cleanup) no longer removes a file that any other
book_file row, retired ones included, still names, nor one that any other book
names with its own file path, a trashed (restorable) book included, and it stops
without deleting anything when it cannot tell who names a path or when the store
cannot answer the all-books lookup at all. Nothing retires a row yet, so
running behavior does not change.

The web client knows the new refusal. The purge dialog lists a book that holds
only retired versions as its own kept reason ("hold file rows kept for another
version of the book (retired versions; the purge never deletes those)") and counts it as decided, and a delete, purge-now or discard
refusal with the `HOLDS_RETIRED_VERSIONS` code shows a message saying the book
has file rows kept for another version of the book (retired versions), which
cannot be moved, instead of the generic error. A file row that lands after the
purge's pre-check and refuses the final delete is reported under the same
reason, not as a book that owns files.

#### Version purge: re-checked and locked per file, a stuck purge ends in a state, kept files are reported

A review of the version purge (Purge now and the nightly trash cleanup) found
that it deleted files it should not have, left versions stuck, and reported
partial purges as complete.

- Before each file is removed, the purge asks again whether another book_file
  row (retired ones included) or another book names the path, and it holds the
  merge lock from the claim until the last file is gone, so a row that lands
  between the decision and the removal can no longer lose its file. A merge or
  combine that holds the lock makes Purge now answer 409 with nothing started.
  The nightly cleanup waits up to ten minutes for the lock, then stops at the
  first version that could not get it instead of waiting once per version; it
  does not count that as a failure, reports `stopped_for_merge` in the result
  and the log, and the next night purges the rest.
- A version purge now applies the book purge's own file checks
  (`planPurgePathIn`, one function for both): it never deletes a file under an
  import path, the iTunes library or a `.failed` folder, nor one outside the
  library root, and it does nothing at all while the library root does not look
  mounted (an unmounted library made every file read as already removed).
  Purge now answers 503 for that.
- A file that can never be removed (permission, read-only filesystem) ends the
  version in the new terminal status `purge_failed`, with the file and the
  reason recorded on the version, instead of leaving it in `purging` and
  failing every night. Purge now answers 409; the Trash tab shows the failure
  with a Retry Purge and a Hard Delete. The nightly cleanup reports such a
  version as needing attention and does not retry it. A transient error keeps
  the version `purging`, to be retried. A row naming a directory that still
  holds files is kept and reported, not a failure.
- Files a purge kept (another row or book names them, a protected path, a
  folder that still holds files) are recorded on the version (`purge_kept`),
  and the trash-cleanup operation's log and result say "purged N versions, M of
  them with files kept (versions ...)" and carry the kept versions, books and
  paths, and the versions that need attention.
- A purge whose book is gone still removes the version's `.versions/{id}` slot
  files, with the same checks, and removing a version's last file removes the
  slot directory it leaves empty.
- The Trash and Purged tabs of the version page work: `GET
  /api/v1/audiobooks/trashed-versions` and `/purged-versions` were called by
  the page but never registered.

#### Version purge: a retry trusts only what it removed, a vanished root is transient, hard delete refuses a running purge

A second review of the version purge found three ways it could still go wrong,
and several smaller ones.

- A retried purge used to treat the folder of every path still to be deleted as
  a folder the earlier attempt had emptied, so a nested folder on an unmounted
  drive read as legitimately gone. `database.BookVersion` now records
  `purge_unlinked` (each path actually removed, written after the removal and
  cleared when the purge finishes), and a retry seeds only those folders. A
  crash between the removal and the record leaves a path unrecorded, which only
  makes the retry stricter.
- `guard.RootReady()` runs again once the merge lock is held, and a
  `PurgeDecider.Decide` that cannot resolve the library root (the path vanished,
  `fs.ErrNotExist`) answers with a transient error instead of a permanent keep,
  so the version stays `purging` and the next run decides again. The rename of
  `PurgeDecider.Unlinking` to `Unlinked` reflects that it is now called after
  the removal.
- `PebbleStore.DeleteBookVersion` refuses a `purging` version
  (`ErrVersionPurging`); the hard-delete route answers 409 for it.
- `purgeVersionLocked` re-reads the version after taking the lock, so a version
  restored or purged between the read and the lock is not purged from a stale
  copy. Purge now answers 409 "already purged" (`ErrVersionAlreadyPurged`) when
  the nightly cleanup got there first, instead of saying it was restored.
- ENOTEMPTY and EEXIST are transient removal errors, not permanent ones.
  `pruneEmptyDirs` no longer enters a symlink. An ENOENT from a removal the
  decider had approved re-runs the decider before the file counts as removed.
  The import-path lookup failing now keeps the file (`isProtectedPath` fails
  closed on the deletion paths).
- The trash cleanup counts consecutive nights it stopped for the merge lock
  (`MergeStopStreak`, kept in the raw KV store). At
  `versions.MergeStopEscalateAfter` (3) the operation logs a warning and sets
  `stopped_for_merge_repeatedly` and `stopped_for_merge_nights` on its result,
  which the operation panel shows; the operation still completes. The wait is
  `sync.Mutex.TryLock` based, which can starve under Go's starvation mode; that
  limit is documented on `BackgroundMergeLockWait`. A failed run now logs the
  same summary line a successful one does.
