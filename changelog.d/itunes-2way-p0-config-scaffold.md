<!-- file: changelog.d/itunes-2way-p0-config-scaffold.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7b3e0c94-2a61-4d85-9f27-1c8a5b0e4d63 -->
<!-- last-edited: 2026-07-23 -->

### Added

#### iTunes 4-state library config model (`LibrarySet`) — scaffold, inert by default

`internal/config/itunes_libraries.go` adds the explicit 4-state iTunes library model
(`LibraryRef`/`LibrarySet`: Original/AO × .itl/.xml, plus separated `PointedAt` and
`ImportSource` mode facts) from the 2-way-sync system design. `ITunesConfig.Resolve()`
derives the legacy `LibraryReadPath`/`LibraryWritePath` shims from it, and
`ValidateLibraries()` adds four fail-closed config-load assertions (Original tree must be
covered by `protected_paths`; the AO write target must never resolve under `books/itunes/**`;
the Original must be frozen once `pointed_at=="ao"`; no zero-value write target while sync is
enabled). Entirely inert until `itunes.libraries` is populated — existing deployments behave
byte-for-byte as before. P0 of the phased plan (design + measurement only; nothing applied).
