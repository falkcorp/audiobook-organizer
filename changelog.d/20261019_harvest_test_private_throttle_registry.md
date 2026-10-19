### Fixed

- `runCatalogHarvest` now takes the throttle registry as a parameter (production passes the process-wide one), so the catalog-harvest tests use a private registry and no longer fail with "provider is throttled; harvest standing down" when an earlier test in the `internal/server` binary left a hold on audible.
