### Fixed

- Three iTunes operations — **Sync**, **Path Reconcile** and **Path Repair** —
  have been reporting success without doing anything since 2026-07-17. A
  half-finished refactor left placeholder versions of them in the iTunes plugin,
  and because plugins load before the rest of the server, the placeholders took
  over from the working versions. Running any of the three produced a green
  "completed" row and no work. All three now run their real implementations
  again. A fourth, **Position Sync**, had no working version to fall back on and
  now reports an honest failure instead of a false success.

### Changed

- A placeholder operation that has not been implemented yet now fails when run
  instead of quietly reporting success. Reporting success for work that did not
  happen is the more expensive failure: nothing looks wrong, so nobody looks.
