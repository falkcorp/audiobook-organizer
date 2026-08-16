- [ ] **`library.scan` "resume" currently means "start over".** `ResumePolicy` moved
      from `ResumeDrop` to `ResumeRestart` on 2026-08-16, which stops a restart from
      silently discarding the scan — but it re-runs from the beginning.
      `resumeRestart()` merges a saved checkpoint blob into params, and the scan has
      nothing to merge: `libraryScanParams` carries only `folder_path` and
      `force_update`, and nothing in the scan path calls `Checkpoint()`. For a
      ~5-hour full scan that is a lot of re-walking. Give the params struct a phase
      + high-water mark and checkpoint per batch, so a restart continues instead of
      restarting. The v2 machinery (`GetOpStateV2`, `HighWaterProgress`,
      `LastCheckpointAt`) is already there and unused by this op.

- [ ] **`library.import`, `library.organize` and `library.transcode` still carry the
      4h ceiling and `ResumeDrop`.** Only `library.scan` was changed, deliberately —
      it is the one measured to exceed 4h. Check whether the others can also exceed
      their ceiling on a 63k-book library before assuming they are fine; `organize`
      in particular touches every book.
