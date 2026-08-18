### Added

- CI now runs the interface-width ratchet (`scripts/check-interface-width.sh`) as
  its own `Interface Width Ratchet` job, failing a PR that raises the
  `interfacebloat` count above the committed baseline — or lowers it without
  lowering the baseline in the same change.
- `golangci-lint` now runs in CI at all, via the shared `go-lint` job. It had
  never executed: `.golangci.yml` existed and no workflow invoked it (`make ci`
  runs staticcheck, not golangci-lint).
- Super-linter runs in advisory mode — it reports and never blocks.

### Fixed

- The width gate is enforced by the ratchet script rather than by a
  `golangci-lint --enable-only interfacebloat` selector. golangci-lint exits
  non-zero on any finding and the tree legitimately holds five, so the selector
  form would have been red on every PR from the day it landed.
