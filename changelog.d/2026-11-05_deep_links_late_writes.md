### Fixed

- On pages that keep their state in the address bar, a change made just before
  following a link no longer rewrites the address afterwards and drags you back
  to the page you left. The change is also written to the page's history entry
  before the link is followed, so Back returns to the page as you left it. (The
  browser's own Back and Forward buttons are the one way out that cannot do
  this: by the time the browser reports them, the entry being left can no longer
  be written, so a change made in the last fraction of a second before pressing
  them may not be in that entry.) A link to the Review page's Repairs fixer or
  Authors focus also keeps its parameter while the list is still loading, and a
  Repairs fixer link that names a fixer the list does not hold no longer leaves
  that fixer in the address bar. These pages also write correctly when the app is
  served under a sub-path.
