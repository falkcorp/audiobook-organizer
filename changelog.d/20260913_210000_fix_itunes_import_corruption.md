### Fixed

- **iTunes sync no longer attaches an album to the wrong book.** When an
  album's persistent ID was not yet known, sync fell back to a bare-title index
  and a last-wins path index. When two books shared a title or a path, the
  album's PID, play count, rating, bookmark and track files went to whichever
  book was indexed last. The title fallback is gone. A path held by several
  books is now logged and skipped.
- **iTunes sync no longer duplicates organized books.** Sync looked a book up
  only by the first track's PID and by the iTunes path. An organized book's
  path is under the library root, and albums whose tracks share disc/track
  numbers (0/0) put a different track first on each run, so both lookups
  missed and sync created a second book, moving the track files' PIDs onto
  it. Tracks now sort with a PID tie-break, and before creating a book sync
  checks every track's PID on book files. A match on several books, a failed
  lookup, or a match only on books marked for deletion skips the album and is
  counted in the sync summary.
- **Position sync counts a finish once.** Every run added a play to any
  finished book whose position had moved in the last 24 hours, and a first fix
  still counted any position written after a manual "finished" as a new
  finish. A user book state now records when the book became finished
  (`finished_at`, stamped by the store on the change into finished, whatever
  wrote it). The play count goes up only for a finish newer than the last one
  counted (`itunes_play_count_bumped_at`), and a finish seeded from iTunes'
  own play count is recorded as already counted. The bookmark and the bump
  are one locked write. State read errors are now logged and counted instead
  of dropped.
- **Import no longer reverts edits made while it works.** The organize phase,
  hash validation, the blocked-hash soft delete and the sync playback write all
  used to write back a copy of the row read before the slow step (file copy,
  hashing). They now change only their own fields on the current row, via
  `ModifyBook` (or `SnapshotBook` plus `MergeBookChanges` for organize).
- **Linking an existing book no longer changes its version group.** The link
  step created a new group for a groupless book and forced every linked book to
  be primary. That produced groups with two primaries. It now adds only the
  iTunes fields.
- **Re-importing a library no longer duplicates books.** The check for an
  existing book at the same path ran only with "skip duplicates" turned on. It
  now always runs, together with a lookup of each track's persistent ID in
  book files. The path check reads every live book at the path, so two books
  there are seen as ambiguous rather than as whichever was written last. When
  the keys point at different books the album is skipped and nothing is
  created. An album whose only matches are books marked for deletion is also
  skipped and reported in the import errors: restore the book to re-link it,
  or purge it to import a fresh one.
- **A failed Deluge import can be retried.** If recording the new path failed,
  the copied file stayed in the library, the caller's record pointed at the
  copy, and every retry failed. Now the copy is removed and the record is
  restored. An identical file already at the destination is adopted, but only
  when no other book file row claims that path; one that does is refused.
  Imports into the same destination run one at a time, so one import cannot
  adopt a copy another is removing. Sizes are compared before hashing. Both
  callers report any leftover copy.
