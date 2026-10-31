<!-- file: docs/decisions/2026-10-31-transcribe-flakes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f8a2d6c-9b13-4e57-a0c4-6d1e8b3f7a95 -->
<!-- last-edited: 2026-10-31 -->

# Transcribe test flakes: cancel after the gate, and the 10 ms probe timeout

## Decision log

- Cancel between the gate and `dispatchSingle`: fixed in production code by returning the
  cancellation at the top of `dispatchSingle`. Rejected: making only the test tolerant (the
  test was right; the dispatcher really skipped the canary check and sent work on a dead
  context), and rejected: dropping the `ctx.Err() == nil` guard on the remembered-machine
  block without an early return (the unremembered path would still send on a dead context).
- The cancel test no longer uses a 50 ms timer: it cancels from `poolOptions.extraRefusal`,
  which runs inside the gate, so the cancel lands at a known point. Rejected: a fake clock
  (the wait uses `sleepCtx` and the real cooldown clock; hooking the gate is smaller and exact).
- `healthProbeTimeout` narrowing moved out of `unverifiedWindow` into `narrowProbeTimeout`,
  called after the setup probe. Rejected: raising the 10 ms (it is the window size the test
  depends on: `livenessInterval+healthProbeTimeout` must stay below the 300 ms request).
