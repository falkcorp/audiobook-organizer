### Fixed

- **The regroup review lane re-rendered all 500 holds to repaint one.** Its rows
  were the only ones of the three lanes still written as inline JSX, so every
  hold on the page re-rendered whenever any single hold went busy, whenever a
  character was typed in the search box, and on every refresh. The row is now a
  memoized component, and the lane's approve/reject callbacks were made stable
  (they read the chosen action through a ref) so that memo is not inert.
