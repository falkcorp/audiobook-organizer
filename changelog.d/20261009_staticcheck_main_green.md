### Fixed

- `make staticcheck` (the Woodpecker `checks-lint` stage) is green on main again. The stale-iTunes-path repair, its revert and undo preflight read the legacy `book.itunes_path` column by design, so those reads now carry a per-statement `//lint:ignore SA1019` with the reason; the unused `OwnerApproval.has` helper (never called since it was added; the owner-row check lives in `RunApply`) is removed.
