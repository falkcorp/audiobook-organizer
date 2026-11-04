<!-- file: docs/agent-tasks/platform-core/PC-01-03-archtest-engine-layering-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: f630da3a-3d7c-4787-9893-440051b57e19 -->
<!-- last-edited: 2026-11-04 -->

# PC-01-03 — `archtest` engine in core and the layering guard redesign, with `TestCoreImportsOnlyCore` and `TestCoreHoldsNoAppKeyNames`

| Field | Value |
|---|---|
| ID | PC-01-03 |
| Tier | **T3 (Opus/high)**: 03 §3.5.1's guard rules are intricate and a wrong guard passes silently; every guard needs a positive control that proves it can fail |
| Wave | W01 (S0, release `S0`) |
| Depends on | PC-01-02 (the core module exists) |
| Priority / effort | P1 / L |
| Branch | `feat/pc-01-03-archtest` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §3 (archtest row, the `layering_test.go` bullet), §6.3 |
| Owning spec | `03-architecture.md` §3.5 rows R1–R4 and §3.5.1 items 1–4; `04-package-mapping.md` §8.4 note 3 and §8.1 row S0; `09-abo-on-core.md` S0 "Moves" and the S0 CI table rows "core layering gate, `TestCoreImportsOnlyCore`" and "`TestCoreHoldsNoAppKeyNames`" |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" show origin/main:core/go.mod | head -3      # PC-01-02 merged; else STOP
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-01-03" -b feat/pc-01-03-archtest origin/main
cd "$REPO/../audiobook-organizer-pc-01-03"
npm ci --prefix web
```

Never run `go work init` in the checkout or the worktree.

## Exact files

Create: `core/archtest/archtest.go`, `core/archtest/graph.go`, `core/archtest/archtest_test.go`,
`core/archtest/coreimports_test.go`, `internal/arch/core_key_names_test.go`, and (see Planner notes)
`core/internal/archtest_layers_test.go` plus one reviewed allowlist file for `TestCoreHoldsNoAppKeyNames`
(`internal/arch/testdata/core_key_names_allow.txt`, name coined here).
Modify: `internal/arch/layering_test.go`; `core/Makefile` (the `archtest` target stops printing "not applicable");
one `changelog.d/` fragment.

## Re-verify (expected at `ac4368bdb` plus PC-01-01/02)

```bash
grep -n 'const minModulePackages = 150' internal/arch/layering_test.go | wc -l                      # 1 (:38)
grep -n 'var positiveControlEdge = edge{"internal/server", "internal/database"}' internal/arch/layering_test.go | wc -l   # 1 (:40)
grep -n 'func loadGraph\|func computeLayers\|func familyLayer\|^var allowed\|^var layerOf' internal/arch/layering_test.go | wc -l   # 5
grep -n '"go", "list", "-json=ImportPath,Imports", "./..."' internal/arch/layering_test.go | wc -l   # 1 (loadGraph keeps only in-module imports today)
grep -c 'audiobook-organizer/cmd"' main.go                                                       # 1 (the new in-module control: . -> cmd)
grep -n 'func KeyFamilies() \[\]KeyFamily' internal/database/keyfamilies.go | wc -l                 # 1 (:330)
grep -vc '^#\|^$' internal/server/testdata/op_ids.golden                                           # 269
go list ./... | wc -l                                                                               # 213 (+0 or +1 after PC-01-02)
ls core/archtest 2>/dev/null | wc -l                                                                # 0
```

Different counts are expected only where an earlier merge explains them; otherwise STOP and report.

## What to build (rules quoted from 03; the spec fixes them, the code shape is yours)

### 1. Shared engine `core/archtest` (03 §3.5.1 item 3), test-support, stdlib only

The classification/compute code of `internal/arch/layering_test.go` moves into `core/archtest` as a library:
`archtest.Layering(t, archtest.LayerSpec{...})` (the spec's call shape) so both modules and every later app run one
algorithm. `graph.go` holds the loader (`go list -e -deps -test -json`, per (GOOS, tag set), read as the **union** of the
file lists; 03 §3.5.1 item 4). Required behaviour:

- one tag-set list (03 §3.5.1 item 4), used by every tool: default, `bench`, `integration`, `pprof`, `race`,
  `native_taglib`, `embed_frontend fts5 native_taglib`, `GOOS=darwin CGO_ENABLED=0` with `embed_frontend`, and
  `GOOS=windows` for `./cmd/mtls-bridge` only;
- `-e` always; ignore exactly a `pattern …: no matching files found` error on a package whose embed pattern names an
  output on the module's committed in-image list (root: `scripts/ci/embed_build_outputs.txt`, created by PC-01-05;
  until it exists, the one root entry is `web/dist`; core: none at S0), after normalising `all:` and joining with the
  package directory (03 §3.2 item 4); fail on every other `Error` or `DepsErrors`;
- honour `core/go.mod`'s `ignore` directives; filter synthetic `.test` and `[pkg.test]` packages; never parse
  `IgnoredGoFiles` itself.
- exported helpers named by 03 §3.5: `Layering`, `NoKernelImports(t, prefix)` (R2), `ModulesIsolated(t,
  map[moduleID]pkgPrefix)` (R3; vacuous with one module but wired from S0).

`archtest_test.go` is the core guard's **S0 positive control** (03 §3.5.1 item 2): a self-test on a synthetic graph in
which a known wrong-way edge is reported, an empty graph fails, and the "no matching files" tolerance accepts only a
listed output (fixtures: a no-frontend tree passes, any other package error fails).

### 2. Core guards

- `core/archtest/coreimports_test.go`: **`TestCoreImportsOnlyCore`** (R1(b)): every core package's deps and test deps,
  over the tag union, contain only `github.com/falkcorp/mediacore/...`, the standard library and an allowlisted
  third-party set (empty at S0). A fixture core file with an ABO import, tagged `darwin` or `integration`, must fail it
  (that is why the union exists). Add **`TestNoAppRequire`** (R1(a)): `core/go.mod` requires no app module.
- `core/internal/archtest_layers_test.go`: the core layer guard (03 §3.5.1 item 2), R4's table as its `layerOf`, its own
  ratio floor, and at S0 only "the loader returned at least the packages core holds" plus the engine self-test as its
  control. Registered as a core gate through `make -C core ci`'s `archtest` target (03 §3.2 item 4); its `ci/gates.txt`
  row is PC-01-04's (it owns that file in this wave).

### 3. Root guard redesign, `internal/arch/layering_test.go` (03 §3.5.1 item 1; 04 §8.4 note 3)

Keep its rule, `layerOf` and the shrink-only `allowed` map, and change exactly:

- the loader-health floor: `len(packages) >= 0.8 * len(layerOf)` replaces `minModulePackages = 150`;
- the in-module positive control: the root `main` package (key `.`) imports `cmd` (`main.go`), replacing
  `internal/server -> internal/database`; a PR that removes it names a replacement in the same change;
- `loadGraph` keeps imports with the `github.com/falkcorp/mediacore/` prefix and hands them to the classifier; a core
  import is never an in-module edge but is checked against the importer's layer like one, mapped by core's R4 layer:
  core **L0–L5 → −1**, core **L6** (`httpapi`, `realtime`, `ui`) → **5**, core **L7–L9** (`module`, `app`,
  `examples/*`) → **6** with no allowance; `coretest`, `archtest`, the `<pkg>/<pkg>test` suites, `legacy/legacytest`,
  `storage/*` → −1; `shared/...` → 3; mapped core imports **feed the computed layer** (max of in-module and mapped
  imports, floored as today);
- the assertion that no package other than `.`, `cmd` and `internal/books` imports `mediacore/module` or
  `mediacore/app` (non-test imports);
- a **classifier positive control** effective from S0: at least one root package imports a
  `github.com/falkcorp/mediacore/...` path and the guard classified that import onto the mapped layer. At this PR the
  only ABO importers of core are test files (`internal/arch`'s own, which call `archtest`; 03 §3.5.1 item 1 counts
  "the root guard itself calls `archtest.Layering`" as ABO importing core), so the control reads `TestImports` and
  `XTestImports` of `internal/arch` for this one assertion; layering edges stay non-test imports as today;
- the root calls `archtest.Layering(t, archtest.LayerSpec{...})` and `archtest.NoKernelImports(t,
  "github.com/falkcorp/audiobook-organizer/...")`.

### 4. `internal/arch/core_key_names_test.go`: **`TestCoreHoldsNoAppKeyNames`** (R1(c); its one match rule is 03 §3.5 R1, quoted there; implement it, do not restate it differently)

Scan every string literal and struct-tag value of every non-test `core/` file over the tag union (`go list -e`, the
same tolerance as above). Name set: `database.KeyFamilies()` prefixes (`internal/database/keyfamilies.go:330`) with
core's reserved `c/` and `m/` excluded; ABO's setting and preference sub-keys generated from the `GetSetting`/
`SetSetting` call sites; the 269 op IDs of `internal/server/testdata/op_ids.golden`; and a reviewed list of row-type
and param-key literals (`legacy_op_id`). Exceptions only in the one allowlist file. The test must carry R1's cases as
fixtures: `"library: root escapes"` passes, `"library:" + id` fails, `"c/tasks/"` passes,
`FormerKinds: []tasks.Kind{"diagnostics.export"}` fails, `"library.organize.extra"` fails,
`"library.organize-when-scanned"` is judged as its own key, `"scheduled_task_last_tick.x"` fails (folded prefix),
`` `json:"legacy_op_id,omitempty"` `` fails. Run the fixtures through the same matcher the real scan uses (a matcher
function tested on a fixture directory), so a broken matcher cannot pass the real scan vacuously. At S0 the real scan
finds no violation (core holds only `buildid`, `storage/keys`, `archtest`).

## Invariants and the tests that prove them

| Invariant | Proof |
|---|---|
| The core guard can fail | `archtest_test.go` synthetic-graph violation reported; empty graph fails |
| R1(b) reads every tag set | `TestCoreImportsOnlyCore` fails on a darwin- or integration-tagged fixture importing an ABO path |
| The root guard still sees the module | ratio floor; `. -> cmd` control; a test that the floor fails on a truncated graph |
| Core imports cannot hide wrong-way edges | a classifier unit test: an ABO layer-3 package importing a core L6 path is reported |
| The classifier is live | the classifier positive control on `internal/arch`'s core import |
| Copied app key formats cannot enter core | `TestCoreHoldsNoAppKeyNames` and its R1 fixtures |
| "no matching files" is tolerated only for a listed output | engine fixture: unlisted embed path fails |

## STOP conditions

- The redesigned root guard reports any new violation on HEAD's tree: STOP and report it; never add an `allowed` entry
  to make it pass (04 §8.4 note 3: entries are re-evaluated, never dropped or invented).
- `TestCoreHoldsNoAppKeyNames` would need a family prefix, op ID or setting name you cannot generate mechanically:
  STOP; do not hand-write the name set.
- Any guard that passes when its positive control is deleted: STOP; that guard is vacuous.

## Acceptance

```bash
go -C core test -race -count=1 ./archtest/... ./internal/...      # ok, including the synthetic-graph self-test
go -C core test -count=1 -run 'TestCoreImportsOnlyCore|TestNoAppRequire' ./archtest/   # ok
go test -race -count=1 ./internal/arch/                           # ok (TestLayering, TestCoreHoldsNoAppKeyNames, the controls)
go test -count=1 -run TestCoreHoldsNoAppKeyNames -v ./internal/arch/ | grep -c -- '--- PASS'   # >= 9 (the scan plus the 8 R1 fixture cases)
grep -n 'minModulePackages = 150' internal/arch/layering_test.go | wc -l   # 0
make -C core ci                                                    # exit 0; archtest no longer "not applicable"
make ci                                                            # last line: ✅ All CI checks passed!
```

## Out of scope

`ci/gates.txt` (PC-01-04 registers the core gates); `SDKOnly` for `examples/...` (no examples until S2; 03 R2);
`TestNoGlobals` (R5) and `RunItemsDeclareParallelism` (R7), which 09 S0 does not list; any package move.

## Rollback

Revert the PR. Test-only and test-support code; no runtime change.

## Repo rules

Headers on every new/changed file. Conventional commit `test(arch): shared archtest engine in core; layering guard
redesign (03 §3.5.1)`, ending `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
`gh pr merge <n> --rebase` after every required check; never squash, never `--admin`. Remove the worktree after merge.

## Planner notes

- 03 §3.5.1 item 2 names the core layer guard `core/internal/archtest_layers_test.go`; the inventory row lists no
  such file (and no row creates it). Added here.
- The core guard's `ci/gates.txt` registration (`core-job`, 09 S0 table) cannot be in this row without colliding with
  PC-01-04, which runs in the same step and owns `ci/gates.txt`; PC-01-04's brief registers it.
- The classifier positive control "from S0" has no non-test ABO importer of core until W02 (PC-02-01/02 import
  `storage/keys` and `buildid`); this brief reads `internal/arch`'s test imports for it, which is 03 §3.5.1 item 1's
  own reading ("the root guard itself calls `archtest.Layering`"). Once W02 lands, a non-test edge can replace it.
- The reviewed allowlist file for `TestCoreHoldsNoAppKeyNames` has no path in the spec ("one reviewed allowlist file");
  the path is coined here.
