### Fixed

- **`internal/plugins/maintenance` fits inside GitHub's 10-minute Go test
  budget again.** Frontend CI's Go job (the shared `reusable-ci.yml`) runs
  `go test -v -race -coverprofile ./...` with no `-timeout`, so each package
  has Go's default 10 minutes, and this repo cannot change that. The
  maintenance package hit it at 600.6s (the last green run took 555s). Its
  ~470s of fragment-fixer cut-point sweeps ran one after another because
  every fixture swapped the global `config.AppConfig.RootDir`.
  - The fragment fixer and `retireInto` now read the library root through
    `Plugin.libraryRoot()`. It returns the new `Plugin.rootDir` field when a
    test sets it, and `config.AppConfig.RootDir` otherwise, so production
    behaves as before.
  - The fragment-fixer fixtures set `rootDir` and no longer touch the
    global. 119 fragment-fixer and retire tests, plus the subtests of the
    seven biggest sweeps, now call `t.Parallel()`. Tests that still need
    the global (folder-books, duplicate-copies, and anything that sets
    `config.AppConfig` or env) stay serial through
    `newGlobalRootFragFixture`.
  - `newFragStore` now uses the in-memory Pebble store, as the cut fixture
    already did, so the ~170 fragment tests no longer pay an fsync on
    every write.
  - `frontend-ci.yml` explains that its Go job has a fixed 10-minute
    per-package budget, and that the durable fix is a timeout input
    upstream.
