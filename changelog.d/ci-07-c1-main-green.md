### Fixed

- `main` no longer fails CI on its inherited causes: a gofmt miss, 21 direct `slog` calls (now routed through `internal/logger`, ceilings lowered to files=296 calls=1699), the errcheck baseline (779 -> 770) and the `BookFileUpserter` interface width (batch methods split into `BookFileBatchUpserter`). The slog guard no longer fails when a ratchet entry is stale.
- CI tooling against go1.27.2: `setup-go`'s floating `1.27` started resolving to go1.27.2, whose export data neither golangci-lint v2.12.2 nor mockery v3.8.0 can read; golangci-lint moves to v2.14.0 (same finding counts), the Mock Freshness job pins go1.27.1 until mockery catches up, and both ratchet scripts now refuse a run in which the linter could not type-check the tree instead of reporting it as zero findings.
