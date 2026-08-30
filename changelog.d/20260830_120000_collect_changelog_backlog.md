### Fixed

#### `CHANGELOG.md` — folded the 675-fragment backlog into correctly-attributed release sections

Changelog collection had silently stopped after v0.218.0 (2026-08-08), so 675
fragments covering six releases piled up in `changelog.d/` while CI kept
requiring a new one on every PR. They are now folded into `CHANGELOG.md` as one
section per release — v0.218.1 (322), v0.219.0 (98), v0.219.1 (190), v0.219.2
(3), v0.220.0 (35) and v0.221.0 (27) — rather than one lump attributed to the
most recent tag.

Each fragment was attributed by asking which release actually shipped it: find
the commit that introduced the fragment, then take the earliest stable tag
containing that commit. Filename timestamps were not used, because v0.218.1 was
published (2026-08-24) after v0.219.0 (2026-08-20) and a date-based split would
have misfiled changes across that overlap. Section dates are each release's tag
commit date, the convention the existing v0.218.0 section already follows.

The root cause of the stall is a guard in the shared `reusable-release.yml` and
is fixed separately in `falkcorp/github-common`; until that lands and the pin in
`release-prod.yml` is bumped, collection stays manual.
