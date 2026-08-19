### Fixed

- The six `/maintenance/wipe` helpers had no test asserting **which** key
  prefixes they pass. Each deletes a whole keyspace by raw Pebble prefix, so a
  typo'd or dropped prefix deletes the wrong data — or, for `wipeSegments`,
  silently leaves the `bfs:` secondary index pointing at deleted segments — and
  every existing test would still have passed. All six are now pinned to their
  exact prefixes, along with the dry-run/wipe asymmetry in `wipeSegments`
  (the count uses `bf:` only while the wipe uses `bf:` and `bfs:`).
- The `recompute-book-aggregates` backfill sentinel is now tested for whether the
  job **honours** it, not just whether it can read it. Both halves are pinned:
  the short-circuit on a set sentinel, and the write on completion that makes
  the short-circuit reachable next run.

### Changed

- `.mockery.yaml` gained `prefixWiper` and `aggregatesBackfillMarker`, generated
  in-package with `_test.go` filenames since both are unexported (the
  `metadataLLMBackend` arrangement). The config records that these mocks are for
  call-site behaviour tests only and must never be used to test the `resolveX`
  functions: a mock satisfies its interface by construction, so it would pass in
  exactly the case those tests exist to catch.
