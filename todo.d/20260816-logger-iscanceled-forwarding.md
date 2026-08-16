- [ ] **Forward `IsCanceled()` through `reporterLogger` and exercise the four guards it wakes up.**
      `LoggerFromReporter` now bridges `UpdateProgress` to the ops registry
      reporter, but `IsCanceled()` still delegates to the wrapped logger, which
      answers `false` unconditionally. That leaves four cancellation guards
      unreachable, as they have been since the 2026-05-11 BridgeQueue removal:
      `internal/scanner/service.go:190`, `internal/organizer/service.go:897` and
      `:1082`, `internal/reconcile/reconcile.go:597`.
      Cancellation itself is not broken — every one of these services also
      honours `ctx`, which is what the watchdog cancels — so this is a
      responsiveness and correctness-of-intent item, not an outage. It was held
      back from the progress fix deliberately: switching on four branches that
      have not run in three months, in the same change that unblocks production
      scanning, would make a bad first run impossible to bisect.
      Before flipping it: read each guard for what it does on the way out
      (partial state, half-written aggregates, skipped cleanup), and check
      whether `scanner/service.go:177`'s "both cancellation channels have to be
      checked here" comment still describes the intended behaviour once the
      logger channel is live.

- [ ] **Audit the other two silently-stubbed `StandardLogger` methods.**
      `RecordChange` and `ChangeCounters` (`internal/logger/standard.go:62-63`)
      are also empty/nil, so any operation running through
      `LoggerFromReporter` that records changes is discarding them the same way
      progress was being discarded. Determine whether the scanner/organizer
      change-tracking counters are consumed anywhere (activity feed, op summary)
      and, if so, whether they have been empty since 2026-05-11.
