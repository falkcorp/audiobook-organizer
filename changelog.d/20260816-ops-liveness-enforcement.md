### Changed

- Operations that are cancelled for inactivity now say **why** in two distinct
  ways instead of one ambiguous one. Previously an operation that stalled and an
  operation that was never connected to the progress system produced an identical
  "stuck" message, which is how a broken connection went unnoticed for three
  months and got worked around three separate times as if it were a slow
  operation. Being cancelled after never once reporting is now recorded as
  `never_reported` and logged as an error that names the likely cause, while a
  genuine stall keeps the existing `stuck` label.

### Added

- An operation that runs for more than a minute and finishes successfully without
  ever reporting progress now logs an error. This catches the problem while the
  operation still works — before it grows slow enough to start being cancelled —
  which is the point at which the library-scan fault could have been caught in
  May rather than August.
