### Fixed

- A deliberately killed operation (watchdog or user cancel) that took longer
  than the abandon grace to unwind was recorded as `interrupted_quiesced` and
  resumed on the next restart. The abandon path now uses the same status
  decision as every other cancel, so it is recorded `canceled`. Hit on
  2026-09-22: the watchdog-killed library scan was left resumable.
