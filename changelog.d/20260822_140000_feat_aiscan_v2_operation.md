### Added

- The AI author scan now appears in the operations timeline and notification bell
  like every other long-running job. It runs as an `ai.author-scan` v2 operation
  with real progress, cancellation, and resume-after-restart.

### Fixed

- Cancelling an AI author scan from the operations view now actually stops it.
  The cancel path matched an operation id against a field that held an id from
  the old operations system, so the two never matched and the cancel silently did
  nothing while the scan carried on.

- An AI scan interrupted by a restart is no longer left running forever with
  nothing tracking it. A batch scan re-attaches to the job OpenAI is still
  holding; a realtime scan, whose requests died with the process, is now marked
  failed instead of appearing to still be in progress.

- Three failure paths in the scan's cross-validation step left the scan stuck at
  "scanning" indefinitely and made it impossible to cancel. They now mark the
  scan failed.

### Changed

- Starting a second AI author scan while one is already running now queues it
  behind the first instead of running both at once. The scan is a paid
  whole-library AI pass, and two concurrent runs were almost always an accidental
  double-click.
