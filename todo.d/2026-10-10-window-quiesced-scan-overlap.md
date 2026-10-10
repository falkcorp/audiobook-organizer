- [ ] **WINDOW-QUIESCED-SCAN-OVERLAP** A maintenance window
      (`internal/server/scheduler_maintenance_window_op.go:186`) stops waiting
      on a child as soon as it reads `interrupted_quiesced`
      (`scheduler.WaitForOperation`, `internal/scheduler/scheduler.go:607`; the
      same holds for `childop.Follow`). But an `interrupted_quiesced` scan can
      run again under the same id while the process is up: the worker pickup
      gate calls `resumeDroppedScanOnRelease`
      (`internal/operations/registry/worker.go:367` →
      `scan_standdown.go:429`) → `resumeQuiescedOp` (`resume.go:414`) →
      `ResetOperationV2ForResume`. So the window can move on to its next task
      while the scan it stopped waiting on comes back and runs alongside it.
      This is old behaviour: the hand-written list before 05-PR1 also ended
      the wait on `interrupted_quiesced`. Decide whether a window should keep
      waiting on a quiesced scan, or re-check it before starting the next
      task.
