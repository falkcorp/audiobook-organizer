### Fixed

#### The bench-tagged build compiles again

`go build -tags bench ./...` had been broken since 2026-04-18. Commit `b6fe7c5a`
("refactor: extract internal/dedup package from server") moved
`AuthorDedupGroup` and `FindDuplicateAuthors` from `internal/server` to
`internal/dedup` and updated `internal/server/bench.go`, but missed four call
sites in `cmd/dedup_bench.go`, `cmd/dedup_bench_batch.go`,
`cmd/dedup_bench_runner.go` and `cmd/dedup_bench_types.go`.

Because those files sit behind `//go:build bench`, neither `go build ./...` nor
`make ci` ever compiled them, so the breakage survived four months of green CI.
The four references are repointed at `internal/dedup`; no conversion was needed,
since `AuthorData.Authors` was already `[]database.Author`.

#### `make sdkguard` passes again

The SDK dependency guard had been failing since 2026-07-18 on three packages
that leaked into `pkg/plugin/sdk`'s dependency tree: `internal/audioutil` (via
`internal/fingerprint`), `internal/syncapi/progress` and `internal/cache` (both
via `internal/database`).

None of the three is a real contract violation — all are *transitive* deps of
packages the guard already approved, invisible to SDK consumers. The guard's own
doc comment claimed it allowed approved packages "and their own transitive
dependencies", but it matched against a flat hand-maintained list, so every
transitive dep that landed through an unrelated PR showed up as a violation.

### Changed

#### `sdkguard` is now a two-tier check with a tracked snapshot

`tools/cmd/sdkguard` was restructured to enforce the policy it documents:

- **Tier 1 (roots)** — the packages `pkg/plugin/sdk` imports *directly* must be
  declared in `allowedRoots`. This is the SDK's public contract; widening it
  requires a human edit and cannot be silenced by regenerating anything.
- **Tier 2 (closure ratchet)** — the full transitive internal dependency set
  must match the committed `tools/cmd/sdkguard/internal-deps.txt` exactly, in
  **both** directions. New deps and stale entries both fail.

Legitimate growth is accepted with
`go run ./tools/cmd/sdkguard/main.go -update`, which rewrites that tracked file
so the change lands in the pull request diff where a reviewer sees it, rather
than as a stderr line in a job nobody reads. Verified with four negative
controls: a forbidden direct import, an unrecorded new dependency, a stale
snapshot entry (all exit 1), and the clean tree (exit 0).

### Added

#### CI now runs the two checks that nothing was running

Neither failure above was detectable from CI, because neither check ran there —
`sdkguard` existed only in the local `Makefile`, and nothing compiled the
`bench` build tag at all. A new `SDK Deps & Bench Build` job in
`.github/workflows/ci.yml` runs both.

A new `make bench-check` target typechecks the bench-tagged tree, and `make ci`
now depends on it alongside `sdkguard`, so the local and CI contracts match and
neither can quietly start checking less than the other. It is deliberately
distinct from the existing `build-bench`, which links a real binary scoped to
what `main` can reach; `bench-check` compiles `./...` with no output file.
(`build-bench` would in fact have caught this particular breakage — it simply
never ran anywhere.)

Note that, like the existing interface-width ratchet, this job **reports but
does not block**: main's required checks are currently just "Minimal CI /
Minimal CI Summary", "Require changelog fragment" and "TODO Fragment Headers".
Making it binding is a branch-protection change, i.e. a human action.
