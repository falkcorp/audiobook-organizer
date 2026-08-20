- [x] **BENCH-BUILD** `go build -tags bench ./cmd/...` fails on `main`
      (confirmed pre-existing, unrelated to the env-var consolidation PR that
      surfaced it): `cmd/dedup_bench_batch.go`, `cmd/dedup_bench_runner.go`,
      `cmd/dedup_bench_types.go` reference `server.AuthorDedupGroup`, and
      `cmd/dedup_bench.go` references `server.FindDuplicateAuthors` — neither
      symbol exists in `internal/server` anymore. The dedup-bench CLI tooling
      is `//go:build bench`-gated so `make ci`/plain `go build ./...` never
      catches this; someone removed/renamed the symbols in `internal/server`
      without updating the bench tools.

      **Fixed in #2643.** `b6fe7c5a` (2026-04-18) moved both symbols to
      `internal/dedup`; the four call sites are repointed. Broken for four
      months. Recurrence closed by a new `make bench-check` target, wired into
      `make ci` and into the `SDK Deps & Bench Build` CI job.

- [x] **SDKGUARD-LEAK** `make sdkguard` fails on `main` (confirmed
      pre-existing): `internal/cache`, `internal/audioutil`, and
      `internal/syncapi/progress` have leaked into `pkg/plugin/sdk`'s
      dependency tree, which `tools/cmd/sdkguard` treats as forbidden. Either
      remove the imports pulling these in, or add them to `allowedInternals`
      in `tools/cmd/sdkguard/main.go` with a comment explaining why each is
      safe to expose to SDK consumers.

      **Fixed in #2643**, but neither suggested remedy was the right one: all
      three are *transitive* deps of packages the guard already approved, and
      five of its nine entries were already undocumented accretions of the same
      kind, so appending three more would have re-broken on the next one. The
      guard is now two tiers — declared roots for direct imports, plus a
      bidirectional snapshot ratchet (`internal-deps.txt`) for the transitive
      closure. Red for 33 days on a green `main`, because `sdkguard` ran in no
      workflow at all.
