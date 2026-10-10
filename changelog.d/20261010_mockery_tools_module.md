### Changed

- mockery is now built from a pinned tools module (`tools/mockery/go.mod`: mockery v3.8.0 with golang.org/x/tools raised to v0.51.0) by `make mockery-install`, the GitHub Mock Freshness job and Woodpecker `checks-lint`. The v3.8.0 release's own x/tools cannot read go1.27.2's export data, which had held the Go pin on 1.27.1; the two `GOTOOLCHAIN: go1.27.1` overrides in `ci.yml` are gone. `make mocks` and `make mocks-check` call the binary they just built, so a same-version Homebrew mockery earlier on PATH no longer gets used.
