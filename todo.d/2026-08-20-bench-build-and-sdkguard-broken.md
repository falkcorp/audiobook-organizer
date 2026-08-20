- [ ] **BENCH-BUILD** `go build -tags bench ./cmd/...` fails on `main`
      (confirmed pre-existing, unrelated to the env-var consolidation PR that
      surfaced it): `cmd/dedup_bench_batch.go`, `cmd/dedup_bench_runner.go`,
      `cmd/dedup_bench_types.go` reference `server.AuthorDedupGroup`, and
      `cmd/dedup_bench.go` references `server.FindDuplicateAuthors` — neither
      symbol exists in `internal/server` anymore. The dedup-bench CLI tooling
      is `//go:build bench`-gated so `make ci`/plain `go build ./...` never
      catches this; someone removed/renamed the symbols in `internal/server`
      without updating the bench tools.
- [ ] **SDKGUARD-LEAK** `make sdkguard` fails on `main` (confirmed
      pre-existing): `internal/cache`, `internal/audioutil`, and
      `internal/syncapi/progress` have leaked into `pkg/plugin/sdk`'s
      dependency tree, which `tools/cmd/sdkguard` treats as forbidden. Either
      remove the imports pulling these in, or add them to `allowedInternals`
      in `tools/cmd/sdkguard/main.go` with a comment explaining why each is
      safe to expose to SDK consumers.
