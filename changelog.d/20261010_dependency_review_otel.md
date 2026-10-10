### Fixed

- Dependency Review no longer fails PRs on the OpenTelemetry Go modules: their LICENSE is Apache-2.0 plus a BSD-3-Clause section, both allowed, but the scanner reports the pair as one unrecognised expression. The 14 affected `go.opentelemetry.io` modules are allowlisted by name, as the `golang.org/x` modules already were.
