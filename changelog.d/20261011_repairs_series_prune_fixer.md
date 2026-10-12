### Added

- Repairs lane: a **Prune series** fixer (`dedup.series-prune`) replaces the old Series tab's "Prune Series" preview and confirm. It lists two kinds of row: duplicate series (same name and author), whose apply moves every book of the copies (other versions included) onto the kept series and removes the emptied copies, and series no book is listed in, whose apply removes the series row and changes no book. A row is never applicable while any book in any state (trashed, other versions) still names a series it would remove (`still referenced`), and an empty series kept for an undo is listed as `held for undo`. It uses the nightly op's rules (held series are never merged away), journals every step under the apply operation with the op's change types, and drops the cached series list after a change. A removed series cannot be brought back, so these applies are recorded, not undoable.

### Changed

- `executeSeriesPrune` (the `dedup.series-prune` op and the scheduled `series_prune` task) now runs over cores extracted to `internal/dedup/series_prune_core.go` that the fixer shares; its output, counters, error text and ledger rows are unchanged (pinned by a golden recorded before the move).
