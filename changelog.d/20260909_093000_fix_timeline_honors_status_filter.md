### Fixed

- **The operations timeline now honours its `status` filter.** Asking it for
  canceled operations returned exactly the same rows as asking it for everything:
  the parameter was neither applied nor rejected, so the answer looked like a
  filtered one and was not. The count it reports alongside the rows is now the
  count of rows that match the filter, over the whole window rather than the page
  — which matters because that number is what a cleanup gets sized against. One
  was: a request to delete canceled operation history was judged to affect a
  single row from this view, and removed seventy.

  Asking for a status that does not exist now answers plainly — nothing matched,
  and here are the statuses that were actually present in the window — rather than
  a bare zero that reads the same whether you asked for the wrong thing or there
  was nothing to find. The filter is deliberately not checked against a fixed list
  of known statuses, because such a list goes stale silently every time a new one
  is added, and the failure would be a rejected query for a status that exists.

  The listing remains a bounded view of recent activity rather than a complete
  history, and says so in its own response; a filtered count is exact only when it
  reports that it did not hit that bound.
