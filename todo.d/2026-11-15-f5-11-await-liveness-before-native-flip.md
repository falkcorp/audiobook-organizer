- [ ] **TODO-F5-11-AWAIT-LIVENESS** Do not register native `books.maintenance.library-optimize`
      until core's `await` keeps the waiting run alive. `rc.Await`'s doc
      (`core/tasks/rc.go`) says it relays liveness, but the executor's await
      (`core/internal/executor/dispatch.go`) never touches it, so a run blocked on a
      child longer than its `ProgressTimeout` (65 minutes for optimize) is stopped by
      the watchdog as a cancel, and a cancel is never resumed. The native optimize
      ticks liveness itself while it waits (`optimizeAwaitTicking`), so it is covered
      today; acceptance is that core's await touches (or the doc is corrected) and a
      core test fails when it stops, after which the F5-11 tick may stay as a second
      line or go. F5-18 blocker, beside TODO-F5-11-HOLD-RENEW. Notes:
      `docs/agent-tasks/platform-core/seams.d/F5-11.md`.
