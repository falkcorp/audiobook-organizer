- [ ] **RESUME-SWEEP-INDEX-CONSTRAINT** Whatever fixes the v2 resume sweep's
      blindness to `interrupted_*` rows, it must **not** widen the `opv2:act:`
      index. Use a separate key prefix.

      This is a hard constraint on
      [[20260823-v2-resume-sweep-is-blind-to-interrupted-rows]], whose most
      obvious fix is exactly the forbidden one. The sweep reads the
      queued|running active set, `interrupted_*` rows are not in it, and the
      tempting one-line fix is to keep them in it.

      **Why that breaks the maintenance window.** Verified in code
      2026-08-23, not assumed:

      - `UpdateOperationV2Status` maintains the act key in the *same Pebble
        batch* as the row write, committed with `pebble.Sync`
        (`pebble_store_ops_v2.go:273-281`): `status == "running"` sets it,
        `status != "queued"` deletes it. A run ending `interrupted_quiesced`
        therefore leaves the active set atomically with the status change.
      - `hasActiveV2Op` (`scheduler/maintenance.go:99-113`) returns true for
        **any** row `ListActiveOperationsV2()` returns whose `DefID` matches.
        There is no status filter — the index membership *is* the answer.
      - `IsTaskRunning` (`scheduler/maintenance.go:123`) is that function, and
        `scheduler_maintenance_window_op.go:150` skips a task when it returns
        true.

      So an `interrupted_*` row retained in `opv2:act:` makes `IsTaskRunning`
      answer true **forever** for that def. The maintenance window then silently
      skips every remaining run of it. Not a crash — a skip that reports
      success, which is the hardest shape to notice.

      `library.scan` ends `interrupted_quiesced` on nearly every run
      (8 of 9 prod rows, 2026-08-21..23), so it would be among the first
      affected.

      **Also note this is now load-bearing in a way it was not before.** PR
      #2831 makes `WaitForOperation` genuinely block until terminal, where it
      previously returned on the first tick. That promotes `IsTaskRunning` from
      a hint to the only thing preventing the interval ticker and the
      maintenance window from double-launching the same def.

      Raised by a parallel session working #2831; the invariant above was
      re-verified against `origin/main` here rather than taken on trust. No test
      pins it yet — that test belongs with whoever changes the sweep, and should
      assert that a def whose last run ended `interrupted_quiesced` is
      schedulable again.
