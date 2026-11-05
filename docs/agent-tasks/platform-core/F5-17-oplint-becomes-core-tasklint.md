<!-- file: docs/agent-tasks/platform-core/F5-17-oplint-becomes-core-tasklint.md -->
<!-- version: 1.0.0 -->
<!-- guid: a0c56fb1-78bc-4c94-a125-65dbf12f251b -->
<!-- last-edited: 2026-11-05 -->

# F5-17 — `tools/cmd/oplint` becomes `core/archtest/tasklint`, re-rooted on the core module path, run over the converted definitions

> **SPEC-GATED.** Step 0 checks for spec text this task needs and does not have. If it is absent, STOP and report;
> do not design the missing rules.

| Field | Value |
|---|---|
| ID | F5-17 |
| Release / flip | R2, tasks flip F5 (dark move PR, inside an announced move window) |
| Wave | W24 |
| Tier | **T2** (Sonnet / high): an analyzer re-rooted on a new module path, with its fixtures and its `make ci` gate |
| Depends on | F2-01 (`core/tasks` exists, so the analyzer has a package to recognise) |
| Gate | Plan, then execute autonomously in a worktree with its own PR, **after** Step 0 passes |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-17-tasklint
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` (CLAUDE.md). `core/` is the nested module `github.com/falkcorp/mediacore`. This is a
move PR: announce the move window for `tools/cmd/oplint` at least one working day ahead in the tracking issue
(09 §9.2.10); no other PR of the wave touches `Makefile`, `internal/arch/layering_test.go` or
`scripts/ci/short_test_weights.json`.

## What the spec fixes

- 04 §6: "`tools/cmd/oplint` survives as `mediacore/archtest/tasklint`; its hard-coded module path
  (`analyzer.go:45`) and the ~12 fixture packages under
  `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` are re-rooted on the core module path."
- 04 §3 row `tools/cmd/oplint` (bucket `merge`, → `mediacore/archtest/tasklint`): it lints task bundles
  (unbundled-def, writer-bypass, store-in-deps, missing-help, sequential-empty-reason, deletes-owner-review);
  "these rules govern every app's `tasks` definitions, so they are a `tasks` platform concern delivered through
  `archtest` (apps run it from their own tests)"; `modulePath` "becomes a parameter"; the test imports
  `pkg/ops/opstest` (`analyzer_test.go:35`).
- 06 §6.11.4 (the `func init()` table, row H): "`oplint` moves to core with `tasks` and drops its `init` for an explicit
  `analysis.Analyzer` flag set".
- 03 §3.3 `archtest` row and §3.5 R4: `archtest/tasklint` is a **test-support** package (may import any core
  package; no core non-test package imports it).
- 09 §9.4 R1 CI classification: `oplint` is root-only "until their subject moves (oplint → `archtest/tasklint` at F5)".
- 04 §8.1 F5 row and §8.2 (`mediacore/archtest/tasklint` ← `tools/cmd/oplint`, 0 rewired importers).

## Step 0 — the missing spec text (run first)

HEAD's analyzer (`tools/cmd/oplint/analyzer.go`, `Analyzer.Doc`) has seven rules. Three of them are defined
against HEAD's v3 registration model, which core does not have:

- `unbundled-def`: "an `ops.Task/Batch/Fixer/Pipeline` call not reachable from the package's `Ops(Deps) ops.Bundle`";
- `unregistered-bundle`: "a package defining `Ops(...) ops.Bundle` that `internal/opscatalog` `All` does not
  reference";
- `store-in-deps`: "a `Deps` struct (or a struct an `Ops` function takes) with a field that is or implements
  `database.Store`".

Core's `tasks` has no `Bundle` type and no `Ops(Deps)` convention (05-tasks §5T.3.1: registration is only through
`module.Registrar.Tasks`), and `internal/opscatalog` is deleted by the F5 flip (05-tasks §5T.8.1). So re-rooting the
analyzer "verbatim" either flags every `tasks.Task` call in ABO (`unbundled-def`: no `Ops` bundle exists) or checks
nothing. The spec does not say what these rules become. Check whether it does now:

```bash
grep -rn 'tasklint' docs/specs/platform-core/*.md | grep -i 'bundle\|Registrar\|rule\|unbundled\|store-in-deps\|parameter\|flag' | wc -l
grep -rn 'unbundled-def\|unregistered-bundle\|store-in-deps' docs/specs/platform-core/*.md | grep -v '04-package-mapping.md:410' | wc -l
```

At the plan's base these print **3** and **0**: the three hits of the first line are 03's `archtest` row (line
344), 04 §3's `tools/cmd/oplint` row (line 410, which only lists the rule names) and 04 §8.1's F5 row (line 889),
none of which defines the rules over core's model. If the first line still prints 3 (or every extra hit is
another mention without rules) and the second still prints 0, STOP and report:

> SPEC-GATED (F5-17): 04 §6/§3 move `oplint` to `archtest/tasklint` and parameterise its module path, but no spec
> text says what `unbundled-def`, `unregistered-bundle` and `store-in-deps` check once `ops.Bundle`,
> `Ops(Deps)` and `internal/opscatalog` are gone, or which packages ABO runs `tasklint` over. Needed: the rule set
> over `module.Registrar.Tasks` (or their deletion), the analyzer's parameters (module path, store package,
> anything replacing the catalog), and the ABO gate that runs it (the `make ci` target or the ABO test).

If the spec has gained that text, follow it, and the mechanical part below applies unchanged.

## Base state (after Step 0 passes)

```bash
ls tools/cmd/oplint/                                                     # analyzer.go analyzer_test.go main.go testdata
grep -n 'modulePath  = "github.com/falkcorp/audiobook-organizer"' tools/cmd/oplint/analyzer.go | wc -l   # 1 (line 45)
grep -n 'opsPath     = modulePath + "/pkg/ops"' tools/cmd/oplint/analyzer.go | wc -l                    # 1
grep -n 'func init()' tools/cmd/oplint/analyzer.go | wc -l                                              # 1 (the probe-v2 flag; 06 says drop it)
grep -n '"github.com/falkcorp/audiobook-organizer/pkg/ops/opstest"' tools/cmd/oplint/analyzer_test.go | wc -l   # 1
find tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases -mindepth 1 -maxdepth 1 -type d | wc -l   # 10
grep -n '^oplint:' Makefile | wc -l                                                                     # 1
grep -n '^ci: .*oplint' Makefile | wc -l                                                                # 1
grep -n '"tools/cmd/oplint":' internal/arch/layering_test.go | wc -l                                    # 1
grep -c 'oplint' scripts/ci/short_test_weights.json                                                     # 0 (no weight at f7ac5b11e)
test -d core/tasks && echo tasks                                                                        # tasks (F2-01)
test -d core/archtest && echo archtest                                                                  # archtest (R1)
grep -rln 'ops.Bundle' internal/opscatalog/*.go | grep -v _test | wc -l                                    # >=1 (the v3 catalog; empty: "v3 has 0 native defs", 05-tasks §5T.2)
```

## Files

| Action | Path |
|---|---|
| move | `tools/cmd/oplint/analyzer.go` → `core/archtest/tasklint/analyzer.go` (package `tasklint`, exported `Analyzer`) |
| move | `tools/cmd/oplint/analyzer_test.go` → `core/archtest/tasklint/analyzer_test.go` |
| move | `tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/...` → `core/archtest/tasklint/testdata/src/<re-rooted path>/...` |
| delete | `tools/cmd/oplint/` (with `main.go`; where the driver goes is part of Step 0's missing text) |
| modify | `Makefile` (the `oplint` target and its use in `ci`), `internal/arch/layering_test.go` (drop `"tools/cmd/oplint"`), `scripts/ci/short_test_weights.json` (only if the new test package needs a weight) |
| create | one `changelog.d/` fragment |

## Mechanical steps (only after Step 0)

1. `git mv` the three paths (history kept); package `tasklint`; header blocks updated (path and version).
2. Re-root: `opsPath` → `github.com/falkcorp/mediacore/tasks` and the recognised constructors `ops.Task/Batch/Fixer`
   → `tasks.Task/Batch/Fixer` (A7: there is no Pipeline shape, 05-tasks §5T.0; drop its case and the
   `unbundled_def_nobundle` fixture's `ops.Pipeline(` call or rewrite it per the spec text found in Step 0).
   `modulePath`, the store package (`dbPath`) and whatever replaces `catalogPath` become `Analyzer.Flags`
   (06 §6.6), set by the app's gate (06 §6.11.4); delete `func init()` and the `probe-v2` flag (it probes v2 defs,
   which F5-18 deletes).
3. Fixtures: re-root `testdata/src/github.com/falkcorp/audiobook-organizer/...` on the core module path (04 §6); the
   fake `pkg/ops` and `pkg/ops/opstest` fixture packages become fakes of `tasks` and `tasks/taskstest`;
   `analyzer_test.go` imports `taskstest` instead of `pkg/ops/opstest`.
4. The ABO gate: as Step 0's text says (the `make ci` target name and the package set). Until a converted
   definition exists (W26), it checks nothing in ABO and must pass ("HEAD's v3 catalog is empty, so nothing loses
   its lint", plan §(l) item 5).
5. `internal/arch/layering_test.go`: remove the `tools/cmd/oplint` entry (a deleted package).

## Acceptance (commands and expected output, after Step 0)

```bash
test ! -e tools/cmd/oplint && echo gone                                         # gone
cd core && go vet ./archtest/tasklint/ && go test ./archtest/tasklint/ -count=1 && cd ..   # ok
(cd core && go list -deps ./tasks/ ./kernel/ ./app/) | grep -c 'archtest/tasklint'        # 0 (test-support only, 03 R4)
grep -c 'falkcorp/audiobook-organizer' core/archtest/tasklint/analyzer.go       # 0 (no hard-coded ABO path)
go test ./internal/arch/ -count=1                                               # ok
make ci                                                                         # exit 0 (the renamed gate included)
make -C core ci                                                                 # exit 0
```

## Out of scope

`tools/cmd/sdkguard` (deleted with `pkg/plugin/sdk` in R3); `tools/cmd/opsgen` (R3-02); fixing any finding in ABO
code (none exist before W26).

## Idempotency / rollback

If `core/archtest/tasklint/analyzer.go` exists and `tools/cmd/oplint` is gone, run the acceptance instead. Rollback =
revert the move PR (one revert restores `tools/cmd/oplint` and the `make oplint` gate).

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task; move window announced.
- File headers on every moved or created file, version bumped; `changelog.d/` fragment without a header.
- Move PR: list every `old ==> new` path in the description; build at every commit
  (`scripts/ci/check_commits_build.py`); `go fix` on the moved files (04 §2.2).
- Conventional commit `refactor(core/archtest): oplint becomes tasklint on the core module path`, body ending
  `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- **SPEC-GATED** for the reason Step 0 states: three of the seven rules are defined over `ops.Bundle`, `Ops(Deps)`
  and `internal/opscatalog`, none of which exists in core's model, and no spec text says what they become or which
  ABO packages the gate covers.
- **Order risk with the conversions.** This row lands in W24, before the T2–T7 conversions (W26–W28). Once
  re-rooted, `writer-bypass` and `store-in-deps` would flag every converted body that keeps HEAD's direct
  `internal/database` writes or captures a `database.Store`, which nearly all do, while 05-tasks §5T.8.2 lists only
  three body changes. The conversion briefs STOP on such a finding. The spec should state whether dark conversions
  route writes through `tasks.Writer` (then the conversions are not "mechanical") or whether `tasklint`'s
  `writer-bypass` applies only from the flip.
- `scripts/ci/short_test_weights.json` has no oplint entry at `f7ac5b11e`; the row lists it for the move-PR
  convention only.
