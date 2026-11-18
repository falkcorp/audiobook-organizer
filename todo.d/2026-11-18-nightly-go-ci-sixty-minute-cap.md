- [ ] **NIGHTLY-GO-CI-CAP** `Nightly Full CI`'s `Go CI` job runs the whole
      non-short suite in one unsharded `go test -race` and hit reusable-ci's
      hard-coded 60 minute job cap on 3 of its last 8 runs (2026-11-14, -16,
      -18; the other five took 42-47 min). Since `frontend-ci.yml` stopped
      running Go tests (`skip-tests`, 2026-11-18) it is the only place the tests
      `-short` skips run at all. Shard it the way `ci.yml`'s
      `go-test-short-shards` shards the short suite (a sharded job in
      `nightly.yml`), or add a timeout input upstream in github-common; do not just raise the
      cap. `internal/plugins/maintenance` alone is ~715 s there.
