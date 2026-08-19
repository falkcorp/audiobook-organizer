### Added

- `docs/plans/2026-08-19-server-store-narrowing-worklist.md` — a measured, ranked
  worklist for narrowing `Server.Store()`, with the finding that reframes it: the 90
  interfaces a store gets passed into are already the per-consumer narrow
  dependencies, so those call sites are narrow at the callee already and the wide
  accessor only governs the 88 direct calls. 60 of the 90 contribute zero unique
  methods, and narrowing every callee individually would remove only 83 of the 268.

### Changed

- The `*PebbleStore` struct-split decision doc is marked PARKED at lowest priority
  rather than left open, and tracked as a TODO fragment so it stays visible without
  competing for attention. It has been costed twice and corrected twice; the doc now
  says so up front, along with the two traps that produced the earlier wrong numbers.
