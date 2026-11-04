### Added

#### Platform core design spec (`docs/specs/platform-core/`)

Added the design spec for extracting a shared platform core (`github.com/falkcorp/mediacore`, nested at `core/`
first) from audiobook-organizer, so that ABO, a TV app and a Movies app can share core packages, an SDK and a
React shell. The spec covers the architecture and layer rules, a per-package mapping of all 213 packages, one
subsystem spec per core service, the ABO-on-core stage plan (S0–S10, T0–T8) with rollback costs per stage, the
UI shell, the optional *arr facade extension point, the TV/Movies contract proof, the repo-split criteria, the
toolchain, and the risks and owner questions. The raw evidence base it rests on is in
`docs/plans/platform-core/raw/`, and the eight review rounds with their dispositions are in
`docs/plans/platform-core/reviews/`. Planning only: no production code changed.

### Changed

#### 2026-09-18 pluggable media platform decision record superseded

The 2026-09-18 decision record is marked superseded by `docs/specs/platform-core/02-decision-record.md`, which
gives a kept/changed/dropped verdict for each of its 13 decisions.
