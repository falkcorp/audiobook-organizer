### Dedup

- [ ] `Engine.SetLSHStore` and `Engine.SetAcoustIDBookFileStore`
      (`internal/dedup/engine.go`) have **no call sites anywhere** — not in
      production wiring, not in tests — so `de.lshAcoustIDStore` and
      `de.acoustidBookFileStore` are always nil and `CollectLSHAcoustID` /
      `CollectExactAcoustID` (`internal/dedup/collectors_acoustid.go`) never run.
      Verified structurally, not by name grep: both fields are assigned only
      inside their own setter bodies (`engine.go:202`, `engine.go:208`), and all
      four collector call sites — `engine.go:530`/`:536` and
      `rescore.go:233`/`:239` — sit behind an `if de.<field> != nil` guard, which
      is also why a nil store does not panic on `CollectLSHAcoustID`'s
      unconditional `store.IsLSHIndexBuilt()`. The collectors' own unit tests
      pass stubs directly and so cannot detect the missing wiring. Found
      2026-08-19 while fixing the neighbouring Tier-0 candidate-lookup decorator
      bug. Decide whether to wire them in `registry_wire.go`'s dedup engine
      factory (resolving the concrete store with `database.AsPebbleStore`, not a
      bare assertion) or delete the collectors and setters together.
