<!-- file: docs/agent-tasks/platform-core/F2-17-oplint-becomes-core-tasklint.md -->
<!-- version: 1.1.4 -->
<!-- guid: cf14f86a-65b9-449c-a52b-9c60ce53a8ff -->
<!-- last-edited: 2026-11-05 -->

# F2-17 — `tools/cmd/oplint` becomes `core/archtest/tasklint` with 05-tasks' six rules over core's model, run in CI from F2 over `core/...` and books' task packages, with one shrink-only exemption entry file per conversion row, read as one union

| Field | Value |
|---|---|
| ID | F2-17 (the retired F5-17's work, moved to F2) |
| Release / flip | R2, foundation PRs (F2), **dark** move PR inside an announced move window; no route, schedule or stored row changes |
| Wave | W27 |
| Tier | **T3** (Opus / high): an analyzer re-rooted on a new module path, with the shrink-only ratchet over every conversion row's entry file |
| Depends on | F2-01 (`core/tasks` exists, so the analyzer has constructors to recognise) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f2-17-tasklint
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` (CLAUDE.md). `core/` is the nested module `github.com/falkcorp/mediacore`. This is a
move PR: announce the move window for `tools/cmd/oplint` at least one working day ahead in the tracking issue
(09 §9.2.10); no other PR of W27 touches `Makefile`, `internal/arch/layering_test.go` or
`scripts/ci/short_test_weights.json`.

## What the spec fixes

- 05-tasks §5T.8.4 (normative): `tools/cmd/oplint` moves to `core/archtest/tasklint` (04 §6) with its module path a
  parameter, lands at **F2** with the tasks engine, with its six rules and its exemption list, and runs in CI from F2.
  The rule mapping, copied exactly:

  | HEAD rule | `tasklint` rule | Over core's model |
  |---|---|---|
  | `unbundled-def` | `unregistered-kind` | every `TaskSpec`, `BatchSpec` or `FixerSpec` value built in a module package (a `tasks.Task`, `tasks.Batch` or `tasks.Fixer` call) is returned by that module's `Registrar.Tasks` registration, directly or through a list function it passes there; from F2 until the F5 flip, when ABO makes no `Registrar.Tasks` call, a books list function (5T.8.2 conversion rule 4) that returns it counts |
  | `unregistered-bundle` | — (dropped) | a kind is registered exactly when `Registrar.Tasks` receives it, which `unregistered-kind` already checks |
  | `store-in-deps` | `store-capture` | a definition's `Run` (or `Source`, `Item`, `Finish`, `Candidates`, `Load`, `Evaluate`, `Apply`) captures no store except through `tasks.Writer` (`rc.Writer()`) or the dependencies its owner declared for it |
  | `writer-bypass` | `writer-bypass` | a definition's body writes a module entity only through `tasks.Writer` |
  | `missing-help`, `sequential-empty-reason`, `deletes-owner-review` | the same names | read from `Common.Help`, `tasks.Sequential("<reason>")` and the definition's `Effects` |

- **Where it runs** (§5T.8.4): over `core/...` (core's own kinds, `hello`, `coretest`) and over books' task packages
  (`internal/books/...` and every ABO package whose definitions books will register), as a test in each module (03
  §3.3 `archtest` row: apps run it from their own tests).
- **The dark-conversion ratchet** (§5T.8.4; §5T.8.2 conversion rule 3): `writer-bypass` and `store-capture` run as a
  ratchet over a checked-in exemption list created at F2 with every not-yet-converted books task package on it; it may
  only shrink (a PR that adds an entry fails); each dark conversion PR removes what it converts; the F5 flip requires
  it empty. The other four rules have no exemption and apply from F2.
- **The plan's form of that list** (plan §(l) item 5): a directory, `internal/books/testdata/tasklint_exempt.d/`, read
  as **one union**. This row creates every entry file: one per conversion row, `<row>.txt`, listing the books files
  that row converts (F5-01, F5-02, F5-04, F5-05, F5-06, F5-07, F5-08, F5-09, F5-10, F5-11, F5-12, F5-13: twelve files),
  and `F5-18.txt`, listing the files whose HEAD definitions no conversion converts (T4's among them). F5-03 converts
  core kinds only and has **no** entry file. Each conversion row deletes only its own file; F5-18 deletes its own and
  requires the directory empty. The directory sits in ABO, not in core, because it names books' files.
- 05-tasks §5T.9 "guards" row: the self-tests of `archtest.ResumeContinueCheckpoints` and
  `archtest.RunItemsDeclareParallelism` (§5T.3.6 table, §5T.3.2 paragraph after the behaviour table; 03 R7) are this
  row's.
- 03 §3.3 `archtest` row, §3.5 R4: `archtest/tasklint` is test-support; no non-test core package imports it.
- 06 §6.11.4 row H: `oplint` drops its `init` for an explicit `analysis.Analyzer` flag set.
- 09 §9.4 R1 CI classification (`oplint` row): root-only until its subject moves (to `archtest/tasklint` at F2); 09
  §9.4 F2 row; 04 §8.1 F2, §8.2 (`mediacore/archtest/tasklint` ← `tools/cmd/oplint`, 0 rewired importers).

## Base state: re-verify before editing

Counts at the plan's base `f7ac5b11e`; regenerate at your base and STOP on a zero-hit edit target.

```bash
grep -c 'archtest/tasklint' docs/specs/platform-core/05-tasks.md                      # >=1 (§5T.8.4 exists), else STOP
grep -c 'unregistered-kind' docs/specs/platform-core/05-tasks.md                      # >=1, else STOP
ls tools/cmd/oplint/                                                                  # analyzer.go analyzer_test.go main.go testdata
grep -n 'modulePath  = "github.com/falkcorp/audiobook-organizer"' tools/cmd/oplint/analyzer.go | wc -l   # 1 (line 45)
grep -nE 'dbPath *= modulePath \+ "/internal/(database|books/store)"' tools/cmd/oplint/analyzer.go | wc -l   # 1 (R1-26 may have re-pointed it)
grep -n 'catalogPath = modulePath + "/internal/opscatalog"' tools/cmd/oplint/analyzer.go | wc -l          # 1
grep -n 'func init()' tools/cmd/oplint/analyzer.go | wc -l                            # 1 (the probe-v2 flag)
grep -n 'catSequential   = "sequential-no-reason"\|catOwnerReview  = "owner-review"' tools/cmd/oplint/analyzer.go | wc -l   # 2 (see "Rule names")
grep -n '"github.com/falkcorp/audiobook-organizer/pkg/ops/opstest"' tools/cmd/oplint/analyzer_test.go | wc -l   # 1
find tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases -mindepth 1 -maxdepth 1 -type d | wc -l   # 10
grep -n '^oplint:' Makefile | wc -l                                                   # 1
grep -n '^ci: .*oplint' Makefile | wc -l                                              # 1
grep -n '"tools/cmd/oplint":' internal/arch/layering_test.go | wc -l                  # 1
grep -c 'oplint' scripts/ci/short_test_weights.json                                   # 0 (no entry; listed for the move convention)
grep -c '"tools/cmd/oplint"' scripts/ci/fixture_weights.json                          # 2
test -d core/tasks && echo tasks                                                      # tasks (F2-01)
test -d core/archtest && echo archtest                                                # archtest (R1-01)
test ! -e core/archtest/tasklint && test ! -e internal/books/testdata/tasklint_exempt.d && echo absent   # absent; else see Idempotency
git grep -lE 'OperationDef\{' -- '*.go' ':!*_test.go' ':!tools/' | wc -l              # 182 at f7ac5b11e (HEAD's v2 definition files; input to the entry files)
ls internal/arch/resume_restart_guard_test.go                                         # exists (successor: archtest.ResumeContinueCheckpoints)
ls internal/plugins/runitems_concurrency_lint_test.go                                 # exists (successor: archtest.RunItemsDeclareParallelism)
```

## Files

| Action | Path |
|---|---|
| move | `tools/cmd/oplint/analyzer.go` → `core/archtest/tasklint/analyzer.go` (package `tasklint`, exported `Analyzer`) |
| move | `tools/cmd/oplint/analyzer_test.go` → `core/archtest/tasklint/analyzer_test.go` |
| move | `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` → `core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/...` (re-rooted, 04 §6) |
| create | `core/archtest/tasklint/run.go` (the test entry point apps call; below) and `core/archtest/tasklint/core_test.go` (core's own run over `core/...`) |
| create | `core/archtest/resumecontinue.go`, `core/archtest/runitems_parallelism.go` and their `_test.go` self-tests (the two guards; file names coined here) |
| delete | `tools/cmd/oplint/` (with `main.go`: tasklint runs as a test in each module, not as a command) |
| create | `internal/books/tasklint_test.go` (ABO's run and the ratchet) |
| create | `internal/books/testdata/tasklint_exempt.d/` with 13 files: `F5-01.txt`, `F5-02.txt`, `F5-04.txt` … `F5-13.txt`, `F5-18.txt` |
| modify | `Makefile` (the `oplint` target and its use in `ci`), `internal/arch/layering_test.go` (drop `"tools/cmd/oplint"`), `scripts/ci/short_test_weights.json` (only if the new test package needs a weight), `scripts/ci/fixture_weights.json` (drop the two `tools/cmd/oplint` entries) |
| create | one `changelog.d/` fragment |

## Code contract

- `tasklint.Analyzer *analysis.Analyzer` (name kept from HEAD) with `Analyzer.Flags` (06 §6.6, §6.11.4) for every
  path HEAD hard-coded: the module path, the tasks package (default `github.com/falkcorp/mediacore/tasks`), the
  tasks-test package (default `.../tasks/taskstest`), and the module's store package(s) that `writer-bypass` and
  `store-capture` treat as stores (books passes `internal/books/store`, or `internal/database` if R1-26 has not landed
  at your base). Flag names coined here. No `func init()`; the `probe-v2` flag is deleted.
- `func Run(t *testing.T, opts Options)` (name coined here, in `run.go`): loads `opts.Packages`, runs the analyzer with
  `opts`' flags, and fails `t` on every finding except (a) `missing-help` warnings, reported with `t.Log`, and (b)
  `writer-bypass` and `store-capture` findings in a file the exemption union lists. `Options` (coined): `Packages
  []string`, `ModulePath`, `StorePackages []string`, `ExemptDir string` ("" = no exemptions, core's case),
  `ListFuncs` (how a list function is recognised before the F5 flip; below).
- Rule names: the category strings are exactly the spec's six: `unregistered-kind`, `writer-bypass`,
  `store-capture`, `missing-help`, `sequential-empty-reason`, `deletes-owner-review`. HEAD's analyzer spells two of
  them differently (`sequential-no-reason`, `owner-review`); rename both. The `//oplint:allow <reason>` directive
  becomes `//tasklint:allow <reason>` (coined here) with HEAD's meaning (owner sign-off for `deletes-owner-review`,
  a reviewed exception for `writer-bypass`).
- Recognised constructors: `tasks.Task`, `tasks.Batch`, `tasks.Fixer` (A7: there is no Pipeline shape, 05-tasks
  §5T.0; drop its case and the `unbundled_def_nobundle` fixture's `ops.Pipeline(` call). `unregistered-kind`'s
  reachability roots are the arguments of a `Registrar.Tasks` call and, until F5, the functions `ListFuncs` names
  (a books list function returns `[]tasks.Definition`, 05-tasks §5T.8.2 rule 4). `catalogPath` and every
  `opscatalog` reference are deleted with `unregistered-bundle`.
- **HEAD's definitions.** §5T.8.4 says HEAD's not-yet-converted task packages would fail `writer-bypass` and
  `store-capture`, which is why the ratchet exists. Over core's model alone they contain no `tasks.*` call, so ABO's run
  must also treat HEAD's definition forms as definitions for those two rules: the v2 `OperationDef` literals' `Run`
  (what HEAD's `probe-v2` already analysed) and the `pkg/ops` `Task`/`Batch`/`Fixer` specs. Do this through an
  `Options` field (coined here, e.g. `LegacyDefinitions`) that only `internal/books/tasklint_test.go` sets; core's run
  never sets it; F5-18 deletes it with HEAD's engine. If you find a cleaner way that keeps the invariants below, use it
  and say so in the PR body.

## The entry files (this row creates every one)

Each file is plain text, one repo-relative path per line, sorted, `#` comments allowed (the first line states which
row owns the file and that only that row may delete it). The union is the set of all lines of all files.

1. **Conversion rows (12 files).** `<row>.txt` lists the non-test `.go` files under that row's paths in the README's
   task table (the `Files` column of F5-01, F5-02, F5-04 … F5-13, regenerated at your base; a directory path means
   every non-test `.go` file below it), minus its own entry file. A file is listed by exactly one conversion row; if
   two rows' paths overlap, STOP and report (the waves rely on disjoint conversion files).
2. **`F5-18.txt`.** Every other non-test `.go` file that ABO's run flags with `writer-bypass` or `store-capture` and
   that no conversion row lists (the files whose HEAD definitions no conversion converts, T4's among them).
3. **No other entry.** After the union is written, ABO's run must pass with it, and must fail if any one line is
   removed from a file that still has a finding (the self-check below).

## Invariants and their tests

1. **The six rules hold over core from F2.** `TestTasklint_Core` (name coined here, `core/archtest/tasklint/core_test.go`):
   `tasklint.Run` over `github.com/falkcorp/mediacore/...` with no exemptions passes (`hello.count` and the three
   late-owner kinds included, once F2-05 and F2-11 land; at this row's base only the `core/tasks` fixtures exist).
2. **Each rule fires.** `analyzer_test.go` (moved) runs `analysistest` over the re-rooted fixtures: one fixture package
   per rule with a `// want` line per diagnostic, plus `clean`; `unregistered_bundle` is deleted with its rule; the
   `unbundled_def*` fixtures are renamed `unregistered_kind*` and rewritten over `tasks` and a fake `Registrar.Tasks`
   and list function; fake `pkg/ops` and `pkg/ops/opstest` fixture packages become fakes of `tasks` and
   `tasks/taskstest`; `analyzer_test.go` imports `taskstest` instead of `pkg/ops/opstest`.
3. **ABO's run passes only through the union.** `TestTasklint_Books` (name coined here, `internal/books/tasklint_test.go`):
   `tasklint.Run` over books' task packages (every directory that holds an entry-file line, plus `internal/books/...`;
   hold the package list in the test, F5-18 replaces it with the `Registrar.Tasks`-derived set) with
   `ExemptDir: "testdata/tasklint_exempt.d"`. Every line of the union must name an existing file, else the test fails
   ("stale exemption").
4. **Shrink-only.** `TestTasklintExemptions_ShrinkOnly` (name coined here): compares the union and the set of entry
   files with the same directory at the merge base with `origin/main` (`git merge-base HEAD origin/main`, then
   `git show <base>:<path>`); an added entry file or an added line fails, a removed one passes. Without git or
   `origin/main` it skips with a message locally and fails under `CI=true`. At F2-17's own PR the base has no
   directory, so the test passes (creation is this row's).
5. **The guards.** `archtest.ResumeContinueCheckpoints(t, pkgs...)` and `archtest.RunItemsDeclareParallelism(t, pkgs...)`
   with the rules of 05-tasks §5T.3.6 (a Task-shape def with `ResumeContinue` reaches `.Checkpoint(` or a
   `RunItems`/`RunSeq` with `Watermark`; the 21-entry allowlist of `internal/arch/resume_restart_guard_test.go` carried
   over, shrink-only) and §5T.3.2 (accepted parallelism arguments (a), (b), (c); flags `tasks.Parallelism{}`, a zero
   `var`, `Sequential("")`, `Fixed(n, "")`). Self-tests (05-tasks §5T.9 "guards" row): `TestResumeContinueCheckpoints_SelfTest`
   and `TestRunItemsDeclareParallelism_SelfTest` (names coined here) over fixture packages with one passing and one
   failing case per clause. Also the spec-named pair of 05-tasks §5T.9 (the "R7 pass-through" row), beside them:
   `TestArchtest_PassThroughParallelismAccepted` (a pass-through through a `tasks.Parallelism`-typed field, clause (b),
   and through a `//mediacore:required` options field, clause (c), is accepted, so core's own
   `RunItems(rc, o.Parallelism, …)` passes; COR-23) and `TestArchtest_ZeroLiteralFlagged` (a `tasks.Parallelism{}`
   literal, a zero `var`, `Sequential("")` and `Fixed(n, "")` are flagged). Core's own call over `core/...` goes in `core/archtest/tasklint/core_test.go` beside
   invariant 1. The HEAD tests they succeed stay until F5-18 (they check HEAD's code).
6. **Test-support only.** No non-test core package imports `archtest/tasklint` (03 R4).

## Steps

1. Base-state greps; announce the move window; write the plan into the PR description, including the 13 entry
   files' line counts.
2. `git mv` the three oplint paths (history kept); package `tasklint`; update every moved file's header (path,
   version bump, `last-edited`).
3. Re-root the analyzer and fixtures; turn the hard-coded paths into flags; delete `init()`, `probe-v2`,
   `unregistered-bundle` and the Pipeline case; rename the categories and the allow directive.
4. Write `run.go` and the tests of invariants 1, 2, 5 and 6 (they fail), then make them pass.
5. Generate the 13 entry files (a throwaway local script is fine; do not commit it), then write
   `internal/books/tasklint_test.go` (invariants 3 and 4) and make it pass. Self-check: delete one line of an entry
   file whose path has a finding, see `TestTasklint_Books` fail, restore it.
6. `Makefile`: replace the `oplint` target with `tasklint` (name coined here) running
   `cd core && go test ./archtest/... -run 'TestTasklint_Core|SelfTest|TestArchtest_' -count=1` and
   `go test ./internal/books/ -run 'TestTasklint_' -count=1`; put `tasklint` where `oplint` was in `ci:` and in the
   `.PHONY` list.
7. `internal/arch/layering_test.go`: remove the `"tools/cmd/oplint"` entry. `scripts/ci/fixture_weights.json`: remove
   the two `"tools/cmd/oplint"` entries. `scripts/ci/short_test_weights.json`: add a weight only if its checker asks.
8. Headers, changelog fragment, acceptance.

## Acceptance (commands and expected output)

```bash
test ! -e tools/cmd/oplint && echo gone                                                   # gone
(cd core && go vet ./archtest/... && go test ./archtest/... -race -count=1)               # ok (tasklint, the guards' self-tests, TestTasklint_Core)
(cd core && go list -deps ./tasks/ ./kernel/ ./app/) | grep -c 'archtest/tasklint'        # 0 (test-support only, 03 R4)
grep -c 'falkcorp/audiobook-organizer' core/archtest/tasklint/analyzer.go core/archtest/tasklint/run.go   # 0 and 0 (no hard-coded ABO path)
grep -rn 'unregistered-bundle\|probe-v2\|func init()' core/archtest/tasklint/*.go | wc -l # 0
grep -ho '"\(unregistered-kind\|writer-bypass\|store-capture\|missing-help\|sequential-empty-reason\|deletes-owner-review\)"' core/archtest/tasklint/analyzer.go | sort -u | wc -l   # 6
ls internal/books/testdata/tasklint_exempt.d/ | wc -l                                     # 13
ls internal/books/testdata/tasklint_exempt.d/F5-03.txt 2>/dev/null | wc -l                # 0 (F5-03 has none)
go test ./internal/books/ -run 'TestTasklint_Books|TestTasklintExemptions_ShrinkOnly' -count=1 -v | grep -c '^--- PASS'   # 2
go test ./internal/arch/ -count=1                                                         # ok
make tasklint                                                                             # exit 0
make -C core ci                                                                           # exit 0
make ci                                                                                   # exit 0 (tasklint in place of oplint)
```

## STOP conditions

- Two conversion rows' paths list the same file: report it; the entry files must be disjoint.
- `TestTasklint_Core` fails on a core definition: report it (a core finding is a defect in an earlier F2 row, not
  something to exempt; core has no exemption list).
- A rule cannot be expressed over core's model without inventing a registration form the spec does not state: report
  it, do not guess.

## Out of scope

Converting any definition or deleting any entry file (F5-01, F5-02, F5-04 … F5-13, F5-18); requiring the directory
empty and deleting `LegacyDefinitions` (F5-18); `TestCoreRun_NeverCallsModuleWriter` (F5-03); `tools/cmd/sdkguard`
(R3); `tools/cmd/opsgen` (R3-02); deleting `internal/arch/resume_restart_guard_test.go` and
`internal/plugins/runitems_concurrency_lint_test.go` (they check HEAD's code until F5-18).

## Idempotency / rollback

If `core/archtest/tasklint/analyzer.go` exists and `tools/cmd/oplint` is gone, run the acceptance instead. Rollback =
revert the move PR: one revert restores `tools/cmd/oplint` and the `make oplint` gate and removes the exemption
directory. Once a conversion row has deleted its entry file, a revert of this PR must also restore nothing of that
row's: revert only before the first conversion PR merges, else report.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task; move window announced (09 §9.2.10).
- File headers on every moved or created Go and Markdown file, version bumped; `changelog.d/` fragment without a
  header. The entry files are data read verbatim: their `#` comment lines say what they are; give them no 4-line header
  (a header line would be parsed as a comment anyway; say so in the PR body).
- Move PR: list every `old ==> new` path in the description; build at every commit
  (`scripts/ci/check_commits_build.py`); `go fix` on the moved files (04 §2.2).
- Conventional commit, e.g. `refactor(core/archtest): oplint becomes tasklint with the exemption ratchet (dark)`, body
  ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer (§9.2.1).
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- CLAUDE.md concurrency rules for any loop you add (the analyzer runs per package under `go/analysis`; add no
  unbounded fan-out).
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- 05-tasks §5T.8.4 says the last three rules keep "the same names", but HEAD's analyzer spells two of them
  `sequential-no-reason` and `owner-review`; 04 §3 and §6 use `sequential-empty-reason` and `deletes-owner-review`. The
  brief uses the spec's spelling.
- The spec does not say how `writer-bypass` and `store-capture` see HEAD's not-yet-converted definitions, which have no
  `tasks.*` call; the brief has ABO's run also analyse HEAD's definition forms (as HEAD's `probe-v2` did) through an
  option F5-18 deletes. A spec line would remove the latitude.
