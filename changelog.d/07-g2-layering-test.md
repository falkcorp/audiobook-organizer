### Added

- `internal/arch` layering test: every package has a layer in one map, an import that points to a higher layer fails `go test ./...`, and the known wrong-way imports are listed in a map that may only shrink. See `docs/architecture/layering.md`.
