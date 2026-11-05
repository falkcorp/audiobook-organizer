<!-- file: docs/agent-tasks/platform-core/R1-01-core-module-first-core-pr.md -->
<!-- version: 1.1.0 -->
<!-- guid: c9068651-3412-486f-b5f9-ac087f228837 -->
<!-- last-edited: 2026-11-05 -->

# R1-01 — Nested module and the first core PR: core/go.mod with the genproto exclude, empty core/go.sum, core/Makefile pin, the L0 leaves buildid and storage/keys, the archtest engine with TestCoreImportsOnlyCore and TestCoreHoldsNoAppKeyNames, module guards, the R1 freeze turned on

| Field | Value |
|---|---|
| ID | `R1-01` |
| Tier | **T3** (Opus / high): module mechanics with known ambiguous-import traps and the guard rules of 03 §3 |
| Release / wave | R1 / W04 |
| Depends on | R0-01, OWNER-01, OWNER-02 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 03 §3.2 items 1, 4, 6, §3.5.1 items 1-4; 09 §9.4 R1 Content: build and CI, R1's CI classification (core layering gate, genproto-exclude-root, check_module_skew.py, TestCoreHoldsNoAppKeyNames); 09 §9.2.1 (R1 freeze); 13 §13.2.2 guard items 1-4; 12 §12.3.11 (toolchain pin, print-toolchain); 04 §8.1 R1 |
| Branch | `agent/platform-core-r1-01-core-module-first-core-pr` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/go.mod`
- `core/go.sum`
- `core/Makefile`
- `core/.gitignore`
- `core/buildid/`
- `core/storage/keys/`
- `core/archtest/`
- `core/scripts/ci/check_genproto_exclude.py`
- `core/scripts/ci/check_core_toolchain.py`
- `go.mod`
- `go.sum`
- `Makefile`
- `internal/arch/layering_test.go`
- `internal/arch/core_key_names_test.go`
- `internal/database/keyfamilies.go`
- `scripts/ci/check_module_skew.py`
- `scripts/tests/test_check_module_skew.py`
- `scripts/ci/genproto_exclude_root.py`
- `scripts/check_toolchain_versions.py`
- `scripts/tests/test_check_toolchain_versions.py`
- `.github/workflows/core-ci.yml`
- `.github/dependabot.yml`
- `.github/workflows/prerelease.yml`
- `ci/gates.txt`
- `scripts/ci/run_gates.py`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-01" -b agent/platform-core-r1-01-core-module-first-core-pr origin/main
cd "$REPO/.worktrees/platform-core-r1-01"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

R1's first core PR (09 §9.4 R1 "Content: build and CI"; 03 §3.2 items 1, 4, 6; 03 §3.5.1 items 1–4; 04 §8.1 R1).
It creates the nested module `core/` (`github.com/falkcorp/mediacore`), wires it into the root build, lands the
three dependency-free L0 leaves (`buildid`, `storage/keys`, `archtest`), redesigns ABO's layering guard onto the
shared engine, adds the module guards (`genproto-exclude-root`, the core-mode exclude check, `check_module_skew.py`,
the toolchain guard's core rules, `TestCoreImportsOnlyCore`, `TestCoreHoldsNoAppKeyNames`), registers the reserved
families `c/` and `m/`, and **turns the R1 freeze on** (09 §9.2.1: "the R1 series' first PR turns it on").
Nothing user-visible changes; ABO's binary behaves exactly as before.

**Preconditions (STOP if any is missing).**
1. OWNER-02 has cut the R0 production release (`gh release list --exclude-pre-releases --limit 3` shows it) and
   R0-01 (the toolchain bump) is merged: `grep -n '^export GOTOOLCHAIN' Makefile` shows the bumped pin.
2. OWNER-01's recorded answers exist in the tracking issue: Q-I (how required checks are enforced) and
   "keep or override the R1 freeze". If the owner overrode the freeze, skip step 12 and say so in the PR.

## Invariants (each one has a test or check below)

- I1. The genproto `exclude` lives **only** in `core/go.mod` (03 §3.2 item 1); root `go.mod` gets `require` +
  `replace` and no exclude. Nothing runs `go work init` in the checkout; every workspace probe runs in a copy made
  with `mktemp -d` outside the repository (09 §9.4 R1 Verification; CLAUDE.md).
- I2. `core/go.sum` is committed and **empty** (core has no requirements yet, 03 §3.2 item 4), and
  `check_module_skew.py` asserts it exists.
- I3. `core/go.mod`: `module github.com/falkcorp/mediacore`, the same `go` directive as the root (`go 1.27.0`),
  no `toolchain` line, `exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1`, `ignore ./web`,
  `ignore ./examples/hello/web` (03 §3.2 code block). Root `go.mod`:
  `require github.com/falkcorp/mediacore v0.0.0-00010101000000-000000000000` and
  `replace github.com/falkcorp/mediacore => ./core`.
- I4. Core imports nothing outside `github.com/falkcorp/mediacore/...`, the standard library and the allowlisted
  third-party set, under every (GOOS, tag set) of 03 §3.5.1 item 4's one list, test imports included
  (`TestCoreImportsOnlyCore`, 03 R1(b)); `core/go.mod` requires no app module (`TestNoAppRequire`, 03 R1(a)).
- I5. No string literal in a non-test `core/` file names a books key family, an ABO setting key or an op ID
  (`TestCoreHoldsNoAppKeyNames`, 03 R1(c), its one match rule; it lands before any file moves into core).
- I6. ABO's guard keeps its rule; its loader floor is a ratio `len(packages) >= 0.8 * len(layerOf)`; its
  in-module control is `.` → `cmd`; `github.com/falkcorp/mediacore/...` imports are kept by the loader and mapped
  onto ABO's layer scale (core L0–L5 → −1, L6 → 5, L7–L9 → 6; `coretest`, `archtest`, `<pkg>/<pkg>test`,
  `storage/*` → −1; `shared/...` → 3) and feed the computed layer; a classifier positive control asserts at least
  one root package imports a core path and it was classified (03 §3.5.1 item 1). Only `.`, `cmd` and
  `internal/books` may import `mediacore/module` or `mediacore/app` (non-test).
- I7. The core guard's first control is the engine's self-test on a synthetic graph (a known violation is
  reported; an empty graph fails); the core guard itself asserts only that the loader returned at least the
  packages core holds (03 §3.5.1 item 2, "first core PR").
- I8. Every core-owned check script lives under `core/scripts/ci/`; the root-only comparisons
  (`check_module_skew.py`, toolchain parity, `genproto_exclude_root.py`) live under `scripts/` (03 §3.2 item 4,
  "Core's own Makefile"; 12 §12.3.11).
- I9. No pin literal outside the guarded copies: `core/Makefile` carries `export GOTOOLCHAIN := <root pin>` and a
  `print-toolchain` target; scripts and workflows read it through `make -s -C core print-toolchain` (13 §13.2.2
  item 4; 12 §12.3.11).

## Dependency check

```bash
grep -n '^export GOTOOLCHAIN' Makefile        # expect the R0-01 pin (go1.27.3 per 13 §13.2.1)
test ! -e core && echo NO_CORE_YET              # expect NO_CORE_YET
```

## Re-verify (expected at `f7ac5b11e`)

```bash
grep -n '^go \|^toolchain\|^ignore\|^exclude\|^replace' go.mod      # expect: 3:go 1.27.0 and 166:ignore ./web only
grep -c 'genproto' go.mod                                            # expect 2 (the split googleapis/{api,rpc} indirect requires; no exclude)
grep -n 'minModulePackages = 150\|positiveControlEdge = edge' internal/arch/layering_test.go   # expect 38 and 40
grep -n '^func loadGraph\|^func computeLayers\|^func familyLayer' internal/arch/layering_test.go   # expect 339, 564, 549
grep -n '^func KeyFamilies' internal/database/keyfamilies.go         # expect 330
grep -c '"c/\|"m/' internal/database/keyfamilies.go                  # expect 0 (no reserved family yet)
grep -n 'directory: /$' .github/dependabot.yml                       # expect the gomod entry (line 60 at f7ac5b11e; 03 cites 59-60)
grep -n 'JOBS = ' scripts/ci/run_gates.py                            # expect: JOBS = {"own-job", "go-suite"}
grep -n 'Promote to the next minor release' .github/workflows/prerelease.yml   # expect 1 (the step to gate)
wc -l internal/server/testdata/op_ids.golden                          # expect 288 lines (header + op IDs)
grep -n '^ci:' Makefile                                              # expect 789 (09 cites :785 at 748fd787a)
```

## Steps

1. **Module skeleton.** Create `core/go.mod` exactly per I3, an empty `core/go.sum`, and `core/.gitignore`
   (`/.ci/readiness/`, `/bin/`, `**/playwright-report/`, `**/blob-report/`; 07 §7.3.1 "Ignored outputs", 12 §12.3.11). Root `go.mod`: add the `require` and `replace` of I3 (no exclude). Run root
   `go mod tidy`; commit its result in the same PR (03 §3.2 item 4 "tidy coupling"). Root `Makefile`:
   `GO_MODULE_DIRS := . core` near the top, used by `build`, `vet`, `test-short`, `staticcheck`, `fmt-check` and a
   new `tidy-check` (`go mod tidy -diff` in each dir); `fmt-check` excludes `web/`, `core/web/`,
   `core/examples/*/web/` (09 §9.4 R1 classification row 1). The full per-module classification is R1-03's; here
   wire only what keeps root `make ci` green with the second module present.
2. **`core/Makefile`** (12 §12.3.11): `export GOTOOLCHAIN := <exactly the root pin>`, `print-toolchain` (prints
   the pin, nothing else), and a `ci` target with the parts that exist now: `build vet test-short fmt-check
   fix-check tidy-check archtest exclude-check toolchain-check`. `fix-check` is `go fix -diff ./...` empty
   (core only, 03 §3.2 item 4). Leave a comment listing the rest of 12 §12.3.11's `ci` list as R1-03's.
3. **L0 leaves.**
   - `core/buildid`: `func Binary(version string) (string, error)` (version + `+` + lowercase hex SHA-256 of
     `os.Executable()`'s file) and `func Commit() string` (`debug.ReadBuildInfo` `vcs.revision`, `+dirty` when
     `vcs.modified` is true, `""` when absent) (03 §3.3 `buildid` row; 06 §6.4.5). Tests (names coined here):
     `TestBinaryHasVersionAndHash`, `TestCommitEmptyWithoutVCS`.
   - `core/storage/keys`: exactly 05-storage §5S.3.3's API: `Seg`, `Str`, `Bytes`, `U64`, `Time`, `Join`,
     `Prefix`, `Split`, `PrefixEnd`, `ValidName` (`[a-z0-9_]{1,32}`), tuple encoding with escape `0x00 0xFF` and
     terminator `0x00 0x01`. Tests: `TestJoinOrderMatchesSegmentOrder` (spec name: a property test over random
     segment lists), plus `TestSplitInvertsJoin`, `TestPrefixEnd` (names coined here).
   - `core/archtest`: the shared engine moved out of `internal/arch/layering_test.go` (04 key "R1 move
     internal/arch -> mediacore/archtest"): the loader (`go list -e -deps -test -json` over a tag-set list, union
     of file lists, honouring `core/go.mod`'s `ignore`, ignoring exactly the "no matching files found" error of a
     listed embed output and failing on every other `Error`/`DepsErrors`, 03 §3.5.1 item 4), the classification and
     compute code (`computeLayers`, `familyLayer`, the `allowed` shrink-only check), exposed as
     `archtest.Layering(t, archtest.LayerSpec{...})` (03 §3.5.1 item 3; the field set of `LayerSpec` is yours: module
     path, `layerOf`, `allowed`, mapping of foreign prefixes, ratio floor, in-module control edge, tag sets). Also
     `TestCoreImportsOnlyCore` and `TestNoAppRequire` in `core/archtest` (03 R1(a)(b); the allowlist of third-party
     paths is empty today and lives in one file, `core/archtest/thirdparty_allow.txt`, which R1-10 and R1-12 extend
     with their pinned modules). The engine's self-test on a synthetic graph (I7) is in
     `core/archtest`. The tag-set list is one exported value in its own file, `core/archtest/tags.go` (03 §3.5.1 item 4: default,
     `bench`, `integration`, `pprof`, `race`, `native_taglib`, `embed_frontend fts5 native_taglib`,
     `GOOS=darwin CGO_ENABLED=0` with `embed_frontend`, and `GOOS=windows` for `./cmd/mtls-bridge` only); R1-28 adds
     the `unitlifecycle` tag there.
4. **Core guard.** `core/internal/archtest_layers_test.go` (03 §3.5.1 item 2): `archtest.Layering` with 03 R4's
   **whole** layer table (every package R4 names, L0 to L9, deferred packages included, the sub-package rule, the
   test-support packages, and the `internal/<capability>http` pattern at L6), its own `layerOf` and its own ratio
   floor; control per I7. Because the table is complete from this PR, a later row that creates a core package R4
   names edits no shared guard file.
5. **ABO guard redesign** (`internal/arch/layering_test.go`, I6): keep `layerOf` and `allowed`; replace the
   floor and the control; the loader keeps `github.com/falkcorp/mediacore/` imports and the classifier maps them;
   add the classifier positive control and the `module`/`app` import assertion. The in-module control edge
   `.` → `cmd` exists at HEAD (`main.go:12`). The classifier positive control needs one root package that imports a core path in this PR
   (03 §3.5.1 item 1: "effective from the first core PR"). Use a non-test import with no behaviour change if one
   exists naturally (for example ABO's store-format guard computing the binary ID through `buildid.Binary`);
   otherwise add a small `internal/arch` test helper that imports `github.com/falkcorp/mediacore/storage/keys` and
   assert on that edge. State in the PR which edge the control uses; a later PR that removes it names its
   replacement (03 §3.5.1 item 2's rule).
6. **`TestCoreHoldsNoAppKeyNames`** in `internal/arch/core_key_names_test.go` (03 R1(c), the one match rule, all
   its listed cases as table tests): name set = `database.KeyFamilies()` prefixes minus `c/` and `m/`, the setting
   and preference sub-keys folded from `GetSetting`/`SetSetting`/`DeleteSetting`/`…UserPreference…` call sites,
   the op IDs of `internal/server/testdata/op_ids.golden` (books' kind-rename map does not exist until F5; 04
   §2.1 (h) names this golden), and the reviewed literal list (`legacy_op_id`); one allowlist file
   (`invite:global`). Scans every string literal and struct-tag value in non-test `core/` files over the tag union.
   With `core/` holding only the three leaves it passes; its fixture cases must fail as 03 R1(c) lists.
7. **Reserved families.** In `internal/database/keyfamilies.go` add `{"c/", ...}` and `{"m/", ...}` with owner
   `core` descriptions ("reserved: core keyspaces", "reserved: module keyspaces"), so the census reports them by
   name (09 §9.4 R1 build row; 05-storage §5S.3.4 kernel validation item 2). Run the keyfamily tests.
8. **Exclude checks** (03 §3.2 item 1): `core/scripts/ci/check_genproto_exclude.py` (core mode: the
   present/misplaced scan of `core/` and the core-mode probe, "not applicable" until core's graph reaches the
   requirer) behind `make -C core exclude-check`; `scripts/ci/genproto_exclude_root.py` (root mode: root probe
   `go work init . ./core && go build ./... ./core/...` in a `mktemp -d` copy, and the tree-wide "no other
   `go.mod` carries a genproto exclude" scan) as root `make ci` job `genproto-exclude-root`. Pass rule: present;
   green with it; load-bearing without it (an applicable probe's `go mod graph` shows
   `<requirer> google.golang.org/genproto@v0.0.0-20230410155749-daa745c078e1` **and** its build fails with
   `ambiguous import`), else stale.
9. **`scripts/ci/check_module_skew.py`** + `scripts/tests/test_check_module_skew.py` (03 §3.2 item 6; 12 SC4(c)):
   fails when a module path in `(cd core && go list -m all)` resolves to a different version in the root's
   `go list -m all`; asserts `core/go.sum` exists; supports `--pin <path>@<version>` (two exact pins per 13 §13.5.2
   once they exist; none needed while core has no requirements: say so in the script's help); also compares
   `core/tools/staticcheck` with `tools/staticcheck` once the former exists. Blocking, in root `make ci`.
10. **Toolchain guard** (13 §13.2.2 items 1–4): extend `scripts/check_toolchain_versions.py` (+ its test) to read
    `core/go.mod`, `core/tools/*/go.mod`, `tools/*/go.mod` (tier 3), the `core/Makefile` pin (tier 1, must equal
    the root pin), and to fail on a `go1.N.N` literal in `scripts/**`, `core/scripts/**` and workflow `run:` steps,
    with the "Not a pin" allowlist by path and line. `core/scripts/ci/check_core_toolchain.py` checks only core's
    own copies (`core/Makefile`, `core/tools/*/go.mod`, `core-ci.yml`'s `go-version`) behind
    `make -C core toolchain-check`.
11. **CI wiring.** `.github/workflows/core-ci.yml`: runs `make -C core ci`, and also root `go build ./...` when
    `core/go.mod` or `core/go.sum` changes; triggers on every PR that changes `core/**`, `go.mod`, `go.sum` or
    `.github/workflows/core-ci.yml` (09 §9.2.1 `core-ci` row; actions pinned to SHAs; `go-version` from the pin).
    `.github/dependabot.yml`: the gomod entry gets `directories: ["/", "/core"]` under the existing
    `multi-ecosystem-group: dependencies` (03 §3.2 item 6). `ci/gates.txt`: register `genproto-exclude-root`,
    `module-skew` and the core layering gate with job `core-job`; add `"core-job"` to `JOBS` in
    `scripts/ci/run_gates.py` (its `JOBS` set is `{"own-job", "go-suite"}` at the plan's base). R1-02 extends
    `test_run_gates.py` to validate `core-job` rows.
12. **The R1 freeze** (09 §9.2.1). In `.github/workflows/prerelease.yml`: add to the workflow-level `env:` block

    ```yaml
      # Release freeze (09 §9.2.1). Non-empty: check-rc-ordinal dispatches no production release.
      # R1-01 sets 'R1' and R1-31 clears it after the R1 release; F3-07 sets 'R2' and R2-02 clears it.
      RELEASE_FREEZE: 'R1'
    ```

    (the plan's one name for the toggle; R1-31, F3-07 and R2-02 use exactly `RELEASE_FREEZE`), change the "Promote to the next minor release" step's condition to
    `if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE == ''`, and add right after it a step
    "Report the release freeze" with
    `if: steps.rc.outputs.at_threshold == 'true' && env.RELEASE_FREEZE != ''` that runs
    `echo "::notice::Release freeze ${RELEASE_FREEZE} is on (09 §9.2.1): not dispatching a production release."`.
    RCs are still cut; nothing else in the workflow changes.
13. Header bumps; fragment `changelog.d/<today>_core_module.md` (`### Added`: "Internal: the core module
    (`core/`) and its build guards; automatic minor releases are paused until the next release (R1)."). Commits may
    be split by concern; every commit builds both modules.

## Tests and checks this task owns (write each before its code)

- `TestCoreImportsOnlyCore` (core `archtest`), `TestNoAppRequire`, the engine's synthetic-graph self-test.
- `TestCoreHoldsNoAppKeyNames` (root `internal/arch`), with 03 R1(c)'s cases as fixtures.
- `TestJoinOrderMatchesSegmentOrder` (core `storage/keys`).
- `genproto-exclude-root` (root `make ci`) and `make -C core exclude-check`.
- `scripts/ci/check_module_skew.py` with `scripts/tests/test_check_module_skew.py` (cases: no skew passes; one
  skewed module fails naming it; missing `core/go.sum` fails; a violated `--pin` fails).
- `scripts/tests/test_check_toolchain_versions.py` extended: a `core/Makefile` pin unequal to the root fails; a
  `go1.N.N` literal in a script fails; an allowlisted "Not a pin" line passes.
- ABO guard: the redesigned `TestLayering` plus a test that an import of `mediacore/module` from a package other
  than `.`, `cmd`, `internal/books` fails (on a synthetic graph through the engine).

## Code contract

Exactly the names of Goal and Steps: `buildid.Binary(version string) (string, error)`, `buildid.Commit() string`, 05-storage §5S.3.3's `keys` API, `archtest.Layering(t, archtest.LayerSpec{...})`, `TestCoreImportsOnlyCore`, `TestNoAppRequire`, `TestCoreHoldsNoAppKeyNames`.

## Acceptance

```bash
cat core/go.mod                          # expect exactly I3's lines (module, go 1.27.0, exclude, two ignore)
test -f core/go.sum && test ! -s core/go.sum && echo EMPTY_SUM     # expect EMPTY_SUM
grep -c 'exclude' go.mod                 # expect 0
git ls-files | grep -c '^go\.work'       # expect 0
make -s -C core print-toolchain          # expect the root pin, one line
go build ./... && go -C core build ./... && go -C core test ./... -count=1   # expect exit 0
go test ./internal/arch/ -count=1        # expect ok (TestLayering, TestCoreHoldsNoAppKeyNames)
python3 scripts/ci/check_module_skew.py && python3 -m pytest scripts/tests/test_check_module_skew.py scripts/tests/test_check_toolchain_versions.py -q   # expect exit 0, all passed
python3 scripts/ci/genproto_exclude_root.py   # expect "present; green with it; load-bearing without it (requirer github.com/cockroachdb/errors@v1.14.0)"
make -C core exclude-check               # expect "not applicable" for the core probe and a clean present/misplaced scan
grep -c "RELEASE_FREEZE: 'R1'" .github/workflows/prerelease.yml    # expect 1
make ci && make -C core ci               # expect exit 0 for both
```

## STOP conditions

- `go build ./...` at the root fails with `ambiguous import` after the `replace`: the exclude is in the wrong
  file or root `go.mod` gained one. Do not add an exclude to root `go.mod`.
- Any instruction would need `go work init` inside the checkout.
- `TestCoreHoldsNoAppKeyNames` cannot read `KeyFamilies()` without importing `internal/database` from a core file
  (it must live in root `internal/arch`).
- OWNER-01's answer to Q-I is missing (the required-check enforcement shapes `core-ci`'s registration).

## Out of scope

Every other row of R1's CI classification table (R1-03), `extraction-gates`, `vet-darwin`, `vet-integration`
(R1-02), the Docker changes (R1-04), any package move beyond `internal/arch`'s engine, `archtest.NoKernelImports`,
`SDKOnly`, `ModulesIsolated` (they arrive with the packages they guard).

## Rollback

Revert the PR: the module, guards and the freeze go with it; no stored data changes. If only the freeze must go,
R1-31's one-line change is the rollback for it.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
