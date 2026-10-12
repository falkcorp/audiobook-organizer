### Changed

#### Deploy builds now include the pprof tag (listener still off by default)

`make build`, `make build-api` and `make build-linux` (so every deploy) are built
with the `pprof` tag and `-trimpath`, defined once in the committed `Makefile`;
only `build-linux` (the deploy artifact) also strips symbols, so `make build-api`
stays readable by `govulncheck -mode=binary`. The profiling listener serves nothing unless
`ABK_PPROF_ADDR` is set, and now runs on its own mux instead of the default one.
Isolated-operation child processes no longer inherit `ABK_PPROF_ADDR`, a guard
test fails if any non-test file serves `http.DefaultServeMux`, and a new
`make pprof-check` (in `make ci`, Woodpecker and ci-remote) vets and tests with
`-tags pprof`. `Makefile.local.example` deploy targets call `make build-linux` and no longer
repeat `go build` flags.
