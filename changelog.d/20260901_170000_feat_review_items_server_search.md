### Fixed

- **The regroup review search now searches the queue, not just the page.**
  `GET /api/v1/review/items` gained a `q` parameter, applied before the total is
  taken, and the regroup lane pushes its search box down to it. Previously the
  lane fetched 500 rows and searched those: measured on the production queue,
  `regroup.ambiguous` alone held 714 pending holds, so **214 of them could not be
  found by typing** — and selecting the kind in the dropdown did not help, because
  they were all the same kind. The lane still filters locally as well, so the list
  narrows on the keystroke rather than after the 250 ms debounce.

  The server matches the hold's own columns and the string **values** inside its
  payload, never the payload's JSON keys — so `q=folder` is a search for the word
  "folder", not a match against every hold. One deliberate behaviour change: the
  frontend's kind **labels** ("Abridged / Unabridged editions") are a display map
  with no backend counterpart and are not searched server-side. Copying that table
  into Go to preserve one substring match would have created exactly the kind of
  divergent duplicate this codebase has been deleting; the kind dropdown selects
  that bucket directly.
