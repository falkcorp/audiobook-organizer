### Added

- `audiobook-organizer --print-storage-levels` prints, as one JSON object, the
  storage level this build supports for each owner (`{"books":0,"core":0}` in
  this release), then exits without opening anything. The Docker image check
  now requires it to exit 0 and print JSON (`PROBE_STORAGE_LEVELS=1`), and
  `make rollback` will use it to compare builds once a release raises a level.
- Before the first start of this release writes anything to an existing
  database, it saves a copy of the database (hard-linked where the filesystem
  allows) together with the encryption key and the gap-fill settings file. The
  copy goes in a `.mediacore-checkpoints/` folder next to the database, and a
  `<database>.migration-checkpoint` file points at it, so an upgrade can be
  rolled back to that exact state.

### Changed

- The core runtime now opens the library database, before the books module
  starts. The server no longer opens it, runs its upgrades or writes to it
  before startup itself. Upgrade steps, the encryption-key checks and the
  start-of-day repairs now run in the core runtime's own startup order. The
  stored format is unchanged, so the previous release (v0.223.0) still opens a
  database this release has used, and `make rollback` to it still works.
- The database now refuses a write to a key outside the families the books
  module has registered, instead of letting it through. If a write is refused
  in production, setting `ABO_CORE_STORAGE_CLAIMS_MODE=shadow`
  lets such writes through while still logging and counting them
  (`audiobook_organizer_storage_claim_violations_total`).
- Startup now stops with an error, instead of logging and carrying on, if it
  cannot save a settings upgrade (the settings blob's new layout, the
  maintenance-window marker, or a reset of an unreadable secret). Before, the
  server kept serving, and the same upgrade ran again on every start.
- Startup refuses a database that holds core-runtime keys but no install
  marker, which would mean some other program wrote them, and changes nothing
  in it.
