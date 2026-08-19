### Changed

- Narrowed eleven more `database.Store` consumers to focused interfaces, taking
  the non-test consumer count outside `internal/database` from 38 to 18: the
  scheduler's extra-ops registrar, the AI / diagnostics / split-book HTTP
  handlers, the operations registry service, the batch poller, the bulk
  metadata-fetch-by-ID op, the version-group backfiller resolution, and the
  `diagnostics` CLI subcommands.
- `internal/plugin.Deps.Store` is gone rather than narrowed. It was write-only:
  the server set it and no plugin ever read it.
- `metabatch.Store`, `sweep.ArchiveSweepStore`, `diagnostics.Store` and
  `merge.Store` are now exported so callers can name them instead of reaching
  for `database.Store`.
