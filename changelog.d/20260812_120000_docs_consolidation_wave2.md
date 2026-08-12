### Changed

- **Docs consolidation wave 2: 8 superseded documents archived** (7,455 lines) covering the
  server-plugin-registry plan/handoff, the metadata-cached-matcher design/plan, the handler-
  extraction phase-1 map, the series-embedded-positions plan, consumed Sonarr/Radarr research,
  and `docs/itunes-flow-diagrams.md`. Nothing deleted; everything moved under `docs/archive/`.
  This closes the four items the 2026-08-11 inventory deferred.

### Fixed

- **`docs/system/storage.md` documented a database schema production never used.** It asserted
  ULID ids and `a:`/`b:` key prefixes; production uses integer ids and `book:`/`author:`
  (`internal/database/memdb_warmup.go:69,86`). The table had been copied from
  `docs/database-pebble-schema.md` **without** that document's correction note, laundering a
  known-abandoned design back into circulation as fact. Corrected inline.
- **`docs/itunes-flow-diagrams.md` indexed a codebase that no longer exists** — all six files it
  anchored on are absent at HEAD and it claimed `server.go` was "~9000+ lines" against an actual
  1,091. It also contained a leaked agent transcript in its body (the authoring agent explaining
  it could not write files). Archived rather than repaired.
