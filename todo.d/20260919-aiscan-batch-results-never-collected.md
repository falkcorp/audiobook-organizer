- [ ] **AISCAN-BATCH-COLLECT** Batch-mode `ai.author-scan` results are never
      collected. Its batches are tagged `author_review` / `author_dedup`, but
      nothing creates a `type:"pipeline"` batch, so the poller's `pipeline`
      handler — the only caller of `PipelineManager.PollBatchPhases` — never
      runs. `decideResume`'s attach path and the comment at
      `internal/aiscan/pipeline.go` (~line 238, "PollBatchPhases will collect
      it") both assume it does, so a resumed batch scan waits on a collector
      that never fires. Fix, in order: (1) register `author_review` and
      `author_dedup` poller handlers that call `PollBatchPhases`; (2) add a
      poller reconciler that reads the `scan_id` / `scan_phase` batch metadata
      (now emitted at both CreateBatch sites) and attaches a listed batch to a
      phase with no batch id via `UpdatePhaseStatus(scanID, phase,
      "submitted", batchID)`; (3) give batch phases a pre-submit state written
      before `CreateBatch` — they stay `pending` until submitted today, so a
      kill after CreateBatch makes `decideResume` return `resumeLaunch` and pay
      for a second batch. `internal/plugins/maintenance/dedup_ops.go` has the
      same orphan shape (batch id recorded only after completion) and needs the
      same metadata + reconcile treatment.
