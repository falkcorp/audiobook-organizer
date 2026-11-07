### Changed

- Woodpecker CI now runs only what GitHub Actions cannot: `checks-lint` (reduced to staticcheck, the one `make ci` gate with no GitHub PR job) and `test-native-taglib` (needs libtag on the host). The seven duplicate workflows (`test-rest`, `test-database`, `test-server-scanner`, `test-fixtures-1/2/3`, `checks-build`) are deleted; the PR gate is ci.yml's `go-test-short-shards` and `Fixture Tests` jobs, which cover the same packages and make targets. This takes the duplicated load off our servers.
- ci.yml `Repo Guards` gained a `make pprof-check` step: it was run only by Woodpecker's `checks-build`, so deleting that workflow would otherwise have dropped the gate.
