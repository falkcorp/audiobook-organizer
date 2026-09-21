### Added

- **Pause and resume operations.** `POST /api/v1/operations/pause`,
  `POST /api/v1/operations/resume` and `GET /api/v1/operations/pause` hold item
  dispatch across every op that runs items: work already in flight finishes, and
  the next item parks until you resume. It is not a cancel — nothing ends and no
  progress is lost. The hold is persisted, so a restart or a deploy cannot
  silently resume work someone stopped, and a paused op is still cancellable
  without resuming it first. The response lists which running ops will actually
  park and which will not: an op with no per-item loop, `library.scan` above all,
  has no safe place to hold and keeps going.
