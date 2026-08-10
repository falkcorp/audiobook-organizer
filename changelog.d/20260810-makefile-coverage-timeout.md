<!-- file: changelog.d/20260810-makefile-coverage-timeout.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7b2e5c81-94af-4d63-8e02-15c9a6f3d740 -->
<!-- last-edited: 2026-08-10 -->

### Fixed

- `make coverage` and `make coverage-check` could die with
  "panic: test timed out" on a slow or busy machine. Go's default test
  timeout is 10 minutes per package and `internal/server` alone takes about
  500 seconds; running packages in parallel makes them contend and tips it
  over. The failure names whichever test happened to be running when the
  clock expired, so it reads as a bug in an unrelated test. Both targets now
  pass `-timeout 25m`, matching the three sibling targets that already did.
