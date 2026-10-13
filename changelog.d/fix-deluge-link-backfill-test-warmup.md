### Fixed

#### Deluge link-backfill tests wait for the in-memory database

Two tests in `internal/plugins/deluge` opened their store without waiting for the in-memory database to finish loading. On a loaded `-race` shard the backfill then refused to run (it fails closed until that load completes) and the test failed. Both now use the shared test store, which waits. A new test pins the fail-closed behaviour.
