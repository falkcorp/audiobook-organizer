<!-- file: changelog.d/20260730_060700_abs-sync-scanner-chapter-hook.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4240a974-775f-489f-9e3f-491ccd12544d -->
<!-- last-edited: 2026-07-30 -->

### Added

- **Chapter extraction at scan time (abs-sync Phase 4).** New
  `scanner.PersistChaptersForBook` (`internal/scanner/process_file.go`) wires the
  already-merged `internal/audioutil` chapter primitives and the newly-merged
  `database.Chapter` persistence into the scan pipeline: single-file books keep
  their embedded chapters as-is; multi-file books get one synthesized chapter per
  file, titled from each file's own tag. Multi-file chapter boundaries use
  re-probed, unrounded per-track durations (not the stored, rounded
  `BookFile.Duration`), so the total matches real Audiobookshelf `startOffset`
  precision. Idempotent — a rescan skips re-extraction for books that already
  have chapters.
  - **Not yet wired into `scanner.go`.** This PR adds the function and its full
    test suite but intentionally does not add the two `scanner.go` call sites
    described in the task brief, because `scanner.go` was flagged as owned by a
    parallel in-flight change at the time this task ran. The feature is inert
    (dead code, covered by direct-call tests only) until a follow-up applies the
    two-line hook after each of `scanner.go`'s book-save call sites.
