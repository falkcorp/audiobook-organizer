### Fixed

- `GET /operations/pause` (and the pause and resume POST answers), `GET /operations/stale` and the operations
  timeline no longer list Repairs operations (`repairs.plan`, `repairs.apply`) the caller may not read. The pause
  lists named the definition id of every running Repairs run, and the stale list returned the legacy rows whole.
  Every overview list now filters through one shared helper, `opaccess.Reader.ReadableOps` (and
  `ReadableLegacyOps` for the retired v1 table), and a guard test walks the wired `/operations` and `/op-defs`
  routes and fails on a list route that has no test seeding a hidden Repairs run.
