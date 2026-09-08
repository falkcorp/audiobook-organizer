### Fixed

- **File I/O pool could crash the process on shutdown.** `FileIOPool.SubmitTyped`
  guarded itself with an atomic `stopped` flag and then sent on the worker
  channel, while `Stop` set that flag and closed the same channel. A submitter
  that passed the check just before `Stop` ran sent on a closed channel — an
  unrecovered panic, because the pool's only `recover()` is inside the worker
  body, not in the submit path. Reproduced deterministically in a new test.
  Submitters now hold a read lock across the whole check-and-send and `Stop`
  takes the write lock before closing, so the window is gone.
- **`Stop` no longer returns while overflow work is still running.** When the
  500-slot buffer was full, `SubmitTyped` ran the job in a bare `go func()` that
  was never enrolled in the pool's `WaitGroup`. `Stop` waited only for the fixed
  worker set, logged "all jobs complete", and returned with that goroutine still
  executing — and it writes to the store, so shutdown could race a closing
  database. Measured before the fix: `Stop` returned with 0 of 2 overflow
  goroutines finished. They are now enrolled in the `WaitGroup`, so `Stop`'s
  wait is a complete join.
- A job that arrives while `Stop` is in progress and has already been persisted
  now leaves its `pending_file_op` row in place instead of dropping silently, so
  the work is recovered on next start rather than lost.
