### Search placeholder hint missing when navigating to All Books from Finished

Navigating from **Finished** directly to **All Books** leaves the search bar
without its `try author:"Name"` placeholder hint. Clicking away to the Dashboard
and then back to All Books makes it appear.

Reported 2026-08-15.

The refresh-fixes-it shape points at state that is computed on mount (or on a
particular route transition) rather than derived from the current route/filter on
every render — so the Finished → All Books transition reuses a mounted component
without recomputing the hint, while Dashboard → All Books remounts it.

Worth checking:
- whether the placeholder is derived from a `useState` initialized once vs a
  `useMemo`/derived value keyed on the active view
- whether the Finished and All Books routes share a component instance (same
  route element, differing only by a query param or filter prop), which would
  skip the remount
- whether the hint depends on a fetched field list that is only requested on
  mount

Low severity, cosmetic, but it makes the search syntax undiscoverable for anyone
arriving via that path — which is the one path where a user has just finished a
book and is most likely to go looking for another by the same author.
