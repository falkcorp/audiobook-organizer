### Fixed

#### Fingerprint windows: a timeout or cancel before the tools start is reported as the context error

`FileWindow` wrapped the error from `exec.Cmd.Start` with `%v` as a transient
ffmpeg/fpcalc failure. When the context was already done, Start returns
`ctx.Err()`, so a per-window timeout or a shutdown cancel that landed before
ffmpeg started came back as "ffmpeg failed: transient" with no
`DeadlineExceeded`/`Canceled` in the chain. Both Start sites now return the
context error, the same way the post-Wait check does. This was the cause of
the intermittent `TestFileWindow_ContextKill` failure (FLAKE-FPWINDOW).

#### Four flaky tests made deterministic

- `TestPendingBlocksOnlyTheIdleAcquires` (scanlock): waits on a `SetTrace`
  gate for the waiter to park instead of a 30ms sleep.
- `TestFileWindow_ContextKill` (fingerprint): cancels only after the fake
  ffmpeg is running, and proves the orphaned grandchild is still alive when
  `FileWindow` returns instead of asserting a 5s wall-clock bound.
- `TestWatchdog_TouchLivenessAloneKeepsOpAlive` (registry): the watchdog's
  idle clock is injectable (`registry.Options.LivenessNow`, tests only) and the
  test steps it below the timeout each round instead of racing 20ms sleeps
  against a 100ms timeout.
- `TestBulkWriteBack_ResumeSkipsCheckpointedBooks` (server): later progress
  callbacks wait until the midpoint cancel has landed, so a stalled cancelling
  worker can no longer let the pool finish every book first.
