### Fixed

- The fixture-test sharder's `go test -list` step failed on every Woodpecker run after the single-call lister landed (#3882): it attributed test names by import path, and the fixture steps pass `./internal/...`. Paths are now resolved with `go list` first.
