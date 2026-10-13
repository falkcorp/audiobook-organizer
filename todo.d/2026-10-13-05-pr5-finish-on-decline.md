- [ ] **05-PR5-FINISH-ON-DECLINE** (owner decision) A v3 `ResumeAsk` run (the
      Fixer default) stopped by a shutdown skips its `Finish`, because the
      resumed attempt runs it once over every attempt's ledger. If a person
      then picks **Discard** instead of **Retry** in `awaiting_decision`, the
      record, ledger and marks are deleted and `Finish` never runs: rows the
      fixer applied before the shutdown keep their writes but never get
      `AfterApply`. Proposed fix: a third decision, "finish without
      resuming", dispatched as an attempt that leases no chunks and runs
      `Finish` over the stored ledger and marks (fenced, reported, under the
      same op id), then ends the run. Needs an API route and a button in the
      decision UI. The restart-strike force-drop needs nothing: it fires only
      when no chunk ever completed, so the ledger is empty. See the
      `batchConfig.resumes` comment in
      `internal/operations/registry/batch_runner.go`.
