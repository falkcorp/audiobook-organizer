- [ ] **METRICS-DEGRADED-TOKENS** core's `metrics_family_taken` degrade (and the other
      `Provider.Degraded()` tokens) is never read by ABO, and the instrument call sites
      discard the creation error (`internal/opsmetrics/opsmetrics.go` `r.duration, _ =` and
      `r.schedLag, _ =`, `internal/aidispatch/metrics.go` `slotWait, _ =`), so a refused or
      clashing family is silent. Either surface the Provider's degraded tokens in `/health`
      or check those errors (R1-10 review MINOR-2).
