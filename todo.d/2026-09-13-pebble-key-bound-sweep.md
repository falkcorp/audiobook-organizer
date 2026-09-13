- [ ] **PEBBLE-KEY-BOUND-SWEEP** Replace the fragile `[]byte("<prefix>:0")` /
      `[]byte("<prefix>:;")` iterator-bound pairs in `internal/database` with the
      true prefix range `[]byte("<prefix>:")` / `[]byte("<prefix>;")`. This is gap 2
      of `PEBBLE-KEY-BOUND-CENSUS` (#2896). Gap 1 (colon-bearing book IDs) was fixed
      at `CreateBook`. The census counted at least 47 non-test sites across 14 files
      (`pebble_store.go` 20, `pebble_store_authors.go` / `_series.go` / `_stats.go`
      4 each, ...). Both regexes are lower bounds, and bounds built with `+` or
      `fmt.Sprintf` are missed. Re-run both regexes before sizing it. The bug is
      latent: every ID minted so far starts with a digit. A mechanical
      `/parallel-sweep` fits.
