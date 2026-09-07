- [x] **A resumed `library.scan` could not be quiesced by the scan stand-down, and
      was invisible in the UI.** RESOLVED in the scan-standdown-resumed PR. Root
      cause was NOT the resume handle (the stand-down cancels the correct handle and
      the ctx does propagate) — it was a ctx-plumbing gap: `ScanDirectoryParallel`'s
      discovery walk + per-directory worker loop (and `groupFilesIntoBooks`,
      `countFilesAcrossFolders`) accepted or ignored `ctx` but never checked it, so a
      scan cancelled during that phase ran for minutes (measured ~9m on a 12k-dir
      root, confirmed in the 2026-09-06 20:15 journal) — long past the registry's 5s
      abandon grace, so the stand-down abandoned it, hung on the dead handle's
      `parked` for the full 5m lease, and re-dispatched a second scan. Fix threads
      `ctx.Err()` checks into those loops (mirroring `ProcessBooksParallel`), so a
      quiescing scan aborts in ~1s and parks cleanly inside the existing grace.
      Visibility (B1): `ResetOperationV2ForResume` clears the interrupt-time
      `CompletedAt` on resume so the running resumed op reappears in Active Operations.
- [ ] **Defense-in-depth: the abandonment path never closes `parked`.** Independent
      of the scan fix, if ANY op's goroutine is abandoned mid-quiesce (worker.go
      abandonment branch, 5s grace), a stand-down waiting on that handle still orphans
      until the 5m lease instead of failing fast. The scan fix means this no longer
      fires for scans, but the general hole remains. Fix: on abandonment, wake a
      waiting stand-down with a "could not park" error rather than burning the lease.
