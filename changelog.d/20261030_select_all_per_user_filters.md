### Fixed

- A select-all on a Library view filtered by `read_status`, `progress_pct` or
  `last_played` now runs the background operation over the books that view showed
  for the requesting user. The resolver judges those filters (and per-user terms in
  the search text) against that user's listening rows, the same way the Library
  list does, instead of refusing them. With no user to judge by (auth off, or an
  operation row that records no actor) it still refuses and names the filter
  rather than widening to the whole view. The bulk metadata fetch continuation
  keeps the original actor so later links of a chain resolve the same selection.
- The bulk metadata fetch no longer widens to the whole library when a given
  selection resolves to zero books. A filter that matches nothing (including a
  per-user filter that matches none of the requesting user's books) now finishes
  with nothing fetched; only a run that names no filter and no books fetches the
  whole library. Each user's identical per-user selection now gets its own run
  instead of being folded into another user's active one.
