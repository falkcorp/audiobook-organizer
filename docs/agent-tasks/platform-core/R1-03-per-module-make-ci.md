<!-- file: docs/agent-tasks/platform-core/R1-03-per-module-make-ci.md -->
<!-- version: 1.1.3 -->
<!-- guid: b539aa28-9ed9-4fa5-a48b-a088e5c9fd48 -->
<!-- last-edited: 2026-11-05 -->

# R1-03 — Per-module make ci: the R1 CI classification table (per-module targets, core-only fix-check, separate core ratchet baselines, shards with one core shard, nightly and ratchet-lower per module)

| Field | Value |
|---|---|
| ID | `R1-03` |
| Tier | **T2** (Sonnet / high): many targets and baselines; a move must not silently re-baseline a ratchet |
| Release / wave | R1 / W09 |
| Depends on | R1-02 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 09 §9.4 R1's CI classification (every row); 12 §12.3.11 (core ci target); 03 §3.2 item 4 |
| Branch | `agent/platform-core-r1-03-per-module-make-ci` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `Makefile`
- `core/Makefile`
- `.github/workflows/ci.yml`
- `.github/workflows/nightly.yml`
- `.github/workflows/ratchet-lower.yml`
- `.github/workflows/codeql.yml`
- `.github/workflows/memory-leak-scan.yml`
- `.woodpecker/`
- `scripts/ci/short_test_shards.py`
- `scripts/ci/short_test_weights.json`
- `scripts/tests/test_short_test_shards.py`
- `scripts/deadcode-ratchet.py`
- `core/.errcheck-baseline`
- `core/.interface-width-baseline`
- `core/scripts/ci/check-errcheck-ratchet.sh`
- `core/scripts/ci/check-interface-width.sh`
- `ci/gates.txt`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-03" -b agent/platform-core-r1-03-per-module-make-ci origin/main
cd "$REPO/.worktrees/platform-core-r1-03"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Implement every row of **R1's CI classification table** (09 §9.4 R1, the table after the R1 rows; 03 §3.2 item 4;
12 §12.3.11 `ci` row) so root `make ci` and `make -C core ci` together run each `make ci` prerequisite and each CI
job exactly once, classified per-module, root-only or core-only, and an export of `core/` alone runs a whole core
gate (`core-ci` = `make -C core ci`). The rule that makes this delicate: **a move must not silently re-baseline a
ratchet** (09 §9.4 R1 row `lint-errcheck-ratchet`). Core starts with its own baselines at the values measured on
core's own code (nearly empty today), and the root baselines stay at their committed values.

The table rows, and what this PR does for each (09 §9.4 R1, quoted by row name):

| Row | This PR |
|---|---|
| `staticcheck`, `fmt-check`, `test-short`, `go vet`/`go build`, `tidy-check` | per `GO_MODULE_DIRS`; `fmt-check` excludes `web/`, `core/web/`, `core/examples/*/web/`; `tidy-check` = `go mod tidy -diff` in `.` and `core` (R1-01 wired a minimal form; complete it) |
| `fix-check` | core only (`make -C core fix-check`); no root gate |
| `web-test` | per npm workspace: `web` now; `core/web` gets its line in `make -C core ci` as "not applicable" until R1-30 creates it; `test-all-short` = `test-short` + `web-test` |
| `lint-errcheck-ratchet`, interface-width | per module, separate baselines: create `core/.errcheck-baseline`, `core/.interface-width-baseline` (the measured value on core's code at your base), core copies of the scripts under `core/scripts/ci/` (`check-errcheck-ratchet.sh`, `check-interface-width.sh`), run from `make -C core ci`; root keeps `.errcheck-baseline` and `.interface-width-baseline` unchanged |
| `lint-deadcode-ratchet` | root, with ABO's and (once it exists) hello's mains, `-filter 'github.com/falkcorp/mediacore/(internal\|kernel)'`; no core baseline; `scripts/deadcode-ratchet.py` gains the filter and the hello main when present |
| `coverage-check-short` | per module: root floor `.ci/coverage-floor.txt` unchanged; core gets a ratchet floor in `make -C core ci` **from the storage move** (R1-23); here add the target that reports "no floor yet" |
| `mocks-check`, `pprof-check` | per module; `core/.mockery.yaml` only from the first core interface with a mock (none yet: the core target reports "not applicable"); core `pprof-check` = `go vet -tags pprof ./... && go test -tags pprof -run Pprof ./...` in core (R1-09 moved the flight recorder's pprof test there) |
| `oplint`, `sdkguard`, `ops-check`, `types-check`, `bench-check` | root only, unchanged |
| `go-test-short-shards` and Woodpecker `test-rest`/`test-native-taglib` | root module plus **one core shard**; `scripts/ci/short_test_shards.py` gains the core shard and fails on stale weight entries (it already does, `short_test_shards.py:257-258`) |
| `nightly.yml` (`test-nightly`), `ratchet-lower.yml` | per module: core's non-short tests nightly; `ratchet-lower` learns core's two baselines |
| `codeql.yml`, `memory-leak-scan.yml`, `fixture-tests`, Woodpecker `test-database`, `test-fixtures-{1,2,3}`, `test-server-scanner`, `checks-build`, `checks-lint` | root (fixtures, scanner, database) / per module (CodeQL, leak scan, build, lint); each in `ci/gates.txt` with its classification |
| `ci/gates.txt` `store-width`, `store-references` | root, unchanged until the split deletes `database.Store` |

Rows owned elsewhere (do not touch): core layering gate, `exclude-check`, `genproto-exclude-root`,
`check_module_skew.py`, `TestCoreHoldsNoAppKeyNames` (R1-01); `vet-darwin`, `vet-integration`,
`check_commits_build.py` (R1-02); `unit-lifecycle` (R1-28); `check_core_version.py`, `core-checkpoint` (R1-06);
`TestBooksMinCoreVersion` (R1-15).

## Dependency check

R1-02 merged: `test -f .github/workflows/extraction-gates.yml && grep -q '^vet-darwin:' Makefile && echo ok`.

## Re-verify (expected at `ac4368bdb`)

```bash
grep -n '^ci:' Makefile        # expect 789 (09 cites :785): mocks-check staticcheck oplint sdkguard ops-check types-check bench-check pprof-check fmt-check test-all-short coverage-check-short lint-errcheck-ratchet lint-deadcode-ratchet
cat .errcheck-baseline .interface-width-baseline     # record both integers in the PR: they must be identical after this PR
sed -n 64p scripts/deadcode-ratchet.py               # expect EXCLUDED_DIR_PREFIXES = ("pkg/", "internal/writeback/")
sed -n 255,260p scripts/ci/short_test_shards.py      # expect the "assigned but not in go list ./..." stale-entry error
ls .woodpecker/                                      # expect the 9 pipeline files of the table
grep -c 'go-version' .github/workflows/nightly.yml .github/workflows/ratchet-lower.yml .github/workflows/codeql.yml .github/workflows/memory-leak-scan.yml
```

## Steps

1. Write the tests first: `scripts/tests/test_short_test_shards.py` gains a case that a core package is
   assigned to exactly one core shard, and that a weight entry for a package absent from both modules'
   `go list ./...` fails (the gate test this task owns, "one core shard, stale entries fail").
2. Implement the table row by row in the files the row lists. Keep each row's change small and named in the
   commit body. Root scripts that core also runs are **copied** into `core/scripts/ci/` (so an export of `core/`
   has them, 12 §12.3.11), with the copies' go-version comments allowlisted per 13 §13.2.2 item 4.
3. Baselines: measure core's errcheck and interface-width counts with the new core scripts' `--measure` and
   write them to the two new core baseline files; the root numbers must not change in this PR (if the root
   measurement differs from the committed root baseline at your base, STOP: a predecessor re-baselined).
4. `ci/gates.txt`: one row per new gate (`core-errcheck`, `core-interface-width`, `core-pprof`, `core-mocks`,
   `core-coverage-floor`, with job `core-job`), and the classification of the CodeQL, leak-scan and Woodpecker
   rows; `scripts/tests/test_run_gates.py` must stay green.
5. Workflows: `ci.yml` jobs mirror root `make ci` (per-module where the table says so); add the core shard to
   `go-test-short-shards`; `nightly.yml`, `ratchet-lower.yml`, `codeql.yml`, `memory-leak-scan.yml` and the
   affected `.woodpecker/*.yaml` gain the core module where the table says "per module". Actions pinned to SHAs;
   `go-version` from the pin (never a new literal; 13 §13.2.2 item 4).
6. Keep the 35-minute budget (09 §9.2.10): no job above `timeout-minutes: 30` in a required check except the
   existing 35-minute shard job; if adding the core shard pushes the shard job past its budget, add a shard rather
   than raising a timeout.
7. Header bumps; fragment `changelog.d/<today>_per_module_ci.md` (`### Changed`: "CI: every gate runs per
   module or for one module, as R1's classification table states; core has its own baselines."). Commit:
   `ci(core): per-module make ci and the R1 CI classification`.

## Tests this task owns

- Every row of R1's CI classification table (09 §9.4 R1 Verification): the PR description has one line per row
  with the command that proves it and its result.
- `scripts/ci/short_test_shards.py` (one core shard, stale entries fail), via `scripts/tests/test_short_test_shards.py`.

## Code contract

No Go API. Every new make target prints one line per gate and exits non-zero only on a real failure; "not applicable" lines exit 0.

## Acceptance

```bash
make ci                          # expect exit 0 and "All CI checks passed"
make -C core ci                  # expect exit 0; lines "web-test: not applicable", "mocks-check: not applicable", "coverage-floor: no floor yet"
git diff origin/main -- .errcheck-baseline .interface-width-baseline .ci/coverage-floor.txt | wc -l   # expect 0
cat core/.errcheck-baseline core/.interface-width-baseline          # expect two integers equal to the --measure output
python3 scripts/ci/short_test_shards.py --check                     # expect exit 0
python3 -m pytest scripts/tests/test_short_test_shards.py scripts/tests/test_run_gates.py -q   # expect all passed
(tmp=$(mktemp -d) && git archive HEAD core | tar -x -C "$tmp" && make -C "$tmp/core" ci; echo "export ci exit $?")   # expect "export ci exit 0": core CI runs from core/ alone (12 SC4(a))
```

## STOP conditions

- A root baseline would have to change to keep `make ci` green.
- A gate would run twice (once per module and once again from the other module's target).
- An export of `core/` alone cannot run `make -C core ci` because a script lives outside `core/`.

## Out of scope

The rows owned by R1-01, R1-02, R1-06, R1-15, R1-28 (listed above). The core coverage floor's value (R1-23 sets it
at the storage move).

## Rollback

Revert the PR; baselines and runtime code are untouched.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
