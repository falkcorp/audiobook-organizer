- [ ] **WHISPER-DISPATCHER-TOTAL-CAP-SETTER** `aidispatch.WithTotalCap` was
      deleted from production (it lives in `dispatch_testseams_test.go`), so
      `Dispatcher.totals` is set by tests only. `internal/transcribe/inflight.go`
      says Whisper will move onto the dispatcher in "PR 3"; that move must
      reintroduce a production way to set the pool-wide cap (group `whisper`,
      limit `whisper_max_in_flight`), or the `whisper_max_in_flight` cap is
      silently dropped.
