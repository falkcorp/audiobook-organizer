- [ ] **8 ops declare `ResumeRestart` but never checkpoint — they silently
      behave as `ResumeRequeue` without the idempotency review that policy
      requires.** Found 2026-09-09 while answering "have we made all these scans
      resumable?" A census of all **157** registered `OperationDef`s
      (`ID:` + `ResumePolicy:` parsed structurally, not sampled) breaks down as:
      `ResumeDrop` 111, `ResumeRestart` 23, `ResumeRequeue` 20, `ResumeAsk` 3.

      `resume.go:26` defines `ResumeRestart` as "increment resume_count,
      **dispatch with saved state**". An op that never calls
      `reporter.Checkpoint` has no saved state, so it is dispatched with
      `state_bytes=0` and unmodified params — i.e. it restarts **from zero**.
      TODO.md already spells out this exact failure for
      `metadata.batch-apply-cached` and calls a bare policy flip *"an unbounded
      re-apply loop, strictly worse than today's drop"* — but that op is
      `ResumeDrop` today and therefore **safe**. These eight already sit in the
      state that entry warns about:

      | op | file | writes? |
      |---|---|---|
      | `entities.author-merge` | `internal/server/entities_ops.go:56` | yes — `CapLibraryWrite` |
      | `entities.resolve-production-author` | `internal/server/entities_ops.go:206` | yes — `CapLibraryWrite` |
      | `library.bulk-write-back` | `internal/server/library_writeback_op.go` | yes — tag write-back |
      | `maintenance.series-denumber` | `internal/plugins/maintenance/series_denumber_op.go` | yes |
      | `maintenance.author-conjunction-repair` | `internal/plugins/maintenance/author_conjunction_repair.go` | yes |
      | `maintenance.isbn-enrichment` | `internal/plugins/maintenance/metadata.go` | yes |
      | `metadata.candidate-fetch` | `internal/server/metadata_candidate_op.go` | fetch/cache |
      | `ai.author-scan` | `internal/server/aiscan_op.go` | nominates |

      Both `entities.*` ops carry a bare `ResumePolicy: opsregistry.ResumeRestart`
      with **no comment justifying it** and a 2h timeout. Note `entities.author-merge`
      is precisely the "auto-merge/auto-resolve apply path that must not
      double-merge" shape CLAUDE.md's concurrency section calls out.

      **What is verified vs. not.** Verified: the policy values, the absence of
      `reporter.Checkpoint`/`RunItems` in each op's file, and the registry
      semantics. NOT verified: whether re-running each from zero is actually
      destructive — a re-issued merge may no-op because the source author is
      already gone. That per-op idempotency review is the work here; it is
      exactly the review `ResumeRequeue` ("idempotent ops only") demands and
      that a silent `ResumeRestart` skips.

      **Per op, pick one and say why in a comment:** add a real checkpoint
      (watch the parallel-loop trap TODO.md already documents — a done-set or
      contiguous watermark, *not* a `LastBookID` cursor); or downgrade to
      `ResumeRequeue` after confirming idempotency; or `ResumeDrop`. Consider a
      registry-level guard so a def cannot declare `ResumeRestart` unless it
      checkpoints — `RegisterOp` already rejects `ResumeUnspecified`, so the
      enforcement point exists.

      Reference for what a working checkpoint looks like: `library.scan`,
      `maintenance.transcribe-book-intros`, and the migration checkpoint proven
      in prod on 2026-09-08 (`resumed=true`, cursor 972k→1.72M).
