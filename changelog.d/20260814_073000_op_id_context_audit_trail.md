### Fixed

- **Scheduled maintenance operations recorded no undo history.** Eight operations
  — including the nightly purge of soft-deleted books, the series prune, temp-file
  cleanup and metadata write-back — record what they changed so the change can be
  reviewed later and, where supported, reverted. Each looks up the ID of the
  operation it is running under, and skips recording when that ID is missing. The
  code that supplies the ID existed but was never connected to anything, so the
  ID was always missing and every one of those operations skipped recording, with
  no error and a successful-looking result. A series prune on 2026-08-14 deleted
  326 rows and recorded zero changes. The ID is now attached to every operation
  run, so the history is written again. This also means an empty change list can
  once more be read as "this operation changed nothing" rather than "the recording
  was disabled" — during the investigation above, the two were indistinguishable.
