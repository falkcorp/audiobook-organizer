### Fixed

- `maintenance.chapters-backfill` now CLEARS a degenerate stored chapter
  timeline when the container has no markers to restore, instead of leaving the
  junk in place. The first production repair run fixed 21 of 31 books and left
  10 holding a zero-length chapter, because `overwrite` could replace a wrong
  timeline but the no-markers branch returned before any write. Clearing lets
  the live one-chapter synthesis take over, which is navigable. Gated on the
  stored list being broken on its face — a book whose container merely lost its
  markers keeps the only surviving copy of its timeline.
