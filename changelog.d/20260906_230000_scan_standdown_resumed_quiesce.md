### Fixed

- **A library scan can now be paused/quiesced promptly while it is discovering
  files, and a resumed scan is visible again in the UI.** The scanner's
  discovery/scan phase (`ScanDirectoryParallel`'s directory walk and per-directory
  worker loop, plus `groupFilesIntoBooks` and the pre-scan count pass) accepted a
  cancellation context but never checked it, so a scan cancelled during that phase
  kept walking for minutes — on a large import root, ~9 minutes, far past the
  operations registry's 5-second abandon grace. That is why the scan stand-down
  (used by DB-writing repair ops so a scan can't clobber their writes) failed to
  quiesce a scan that had resumed after a restart: it abandoned the still-running
  goroutine, waited out the full 5-minute lease on a handle nothing would signal,
  and re-dispatched a second scan. The phase now checks the context between
  directories and files (matching the per-book pass, which already did), so a
  quiescing scan aborts within about a second and the stand-down parks it cleanly.
- **A resumed operation no longer disappears from the Activity → Active Operations
  panel.** A restart-resumed op reuses its database row, which still carried the
  completion timestamp stamped when it was interrupted; the resume path could not
  clear that timestamp, so the running op was filtered out of the live timeline. The
  resume now clears it (`ResetOperationV2ForResume`), leaving the queued-at time
  untouched, so resumed scans and other resumed ops show as running again.
