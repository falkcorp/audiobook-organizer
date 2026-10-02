### Fixed

- The metafetch plugin's operations (`metafetch.asin-backfill`, `metafetch.calibrate-scoring`) now exist in the running server. The plugin was listed in `internal/plugins` but nothing in the binary imported that package, so the ops were never registered in prod while tests passed through a test-only import. The server now imports the one plugin list, and a test checks the binary's real import graph.
