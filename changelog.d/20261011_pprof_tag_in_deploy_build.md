### Changed

#### Deploy builds now include the pprof tag (listener still off by default)

`make build`, `make build-api` and `make build-linux` (so every deploy) are built
with the `pprof` tag, `-trimpath` and stripped symbols, defined once in the
committed `Makefile`. The profiling listener serves nothing unless
`ABK_PPROF_ADDR` is set, and now runs on its own mux instead of the default one.
`Makefile.local.example` deploy targets call `make build-linux` and no longer
repeat `go build` flags.
