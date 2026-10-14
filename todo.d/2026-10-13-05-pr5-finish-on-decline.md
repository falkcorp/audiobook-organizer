- [ ] **05-PR5-FINISH-ON-DECLINE** Add a third decision, **Finish without
      resuming**, next to Retry and Discard for a v3 run in
      `awaiting_decision` (owner decision 2026-10-13; a follow-up, not part
      of 05-PR5). Today a `ResumeAsk` run (the Fixer default) stopped by a
      shutdown skips its `Finish`, so the resumed attempt can run it once
      over every attempt's ledger. If a person then picks Discard, the
      record, ledger and marks are deleted and `Finish` never runs: rows the
      fixer already applied keep their writes but never get `AfterApply`.
      The new decision dispatches an attempt under the same op id with a
      fresh lease and fence that leases no chunks and runs only
      `Finish`/`AfterApply` over the stored ledger and marks (the rows
      already applied), then ends the run. Work: a registry decision event
      and API route, the runner's finish-only mode, the button in the
      decision UI, and a test that the follow-up runs once over exactly the
      applied rows. The restart-strike force-drop needs nothing: it fires
      only when no chunk ever completed, so its ledger is empty. See the
      `batchConfig.resumes` comment in
      `internal/operations/registry/batch_runner.go`.
