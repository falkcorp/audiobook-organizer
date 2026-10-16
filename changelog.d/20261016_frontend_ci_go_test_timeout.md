Frontend CI and Nightly Full CI now run Go tests with a 25m per-binary
timeout instead of Go's 10m default. Both call github-common's
`reusable-ci.yml`, whose go-test helper passed no `-timeout`, and
`internal/database` runs 510-600s under `-race` there, so it was hitting the
default. The workflows now pin github-common at the commit from
github-common#376, which adds a `go-test-timeout` input (applied as
`GOFLAGS=-timeout=...`) and raises the Go CI job cap to 60m so the test
timeout still fires first. 25m matches the Makefile and `ci.yml`.
