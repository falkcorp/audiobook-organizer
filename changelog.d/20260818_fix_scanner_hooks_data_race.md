### Fixed

- `scanner.SetScanHooks` wrote an unsynchronised package-level global that scan
  worker goroutines read from `saveBookToDatabase`, so installing or clearing
  hooks raced with any scan still running. Because the operations registry runs
  scans in the background, a test's deferred `SetScanHooks(nil)` genuinely
  overlapped a scan started by an earlier test — which is how it surfaced, as
  `race detected during execution of test` in `TestAddImportPathFallbackScan`.
  The global is now guarded by an `RWMutex` and read once through a helper, which
  also closes a nil-check TOCTOU: the old code re-read the global three times, so
  hooks could go nil between the check and the call.
