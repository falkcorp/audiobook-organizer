### Added

- `maintenance.missing-file-audit` now reports an identity-signal census alongside
  its counts: for missing rows and (as a control) present rows, how many carry a
  file hash, an AcoustID fingerprint, a duration, a size, an iTunes PID or a
  transcription. This is the input that decides which missing-file repairs can be
  *verified* rather than guessed. The op remains read-only.
