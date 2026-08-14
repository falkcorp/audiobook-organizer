### Fixed

- Repaired the `internal/server` integration test that still asserted the old
  behaviour for an unrecognised search filter. It expected a 200 with an empty
  list; that answer was the bug, and rejecting the request with a 400 is the
  fix. The expectation is inverted rather than deleted — a well-formed request
  still has to be answered, just answered honestly.
