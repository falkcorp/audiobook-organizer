### Fixed

- The chapter-extraction idempotency test skipped on every CI run. It guarded on
  `ffprobe` and an audio fixture it never used — the code path returns before any
  probe or file access — and no workflow installs ffmpeg. The assertion that a
  rescan does not re-extract and overwrite an existing chapter list had therefore
  never executed in CI. The guards are gone; the test now runs everywhere.

### Changed

- The ABS contributor-cache warm takes its store as a function argument
  (`spawnContributorWarm`) instead of capturing it. The store must be read before
  `Server.Start` overwrites that field, and that ordering was previously held in
  place only by a comment; as a parameter it is a language guarantee, since Go
  evaluates arguments in the caller. It is also now testable — the old inline form
  could only be reached through `wireABSRoutes`, which returns early unless
  `ABS_API_ENABLED` is set and calls `os.Exit(1)` on a misconfigured ABS block, so
  `-race` never entered the path at all.

### Added

- Mocks and behaviour tests for two capability resolvers: `warmupWaiter` (the
  contributor cache must not be built before the memdb warmup completes) and
  `keyCounter` (the db-health Pebble section is omitted when the capability is
  absent, but reports zeros when the count *fails* — an asymmetry that is easy to
  collapse into a bug). All six tests were mutation-verified.
