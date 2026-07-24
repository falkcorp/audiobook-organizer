<!-- file: changelog.d/itunes-location-form-scope-writeback.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c3f1a94-6b70-4d52-9e18-2f7c5a0e1d63 -->
<!-- last-edited: 2026-07-24 -->

### Changed

#### iTunes location-form guard: scope the `.itunes-writeback/` staging-marker check to the write target (F7)

The `location-form` safety guard rejects any track location containing
`.itunes-writeback/` as a staging-dir leak (the "damaged-4" class). But when iTunes is
pointed AT the AO writeback library — the locked hard-cutover design — that library's own
media legitimately lives under its `.itunes-writeback/iTunes Media/` root, so every track
carries the substring correctly. The unconditional check therefore rejected the **entire
live AO library (82,981 track-locations)**, making the P2 relocate op unable to write it
(findings §F7).

`ContractConfig.AllowedWritebackRoot` now scopes the check: a marker whose location sits
**under** the configured root (the AO library's own media root, e.g.
`audiobook-organizer/.itunes-writeback/`) is allowed; a marker anywhere else is still a
leak and rejected. Matched case-insensitively with path separators normalized, so it
applies to both the 0x0D WinPath and 0x0B URL forms. **Empty (default) = strict — ALL
`.itunes-writeback/` rejected, fully backward-compatible and fail-closed.** Only the sync
cycle writing the AO library sets it (from the 4-state `LibrarySet` config); it is never
set when writing the Original library.

Verified on the real library: strict flags 82,981 staging violations; AO-scoped flags 0.
Unit-tested via the pure `stagingMarkerIsLeak` decision (strict / scoped-allow /
leak-under-different-root / misconfigured-root → fail-closed). This adds the guard
capability; the P2 sync cycle wires it in a later PR. Review-gated (production safety guard).
