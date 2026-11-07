- [ ] **TRACING-INIT-ERROR** `internal/telemetry/telemetry.go` `InitOTEL`: when `initTracing`
      returns an error (`metrics.New` refused the tracing half's Options), the `err != nil`
      branch sets `tracingOff` and drops the error, so the start-up summary says tracing is off
      without the reason. Log it (redacted, through `emit`) (R1-10 review MINOR-7).
