### Fixed

- `maintenance.purge-empty-authors` with `apply=true` no longer takes the scan stand-down when no author is eligible, so an apply that would delete nothing does not pause a running library scan.
