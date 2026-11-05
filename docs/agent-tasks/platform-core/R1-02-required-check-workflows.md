<!-- file: docs/agent-tasks/platform-core/R1-02-required-check-workflows.md -->
<!-- version: 1.1.1 -->
<!-- guid: 08146005-8663-4d6f-ab5e-f7cc388f67e9 -->
<!-- last-edited: 2026-11-05 -->

# R1-02 — Required-check workflows: extraction-gates as parallel jobs, vet-darwin and vet-integration over both modules, the changes classifier, check_commits_build.py

| Field | Value |
|---|---|
| ID | `R1-02` |
| Tier | **T2** (Sonnet / high): workflow and classifier logic with skip-as-success semantics; no runtime code |
| Release / wave | R1 / W05 |
| Depends on | R1-01 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.2.1 required-checks table; 09 §9.2.10 (35-minute budget, timeout-minutes <= 30); 09 §9.4 R1 CI classification (vet-darwin, vet-integration, check_commits_build.py) |
| Branch | `agent/platform-core-r1-02-required-check-workflows` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `.github/workflows/extraction-gates.yml`
- `.github/workflows/ci.yml`
- `scripts/ci/check_commits_build.py`
- `scripts/tests/test_check_commits_build.py`
- `scripts/tests/test_ci_classifier.py`
- `scripts/tests/test_run_gates.py`
- `ci/gates.txt`
- `Makefile`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-02" -b agent/platform-core-r1-02-required-check-workflows origin/main
cd "$REPO/.worktrees/platform-core-r1-02"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Create the required-check workflows R1 needs before its moves start (09 §9.2.1 required-checks table; 09 §9.2.10
CI budget; 09 §9.4 R1 CI classification rows `vet-darwin`, `vet-integration`, `check_commits_build.py`,
`ci/gates.txt` `store-*`):

1. **`extraction-gates`** (`.github/workflows/extraction-gates.yml`, new): parallel jobs, each with
   `timeout-minutes` ≤ 30 (09 §9.2.10). In this PR it holds one job, `check-commits-build`, which runs
   `scripts/ci/check_commits_build.py` on a PR whose diff renames a file into `core/`, and a terminal job that
   reports success when every job ran or was skipped by its job-level `if:`. Later rows add jobs: R1-10 the
   dashboard-reference test with promtool, R1-23 `TestBootCrashMatrix_ABO`, F2-14/F2-15 the cutover suite.
   Trigger: every PR that touches a non-doc file (09 §9.2.1), via the `changes` classifier (below).
2. **`vet-darwin` and `vet-integration`** as required PR jobs over **both** modules: `GOOS=darwin
   CGO_ENABLED=0 go vet ./...` **without** `embed_frontend` (no placeholder `web/dist` is ever created; 03 §3.5.1
   item 4) and `go vet -tags integration ./...`, each for `.` and `core`. Root Makefile targets `vet-darwin`
   (new) and `vet-integration` (exists, `Makefile:370`, root only today) loop over `GO_MODULE_DIRS`; both join
   root `make ci`; both are registered in `ci/gates.txt` (09 §9.4 R1 classification).
3. **The `changes` classifier** (`ci.yml` job `changes`, lines 49–99 at `f7ac5b11e`): every path-skipped job skips
   through a job-level `if:` on its outputs, so a skip reports success instead of leaving the check "expected";
   the classifier sets `go=true` **before** its docs glob for every Markdown file a Go test or `//go:embed`
   reads (09 §9.2.1 `ci.yml` row, OPS5-4). `core/**` already falls to `go=true` (the `*)` case); keep it so.
4. **`scripts/ci/check_commits_build.py`** + `scripts/tests/test_check_commits_build.py` (12 §12.3.11 `ci` row;
   04 §2.2): for a PR whose diff renames a file into `core/` (`git diff --name-status -M90% --diff-filter=R
   <base>...HEAD`), check out each commit of the PR in a temporary worktree and run `go build ./... && go -C core
   build ./...`; report every commit that fails, and every moved file whose first-commit rename similarity is
   below 90 % (12 §12.8.1's threshold). Exit 0 with "no rename into core/: nothing to check" otherwise.
5. **`scripts/tests/test_ci_classifier.py`** (gate test this task owns): extracts the `Classify changed paths`
   `run:` script from `ci.yml`, runs it under `bash` with a stub `git` on `PATH` that prints a given file list,
   and asserts the outputs for: a docs-only PR (`go=false web=false`); `web/src/x.tsx` (`web=true`); a `core/`
   file (`go=true`); `web/src/generated/ops.ts` (`go=true web=true`); and for **every** Markdown path a Go test
   or `//go:embed` reads (found by scanning `*_test.go` for `os.ReadFile`/`os.Open` of a `.md` literal and
   `//go:embed` patterns matching `*.md`), `go=true`. At `f7ac5b11e` that list is empty (no Go file embeds or
   reads a `.md` file); the test must still fail on a fixture that adds one without a classifier case.
6. **`scripts/tests/test_run_gates.py`**: validate the `core-job` job value R1-01 added: every `core-job` gate's
   command runs inside `make -C core ci` or `.github/workflows/core-ci.yml` (mirror of the existing
   `test_own_job_gates_run_in_a_ci_job`), and every `own-job` gate of this PR (`vet-darwin`, `vet-integration`)
   runs in a `ci.yml` or `extraction-gates.yml` job.

## Dependency check

R1-01 merged: `test -f core/go.mod && test -f .github/workflows/core-ci.yml && grep -q core-job scripts/ci/run_gates.py && echo ok`.
If `run_gates.py` has no `core-job`, STOP and report: R1-01 owns that change.

## Re-verify (expected at `f7ac5b11e`, plus R1-01's changes)

```bash
grep -n '^  changes:' .github/workflows/ci.yml                     # expect 49
grep -n 'name: Classify changed paths' .github/workflows/ci.yml     # expect 1 (line 64)
grep -n 'timeout-minutes: 35' .github/workflows/ci.yml              # expect 1 (667: go-test-short-shards; 09 cites :667,859)
grep -n '^vet-integration:' Makefile                                # expect 1 (370)
grep -c 'vet-integration\|vet-darwin' .github/workflows/*.yml ci/gates.txt   # expect 0 everywhere: not run by any workflow today
test ! -e .github/workflows/extraction-gates.yml && echo NEW        # expect NEW
git grep -n 'go:embed' -- '*.go' | grep -c '\.md'                   # expect 0
```

## Steps

1. Write `scripts/tests/test_ci_classifier.py` and `scripts/tests/test_check_commits_build.py` first (they fail).
2. Implement `scripts/ci/check_commits_build.py` (Python, stdlib only; `git worktree add --detach` under
   `mktemp -d`, removed in `finally`). Its test builds a throwaway git repo with two tiny modules (root with a
   `replace` to `./core`) and a three-commit history: (a) all commits build, a rename into `core/` at 100 %
   similarity: exit 0; (b) a middle commit that breaks the root build: exit 1 naming that commit; (c) a rename
   whose first commit changes the file below 90 % similarity: exit 1 naming the file.
3. Edit the classifier in `ci.yml`: add a commented block of `go=true` cases placed **before** the
   `web/*|docs/*|*.md|...` line, empty today except a comment that `test_ci_classifier.py` enforces it. Do not
   change the other cases. Make every job that has `needs: changes` skip through a job-level `if:` (it already
   does at `f7ac5b11e`; add the same to any job you add).
4. Add `vet-darwin` to root `Makefile` (`for d in $(GO_MODULE_DIRS); do (cd $$d && GOOS=darwin CGO_ENABLED=0 go vet ./...); done`)
   and make `vet-integration` loop over `GO_MODULE_DIRS` too; add both to the `ci:` prerequisites (line 789).
   Add `vet-darwin` and `vet-integration` jobs to `ci.yml` (`needs: changes`, `if: needs.changes.outputs.go == 'true'`,
   `timeout-minutes: 15`, `go-version` from the pin, actions pinned to SHAs, run the make targets).
5. Create `.github/workflows/extraction-gates.yml`: `on: pull_request` (and `merge_group` if Q-I chose a merge
   queue); a `changes` job (reuse the classifier by calling the same script text: copy the step verbatim and add a
   comment that `test_ci_classifier.py` checks both copies are identical, and extend that test to assert it); job
   `check-commits-build` (`if: needs.changes.outputs.go == 'true'`, `fetch-depth: 0`, `timeout-minutes: 30`); a final
   job `extraction-gates` (`needs: [changes, check-commits-build]`, `if: always()`) that fails if any needed job
   `failed` or was `cancelled`, so the required check is one stable name.
6. `ci/gates.txt`: add `vet-darwin` and `vet-integration` (`check`, `own-job`) and `commits-build`
   (`python3 scripts/ci/check_commits_build.py`, `check`, `own-job`). Extend `scripts/tests/test_run_gates.py`
   per Goal item 6 so `own-job` gates may also run in `extraction-gates.yml`.
7. Header bumps; fragment `changelog.d/<today>_required_checks.md` (`### Added`: "CI: extraction-gates,
   vet-darwin and vet-integration required checks; every commit of a move PR must build."). Commit:
   `ci(extraction): extraction-gates, vet-darwin, vet-integration and the commit-build check`.

## Code contract

No Go API. Script contract: `scripts/ci/check_commits_build.py --base <ref>` exits 0 (nothing to check, or every commit builds) or 1 (names each failing commit and each moved file under 90 % similarity). Make targets `vet-darwin`, `vet-integration` loop over `GO_MODULE_DIRS`.

## Acceptance

```bash
python3 -m pytest scripts/tests/test_ci_classifier.py scripts/tests/test_check_commits_build.py scripts/tests/test_run_gates.py -q   # expect all passed
python3 scripts/ci/check_commits_build.py --base origin/main      # expect "no rename into core/: nothing to check", exit 0
make vet-darwin && make vet-integration                           # expect both pass over . and core
grep -n 'timeout-minutes' .github/workflows/extraction-gates.yml  # expect every value <= 30
python3 -c "import yaml;[yaml.safe_load(open(f)) for f in ('.github/workflows/ci.yml','.github/workflows/extraction-gates.yml')];print('YAML_OK')"   # expect YAML_OK
make ci && make -C core ci                                        # expect exit 0 for both
```

After the PR is open: the PR's checks list shows `extraction-gates`, `vet-darwin` and `vet-integration` green;
a docs-only test PR (do not merge it) shows them skipped as success, not "expected". Record both in the PR.

## Out of scope

The per-module classification of every other `make ci` target (R1-03); `docker-image` (R1-04);
`unit-lifecycle` (R1-28); the dashboard-reference job (R1-10). Making the checks *required* in the repository
ruleset is the owner's (Q-I); the PR description lists the check names for OWNER-01's ruleset.

## Rollback

Revert the PR; no runtime code changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
