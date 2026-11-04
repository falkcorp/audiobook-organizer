### Fixed

- E2E: "selection persists across page navigation" now waits for each page of
  the library to render before leaving it. It clicked page 1 while page 2 was
  still loading, and on a busy CI runner page 1's rows weren't back in time
  (#4365, 2026-11-04).
