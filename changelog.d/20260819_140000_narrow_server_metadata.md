### Changed

- Ten more `internal/server` helpers take measured store slices: the two bulk
  metadata fetches, the candidate fetcher, the metadata-results cache, the file
  I/O pool, the merge reroute and the Deluge adapter.

### Fixed

- Two more hand-rolled decorator walks replaced with `database.AsCapability`:
  `library_list_warmer.go`'s `unwrapStore` (its own bound of 8, half the shared
  `maxUnwrapDepth`) and an inline one in `server.go`. #2580 reported the
  repo-wide count as zero; that was true only of the single-line anonymous
  spelling it had grepped for. A named local interface and a multi-line
  anonymous one both survived it.
