### Removed

- **`make check-mock-fresh` deleted.** The target claimed to catch a `MockStore` that had
  drifted from the `Store` interface, and could not: it ran `go generate
  ./internal/database/...` in a repo with zero `//go:generate` directives (mocks are
  generated from `.mockery.yaml`), so its regeneration step was a no-op and the
  `git diff --exit-code` that followed only ever detected an uncommitted edit. Measured by
  adding a method to `Store` and leaving the mock alone — the exact drift it named — it
  printed `==> Mock is fresh.` and exited 0. Its failure message also told you to run
  `make generate`, a target that does not exist.

  It was deleted rather than repaired because no coverage depends on it: `Store`/mock
  divergence is a compile error via the assertions at `internal/database/iface_assert.go:12`
  and `internal/database/mock_store.go:30`, `vet` runs over every package as a `test-short`
  prerequisite, and `mocks-check` regenerates from `.mockery.yaml` and diffs. All three go
  red on the same mutation. `make ci` now runs one mock gate instead of two.

### Fixed

- Corrected the `registry.Reporter` implementer counts in `reporter_db.go` and `reporter.go`
  (21 → 24 types, 13 → 15 test fakes). The old figures came from a name-shaped estimate; the
  new ones are structural — every implementer must declare `RunPhase`, and `sdk.Reporter` is
  a type alias of `registry.Reporter` rather than a second interface.
