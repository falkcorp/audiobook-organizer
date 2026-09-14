### Fixed

- **iTunes sync no longer attaches an album to the wrong book.** When an
  album's persistent ID was not yet known, sync fell back to a bare-title index
  and a last-wins path index. When two books shared a title or a path, the
  album's PID, play count, rating, bookmark and track files went to whichever
  book was indexed last. The title fallback is gone. A path held by several
  books is now logged and skipped.
- **Position sync counts a finish once.** Every run added a play to any
  finished book whose position had moved in the last 24 hours. The count is now
  bumped only for a finish newer than the one it last counted
  (`itunes_play_count_bumped_at`). The bookmark and the bump are written together
  in one locked write.
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
  book files. When those keys point at different books, the album is skipped
  and nothing is created.
- **A failed Deluge import can be retried.** If recording the new path failed,
  the copied file stayed in the library, the caller's record pointed at the
  copy, and every retry failed. Now the copy is removed and the record is
  restored. An identical file already at the destination is adopted instead of
  refused. Both callers report any leftover copy.
