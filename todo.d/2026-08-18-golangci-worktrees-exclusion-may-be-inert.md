### Decide whether `.golangci.yml`'s `\.worktrees/` path exclusion should be deleted

`scripts/check-interface-width.sh` (v1.4.0) now scopes `GOLANGCI_LINT_CACHE` per
worktree, so cached positions cannot cross checkouts and the exclusion is inert
*for the width gate*. It is still live for every other golangci-lint invocation,
including the `go-lint` CI job, which shares the global cache.

The evidence says it protects nothing and can only do harm: each worktree carries
its own `go.mod`, so `go list ./...` from the repo root returns **0** packages
under `.worktrees/`. Package loading never goes there. The exclusion therefore
only ever matches cache-replayed positions — and on 2026-08-18 that made the
width gate report 0 findings when the true count was 4, because all four replayed
with `.worktrees/` paths and were silently dropped.

**The discriminator before deleting it:** run golangci-lint from the repo root
with a clean, isolated cache and the exclusion temporarily removed, with a
sibling worktree present. If no reported path is under `.worktrees/`, the line is
confirmed inert and can go. If one is, golangci-lint's loader does not agree with
`go list` and the line is load-bearing.

Do this in a PR that owns the `go-lint` job's counts — removing it changes what
that job reports, and it is not the width gate's to change.
