<!-- file: changelog.d/20260803_194500_duration_display_unit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7a0c5e38-2f91-4b64-8d05-63c1a9e47f20 -->
<!-- last-edited: 2026-08-03 -->

### Fixed

- Book durations displayed as near-zero across the library. Four read paths
  divided `BookFile.Duration` by 1000 on the assumption it stores milliseconds.
  It stores **seconds** by convention — only about 2% of rows are milliseconds,
  written by the iTunes importer.

  Worse, the library-list aggregate applied that integer division **per row
  before summing**, so every file shorter than 1000 seconds contributed exactly
  zero. Hyperion listed 20 seconds against a stored 174,658, and 25,938 of
  44,886 books showed an implausibly small duration — every one of them a book
  that has files, while the books that looked correct were the ones with no
  files, which skip the aggregation entirely.

  All four sites now use `database.NormalizeDurationSec`, which divides only when
  the file's implied bitrate proves the value is milliseconds. Correct rows pass
  through untouched, genuine millisecond rows are still repaired, and books with
  mixed units are judged row by row.
