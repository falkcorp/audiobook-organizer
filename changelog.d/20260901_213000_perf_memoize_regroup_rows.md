### Fixed

- **The regroup review lane re-rendered all 500 holds to repaint one.** Its rows
  were the only ones of the three lanes still written as inline JSX, so every
  hold on the page re-rendered whenever any single hold went busy, whenever a
  character was typed in the search box, and on every refresh. The row is now a
  memoized component, and the lane's approve/reject callbacks were made stable
  (they read the chosen action through a ref) so that memo is not inert. The
  lane also parses each hold's JSON payload once per loaded page instead of
  twice per render — at the 500-hold fetch limit that was ~1,000 `JSON.parse`
  calls per re-render.

  Measured with `benchmark-review-lanes.spec.ts`, same machine, back-to-back
  runs, median of 5 reps. At the 500-hold fetch limit, changing the sort went
  from 344 ms with 10 long tasks and 328 ms of blocking time to 282 ms with
  **no** long tasks and no blocking; searching went from 479 ms/10/298 to
  418 ms/5/266. On a 6x-throttled CPU at 100 holds — the slow-machine case —
  sort blocking time fell from 836 ms to 67 ms.
