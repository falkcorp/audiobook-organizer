### Fixed

- `opstate:<id>` / `opstate:<id>:params` keys leaked forever: only 2 of the 34
  maintenance jobs cleared their persisted resume state on completion. The
  `retention-and-hygiene` job now sweeps the `opstate:` prefix, deleting state
  whose owning operation is gone or terminal (completed/failed/canceled) while
  keeping state for running, queued, interrupted, or unrecognized statuses so
  restart-resume is never broken.
