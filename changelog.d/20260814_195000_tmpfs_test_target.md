### Added

- `make test-fast` / `make test-fast-short`: run the backend suite with TMPDIR
  on a RAM disk (auto-created at /Volumes/abo-test-ram on macOS, /dev/shm on
  Linux). The per-test Pebble setup is write-bound — measured 532s → 33.7s for
  internal/server, and 54.1s → 5.3s for internal/playlist on the day this
  landed. Opt-in only; default `make test` unchanged.
