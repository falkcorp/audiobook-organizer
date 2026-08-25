## staticcheck gates nothing on a PR — decide whether it should

`make ci` runs `staticcheck ./...`, and until 2026-08-25 that target SKIPPED
with "staticcheck not installed, skipping" and let the build continue whenever
the binary was absent. A gate that skips and still reports green has never
proven anything on any machine that lacked the tool, and the output does not
distinguish "ran and passed" from "never ran". Three findings accumulated on
main behind it (SA4004 in `internal/server/reconcile.go`, U1000 in
`internal/audiobooks/service.go`, S1002 in `internal/server/handlers/abs/browse.go`).

The skip is fixed — the target now fails with install instructions, matching
`oplint` / `sdkguard` / `fmt-check`. What is NOT fixed is the coverage gap that
made the skip so costly:

- **staticcheck does not run in the PR workflows.** `ci.yml` delegates to
  `reusable-ci-minimal.yml`, whose `go-lint` job runs **golangci-lint**, not
  staticcheck. So a PR can be all-green on GitHub while `staticcheck ./...` is
  red on the same commit — which is exactly what happened: PRs merged green all
  through 2026-08-24/25 while main's `make ci` was red.
- `nightly.yml`'s header comment says the nightly full suite includes
  staticcheck. That claim has NOT been verified inside the reusable workflow it
  calls; it lives in another repository. **Verify it before relying on it** — a
  comment asserting a job runs is not evidence that it runs (this repo has been
  bitten by exactly that twice this month).

So today the only place staticcheck can block a change before merge is a
contributor's local `make ci`, on a machine that happens to have it installed.

The decision to make:

- **Add staticcheck to the PR workflow**, so it gates like every other check.
  Cost: one more job; the repo is currently at zero findings once the three
  above land, so it would start green.
- **Or** accept it as a local/nightly-only check and say so explicitly in the
  Makefile and CONTRIBUTING, so nobody again reads a green PR as
  "staticcheck-clean".

Doing neither leaves a lint whose findings reach main unopposed.

- [ ] Verify whether `nightly.yml`'s reusable workflow actually runs staticcheck
- [ ] Decide: add to PR CI, or document it as local/nightly-only
- [ ] Audit the other `command -v <tool>` guards in the Makefile for the same skip-and-pass shape
