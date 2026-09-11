### Fixed

- The System → Maintenance tab no longer crashes when you open it. The file-hash
  statistics route (`GET /api/v1/maintenance/book-file-hash-stats`) was putting its
  numbers inside two nested wrappers instead of one, so the page found nothing where
  it expected the file counts and gave up rendering the whole tab. The route now
  answers in the same shape as its sibling
  (`GET /api/v1/maintenance/book-metadata-hash-stats`), which was always correct.
- The maintenance-window panel on that same tab now shows its real settings. The
  browser was reading the reply from `GET /api/v1/maintenance-window/status` one
  layer too high, so the window's on/off state, its start and end hours, and the next
  scheduled run all came through blank — the next-run time displayed as
  "Invalid Date". The values were correct on the server the whole time; only the
  display was wrong.
