### Changed

- `maintenance.StoreProvider` no longer hands the maintenance ops the full
  398-method `database.Store`. `Store()` is replaced by three accessors: `OpsStore()`
  for the common path (53 methods, grouped into eight sub-interfaces each under the
  `interfacebloat` limit), plus `ReconcileStore()` and `PlaylistStore()` for the two
  ops that forward a store into another package and were dragging 46 extra methods
  into everyone else's requirement.

  Measured with `go/packages` at full type resolution: `internal/plugins/maintenance`
  went from 111 call sites reaching 39 methods on a `database.Store` value to **no
  `database.Store` value in the package at all**, and consumer declarations of the
  type fall from 9 to 8. Every accessor returns the same underlying store, so no
  behaviour changes and no wrapper is allocated.
