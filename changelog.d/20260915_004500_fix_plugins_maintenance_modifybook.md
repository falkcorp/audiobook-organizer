### Fixed

- Sixteen plugin maintenance ops no longer revert fields another writer saved
  while they were running. Each read a book row (often before slow ffmpeg,
  whisper, ffprobe or fingerprint work, several inside worker pools), changed a
  field, and wrote the whole stale row back, so a metadata apply or edit that
  landed meanwhile was lost. They now write only their own columns through
  `ModifyBook`, under the book's write lock, and re-make their "still needs
  this" decision on the fresh row:
  - `intro-transcribe` writes only the transcribe columns for every per-book
    outcome, only the silence sentinel on a book still without a transcript,
    and only the three parsed columns on reparse while the stored transcript is
    still the one the parse came from.
  - `duration-reextract` writes only `Duration`, and only while the stored
    duration is still the one the diff was measured against; the
    `DurationVerifiedAt` stamp writes only that column.
  - `author-split` (the twin of the scheduler's split) sets only
    `AuthorID`/`Author` while the primary is still the composite.
  - `author-id-repair`, `author-strip-merge` and `author-conjunction-repair`
    repoint only `AuthorID`/`Author` while the primary still names the row
    being repaired.
  - `title-repair`, `title-backfill` and `repair-junk-titles` share one title
    write that sets only `Title` while the stored title is still the one the
    new title was derived from.
  - `series-phantom-repair` and `series-denumber` write only `SeriesID` (and a
    missing `SeriesSequence`) while the book still holds the series in
    question.
  - `repair-transcribe-status` re-classifies the fresh row and writes only the
    two status columns.
  - The regroup version-group apply, the filesystem regroup survivor/retire
    path and the iTunes regroup title write set only the group, primary, title
    and path columns they own.
  - The booksig recovery restore fills `Description` and the signature columns
    only where the fresh row is still empty.
  Lost-update tests (`internal/plugins/maintenance/lost_update_test.go`) pin
  the transcribe outcome, duration stamp, author split and title writes against
  a concurrent column write.
