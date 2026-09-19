### Fixed

- `TestSQLActivityStore_BackgroundCheckpointerRunsAndTruncatesWhenIdle` no longer flakes: it accepted a successful TRUNCATE that ran in a pause between its 300 writes, and the writes after it regrew the WAL before the 0-byte assertion (CI saw 407,912 bytes). It now counts only a TRUNCATE that happens after the last write. Test-only change.
