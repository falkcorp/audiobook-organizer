<!-- file: docs/agent-tasks/platform-core/PC-01-04-required-check-workflows.md -->
<!-- version: 1.0.0 -->
<!-- guid: 47077052-5d46-45b2-a1be-9b2df328ca0e -->
<!-- last-edited: 2026-11-04 -->

# PC-01-04 — Required-check workflows: `extraction-gates`, `compat-rehearsal`, `vet-darwin`, `vet-integration`, the `changes` classifier and `check_commits_build`

| Field | Value |
|---|---|
| ID | PC-01-04 |
| Tier | **T2 (Sonnet/high)**: workflow and classifier logic whose skips must report success and whose triggers must never skip a PR that changes their inputs; no runtime code |
| Wave | W01 (S0, release `S0`) |
| Depends on | PC-01-02 (core module, `make -C core ci`) |
| Priority / effort | P1 / M |
| Branch | `ci/pc-01-04-required-checks` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §6.1, §6.2, §6.3 |
| Owning spec | `09-abo-on-core.md` §9.2.1 "Release gating is a list of named required checks" (the table), S0 "Gates", the S0 CI table, §9.2.10 "CI-time budget", §9.10 row "R-test"; `14-risks-open-questions.md` Q-I |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" show origin/main:core/Makefile | grep -c '^ci:'     # 1: PC-01-02 merged; else STOP
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-01-04" -b ci/pc-01-04-required-checks origin/main
cd "$REPO/../audiobook-organizer-pc-01-04"
npm ci --prefix web
```

Never run `go work init` in the checkout or the worktree. Push workflow files with `git push` only.

## Exact files

Create: `.github/workflows/extraction-gates.yml`, `.github/workflows/compat-rehearsal.yml`,
`scripts/ci/check_commits_build.py`, `scripts/tests/test_check_commits_build.py`, `scripts/tests/test_ci_classifier.py`,
`scripts/ci/ci_time_p50.py`; and (see Planner notes) `scripts/ci/compat_rehearsal.py` and
`scripts/tests/test_ci_time_p50.py`.
Modify: `.github/workflows/ci.yml`, `.github/workflows/prerelease.yml`, `ci/gates.txt`, `Makefile`,
`scripts/tests/test_run_gates.py`, and `scripts/ci/run_gates.py` (see Planner notes); one `changelog.d/` fragment.

## Re-verify (expected at `ac4368bdb` plus PC-01-01/02)

```bash
grep -n "web/\*|docs/\*|\*.md|changelog.d/\*|todo.d/\*|.claude/\*|agents/\*|skills/\*) ;;" .github/workflows/ci.yml | wc -l   # 1 (the docs glob, ~:92)
grep -n 'No Go test reads a file under a docs glob' .github/workflows/ci.yml | wc -l    # 1 (the comment to correct, ~:46-48)
grep -n '^vet-integration:' Makefile | wc -l                                          # 1 (exists; not called by make ci)
grep -c 'vet-integration' .github/workflows/*.yml | grep -v ':0' | wc -l             # 0 (no workflow calls it)
grep -n 'paths-ignore' -A3 .github/workflows/prerelease.yml | grep -c "'\*\*.md'\|'docs/\*\*'"   # 2
grep -n '^JOBS = {"own-job", "go-suite"}' scripts/ci/run_gates.py | wc -l             # 1
ls .github/workflows/extraction-gates.yml .github/workflows/compat-rehearsal.yml 2>/dev/null | wc -l   # 0
```

## What to build

### 1. `ci.yml` `changes` classifier (09 S0 "Gates"; §9.2.1 table row 1)

- Add explicit `go=true` cases **before** the docs glob for `docs/compat-surfaces.md`,
  `internal/books/compat-surfaces.md` and `core/compat/*.md`.
- Correct the `:46-48` comment: it must say that these Markdown files are Go-test or `//go:embed` inputs and are
  classified `go=true` on purpose, and that `scripts/tests/test_ci_classifier.py` enforces the list.
- A PR that changes only owner bookkeeping (`ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`) gets
  `go=false web=false` from `ci.yml` and `extraction-gates` alone (09 §9.2.1, OPS9-18); add a `bookkeeping` output
  so `extraction-gates` can tell.
- `web=true` for `core/web/*` and `scripts/ci/build_web.sh` is PC-01-06's change; leave it.

### 2. `scripts/tests/test_ci_classifier.py` (stdlib `unittest`)

Extract the classify `run:` script from `ci.yml`, run it with `bash` against fixture file lists (stub `git diff`
through a function or a `PATH` shim), and assert outputs. It **lists every file that a Go test or `//go:embed` reads
under a docs glob** and asserts each sets `go=true`; it also scans the tree so the list cannot go stale: every
`//go:embed` pattern and every string literal in a `_test.go` file that names an existing file under a docs glob
(`docs/`, `*.md`, `changelog.d/`, `todo.d/`, `.claude/`, `agents/`, `skills/`) must be in the list (a scan hit not in the
list fails). Cases: a docs-only PR (`go=false`), each listed file (`go=true`), a bookkeeping-only PR, a workflow change
(both true), a diff error (fails open, both true).

### 3. `vet-darwin`, `vet-integration` (09 §9.2.1 table; S0 table; 03 §3.5.1 item 4)

- Root `Makefile`: new `vet-darwin` target, `GOOS=darwin CGO_ENABLED=0 go vet ./...` in each `$(GO_MODULE_DIRS)` dir,
  **without** `embed_frontend` (`go vet` has no `-e`; a tree with no built `web/dist` must pass); `vet-integration`
  becomes per module too; add both to `ci:`.
- `ci.yml`: two jobs named exactly `vet-darwin` and `vet-integration` (`if:` on `needs.changes.outputs.go == 'true'`,
  so a docs-only PR skips with success) running the make targets. Never create a placeholder `web/dist`.
- Fixture proof: a temp-copy test (in `test_ci_classifier.py` or a separate script test) that adds a `//go:build darwin`
  file in a core package importing an ABO path and shows `make vet-darwin` fails on it.

### 4. `extraction-gates.yml` (09 §9.2.1 table row 2; §9.2.10)

A required check: every PR that touches a non-doc file (same classification as `ci.yml`, the three compat ledger
Markdown paths count as non-doc) and every bookkeeping-only PR. **Parallel jobs, each `timeout-minutes` ≤ 30.** At S0
its jobs are: `module-skew` (`scripts/ci/check_module_skew.py` with the two pins), `commits-build`
(`scripts/ci/check_commits_build.py`, which no-ops with success unless the PR's diff renames a file into `core/`), and a
`gates` aggregator job named `extraction-gates` that `needs` them and succeeds when each succeeded or was skipped.
PC-01-07 adds the `check_soak.py` job; later stages add the ratchets, ledger checks, crash matrices and prev-build
tests. Skips report success through job-level `if:`.

### 5. `scripts/ci/check_commits_build.py` (09 S0 "CI classification"; 12 §12.3.11)

For a PR whose diff (base..head) renames any file into `core/` (`git diff --name-status -M` `R*` with a `core/`
destination): check out each commit in order into a temp worktree-free copy (`git archive <sha> | tar -x -C "$tmp"`)
and run `go build ./... && go -C core build ./...`; report every moved file whose rename similarity in its **first**
commit is below git's default rename threshold (50 %). Exit non-zero on any failed build or low-similarity move. Other
PRs: print "no move into core/; nothing to check" and exit 0. Tests `test_check_commits_build.py`: a fixture repo (built
in `tempfile.mkdtemp()`) whose move PR's first commit does not build **fails**; a buildable move passes; a move whose
first-commit similarity is below 50 % fails naming the file; a non-move PR passes.

### 6. `compat-rehearsal.yml` and `make compat-rehearsal` (09 §9.2.1 table row 3; §9.10 R-test)

- Trigger: a PR whose diff changes the `shipped_stage:` or `release:` line of `ci/stage.txt`; every other PR skips
  through a job-level `if:` and reports success; also a nightly `schedule` on `main` that opens or updates **one** issue
  when red and gates nothing.
- A two-row matrix: `PREV_REF` = the latest production release tag (`v<major>.<minor>.<patch>`, no pre-release
  suffix), and `PREV_REF` = the contents of `ci/prev_ref.txt`. A `PREV_REF` that does not resolve fails the job loudly
  (FE7-2), never defaults.
- `make compat-rehearsal` runs `scripts/ci/compat_rehearsal.py`, the S0 skeleton of the R-test: build `prev` from
  `PREV_REF` (extracted with `git archive`, cached by tag) and `cur`; boot `cur` on a seeded temp store until
  `/health` reports `"ready":true`, stop; boot `prev` on the same store, assert ready, stop; boot `cur` again, assert
  ready. Later stages add their surfaces and cells (§9.10); keep the harness a list of named steps so they can.
- Fixture proof of the trigger: a test (in `test_ci_classifier.py`) that runs the trigger expression's shell logic on a
  fixture diff changing `release:` (runs) and on one that does not (skips).

### 7. `prerelease.yml`, `ci/gates.txt`, `run_gates.py`, `ci_time_p50.py`

- `prerelease.yml` `paths-ignore` gains `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`. No Docker build
  is added there (09 S0 "Gates").
- `ci/gates.txt`: rows for `vet-darwin`, `vet-integration`, `compat-rehearsal`, `check-commits-build`,
  `module-skew`, and the core gates with the job value `core-job` (`core-ci` = `make -C core ci`, which runs the core
  layering gate and `TestCoreImportsOnlyCore` from PC-01-03). `scripts/ci/run_gates.py` accepts `core-job`;
  `scripts/tests/test_run_gates.py` checks that an `own-job` gate is run by some workflow under `.github/workflows/`
  (not only `ci.yml`) and a `core-job` gate by `core-ci.yml`.
- `scripts/ci/ci_time_p50.py`: computes the p50 wall-clock of the required checks over the last N non-doc PRs from
  `gh api` workflow-run history (`--prs 20` default, `--since <date>`), prints one Markdown line for the stage record.
  It never writes the stage record itself (an owner step). Unit-test the arithmetic on canned JSON.

## Invariants

- I1: every new check's skip is a job-level `if:` that reports success; no trigger-level `paths:` filter on a required
  check (09 §9.2.1).
- I2: a PR that changes an input of a check can never skip it (the classifier fails open).
- I3: a docs-only PR runs none of the new jobs except `changes`; a bookkeeping-only PR runs `extraction-gates` alone.
- I4: the extraction-gates jobs are parallel and each `timeout-minutes` ≤ 30 (§9.2.10's 35-minute budget).

## Acceptance

```bash
python3 -m unittest discover -s scripts -p 'test_ci_classifier.py' -v 2>&1 | tail -1      # OK
python3 -m unittest discover -s scripts -p 'test_check_commits_build.py' -v 2>&1 | tail -1 # OK
python3 -m unittest discover -s scripts -p 'test_run_gates.py' -v 2>&1 | tail -1           # OK
python3 -m unittest discover -s scripts -p 'test_ci_time_p50.py' -v 2>&1 | tail -1         # OK
rm -rf web/dist && make vet-darwin vet-integration                                          # exit 0 with no built frontend
grep -c 'timeout-minutes: [0-9]*' .github/workflows/extraction-gates.yml                   # one per job; every value <= 30
python3 scripts/ci/check_commits_build.py --base origin/main --head HEAD                   # "no move into core/; nothing to check", exit 0
make ci                                                                                    # last line: ✅ All CI checks passed!
```

On the PR itself: `extraction-gates`, `vet-darwin`, `vet-integration` run green; `compat-rehearsal` reports
"skipped" with success (this PR changes no `ci/stage.txt`). Paste the run links in the PR description.

## Out of scope

`docker-image` (PC-01-05); `unit-lifecycle` (PC-02-06); `check_soak.py` and its job (PC-01-07); the classifier's
`web=true` cases (PC-01-06); the ruleset that makes these checks required (an owner action, OWNER-01 Q-I).

## Rollback

Revert the PR (09 §9.4.1: revert PR). No runtime change.

## Repo rules

Headers on every new/changed file (workflow YAML uses `#` header comments as the existing workflows do). Pin every
action to the SHAs the existing workflows use. Conventional commit `ci: named required checks for the extraction
(extraction-gates, compat-rehearsal, vet-darwin, vet-integration)`, ending `Stage-Release: S0 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`; never squash, never `--admin`.

## Planner notes

- `make compat-rehearsal` needs a harness script and the row lists none; `scripts/ci/compat_rehearsal.py` is added.
- `scripts/ci/run_gates.py` validates the `job` column (`JOBS = {"own-job", "go-suite"}`), so the `core-job` value the
  spec requires (03 §3.2 item 4; 09 S0 table) needs a change there; the row lists only its test.
- This row also registers PC-01-03's core gates in `ci/gates.txt` (PC-01-03 cannot: both run in the same step and this
  row owns the file).
- **Trigger conflict for PC-01-07.** `compat-rehearsal` is required "on every PR that changes `release:`" and its second
  matrix row reads `ci/prev_ref.txt`, which fails loudly when it does not resolve (§9.10). PC-01-07 creates
  `ci/stage.txt` (the creation case, `release: S0`) before OWNER-02 creates `ci/prev_ref.txt`, so a literal trigger
  would make PC-01-07 unmergeable. This brief's trigger compares the base and head **values** of an existing
  `ci/stage.txt`; a PR that creates the file (absent on base) does not trigger it, mirroring `check_soak.py`'s "one
  creation case" (09 §9.2.1). The plan or spec should state this explicitly.
