- [ ] **ZOMBIE-FOLLOWUPS-05PR3** Follow-ups from the PR #3935 (05-PR3) review
      that were left out of that PR:
  - [ ] **N4: Shutdown waits out its whole context while a genuine zombie
        lives.** The drain poll in `Registry.Shutdown`
        (`internal/operations/registry/registry.go`) counts `len(r.running)`,
        and a zombie stays there until its goroutine exits. So server shutdown
        (`server_lifecycle.go`, 10 s context) always burns the full 10 s,
        returns `ctx.Err()`, and logs "shutdown timeout; marked remaining ops
        as interrupted" even though it skipped the zombie. Before 05-PR3 it
        drained at once. Options: exclude `slotReleased` handles from the drain
        count (`goroutineWG.Wait` still bounds them), or fix the log line.
  - [ ] **N5: The watchdog relies on row status to skip zombies.** If the
        abandonment terminal write fails ("failed to mark abandoned op
        interrupted" in `worker.go`), the row stays `running` while the zombie
        handle stays in `r.running`. The watchdog then writes a `stuck` strike
        every cycle and cancels again, and the EnqueueOp key dedupe returns the
        zombie's id. Add an explicit `h.zombie.Load()` (or `slotReleased`) skip
        to the watchdog loop (`watchdog.go`).
  - [ ] **N6: Rows in unstarted partitions get no RowResult on cancel.**
        `runItemsPar` stops handing out partitions once the context is done, so
        rows in partitions that never started are missing from `res.Rows`.
        Rows in started partitions are reported `canceled_skipped`, so the
        counts understate what was skipped. After `RunItems` in `RunApply`
        (`internal/repairs/engine.go`), settle `canceled_skipped` for every
        selected row that has no result.
  - [ ] **A user cancel of a shutdown-branch zombie is stamped over.** Cancel
        now writes `canceled` to the zombie's still-`running` row, but the
        timeout loop in `Shutdown` then stamps `interrupted_*` over it. That
        loop skips only `slotReleased` handles and ignores `userCanceled`. The
        same was true before 05-PR3 for any handle the user canceled during a
        shutdown. Decide whether a user cancel should win against the shutdown
        stamp.
