### Added

- Four dark core task kinds, none registered until F5-18: `core.storage.compact`, `core.storage.census` (records the latest census at `c/storage/census/latest`), `core.tasks.prune` and `core.log.journal.prune`, plus the books kind `books.maintenance.aux-store-compact` (the AI scan and OpenLibrary store half of `maintenance.db-optimize`, which now shares its code). Production behaviour is unchanged.
- `core.log.journal.retention_days` is capped at 36500 by the schema, and the journal prune refuses a retention outside 1 to 36500, or a cutoff that is not before now, with nothing deleted.
