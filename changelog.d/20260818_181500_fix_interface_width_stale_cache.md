### Fixed

#### The interface-width ratchet no longer reports a count it cannot measure

The gate could be wrong in both directions depending on nothing more than which
git worktrees existed on disk and what was in a cache directory. Both were
measured on 2026-08-18 against a true count of 4.

**It could fail on a clean tree.** Run from `.worktrees/pathrepair` it reported
"interface width went UP (4 -> 6)". The two extra findings were `BookReader` and
`ServerDeps` — both of which carry an explained `//nolint:interfacebloat` at
exactly the line each was reported against, in the live tree. golangci-lint had
attributed them to `../absplit/...`, a worktree that had been deleted.

**Worse, it could pass silently on a tree with four findings.** Run from the repo
root while `.worktrees/widthgate` existed, it reported `actual=0` — all four
findings replayed with `.worktrees/` paths, where `.golangci.yml`'s exclusion
dropped them. The gate then said "went DOWN (4 -> 0)" and advised setting the
baseline to 0. Following that advice would have disabled the gate permanently,
which makes this the more dangerous half: a silent pass whose own remediation
text argues for making the silence permanent.

Root cause for both: golangci-lint's result cache is keyed by file *content*, and
every git worktree of this repo declares the same module path with byte-identical
files, so an unchanged file replays whichever path was recorded first — outliving
the checkout that produced it. A finding's path is not cosmetic. `//nolint`
suppression is resolved by re-reading the source at the reported position, and
the `\.worktrees/` exclusion matches on that same path. So a replayed path either
loses a suppression (leaking a finding) or gains an exclusion (hiding one).

Package loading was never involved: each worktree has its own `go.mod`, so
`go list ./...` from the repo root returns zero packages under `.worktrees/`.
Only cached *positions* cross the boundary.

`scripts/check-interface-width.sh` now scopes `GOLANGCI_LINT_CACHE` to the
worktree it runs in, so positions can only come from the current checkout, and
exits 2 — "the instrument did not run" — if any reported path fails to resolve,
rather than reporting a number. That joins the existing exit-2 case for a v1
binary reading the v2 config; exit 1 stays reserved for a real ratchet violation.

This also corrects `.golangci.yml`, which documented the cross-worktree
attribution problem but concluded "it does not change the number". That held only
while no declaration carried a `//nolint` — suppressed findings are exactly the
ones it fails for.
