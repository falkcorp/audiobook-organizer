### Added

- `audiobook-organizer --print-storage-levels` prints, as one JSON object, the
  storage level this build supports for each owner (`{"books":0,"core":0}` in
  this release), then exits without opening anything. The Docker image check
  now requires it to exit 0 and print JSON (`PROBE_STORAGE_LEVELS=1`), and
  `make rollback` will use it to compare builds once a release raises a level.
  A trailing `--db PATH` is accepted and ignored.
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
- The database now refuses a read or write of a key outside the families the
  books module has registered, instead of letting it through. If a key is
  refused in production, setting `ABO_CORE_STORAGE_CLAIMS_MODE=shadow` lets
  single-key reads and writes through while still counting them
  (`audiobook_organizer_storage_claim_violations_total`) and logging them (the
  1st, 2nd, 4th, 8th... time for each kind, so the log does not flood). Shadow
  mode does not widen listings: a family that is not registered stays
  invisible to every scan either way. These logs and the refusal errors
  describe a key by the registered family that holds it and its length
  (`key under claim "sess:" len=21`), or, for a key in no registered family,
  by its length alone. Neither the key nor a hash of it is shown, so a session
  token or other secret held in a key never reaches a log.
- The first start of this release on an existing database counts the keys that
  belong to no registered family and logs how many there are with a few
  examples, each described by its length. It leaves them in place and does
  not refuse to start.
- If startup cannot save a settings upgrade (the settings blob's new layout,
  the maintenance-window marker, or a reset of an unreadable secret), the server
  logs the error, reports `settings_writes` in `/health`'s degraded list, and
  keeps serving with the upgraded settings in memory. The next start tries the
  save again, as before.
- A database without a `storage_format` record gets one (format 1, which is
  what a missing record already meant) during startup, after the upgrade
  checkpoint is taken, as releases before this one did when they opened it.
- Startup refuses a database that holds core-runtime keys but no install
  marker, which would mean some other program wrote them, and changes nothing
  in it.
