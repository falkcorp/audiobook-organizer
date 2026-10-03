- [ ] **OPS-V2-RESTART-FLAG** `Registry.Start` never clears `shuttingDown`, so a
      registry restarted after `Shutdown` never dispatches anything again.
      `Shutdown` sets the flag at `internal/operations/registry/registry.go`
      (`r.shuttingDown.Store(true)`), and nothing ever stores `false`.
      `dispatchCycle` returns at its first line while the flag is set, and
      since 2026-10-03 (PR #3701) `executeRun`'s shutdown pickup gate also
      drops every run while it is set. Yet `Start` resets `notifyStopped`, and
      the `logWriteSetDeferral` comment in `dispatcher.go` says Start is
      "explicitly restartable after Shutdown". Production builds a new
      registry per process, so it is not affected today; the trap is for
      tests and any future in-process restart. Done means: decide whether
      restart is supported. If it is, clear the flag at the top of `Start`
      (before the dispatcher and workers start) and add a
      Start→Shutdown→Start test that dispatches an op on the second Start. If
      it is not, have `Start` refuse a second call and fix the comment.
