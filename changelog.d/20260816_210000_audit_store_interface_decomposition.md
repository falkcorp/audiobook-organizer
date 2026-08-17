### Added

- `docs/audits/2026-08-16-store-interface-decomposition.md` — arbitrated design review of
  `database.Store` decomposition, consolidating two independent agent investigations (Go
  architecture + adversarial code review). Measured at `8011a755`. Recommends an
  authoring-time CI gate over a third interface-segregation sweep, with six one-line keystone
  signature changes and a ~41,200-line dead-mock deletion. No code changes; proposal only.

### Fixed

- `docs/audits/2026-08-16-manual-mock-inventory.md` — corrected the generated-mock usage
  census. The original count used a bare `mocks.MockX` grep, which collides with the ten
  other mock packages in the repo and misses the three import aliases (`mocks`, `dbmocks`,
  `databasemocks`) under which `internal/database/mocks` is imported. Referenced mocks:
  **3, not 8**. Unused: **42, not 37**. Dead lines in `mock_store.go`: **40,569 (76%), not
  22,001 (42%)**. The remediation is unchanged; its payoff is roughly 2× larger.
