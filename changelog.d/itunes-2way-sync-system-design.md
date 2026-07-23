<!-- file: changelog.d/itunes-2way-sync-system-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1e6b3a90-7c24-4d58-8f31-9b2a5c0e4d76 -->
<!-- last-edited: 2026-07-23 -->

### Added

#### Definitive iTunes 2-way-sync steady-state system design

`docs/specs/2026-07-23-itunes-2way-sync-system-design.md` — the single authoritative
synthesis folding together the three prior iTunes specs. Formalizes the 4-state library
model (Original/AO × .itl/.xml), the explicit `LibrarySet` config with separated
`PointedAt`/`ImportSource` mode facts, the ordered steady-state cycle (decoupled
SafeWriteITL writes with per-write bounded-delta + pre-rename re-verify + a PID-indexed
itl-diff oracle with auto-rollback), partitioned identity count-auto-refresh, the
provenance-anchored cleanup redefinition, cutover + recoverable-fallback mechanics, and a
phased P0–P6 plan whose minimal-viable steady-state (P0–P2) ships without the unbuilt
dedup-on-import dependency. Design only — nothing applied.
