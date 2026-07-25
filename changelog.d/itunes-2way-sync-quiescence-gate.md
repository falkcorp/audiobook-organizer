<!-- file: changelog.d/itunes-2way-sync-quiescence-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e9c1a82-7b64-4d50-8f18-2c6b5a0e1d74 -->
<!-- last-edited: 2026-07-25 -->

### Added

#### iTunes 2-way-sync — quiescence gate + single-flight lock on the relocate cycle

The relocate sync cycle now refuses to write while iTunes has the library open, closing
the two-writers-collide hazard. `RunRelocateSyncCycle` wires the existing
`FileActivityLibraryCheck` (watches the `.itl` + iTunes journal siblings —
`sentinel`, `Temp File*.tmp`, `iT*.tmp` — for a write within `QuiescenceWindow`, default
2m) into `SafeWriteITL`'s `WithLibraryNotInUse` precondition, re-checked **atomically**
immediately before the atomic rename. A read-only reader (Apple Devices, Music.app
browsing) never trips it — reads don't update mtimes; only an active writer does. And
because iTunes doesn't always clean up its journal/sentinel files on close, the gate keys
on the mtime **window**, not mere existence — a stale file ages out and stops blocking.

Adds an AO single-flight write-lock (`<itl>.ao-writeback.lock`, create-exclusive, removed
on the way out — unlike iTunes, AO cleans up after itself) so two AO writers can never
touch the `.itl` at once. `SyncCycleResult` now reports `LibraryInUse` (+ reason):
enforced on `Apply`, informational in dry-run. Unit-tested (recent write blocks, aged file
ages out, sentinel blocks, lock is single-flight and self-cleaning).
