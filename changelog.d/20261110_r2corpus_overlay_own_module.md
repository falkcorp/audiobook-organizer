### Fixed

- `make tidy-check` no longer fails at the repo root. `scripts/r2corpus/overlay` imports `internal/database`, which exists only at the R0 tag the overlay is built against (R1-26 removed it from main); `go mod tidy` reads files whatever their build tags, so it failed on the missing import. The overlay directory is now its own module (a `go.mod` with no requirements), which the root's `./...` and tidy do not see. `build_overlay.py` copies only `*.go`, so the R0-tag build is unchanged.
