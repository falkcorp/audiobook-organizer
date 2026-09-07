- [ ] **Make the scan stand-down (and the UI) target a scan's resume execution
      handle, not just its outer op id.** A `library.scan` that is resumed after a
      restart executes under a fresh resume handle (e.g. outer op `01M1W7PQYJ…` runs
      its scan work under `01M1WKTBA8…`). `AcquireScanStandDown` registers + waits on
      the outer op id's `parked` channel, which the inner handle never signals, so the
      acquire times out after the 5m lease and any apply that needs the gate aborts
      with "scan did not park within 5m0s" (0 writes). The same root cause makes a
      resumed scan **invisible in the Activity → Active Operations panel**. Confirmed
      live 2026-09-06: a user-cancel of the OUTER id *does* stop it (the inner ctx
      derives from the outer handle, so cancel propagates), which is why cancelling
      works but quiescing does not. Fix: register the resume handle as the canonical
      running op for that op id (or have the stand-down find + wait on the innermost
      running scan handle). Add a test that quiesces a resumed scan. NOTE: reflink-only
      `recover-missing-files` runs no longer need this — they skip the stand-down (they
      do no DB write); this is for the DB-writing ops (`missing-file-repoint`,
      `mark-missing-files`, Branch A repoint) and general "all scans must be pausable".
