### Fixed

- **`TestServerStartGracefulShutdown` failed CI while the server was shutting down
  correctly.** The test allowed 5 seconds for shutdown, but the shutdown path's ops-registry
  step alone is granted 10 seconds (`server_lifecycle.go:580`), with a hardcoded 2-second
  goroutine drain inside it that the failing log showed firing twice. 4 of the 5 seconds went
  to deliberate waiting before any real work, so a correct shutdown lost a race with its own
  assertion on a contended runner — the log showed `Server exited` had already been printed.
  Raised to 60s with the arithmetic recorded in-code; a genuinely hung shutdown still fails.

### Internal

- Recorded an intermittent hang in the `internal/database` short-test suite that fails the
  coverage gate with `panic: test timed out after 25m0s`. Same commit passed on re-run, and
  the package passes locally and on `main`, so it is a stall rather than a regression — but
  the ceiling raised in #2270 (10m → 25m) has now been hit at both heights, so the timeout is
  not the fix. Filed with the evidence and next steps in `todo.d/`.
