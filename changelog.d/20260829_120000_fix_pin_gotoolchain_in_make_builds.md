### Fixed

- **Builds and deploys no longer break on a Go 1.27+ machine.** The Makefile now
  pins `GOTOOLCHAIN=go1.26.0` for every build, test and deploy target, including
  the ones defined in a developer's own (gitignored) `Makefile.local`. Previously
  a machine whose default Go was 1.27 would fail every build with
  `undefined: fastrand64` / `undefined: hashFn` from `github.com/cockroachdb/swiss`,
  which reaches removed runtime internals via `//go:linkname` — and because
  `GOTOOLCHAIN=auto` only ever upgrades to a *newer* toolchain, it would never
  step down to the 1.26.0 that `go.mod` asks for. Hit for real on 2026-08-24 when
  `make deploy-debug` died mid-build.
- **A failed `make ci` in a parallel sweep can no longer look like a passing one.**
  The sweep coordinator piped `make ci` into `tee` and then checked the exit
  status, which was `tee`'s (always 0) rather than make's — so a broken tree could
  be pushed and turned into a PR. The command now runs under `set -o pipefail`.
