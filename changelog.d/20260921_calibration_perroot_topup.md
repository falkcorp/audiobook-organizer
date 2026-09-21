### Fixed

- **A new fingerprint root could never be proven, so workers holding it refused
  to start.** Calibration offers files whose windows are already current, so a
  root the backfill has not reached yet has no candidate — and every root has
  none on its first run. The worker's parity gate then exits with "the server
  offered no calibration file under root X", which means the root cannot be
  proven until its files have windows and its files cannot get windows until a
  worker holding it starts. Roots without a windowed candidate are now topped up
  with an identity candidate (a present file with no windows), which proves the
  root mapping — size, mtime, first 64 KiB — without claiming pipeline parity it
  cannot yet demonstrate. Roots that do have windowed candidates keep them, so
  byte-parity is still proven wherever it is possible.
