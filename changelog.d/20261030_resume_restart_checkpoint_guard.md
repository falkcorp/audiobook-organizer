### Added

- `internal/arch` now has a static guard, `TestResumeRestartDefsCheckpoint`:
  every `OperationDef` that declares `ResumePolicy: ResumeRestart` must have a
  `Run` that reaches a `Checkpoint` call, or be listed (with the reason a
  restart from zero is safe) in an explicit allowlist. A new op can no longer
  repeat the eight silent restart-from-zero ops fixed in #3223.
