### Fixed

- `TestSQLActivityStore_BackgroundCheckpointerRunsAndTruncatesWhenIdle` no longer fails
  intermittently under `-race`. The test polled for a 0-byte WAL file and then checked that
  the checkpoint hook had seen a TRUNCATE. The file is already empty when the TRUNCATE
  statement returns, and the hook runs after that, so a poll could land in between. The test
  now waits for the hook to report a successful TRUNCATE and then checks the file. The
  checkpointer itself was not starving: an instrumented probe saw TRUNCATE in 850 of 850
  `-race` runs.
