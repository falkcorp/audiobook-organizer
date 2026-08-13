### Added

#### Chapter backfill op — single-file audiobooks were all serving one chapter

Every audiobook that predates 2026-07-30 has been served over the ABS API with a
**single chapter spanning the entire file**. Measured on production before the
fix: 500 sampled library items all had `numChapters == numAudioFiles`, and all
213 single-file containers longer than 10,000 s reported exactly 1 chapter — a
24-hour audiobook delivered as one unnavigable block titled with the book's own
name. Meanwhile 19 of 40 randomly probed `.m4b`/`.m4a` files on disk carry real
embedded markers, 16 to 118 chapters each.

Root cause: chapter extraction is a write-path-only feature.
`SaveChaptersForBook` has exactly one non-test caller,
`scanner.PersistChaptersForBook`, reached only from the `saveBook` success
branch. The feature shipped after the library was scanned, and incremental scans
skip unchanged files via the scan cache, so no pre-existing book was ever probed
and none ever would be. Production logs show zero extraction warnings across 14
days — it was not failing, it was never running.

The failure stayed invisible because the read path has a fallback: with nothing
persisted, the ABS mapper synthesizes chapters from the track list. For a
multi-file book that yields one chapter per file, which looks right. For a
single-file book it yields one chapter for the whole book, which looks like a
book that simply has no chapters.

New op `maintenance.chapters-backfill` probes single-file books with no persisted
chapters and writes the real timeline. Dry run unless `{"apply": true}`, refuses
to start without `ffprobe`, bounded worker pool at `runtime.NumCPU()`, and
`{"bookIds": [...]}` restricts a run to an explicit cohort. It writes only the
`chapters:` keyspace — no book row is touched, so the write-back-wipe class does
not apply, and `DeleteChaptersForBook` reverts a book to today's behaviour.

Multi-file books are skipped deliberately: their persisted form is byte-identical
to the mapper's live synthesis, so writing it changes no response while creating
a staleness hazard the read path cannot detect (`len(stored) > 0` short-circuits
the live synthesis, so a stale persisted list would silently win after a book
gains or loses a file).
