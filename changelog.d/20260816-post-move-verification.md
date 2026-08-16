### Added

- **Every rename is verified after it reports success.** A rename returning no
  error says the syscall was accepted, not that the file arrived where anyone
  wanted it — the distinction that let 38,895 files be misplaced by operations
  that all reported success. `safeRename` now reads the filesystem back and
  fails loudly if the destination is missing, is a different kind of object
  than the source, has a different size, or if the source survived (meaning the
  move silently became a copy).
