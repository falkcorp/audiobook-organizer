### Fixed

- The nightly AcoustID fingerprint backfill is now gated behind
  `maintenance.acoustid_backfill`, **default off**. Its load phase pulls the entire
  book table into memory before it can start — roughly 862 MB of live heap in
  production, implicated in three OOM kills in one night. Both automatic triggers
  respect the flag: the unconditional enqueue at server startup, and the
  `acoustid-backfill` child of the `library.optimize` sweep. Enqueuing the op
  directly through the ops API is unchanged, so the deliberate opt-in path still
  works.
