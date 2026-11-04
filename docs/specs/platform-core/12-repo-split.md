<!-- file: docs/specs/platform-core/12-repo-split.md -->
<!-- version: 1.11.0 -->
<!-- guid: eb47baa1-640b-4c96-96cc-f6b2bdeaa57e -->
<!-- last-edited: 2026-11-04 -->

# 12 — Repository split: criteria, versioning, release, cutover, rollback

Evidence commit: `748fd787a`. Normative inputs: [03-architecture](03-architecture.md) §3.2 (nested
module `github.com/falkcorp/mediacore` at `core/`, `require` + `replace`, the genproto `exclude` in
`core/go.mod`, no committed `go.work`) and [02-decision-record](02-decision-record.md) N1/N2 (split
only when the measurable criteria here hold).
Raw data: [`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md),
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md). Experiments X4–X7 cited here
are in [`reviews/spec-round-1-modules-build.md`](../../plans/platform-core/reviews/spec-round-1-modules-build.md).

## 12.1 Purpose & scope

D3: core starts as an in-repo nested module and moves to its own repository (`falkcorp/mediacore`)
**only on measurable criteria**. This file defines those criteria with the exact command that measures
each, the versioning and deprecation policy before and after the split, the core version constant,
the release process, how app repos pin core, cross-repo CI, history preservation, the cutover runbook,
and how to undo it.

Out of scope: what is in core (03 §3.3, [04-package-mapping](04-package-mapping.md)); the in-repo build
wiring (`GO_MODULE_DIRS := . core` and the per-target classification, 03 §3.2 item 4 — owned by 09 S0).

## 12.2 Current state at HEAD

| Fact | Evidence |
|---|---|
| One Go module plus four tool modules; no `replace`, no `go.work` | `go.mod:1` (`module github.com/falkcorp/audiobook-organizer`), `go.mod:3` (`go 1.27.0`); `tools/{deadcode,mockery,staticcheck,tygo}/go.mod`; `raw/15-dependencies.md` "No `replace` or `retract` directives"; `go.work` forbidden by `CLAUDE.md` ("Do NOT run `go work init .`") |
| Tools are pinned in their own modules with a `tool` directive and installed with `go -C tools/<name> install tool` | `tools/staticcheck/go.mod` (`tool honnef.co/go/tools/cmd/staticcheck`); `Makefile:513` |
| A public-ish seam already guarded by a tool | `pkg/plugin/sdk` + `tools/cmd/sdkguard` (`raw/14-build-ci.md` §9, §10 item 1) — the precedent for an API gate, but it checks *dependencies*, not API compatibility |
| Releases: git tags, RC on every push to main, manual prod release via reusable workflow; goreleaser for binaries | `raw/14-build-ci.md` §8 (`.github/workflows/prerelease.yml:32-58`, `release-prod.yml:43-65`, `.goreleaser.yml:1-60`) |
| The reported version comes from `git describe --tags`, so any tag on a release commit can become the version | `Makefile:9` (`VERSION := $(shell git describe --tags --always --dirty …)`, injected as `-X main.version`) |
| Changelog: scriv fragments, CI-enforced | `changelog.d/scriv.ini:8-21`; `changelog-check.yml` (`raw/14-build-ci.md` §6) |
| Dependency updates: Dependabot for gomod and npm (free, already configured), each for one directory | `.github/dependabot.yml:59-60` (gomod, `directory: /`), `:69-70` (npm, `directory: /web`, majors blocked) |
| Shared CI backbone in an org repo | reusable workflows from `falkcorp/github-common`, ref pinned in `.github/ghcommon-ref.txt` (`raw/14-build-ci.md` §6, §10 item 4) |
| Toolchain drift guard knows only the root `go.mod` and the root `Makefile` pin; it reads no `tools/*/go.mod` | `scripts/check_toolchain_versions.py:44-46` (tier 3: "`go.mod` — the `go` directive is a minimum"), `:329-351` (reads `go.mod` only) |
| Licence | `LICENSE` is PolyForm Noncommercial 1.0.0 (first line) |

## 12.3 Split trigger and readiness criteria

The split happens when **every readiness criterion SC1–SC10 holds** *and* the **trigger** T holds.
The criteria are named `SC` ("split criterion") so that they never collide with the 09 stages S0–S10
(S2b, UI-0) or with any other `S`-numbered list in the spec.

**T — there is a reason to split.** A second app repository (TV or Movies, D5) is being created, or
the owner asks for it. Splitting with a single consumer adds a release step to every core change and
buys nothing; the criteria below are tracked from S0 regardless, so readiness is known when T fires.
T also starts the deferred builds (`acquire`, `arrfacade`, `integrations/indexer`, 03 §3.4); they are
not readiness criteria, because the split must not wait on code written for the new app.

Measurements are run from the repo root on a clean checkout of `main`. Scripts named here are plan
deliverables (Python per `CLAUDE.md` "Critical Rules" 4); each prints `PASS`/`FAIL` and the measured
value. Results are recorded in `docs/plans/platform-core/split-readiness.md` with date and commit, by
the root-side fold-in job of §12.3.11 (core's own targets never write outside `core/`).
Every command block changes directory only inside `( … )` subshells, so the blocks can run in one
shell under `set -e`.

**Where the scripts live.** A script that checks core alone is **core-owned** and lives under
`core/scripts/ci/` (`core_api_gate.py`, `core_coverage_by_package.py`, `check_core_version.py`,
`check_genproto_exclude.py`, `check_core_toolchain.py`), so `make -C core ci` runs from an export of
`core/` alone (SC4(a)) and the scripts carry their history into the core repo (§12.8.1). A script that
compares core with the root (`scripts/ci/check_module_skew.py`, toolchain parity in
`scripts/check_toolchain_versions.py`) or needs ABO (`core_consumer_smoke.py`,
`web_build_against_tarballs.py`) stays in the root and is dropped or rewritten at cutover step 6.

**No toolchain literals.** Every block first runs `export GOTOOLCHAIN="$(make -s -C core print-toolchain)"`,
which prints the pin in `core/Makefile` (§12.3.11). No command, script or workflow in this file carries
a `go1.x.y` literal; the pin has exactly the guarded copies listed in 13 §13.2.2.

### SC1 — Public API stable across 3 consecutive core checkpoints

*Definition.* Zero incompatible changes in SDK packages (03 §3.1: every core package outside
`internal/`, `kernel/` and `legacy/`; `examples/` is also excluded) between checkpoint *n−1* and *n*, for
the last three checkpoints, spanning at least 6 weeks. The SDK includes the **store interfaces** of 03 §3.3
(R4-1), through which core services persist their state and an app supplies its legacy implementation, so
a change to one counts like any other SDK change. The identifiers that exist only to serve an app's legacy
stores (optional capabilities, `legacy.Stores`, `legacy.LibraryRootSource`, `legacy.Maintenance`, their
suites) live in `legacy/` instead and are **not** measured, like `kernel/`, so the release after a family's
cutover that stops setting its member deletes them without a breaking change (03 §3.3 "Legacy-only
identifiers"; R6-3, SIMP6-2, COR7-5). Because ABO's implementations stay in ABO, no ABO key
name or row format is in core at the split; the split ships only the interfaces and core's native
implementations.

*Why not `gorelease` yet.* `gorelease -base=vX` fetches the base version through the module proxy.
Before the split, `github.com/falkcorp/mediacore` has no fetchable versions (the repo does not exist
and the root module reaches core only via `replace`), so `gorelease` cannot run. `apidiff` works on
locally written export data and does not need the proxy. Both tools exist at
`golang.org/x/exp/cmd/{apidiff,gorelease}` (`x/exp` `v0.0.0-20261014143641-f0251eb4aa3e`, published
2026-10-14, past the release-age soak of 13 §13.6). They are run from a pinned tools module,
`core/tools/apidiff/go.mod` (`tool golang.org/x/exp/cmd/apidiff` and `tool golang.org/x/exp/cmd/gorelease`),
in the repo's existing pattern, never from an ad-hoc `@version`. The module lives under `core/` so that
it moves with core at the split and core CI can run from an export of `core/` alone (SC4).

SC1 is a **readiness measurement, not a merge gate**: v0 allows incompatible changes (§12.4.1), and no
pair of checkpoints exists before the second core release, so a merge gate on it would be red or
forbid an allowed change from the first core PR. It runs in `make -C core readiness` (§12.3.11).

*Output format the filter relies on (X7).* In module mode apidiff prints `- ./<rel>.<Obj>: <msg>` for a
changed object and `- package <full path>: removed` for a removed package; `-incompatible` prints no
header; `internal/` packages are already filtered by apidiff itself. The filter below therefore drops
lines for `kernel/`, `legacy/` and `examples/` in both forms.

*Command (pre-split)*, wrapped by `core/scripts/ci/core_api_gate.py`:

```sh
# checkpoints are refs/core-checkpoints/v0.Y.Z, not tags (§12.4.1); fetch them explicitly
git fetch origin '+refs/core-checkpoints/*:refs/core-checkpoints/*'
go -C core/tools/apidiff install tool
pair() {   # $1 = older checkpoint ref, $2 = newer checkpoint ref
  git worktree add --detach "$TMP/a" "$1"
  git worktree add --detach "$TMP/b" "$2"
  (cd "$TMP/a/core" && apidiff -m -w "$TMP/a.api" github.com/falkcorp/mediacore)
  (cd "$TMP/b/core" && apidiff -m -w "$TMP/b.api" github.com/falkcorp/mediacore)
  apidiff -m -incompatible "$TMP/a.api" "$TMP/b.api" \
    | grep -v -E '^- (\./(kernel|legacy|examples)(/[^.: ]+)*\.|package github\.com/falkcorp/mediacore/(kernel|legacy|examples)(/|:))' \
    > "$TMP/incompatible.txt" || true          # grep -v exits 1 when nothing is left: the passing case
  git worktree remove --force "$TMP/a"
  git worktree remove --force "$TMP/b"
  test ! -s "$TMP/incompatible.txt"
}
refs=$(git for-each-ref --sort=-v:refname --format='%(refname)' refs/core-checkpoints | head -4)
# pair(n-1, n), pair(n-2, n-1), pair(n-3, n-2), then the date span with git log -1 --format=%cs <ref>
```

`core_api_gate.py` carries a fixture test: a known `kernel/` break and a known `legacy/` break must be
filtered and a known SDK break must be reported, so a change in x/exp's output format fails loudly instead of silently passing
or failing every run.

*Command (post-split, in the core repo):* the same `pair` function with the previous tag as the older ref,
from the same pinned tools module, then `gorelease -base=$(git describe --tags --abbrev=0)` for its
module-level checks. `gorelease` cannot exclude packages, so a `kernel/` or `legacy/` removal makes it
suggest a major; `core_api_gate.py` overrides that suggestion only when every incompatible line it reports
lies under `kernel/` or `legacy/` (R6-3).

### SC2 — R1 guard green and core requires no app module

```sh
(cd core && go test ./archtest/ -run 'TestNoAppRequire|TestCoreImportsOnlyCore|TestNoGlobals' -count=1)
! (cd core && go list -m all) | grep -q 'falkcorp/audiobook-organizer'
go test ./internal/arch/ -run 'TestCoreHoldsNoAppKeyNames' -count=1   # root side: R1(c), no app key name in core
```

(`grep -c` would print `0` but exit 1 on the passing case; the negated `grep -q` exits 0 exactly when
no line matches.) `TestCoreImportsOnlyCore` and `TestCoreHoldsNoAppKeyNames` read the union of the file lists over
every (GOOS, tag set) of 03 §3.5.1 item 4, not the default tags alone, so a darwin- or `integration`-tagged core file
cannot import app code unseen (03 R1; R9-10, MOD9-3).

### SC3 — Second consumer builds against core with nothing but a dependency on it

Two consumers, measured from S2, the stage in which `examples/hello` lands as a skeleton (04 §8.1;
it grows with the stages and never pulls a package forward, 03 §3.4):

1. **`core/examples/hello`** — the dummy second module of 02 §2.4 G6. It is a **package in the core
   module** (03 §3.3), not a separate module: a nested module would need a second `require`+`replace`
   in ABO for its G6 test (breaking SC7), and a module whose path is under
   `github.com/falkcorp/mediacore/` could still import `mediacore/internal/...` (X5), so it would not
   consume core "as an external app does" anyway. What keeps it honest is `archtest.SDKOnly` (03 R2):
   its packages import only SDK paths.
2. **Out-of-tree smoke module**, generated in a temp directory by `scripts/ci/core_consumer_smoke.py`
   with an **unrelated module path** (`example.com/mediacore-smoke`): a `go.mod` with one
   `require github.com/falkcorp/mediacore v0.0.0-…` and one `replace` to the checkout's `core/`, plus a
   copy of `examples/hello`'s `main.go`. It proves that nothing in core assumes it is built from inside
   this repo (relative `go:embed`, `../` paths, root-module tools). It also contains a **negative
   compile check**: a file importing any `github.com/falkcorp/mediacore/internal/...` path must fail
   with `use of internal package … not allowed`.

```sh
(cd core && go build ./examples/... && go vet ./examples/... && go test -short ./examples/...)
python3 scripts/ci/core_consumer_smoke.py --core core   # builds, vets, tests in $TMP; negative check; exit 0
```

"Only a version pin" — the form an app uses after the split — cannot be tested before a version is
published; it is the first verification step of the cutover (§12.8.2 step 8).

### SC4 — Core CI is standalone and green

(a) Core's **whole merge gate** runs from an export containing **only** `core/` — not just
`go build/vet/test`, so a target that calls a script outside `core/` fails here. Once `core/web` exists
(UI-0, 04 §8.1), `npm ci` runs first, so the run also proves that `core/go.mod`'s `ignore ./web` keeps
npm-shipped `.go` files out of `./...` (03 §3.2); before UI-0 there is no `core/web/package.json` and
the step is skipped. Core's own build outputs are ignored by files inside `core/`, so they travel with the export
and into the split repo, and the run leaves the tree clean without the root `.gitignore` (FE7-1, FE8-4):
`core/.gitignore`, created by the S0 PR that creates `core/`, lists `/.ci/readiness/` (the `readiness` report,
§12.3.11), `/hello` (the hello binary; or the hello target writes it under an ignored directory with `-o`),
`**/playwright-report/` and `**/blob-report/`; `core/web/.gitignore` (`node_modules/`, `dist/`, `*.tsbuildinfo`,
`coverage/`, `test-results/`, `playwright-report/`, `blob-report/`; created by the UI-0 PR, 07 §7.3 and 09 UI-0)
covers the workspace. A `git archive` extract is not a git work tree, so the check makes the export one
before the run (a fresh repository holding only `core/`, which keeps the "only `core/`" property) and then
asserts that `git status --porcelain` is empty:

```sh
git archive HEAD core | tar -x -C "$TMP"
(cd "$TMP/core" && git init -q && git add -A && git -c user.name=export -c user.email=export@localhost commit -qm export)
if [ -f "$TMP/core/web/package.json" ]; then (cd "$TMP/core/web" && npm ci); fi
make -C "$TMP/core" ci
test -z "$(git -C "$TMP/core" status --porcelain)"
```

(b) The core workflow (`.github/workflows/core-ci.yml`, job `core-ci`, runs `make -C core ci`,
§12.3.11) is one of the **named required checks** of the merge requirement (09 §9.2.1, 14 Q-I): root
`make ci` is a local target that does not contain core's merge gate (MOD4-8). The workflow triggers on
every PR; a `changes` classifier job decides whether its inputs changed — one trigger list, 09 §9.2.1's
(`core/**`, root `go.mod`, root `go.sum`) plus the workflow file (MOD5-15) —
and a job-level `if:` skips the job otherwise, so a skip reports success instead of leaving a required
check waiting. When `core/go.mod` or `core/go.sum` changes it also runs the root `go build ./...` and
`tidy-check`, because every core dependency change needs the root's `go.mod`/`go.sum` too (03 §3.2
item 4). SC4(b) holds when it was green on the last 20 runs on `main`. This measures the merge gate
`ci`, not `readiness`:

```sh
gh run list --workflow core-ci.yml --branch main --limit 20 --json conclusion \
  -q '[.[] | select(.conclusion != "success")] | length'      # must print 0
```

(c) No dependency skew between the binary and core CI (03 §3.2 item 6). The same script also asserts
that the empty `core/go.sum` exists and that the `--pin` pins hold, so it is not only reported: it is a
blocking root-side check in root `make ci` from S0 (09 S0 classification table), and SC4(c) reads its
result:

```sh
python3 scripts/ci/check_module_skew.py   # compares (cd core && go list -m all) with the root's go list -m all
```

### SC5 — Coverage

Total ≥ 70 % and every SDK package ≥ 60 % under `-short` (the root module's floor is 30 %,
`.ci/coverage-floor.txt`, `raw/14-build-ci.md` §2 step 12; a library others build on needs more).
These thresholds are a **readiness report** (`make -C core readiness`), not a merge gate: a 70 % gate
from S2b would block moving ABO code whose coverage is lower. The merge gate carries a coverage
**ratchet floor** instead (`core/.ci/coverage-floor.txt`, raised by `ratchet-lower.yml`'s core entry,
never lowered by a PR), the same mechanism as the root's.

```sh
(cd core && go test -short -coverprofile="$TMP/core.out" ./... \
  && go tool cover -func="$TMP/core.out" | tail -1)                    # total
python3 core/scripts/ci/core_coverage_by_package.py "$TMP/core.out" --min 60 --exclude 'kernel/|internal/|examples/|legacy/'
```

### SC6 — Every core contribution point is documented

Every config key, permission, storage keyspace, task kind, event topic, metric and route that core
registers appears in the generated reference under `core/docs/reference/`. Registration is data
(`module.Registrar`, 03 §3.3), so a generator can boot the `coretest` harness with zero modules and
enumerate it:

```sh
(cd core && go run ./internal/cmd/coredoc -check)   # non-zero if docs differ from registrations
```

### SC7 — No `replace` except the one to core; core has none; the only `exclude` is core's genproto line

```sh
go mod edit -json | python3 -c 'import json,sys;d=json.load(sys.stdin);r=d.get("Replace") or [];assert [x["Old"]["Path"] for x in r]==["github.com/falkcorp/mediacore"],r;assert not d.get("Exclude"),d.get("Exclude")'
(cd core && go mod edit -json | python3 -c 'import json,sys;d=json.load(sys.stdin);assert not d.get("Replace");assert all(x["Path"]=="google.golang.org/genproto" for x in d.get("Exclude") or [])')
```

Whether core's exclude is present, still *needed* and load-bearing is
`core/scripts/ci/check_genproto_exclude.py`'s job (03 §3.2 item 1); it runs in the merge gate.

### SC8 — npm packages build and pack standalone

`@mediacore/shell`, `@mediacore/api-client`, `@mediacore/ui-kit` (03 §3.3) pack on their own, and ABO's
`web/` builds against the packed tarballs instead of its in-repo `file:` dependencies. The workspaces
are named explicitly, in dependency order: `--workspaces` runs them in workspaces-array (glob) order,
which builds `shell` before the `ui-kit` it imports, and it would also pack hello's web package, which is
`"private": true` and never published. The order is **07 §7.3.2's one list**, which this file cites
rather than restates (FE7-12):

```sh
# WS: the -w flags of 07 §7.3.2's build order (hello's private package is not in it)
(cd core/web && npm ci && npm run build $WS && npm pack $WS --pack-destination "$TMP/pkgs")
python3 scripts/ci/web_build_against_tarballs.py --tarballs "$TMP/pkgs"   # copies web/, rewrites deps to file:<tgz>, npm ci, npm run build
```

### SC9 — No known vulnerabilities in core's graph

`govulncheck` comes from a pinned tools module, `core/tools/govulncheck/go.mod`
(`tool golang.org/x/vuln/cmd/govulncheck`; `golang.org/x/vuln` v1.9.0, published 2026-10-11, is the
newest release on 2026-11-04 and past the 14-day soak), never `@latest`:

```sh
go -C core/tools/govulncheck install tool
(cd core && govulncheck ./...)   # exit 0
```

### SC10 — Toolchain parity

`core/go.mod`'s `go` directive equals the root's and neither has a `toolchain` line (03 §3.2;
`.standards/instructions/go.md` "Keep `toolchain` at or above the `go` line, or omit it"); the
`GOTOOLCHAIN` pin in `core/Makefile` equals the root `Makefile` pin. Measured by
`scripts/check_toolchain_versions.py` once it is extended to read `core/go.mod`, `core/Makefile`,
`core/tools/*/go.mod` and the existing `tools/*/go.mod` (today it reads only the root `go.mod` and the
root `Makefile` pin, `scripts/check_toolchain_versions.py:44-46,329-351`; see
[13-toolchain](13-toolchain.md) §13.2.2). There is no `core/examples/hello/go.mod` to check (SC3).

Every criterion is also a **reported** CI job from the stage that introduces it, so readiness is
continuously visible rather than measured once. Only the criteria that must hold at every merged PR head —
SC2, SC7 and SC10 — are also part of the merge gate (`make -C core ci`; SC2's root-side
`TestCoreHoldsNoAppKeyNames` runs in root `make ci`, because it reads books' generated claim list through `KeyFamilies()`, ABO's setting keys and op IDs, 03 R1(c)); SC1, SC5, SC6 and SC9 are
readiness reports (`make -C core readiness`), and SC3, SC4(b), SC4(c) and SC8 run in the root
(SC4(c)'s script is also a blocking root `make ci` check, above).
SC10's parity half (core pin = root pin) is a root-side job (`scripts/check_toolchain_versions.py`, 13
§13.2.2) dropped at cutover step 6; core's own `toolchain-check` (`check_core_toolchain.py`) checks only
the copies core carries (`core/Makefile`, `core/tools/*/go.mod`, the core workflow's `go-version`), which
stays meaningful in the core repo.

### 12.3.11 `core/Makefile`

Core needs its own Makefile because SC4 runs core CI from an export of `core/` alone and, after the
split, the core repo has nothing else. It is specified now so that it does not grow ad hoc:

| Element | Content |
|---|---|
| Toolchain pin | `export GOTOOLCHAIN := go1.27.x`, equal to the root `Makefile` pin. This is one more pinned copy; the toolchain guard checks it (SC10, 13 §13.2.2), so it cannot drift silently. A `print-toolchain` target prints it, so commands and scripts never repeat the literal. |
| Tools | `tools` target: `go -C tools/apidiff install tool`, `go -C tools/govulncheck install tool`, and a core copy of the staticcheck tools module, `tools/staticcheck`, pinned to the same version as the root's `tools/staticcheck` (`check_module_skew.py`, SC4(c), also compares the two tools modules' requirements). |
| `ci` target (merge gate) | `build vet vet-darwin vet-integration test-short fmt-check fix-check tidy-check staticcheck archtest exclude-check coverage-floor replace-check toolchain-check version-check errcheck-ratchet interface-width mocks-check pprof-check web-test embed-check` — i.e. Go build/vet/short tests with `-race`, `go vet` for `GOOS=darwin CGO_ENABLED=0` (goreleaser's darwin target, vetted **without** `embed_frontend`, because `go vet` has no `-e` and hello's `examples/hello/web/dist` is unbuilt in `ci`; the embed-only files carry no GOOS constraint, and the tag-union guards still read the darwin `embed_frontend` set through `go list -e`; 03 §3.5.1 item 4, R10-9, MOD10-8) and with `-tags integration`, so every (GOOS, tag set) of 03 §3.5.1 item 4 that core files can carry is compiled on every PR (root `make ci` runs the same two vets over the root; R9-10, MOD9-3), `gofmt -l` excluding `web/` and `examples/*/web/`, `go fix -diff ./...` empty (13 §13.3), `go mod tidy -diff`, staticcheck, archtest (R1, R2, R5 and the R4 layer guard: SC2), the genproto exclude check in **core mode only** plus the present/misplaced scan of `core/` (03 §3.2 item 1; the root mode and the tree-wide scan are the root job `genproto-exclude-root`), the coverage ratchet floor (SC5), the `replace`/`exclude` check (SC7), core's own toolchain copies (SC10), `check_core_version.py`'s core-only checks (§12.4.3), and the gates that ran for core only through root `make ci` before (MOD5-15): the errcheck and interface-width ratchets over `core/.errcheck-baseline` and `core/.interface-width-baseline`, `mocks-check` over core's mockery config, `pprof-check`, and the `core/web` workspace tests (`npm ci` then vitest, from UI-0; "not applicable" before); `embed-check` (`core/scripts/ci/check_embed_outputs.py`) runs `go list -e -json` over core for 03 §3.5.1 item 4's tag sets and fails on a "no matching files" embed error for any path not on core's own list `core/scripts/ci/embed_build_outputs.txt` (hello's `examples/hello/web/dist` from S9; no Dockerfile cross-check, core builds no image; 03 §3.2 item 4, MOD7-6). Their scripts are core copies under `core/scripts/ci/`, so SC4(a)'s export and the post-split repo run them, and cutover step 6 drops none of them. Every check here holds at every **merged PR head** (the repo merges rebase/fast-forward only; MOD4-14), so a red `ci` always means a defect. Intermediate commits are built only for **move PRs**: for any PR whose diff renames a file into `core/`, `scripts/ci/check_commits_build.py` in `extraction-gates` builds every commit of the PR (`go build ./... && go -C core build ./...`) and reports each moved file whose first-commit similarity falls below §12.8.1's threshold, because the history split needs the move in a buildable first commit (MOD9-5). SC4 is the workflow running this target. |
| `readiness` target (report-only) | `apidiff-gate coverage coredoc-check vulncheck` — SC1, SC5's thresholds, SC6 and SC9. It never fails a merge and never writes outside `core/`: it writes a JSON and Markdown report, with date and commit, to `$(READINESS_OUT)` (default `core/.ci/readiness/`, ignored by `core/.gitignore`, SC4(a)), which CI uploads as an artifact. Pre-split, a root-side nightly job folds the artifact into `docs/plans/platform-core/split-readiness.md` through a PR; that job is dropped at cutover step 6. SC3 and SC8 run in the root (pre-split) because they need ABO's `web/` or a temp module; the history rewrite on a throwaway clone, with its fixed-sample `--follow` assertion, and the missing-origin list (§12.8.1) are also reported here. |
| `release` target | post-split only: `make release VERSION=vX.Y.Z` runs §12.5 steps 1–5. |

## 12.4 Versioning and semver policy

### 12.4.1 In-repo phase (v0, no published versions)

- The root module requires the zero pseudo-version and `replace`s it (03 §3.2). Nothing outside
  this repo can depend on core.
- **Checkpoints.** Every ABO production release that changed `core/` also records a checkpoint
  `refs/core-checkpoints/v0.Y.Z` on the same commit (`git update-ref refs/core-checkpoints/v0.Y.Z
  <commit>`, pushed with an explicit refspec `git push origin refs/core-checkpoints/v0.Y.Z`).
  Checkpoints are deliberately **not tags**: with an annotated `core-v0.Y.Z` tag on the release commit,
  `git describe --tags --always --dirty` (the reported version, `Makefile:9`) returns `core-v0.Y.Z` and
  every later build returns `core-v0.Y.Z-N-g…` (X6), which breaks the version ABO reports, the deploy
  check that compares the served version with `git describe`, updater semver comparison, and any
  release tooling that reads the newest tag. Refs outside `refs/tags` are invisible to `git describe`,
  to Go tooling and to a default clone or fetch (CI fetches them explicitly, SC1), and they sort by
  version with `git for-each-ref --sort=-v:refname`.
- The checkpoint commit has `version.Version == "0.Y.Z"` (§12.4.3).
- **Who bumps and who records.** The constant is bumped by **one** "core version" PR before a production
  release that carries `core/` changes — never per feature PR, which would make parallel core PRs
  conflict on one file (the reason this repo uses fragments). The PR picks the bump from the apidiff
  result against the newest checkpoint: minor for incompatible or additive changes, patch otherwise.
  Recording is a job in the production release workflow, `.github/workflows/release-prod.yml` job
  `core-checkpoint` with `permissions: contents: write` (pushing a ref needs it; the job runs in the
  caller workflow, not inside the reusable `falkcorp/github-common` workflow). The job is
  **non-blocking**: no other job `needs` it and it runs with `continue-on-error`, so an ABO production
  release — including a hotfix or a roll-forward — **never fails because of it**. If
  `git diff <newest checkpoint>..HEAD -- core/` is non-empty and `version.Version` is greater than the
  newest checkpoint, it runs `git update-ref` and pushes the ref with the explicit refspec above. If
  `core/` changed and the constant was not bumped, it records nothing and opens or updates one issue
  labelled `core-version-bump-needed`. The job also runs when `prerelease.yml` auto-promotes an RC
  (09 §9.1). SC1 counts **recorded** checkpoints only, so a skipped record costs readiness time, never
  a release.
- **v0 rules.** Incompatible SDK changes are allowed (v0 makes no promise) but must (a) bump the
  checkpoint minor, (b) carry a changelog fragment in category `Removed` or `Changed` whose first line
  starts with `BREAKING:`, and (c) reset the SC1 counter. Additive changes bump the minor; fixes bump
  the patch.
- `kernel/`, `internal/`, `legacy/` and `examples/` are outside every promise in every phase (03 §3.3; `legacy/`
  by R6-3).

### 12.4.2 At and after the split

- **First published version.** If SC1 holds (it must, for the split to happen) the first tag in the
  new repo is **`v1.0.0`**. The owner may instead publish `v0.(Y+1).0` first and promote to `v1.0.0` after
  one app release cycle on the published module; either way v1 is reached at or after the split, never
  before (there is nothing to publish before).
- **Semver from v1.** Patch: fixes, no API change. Minor: additive API, new contribution points,
  dependency minor/patch bumps, raising the `go` directive (it forces consumers to a newer Go, so it is
  never a patch). Major: any incompatible SDK change — **and in Go a major ≥ 2 changes the module path**
  to `github.com/falkcorp/mediacore/v2`, so every app import changes. Policy: avoid v2; batch any
  unavoidable breaks into one major, announced one minor ahead.
- **Deprecation of SDK identifiers.** A deprecated Go identifier gets a
  `// Deprecated: use X; removed in vN+1.0.0` doc line (picked up by `staticcheck` SA1019 in app CI) and
  stays for **at least two minor releases and 90 days**. Removal therefore only happens in a major.
  `legacy/` is outside this policy (R6-3, SIMP6-2): a family's legacy-only identifiers are deleted in the
  first release after the family's cutover that no longer sets its `legacy.Stores` member (the cutover
  build still reads the legacy data through it; behind the cutover's `UpgradeFloor`, 05-storage
  §5S.3.13), a minor (COR7-5), with a changelog fragment that names them; an app
  that still refers to one fails to compile, which is the intended signal, and an app with no pre-core
  data never imports `legacy`.
  This is core's library deprecation policy for its Go API. It is **not** ABO's compatibility window
  for wire, config, permission and storage surfaces, which is defined once in
  [09](09-abo-on-core.md) §9.2.1 and implemented in `compat`.
- **Bad releases** are withdrawn with a `retract` directive in the next release's `core/go.mod`, never by
  deleting the tag (the module proxy and checksum database keep it forever).
- **npm lockstep.** `@mediacore/*` packages are versioned in lockstep with the Go module (same `X.Y.Z`),
  because `@mediacore/api-client` is generated from the core OpenAPI document of that exact version
  (03 §3.3). A shell-only fix still bumps the Go module's patch (no-op Go release) to keep one number.

### 12.4.3 The core version constant and `MinCoreVersion`

- **Source.** `version.Version` (03 §3.3, public L0 package) is a hand-maintained constant `"0.Y.Z"` /
  `"X.Y.Z"`. It is never derived from `debug.ReadBuildInfo`: under the in-repo `replace` that reports
  `v0.0.0-00010101000000-000000000000`, which is below every `MinCoreVersion` and would fail every boot
  fail-closed.
- **Initial value.** S1 lands `const Version = "0.1.0"` (03 §3.3), so the S2 `hello` skeleton
  (`MinCoreVersion: "0.1.0"`, 06 §6.12) passes P1 before any checkpoint exists.
- **Kept honest by CI.** `version.Version` is kept **≥** the newest checkpoint (03 §3.3): the one
  "core version" PR raises it above the newest checkpoint before the release that records the next one,
  so equality alone would fail that flow. Pre-split, `core/scripts/ci/check_core_version.py` runs in
  core's merge gate (`version-check`, §12.3.11) and checks only what core holds: the constant is ≥ the
  newest checkpoint name (it needs the explicitly fetched `refs/core-checkpoints/*`, SC1; in the SC4(a)
  export, which has no refs, that half reports "not applicable", as the exclude check does for a probe that
  does not apply), hello's `Manifest.MinCoreVersion` is ≤ `version.Version`, and every
  `core/web/packages/*/package.json` `version` equals `version.Version` (the in-repo `file:` dependencies
  make the lockstep check of §12.6 a no-op, so this is what keeps the shell's reported version, 07 §7.5.2,
  equal to the Go constant). Books' `MinCoreVersion` ≤ `version.Version` is a **root-side** test in
  `internal/books` (listed in 09 S0's classification table), because a core-owned script cannot read an
  app's manifest from the `core/` export or after the split (MOD4-13). The `core-checkpoint`
  job (§12.4.1) refuses to record `refs/core-checkpoints/v0.Y.Z` on a commit whose constant is not
  `0.Y.Z`. Post-split, step 1 of
  `make release VERSION=vX.Y.Z` (§12.5) asserts `version.Version == "X.Y.Z"`.
- **Readers.** The kernel's manifest check, `GET /api/core/v1/modules` (`core.version`, which the
  shell compares with its own build, 07) and the backup manifest (`core_version`, 05-backup) all read
  this one constant.
- **`Manifest.MinCoreVersion` semantics** (06): a semver string. At P1 the kernel compares it with
  `version.Version` by semver precedence and fails closed with `ErrCoreTooOld` when the constant is
  lower. An empty value means "no floor". Because Go's minimal version selection already guarantees
  that a module compiled against core vX runs with core ≥ vX, the check can only fire when
  `version.Version` is stale — the defect the CI check above prevents. It is kept (SIMP-22, as
  modified) because it records each module's floor where operators and the shell can read it
  (`/api/core/v1/modules`, backup manifests), not as a second dependency resolver.

### 12.4.4 Hardening cutovers (post-S10)

The extraction is representation-neutral (03 introduction; 02 decision 13, A-2), so the hardening of ABO's stored
representations is not an extraction stage. It is three named **hardening cutovers**, H1–H3 as 09 §9.13 names and stages them, each a one-way, floored
cutover on 02 N15's machinery (mandatory pre-migration checkpoint, per-owner level guard, the cutover release as
its rollback floor):

| Cutover | What it changes for ABO | Until it runs (as at HEAD) |
|---|---|---|
| **H1 — identity cutover** | sessions and one-time tokens stored hashed; existing sessions, invites and other one-time tokens are **deleted**, not rehashed, so raw tokens in older archives are dead against the live store (05-authn §11.2 owns this; SEC10-7); `mck_` keys; roles and keys hold native scopes. The scopes a principal held only through `LegacyAmbientScopes` (`core:self`, `core:metrics.read`, `books:reading`) are written into a role iff the role is not `Confined()`, and into a key iff the key's **effective** S4–S10 set before Ambient (the owner's role atoms ∩ the key's atoms) is not `Confined()`, never from its owner, so a worker-scoped key stays confined and every principal's answers are unchanged across H1 (05-rbac §6.3 owns the rule; R10-2, SEC10-1) | raw `sess:` rows and HEAD's one-time tokens; `abk_` keys and roles holding legacy atoms, with namespaced scopes as an API-level mapping (`rbac.LegacyAtom`) |
| **H2 — secrets cutover** | the plaintext blob secrets are encrypted into `c/secrets/` and their blob fields removed; HEAD's four encrypted secret rows move there too (05-config §1.12.5 decides this, 09 §9.13 cites it; DATA9-8) | plaintext blob secret fields; the four secrets HEAD already encrypts stay as they are |
| **H3 — auth-precedence cutover** | an operator value that enables auth takes effect over a stored `false`; a disabling value keeps HEAD's precedence; the auth-disabled principal can no longer store `enable_auth` (05-config clears `HEADAuthOffWritable` on that key only; the Basic gate keys keep HEAD's auth-off write rule until the settings cutover, which precedes or ships with H3, and the three gate keys lose it together, 05-config §1.3.1; SEC10-5, SEC10-6); nothing else moves: every other `BelowStored` legacy flag (the Basic gate's among them, which compose always sets) keeps HEAD's precedence until a separate, owner-decided cutover (R9-5, SEC9-2, SEC9-7) | HEAD's precedence: the stored value wins |

**Their real reach (R9-11; SEC9-3, DATA9-8).**

- **Level.** Each cutover raises the level of **every** owner whose rows it rewrites: `books` for every rewrite of
  a books legacy family (H1's identity rows, H2's blob fields, H3's flags), plus `core` when it changes `c/` rows. The
  guard therefore refuses the store on every older build, the pre-guard builds through the legacy projection. The
  first cutover **that raises `core`** also lifts books' `{core, 0}` legacy restore bound (09 §9.13); one that raises
  only `books` does not (DATA10-7). The guard models each owner it raises.
- **What is deleted.** H1 and H2 rewrite the live store, then delete **every** pre-cutover checkpoint of the store
  and every old Pebble file, not only their own attempt's, through a forced compaction in the cutover boot, so no raw
  token (H1 stores some in **keys**, `sess:<raw>`) and no plaintext secret survives in an SST, a WAL segment or any
  checkpoint (R10-11, DATA10-6; 05-storage §5S.3.13 owns the list of what a cutover clears).
- **What is not.** Archives taken before the cutover still hold the old representation. The done-checks say so
  plainly. API keys are stored as SHA-256 hashes at HEAD, so an old archive exposes no usable key; the release note,
  whose one text per cutover 09 §9.13 owns and every other file quotes, tells the operator to rotate third-party
  secrets if old archives are a concern (SEC10-7).

They are not split criteria: neither T nor SC1–SC10 waits on them, and each may run before or after the split.
A cutover that runs after the split is an ABO change to its legacy store implementations plus native
behaviour core already ships, released through ABO's own release; the `legacy` identifiers it stops using are
deleted under §12.4.2's `legacy/` rule. Their order, measured inputs and release notes are planned when they
are scheduled; 14 K25 states the residual risk until each has run.

## 12.5 Release process (core repo, after the split)

No goreleaser: core ships no binary. Steps (a `make release VERSION=vX.Y.Z` target in the core repo,
§12.3.11, runs 1–5; 6–8 are the release workflow):

1. Assert `version.Version == "X.Y.Z"` (§12.4.3); then `core_api_gate.py --base <previous tag>` and
   `gorelease -base=<previous tag> -version=vX.Y.Z` from the pinned tools module — fails if the requested
   version understates the API change of the SDK; a suggested major caused only by `kernel/` or `legacy/`
   lines is overridden (post-split form of SC1, R6-3).
2. `scriv collect --version X.Y.Z` with the same `scriv.ini` shape as this repo
   (`changelog.d/scriv.ini:8-21`: Markdown, Keep-a-Changelog categories) → `CHANGELOG.md`; PR; merge.
3. `git tag -a vX.Y.Z -m "mediacore vX.Y.Z"` on the merge commit (signed if the owner's key is configured);
   `git push origin vX.Y.Z`.
4. Warm and verify the proxy: `GOPROXY=https://proxy.golang.org GOTOOLCHAIN="$(make -s print-toolchain)" go list -m github.com/falkcorp/mediacore@vX.Y.Z`.
   (If the repo is private: apps set `GOPRIVATE=github.com/falkcorp/*` and authenticate with a GitHub
   token; the proxy step is skipped. Both are free.)
5. GitHub release with the changelog section as body (`gh release create vX.Y.Z --notes-file …`).
6. Publish npm packages at `X.Y.Z`: public repo → the npm registry (free for public packages). A private
   repo has no token-free option (FE5-16): GitHub Release tarballs of a private repo need an authenticated
   download, the same cost that rejected GitHub Packages — every machine and CI job needs an `.npmrc` with a
   fine-grained read token — and an exact `"X.Y.Z"` specifier cannot name a tarball, so apps pin tarball
   URLs that `check_mediacore_lockstep.py` parses for `X.Y.Z` and that Dependabot's npm updater does not
   bump (07 §7.17). A public core repo is therefore the precondition of the token-free npm path (14 Q-B). `npm publish $WS --access …`
   (07 §7.3.2's order, as in SC8; hello's web package is private).
7. Dispatch the downstream matrix (§12.7) against the tag.
8. Dependabot in each app repo opens the bump PRs (§12.6).

## 12.6 How app repos pin core

```
require github.com/falkcorp/mediacore vX.Y.Z      // no replace
```

```json
"dependencies": { "@mediacore/shell": "X.Y.Z", "@mediacore/api-client": "X.Y.Z", "@mediacore/ui-kit": "X.Y.Z" }
```

Exact npm versions (no `^`), from the npm registry for a public core repo (07 §7.17 states the same
mechanism), because the Go side and the npm side must move together. For a private core repo the
dependencies are tarball URLs fetched with a read token on every machine, and npm bumps are manual
(§12.5 step 6; FE5-16). Before the split ABO's `web/` uses `file:` dependencies on
`core/web/packages/*`; there is no root npm workspace.

Updates come from **Dependabot** (already configured here, free):

```yaml
# .github/dependabot.yml (app repo) — additions
  - package-ecosystem: gomod
    directory: /
    groups: { mediacore: { patterns: ["github.com/falkcorp/mediacore"] } }
  - package-ecosystem: npm
    directory: /web
    groups: { mediacore: { patterns: ["@mediacore/*"] } }
```

Each `groups` entry is per ecosystem, so this produces **two PRs** per core release (one Go, one npm),
not one. A half-bumped state (Go core X+1 with shell X) therefore exists between the two merges, and
`scripts/ci/check_mediacore_lockstep.py` (every app repo) fails CI on it: the `@mediacore/*` versions in
`web/package-lock.json` must equal the `github.com/falkcorp/mediacore` version in `go.mod` (in-repo,
both are the workspace version). The two bot PRs are merged together, or a person combines them.
Renovate (free GitHub app) is an acceptable alternative if one PR across ecosystems is wanted; not
required. ABO's existing npm rule blocks all majors (`.github/dependabot.yml:76-79`); that is the
desired behaviour for `@mediacore/*` too (a core major is a planned migration, not a bot PR).

Co-developing core and an app: either a **temporary** `go mod edit -replace
github.com/falkcorp/mediacore=../mediacore` that CI rejects (SC7-style check in every app repo), or a
local, uncommitted `go.work` (`go work init . ../mediacore`). A **committed** `go.work` is banned; a
local one is allowed because core's `go.mod` carries the genproto exclude, which applies to every
workspace that has core as a member (03 §3.2 item 1).

## 12.7 Cross-repo CI

| Where | Job | Trigger | What |
|---|---|---|---|
| core repo | `downstream` (matrix over `falkcorp/audiobook-organizer`, the TV app, the Movies app) | every PR and push to `main` that touches an SDK package | check out the app at `main`; `go mod edit -replace github.com/falkcorp/mediacore=$GITHUB_WORKSPACE/core`; `go build ./... && go vet ./... && go test -short ./...`; build the app's `web/` against the PR's packed `@mediacore/*` tarballs (SC8 script). **Required** for merge after v1; before v1 a red downstream is allowed only with the `BREAKING:` changelog fragment and a linked app PR. |
| core repo | `core-ci` | every PR | `make ci` (§12.3.11 merge gate). After the split SC1's role passes to release step 1 (`gorelease`); `make readiness` keeps running nightly as a report. |
| each app repo | `core-main` nightly | cron | `go get github.com/falkcorp/mediacore@main && go build ./... && go test -short ./...`; on failure open (or update) one issue labelled `mediacore-main`. Early warning only; never blocks the app. |
| each app repo | normal CI | PRs | builds against the pinned version only; runs `check_mediacore_lockstep.py` (§12.6) |

All jobs use the reusable workflows in `falkcorp/github-common` (`raw/14-build-ci.md` §10 item 4), so the
matrix logic exists once. Cost note: the downstream matrix multiplies core PR minutes by the number of
apps; it runs `-short` only, which on ABO is the ~8-minute budget measured in `Makefile:292-299`
(`raw/14-build-ci.md` §2). Public repos have unlimited free Actions minutes; private repos use the free
monthly allowance — if that becomes binding, the matrix moves to the self-hosted Woodpecker agents
already used by ABO (`raw/14-build-ci.md` §7), which costs nothing extra.

## 12.8 History preservation and cutover

### 12.8.1 Preserving history

`git filter-repo` (MIT-licensed, `pip install git-filter-repo`) rewrites a **fresh clone**, never the
working repo. `--subdirectory-filter core` alone would keep only commits made after files arrived in
`core/`; code that moved there from `internal/...` would lose its earlier history. So the filter uses a
path file with filter-repo's `==>` rename syntax, followed by the subdirectory step. The order of that file is
significant (R9-10, MOD9-4): filter-repo applies `--paths-from-file` directives **in file order**, a filter line
tests a path as renamed by the lines before it, a rename line never marks a path wanted, and with any filter line
present every unwanted path is dropped (git-filter-repo 2.47.0, round-9 modules-build Z10). A `core/` line placed
first would therefore test `internal/logging/sanitize.go` under its old name, find it unwanted, and drop every
pre-move commit of the file.

The path file is generated from **real history at the freeze commit `F` by git's rename detection**, not from
04's cells and not from a curated list (A-4). 04's cells cannot express a source file divided between
destinations (e.g. `internal/telemetry/telemetry.go`, divided between core `metrics` and ABO's remaining façade), give no
destination file names, and cannot express a coexistence window (core's scheduler and executor are written
fresh while ABO's `internal/scheduler` and `internal/operations/registry` files live on, 04 §8.6). Mapping two
coexisting, diverging files to one path either aborts filter-repo with `File renaming caused colliding
pathnames!` when one commit touches both (round-2 X13), or splices the ABO copy's later edits into core
history. Rule:

- an origin path is mapped **only** where `git log -M90% --diff-filter=R --name-status F` (no pathspec: a
  pathspec would hide the source side and turn every move into an add) reports a rename into `core/` — the source path deleted in the same commit that adds the `core/` path — so a copy,
  whose source still exists, is never mapped and two coexisting files never map to one path (X13);
- every other `core/` file keeps only its post-arrival history: a re-implemented file, a split file, a copy
  (e.g. the `core/internal/keyedmutex` copy, 03 §3.2.1, whose origin stays in ABO; the core repo's first
  commit message records such origins and the ABO commit they were copied from), and a moved file whose move
  commit changed it too much for detection;
- a rename chain is mapped **transitively**: every earlier name of a file that ends under `core/` maps to its final
  path, so a file renamed inside ABO before its move keeps the history from before that rename (MOD9-4);
- an origin path that history **re-creates** after its move commit (the package stays, as `internal/fileops` does)
  fails `history_paths.py` and is listed in the readiness report; such a path is mapped by a `--commit-callback`
  that renames it only in commits up to its move commit, so a later ABO file at the vacated path is never spliced
  into core's history (MOD9-4). The callback must see the path **before** any filter does: within one filter-repo
  run, path filtering runs before the commit callback and its result is cached per filename, so a re-created origin
  with no `==>` line would be dropped by the `core/` line before a callback in that run ever saw it (git-filter-repo
  2.47.0; round-10 modules-build E6). So these paths get a separate **pass 0** with no path filter, whose
  `--commit-callback` renames each listed origin to its `core/` path only in the commits of `git rev-list
  <move-commit>`; pass 1's path file then carries no line for them, because pass 0 already renamed them (R10-9,
  MOD10-9);
- files that never arrived in `core/` are not listed, so no ABO-only code (`internal/keyedmutex`, the
  `internal/telemetry/contract/` suite, `internal/telemetry/ratchet_test.go`, `cmd/`) enters core's
  history; core-owned scripts were born under `core/scripts/ci/`, so the `core/` line carries them.

```text
# core-history-paths.txt (generated by core/scripts/split/history_paths.py at F from
# `git log -M90% --diff-filter=R --name-status F`, keeping renames whose destination is under core/)
# Order is significant: every pre-move path ==> its final core/ path first, then the core/ line.
internal/logging/sanitize.go==>core/log/sanitize.go
internal/logging/structured.go==>core/log/structured.go
...
core/
```

**Keeping moves detectable.** Detection works per commit, so a move PR puts the move and the edits every
commit needs to build (import paths, the mechanical rewrites 04 §2.2 lists) in its first commit, and `go fix`
modernizers and any other rewrite in later commits (13 §13.3). Every commit of a move PR builds, because the repo
merges rebase/fast-forward only and `git bisect` and `auto-revert.yml` see each one; this is enforced, not assumed:
`scripts/ci/check_commits_build.py` in `extraction-gates` builds each commit of any PR that renames a file into
`core/`, and reports every moved file whose first-commit similarity is below the 90 % threshold, so a PR that
squashes `go fix` into its move commit is caught before merge (§12.3.11; MOD9-5). A small file whose move commit
still falls under 90 % similarity loses its earlier history; that is the accepted cost of not keeping a
curated origins list (A-4). `history_paths.py`'s readiness report lists every `core/` path whose first commit
is not a detected rename, so the owner sees which files start their history in `core/` before cutover.

The readiness report (§12.3.11) runs the **real** rewrite below on a throwaway `--no-local` clone of `main`, so a
collision and a lost history both surface long before cutover step 3; `--dry-run`, which does not change the
repository, is used only as the cheap collision probe (R10-9, MOD10-9). The history assertion runs on the
**filter-repo output**, never on the source repo, for a **fixed sample** of moved files, the committed list
`core/scripts/split/history_sample.txt` (a verbatim move such as `internal/logging/sanitize.go`, one file of a
transitively mapped rename chain, and every re-created origin of pass 0): for each, `git log --follow --format=%H --
<path>` in the rewritten clone (the path as it stands after the subdirectory pass, without `core/`) must end at the
commit that first created its earliest mapped name at `F`, translated through the `.git/filter-repo/commit-map`
files saved after each pass and composed in pass order, since rewritten commits have new SHAs. A sampled path whose
history ends at its move commit, or at any other commit, fails the report. `history_paths.py`'s list of `core/`
paths whose first commit is not a detected rename covers the rest. Cutover step 3 repeats the assertion before
anything is pushed.

```sh
git clone --no-local --branch main <abo-remote-url> mediacore-split
# pass 0 (only when history_paths.py lists re-created origins): no path filter; the callback renames each listed
# origin to its core/ path only in commits of `git rev-list <move-commit>` (history_paths.py generates it)
(cd mediacore-split && git filter-repo --commit-callback "$(cat ../core-history-recreated.py)" && cp .git/filter-repo/commit-map ../map-0)
(cd mediacore-split && git filter-repo --paths-from-file ../core-history-paths.txt && cp .git/filter-repo/commit-map ../map-1)
(cd mediacore-split && git filter-repo --subdirectory-filter core && cp .git/filter-repo/commit-map ../map-2)
python3 core/scripts/split/history_assert.py --clone mediacore-split --sample core/scripts/split/history_sample.txt \
  --maps ../map-0 ../map-1 ../map-2 --freeze F                              # the fixed-sample --follow assertion
(cd mediacore-split && git tag -l | xargs -r git tag -d)                 # drop every ABO tag (v0.2xx.x, RCs)
(cd mediacore-split && test -z "$(git for-each-ref refs/core-checkpoints)")  # never fetched by a clone
(cd mediacore-split && git log --oneline | wc -l)                         # recorded in split-readiness.md
(cd mediacore-split && export GOTOOLCHAIN="$(make -s print-toolchain)" && go build ./... && go test -short ./...)
```

Tags are deleted because a later tag push would publish ABO version numbers as mediacore module
versions, and the proxy would keep them forever, above the planned first version.

Before anything is pushed: run a secret scanner over the **whole rewritten history** (`gitleaks detect
--log-opts=--all`, free) and the repo's confidentiality scrub over every file at `HEAD`. ABO's pre-commit
hook blocks token files going forward (`scripts/setup-git-hooks.sh`, `CLAUDE.md` "Pre-Commit Hook") but
does not prove the past is clean.

Rewritten commits get new SHAs; spec and CHANGELOG references to ABO SHAs stay valid in ABO, and the core
repo's first commit message records the ABO commit the split was taken from.

### 12.8.2 Cutover runbook

Precondition: T and SC1–SC10 hold, recorded in `split-readiness.md`.

1. **Freeze** `core/` in ABO: a CI check fails any PR touching `core/` (label `core-freeze`), announced.
2. **Re-measure** SC1–SC10 on the freeze commit `F`.
3. **Filter** (§12.8.1) from `F` (pass 0 when needed, then the path file, then the subdirectory pass); run §12.8.1's
   fixed-sample history assertion on the output, which must pass (on a failure the cutover stops at this step and
   nothing has been pushed: fix the path file or the sample's generator, re-run from a fresh clone); delete tags;
   scan; build; test.
4. **Create** `falkcorp/mediacore` (`gh repo create falkcorp/mediacore --private|--public`), set branch
   protection, add `core-ci.yml`, `downstream.yml`, `dependabot.yml`, `CODEOWNERS`, the
   reusable-workflow ref, `LICENSE` (§12.10 item 1). Push **only** `main`, with an explicit refspec:
   `git push origin refs/heads/main:refs/heads/main` (never `--tags` or `--mirror`).
5. **Tag** the first version (§12.4.2) and run the release process (§12.5) including npm publish.
   Precondition: `git ls-remote --tags origin` prints nothing.
6. **ABO PR** (one PR, so it reverts as one unit):
   - `go mod edit -dropreplace github.com/falkcorp/mediacore && go get github.com/falkcorp/mediacore@vX.Y.Z && go mod tidy`
     (the root never carried the genproto exclude, so there is nothing else to drop);
   - `git rm -r core/`; `GO_MODULE_DIRS := .` in the Makefile; remove core jobs and core gates
     (`ci/gates.txt` `core-job` rows) from CI, and the root-side core jobs (`check_module_skew.py`, the
     root-mode genproto job `genproto-exclude-root` of 03 §3.2 item 1, the readiness fold-in job); drop `core/go.mod`, `core/Makefile` and
     `core/tools/*/go.mod` from the toolchain guard; remove `core/web/**`, `core/go.mod`, `core/go.sum`
     from path filters and `/core` and `/core/web` from Dependabot; `web/package.json` `file:` deps →
     exact `@mediacore/*` versions; `npm ci --prefix web` to refresh the lockfile; the Dockerfiles' Go stage stops copying
     `core/go.mod` and `core/go.sum`, and the Docker frontend stage drops its `core/web` copies, its
     `core/web` `npm ci` and its core package builds (back to `npm ci` and the build in `web/`); `make
     web-deps` drops its `core/web` prerequisites, and `scripts/ci/build_web.sh` already skips `core/web`
     when it is absent (07 §7.3.2; FE5-15). Without this, step 7's `docker build .` fails on the first
     `COPY core/web/…`.
   - Root `internal/arch` keeps calling `archtest` — now from the versioned module.
   - Add a guard that fails if a `core/` directory reappears.
7. **Verify ABO**: `make ci`, e2e, `binary-smoke.yml`, `docker build .`; `go version -m ./audiobook-organizer | grep mediacore`
   shows `vX.Y.Z` with a checksum (no `=>` replacement); `go install github.com/falkcorp/audiobook-organizer@<new tag>`
   from outside the repo works again, because the root `go.mod` now has neither `replace` nor `exclude`
   (the cost accepted in 03 §3.2 item 5 is removed; X4).
8. **Verify "version pin only"** (SC3's post-split form): `core_consumer_smoke.py --version vX.Y.Z`
   generates the unrelated-path smoke module with a plain `require … vX.Y.Z` and no `replace`, and builds
   it against the proxy (or with `GOPRIVATE`).
9. **Unfreeze.** All core changes now go to `falkcorp/mediacore`; ABO picks them up through Dependabot.
10. **Docs**: ABO `CLAUDE.md` (including its `go.work` guidance), `AGENTS.md`, and this spec's README get
    a pointer to the new repo.

## 12.9 Rollback of the split

| When | Action | Data/version consequence |
|---|---|---|
| Before step 6 merges | Archive or delete `falkcorp/mediacore`; lift the freeze. ABO is untouched. | A published tag may already be in the proxy; it is harmless (nothing depends on it). Do not reuse that version number later. |
| After step 6, before any core-repo commit beyond the tag | `git revert` the ABO cutover PR (restores `core/`, the `replace`, Makefile/CI/web wiring). | ABO builds exactly as at `F`. |
| After core-repo commits beyond the tag | Revert the ABO cutover PR, then port the new commits: in the core repo `git format-patch vX.Y.Z..main -o ../patches`; in ABO `git am --directory=core ../patches/*.patch`. Archive the core repo with a README pointing back. | Published versions stay in the proxy forever; add `retract [vX.Y.Z, vLAST]` in a final core release so `go get @latest` stops offering them. npm: `npm deprecate '@mediacore/*@<range>' "moved back into audiobook-organizer"` (unpublish is limited to 72 h and would break lockfiles). |
| Any time | Never force-push or delete tags in either repo. | Checksum database consistency. |

Rollback cost is low by construction: module path, import paths and package layout are identical before
and after (03 §3.2 item 2), so a rollback is a `replace` line plus a directory, never an import rewrite.

## 12.10 Open items

1. **Licence of the core repo.** History carries PolyForm Noncommercial 1.0.0 (`LICENSE`), which costs
   nothing and so meets D9 ("nothing paid"; ADV-9). Whether core keeps it or is relicensed (e.g. Apache-2.0)
   is an optional reuse decision, wanted only if third parties should reuse core; it is needed before step 4,
   and relicensing requires the copyright holder's consent for all contributions in the rewritten history,
   confirmed over the full, not the shallow, history. [14-risks-open-questions](14-risks-open-questions.md) Q-A.
2. **Public or private core repo.** Decides proxy vs `GOPRIVATE`, npm registry vs token-authenticated
   GitHub Release tarballs with URL specifiers (§12.5 step 6), and the Actions-minutes note in §12.7. Free
   either way (D9), but only the public repo keeps npm installs token-free. 14 Q-B.
3. Thresholds in SC1 (3 checkpoints, 6 weeks) and SC5 (70 %/60 %) are proposals; the owner may tune them
   before S0 wires the readiness report, not after. 14 Q-G.

(Round 1 closed the former item 4: `examples/hello` is a package in the core module, not a third
`go.mod`; §12.3 SC3.)

## Objections to 03

None. One clarification 03 implies but does not state: §3.2 item 2 calls the split "a directory move plus
deleting one `replace` line"; for the Go imports that is exact, but the cutover also moves the npm
packages, CI jobs, Makefile wiring and the toolchain guard (§12.8.2 step 6). None of that touches
application source, so 03's claim about import rewrites stands.

## Out-of-scope defects found

None.

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- MOD-3 — cutover step 7 holds because the genproto exclude lives in `core/go.mod`; step 6 notes there is no root exclude to drop; S7 asserts no root exclude.
- MOD-4 — local `go.work` guidance in §12.6 relies on core's exclude.
- MOD-5 — checkpoints moved from `core-v*` tags to `refs/core-checkpoints/v0.Y.Z`; S1 reads them with `for-each-ref`.
- MOD-6 — history path file generated per file from 04's Split cells; ABO-only files (keyedmutex, telemetry contract suite, ratchet test) excluded; one telemetry target (`core/metrics/`).
- MOD-7 — `examples/hello` is a package in the core module; smoke module uses an unrelated path plus a negative internal-import check; open item 4 closed; S10 drops the hello `go.mod`.
- MOD-10 — S4(a) runs `npm ci` in `core/web` before the Go commands.
- MOD-11 — S4(c) dependency skew check.
- MOD-12 — §12.4.3: `version.Version` source, CI check, readers, `MinCoreVersion` semantics; release step 1 asserts it.
- MOD-13 — S1 filter matches apidiff's real output forms (kernel, examples); fixture test in `core_api_gate.py`.
- MOD-17 — §12.3.11 specifies `core/Makefile` (pin, tools, `ci`, `release`); S10 guard scope extended.
- MOD-18 — every `cd core` in a subshell; S2 uses a negated `grep -q`.
- MOD-19 — apidiff/gorelease and govulncheck from pinned `core/tools/*` modules; no `@latest`.
- MOD-22 — filtered clone drops all tags; only `main` pushed with an explicit refspec; empty remote tag list is a precondition of step 5.
- MOD-23 — "a committed `go.work` is banned; a local one is allowed" (§12.6).
- FE-14 — npm registry with exact versions (tarballs/GitHub Packages only for a private repo); `file:` deps instead of "workspace links"; Dependabot yields two PRs.
- FE-15 — lockstep check `check_mediacore_lockstep.py` named in §12.6 and §12.7.
- SIMP-1 — T starts the deferred builds; they are not readiness criteria.
- SIMP-7 — SDK deprecation policy separated from ABO's compatibility window (09 §9.2.1).
- SIMP-22 — `MinCoreVersion` kept with fixed semantics (§12.4.3).

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`). The round-1
lines above use the former criterion names S1–S10; they are now SC1–SC10.

- MOD2-21, COR2-19 — split criteria renamed SC1–SC10 throughout; "wave" replaced by 09 stages.
- MOD2-8 — `make -C core ci` is the merge gate (checks that hold at every commit, coverage ratchet floor); `make -C core readiness` reports SC1, SC5, SC6, SC9; SC4(b) measures `ci`; "a reported job".
- MOD2-9 — core-owned scripts under `core/scripts/ci/`; root-comparison checks are root-side jobs dropped at cutover step 6; SC4(a) runs `make -C "$TMP/core" ci` in the export.
- MOD2-6 — SC4(b): `core-ci.yml` also runs the root build and `tidy-check` when `core/go.mod`/`go.sum` change.
- MOD2-5 — SC7 points at the redesigned exclude check, which runs in the merge gate.
- MOD2-10 — SC3 measured from S2, when `examples/hello` lands.
- MOD2-12 — §12.8.1: history path file generated from real history at `F` (pure renames ≥ 90 % only; copies and splits keep post-arrival history); filter-repo dry run in the readiness report.
- MOD2-13 — no toolchain literals: commands use `make -s -C core print-toolchain`; `print-toolchain` target added.
- MOD2-14 — §12.4.1: one "core version" PR picks the bump from apidiff; `release-prod.yml` job `core-checkpoint` (`contents: write`) enforces and records the checkpoint.
- MOD2-17, FE2-14 — SC8 and §12.5 step 6 name the npm workspaces in dependency order; hello's web package is private and never packed.
- FE2-13 — §12.4.3: `check_core_version.py` also asserts every `core/web/packages/*/package.json` version equals `version.Version`.
- FE2-22 — §12.5, §12.6, §12.10: private repo → GitHub Release tarballs; GitHub Packages not used.
- COR2-16 — §12.8.1: the `core/internal/keyedmutex` copy keeps only its post-arrival history; ABO's package stays out of core history.

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- MOD3-7 — §12.4.1: `core-checkpoint` is non-blocking (`continue-on-error`, nothing `needs` it); an unbumped constant records nothing and opens a `core-version-bump-needed` issue; SC1 counts recorded checkpoints only; the ABO release never fails because of it.
- MOD3-12 — §12.3.11 `ci`: the exclude check runs in core mode only (plus the `core/` scan); root mode is the root job `genproto-exclude-root`; cutover step 6 names it.
- MOD3-13 — SC4(a): `npm ci` in the export only when `core/web/package.json` exists (from UI-0).
- MOD3-16 — SC4(c) and §12.3: `check_module_skew.py` is also a blocking root `make ci` check from S0.
- MOD3-17 — §12.8.1: move PRs put `git mv` plus import rewrites in a first commit, other rewrites after; `history_paths.py` reports `core/` files with no detected origin.
- MOD3-18 — §12.3 and §12.3.11: `readiness` writes to `$(READINESS_OUT)` (default `core/.ci/readiness/`, gitignored, uploaded as an artifact); a root nightly job folds it into `split-readiness.md` through a PR and is dropped at cutover step 6.
- MOD3-19 — §12.4.3: initial `Version = "0.1.0"` at S1; `check_core_version.py` asserts every in-tree `MinCoreVersion` ≤ `version.Version`.


Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`; R4-1 refines R3-1):

- SIMP4-2 (R4-1) — SC1: the store interfaces of 03 §3.3 are measured SDK; ABO's implementations stay in ABO, so no ABO key name or row format ships at the split; SC2 adds the root-side `TestCoreHoldsNoAppKeyNames` (03 R1(c)).
- MOD4-8, OPS4-7 — SC4(b): `core-ci` is a named required check; it triggers on every PR and skips through a job-level `if:` on a `changes` classifier, so a skip reports success.
- MOD4-13 — §12.4.3: the constant is kept ≥ the newest checkpoint; `check_core_version.py` runs in core `ci` (`version-check`) with core-only checks (checkpoint half "not applicable" in the export); books' `MinCoreVersion` assertion moves to a root-side test in `internal/books`.
- MOD4-14 — §12.3.11 and SC summary: checks hold at every merged PR head; §12.8.1: the rename-only-first-commit rule is dropped; move PRs append `old ==> new` lines to `core/scripts/split/origins.txt`, which `history_paths.py` treats as authoritative (rename detection is a cross-check).

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`; resolutions R5-1…R5-7):

- MOD5-3 — SC summary: the root-side `TestCoreHoldsNoAppKeyNames` reads books' claim generator output through `KeyFamilies()`, ABO's setting keys and op IDs (03 R1(c)).
- MOD5-4 — §12.8.1: core's scheduler is a re-implementation written at T0 while ABO's path lives on to T7, so it keeps only post-arrival history; no copy is recorded as a move.
- MOD5-14 — §12.8.1: one `old-path ==> new-path` separator (04 §2.2, 09 S0, here); `history_paths.py` writes filter-repo's `old==>new`; the example is a verbatim move (`internal/logging` → `core/log`), and a re-implemented file such as `internal/telemetry/telemetry.go` gets no line.
- MOD5-15 — §12.3.11 `ci` target gains `errcheck-ratchet interface-width mocks-check pprof-check web-test` with scripts under `core/scripts/ci/`; SC4(b) uses 09 §9.2.1's trigger list plus the workflow file.
- FE5-15 — §12.8.2 step 6: the Docker frontend stage drops its `core/web` copies and builds, `web-deps` its `core/web` prerequisites.
- FE5-16 — §12.5 step 6, §12.6, §12.10 item 2: the private-repo npm path needs a read token on every machine and tarball URL specifiers the lockstep check parses and Dependabot does not bump; a public repo is the precondition of the token-free path (14 Q-B).

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`; resolutions R6-1…R6-7):

- SIMP6-2 (R6-3) — SC1 definition, apidiff filter, fixture test, SC5 coverage exclusion, §12.4.1 and §12.4.2: `legacy/` is outside the API promise like `kernel/`; a family's legacy-only identifiers are deleted with its cutover in a minor, not deprecated until a major; post-split SC1 and release step 1 run the filtered apidiff pair and override a `gorelease` major caused only by `kernel/` or `legacy/` lines.

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; resolutions R7-1…R7-8):

- MOD7-11 (R7-8) — §12.8.1: the two-files-one-destination example is `progress.go` and `trigger_source.go` → `mediacore/tasks` (`state.go` stays in ABO); the coexistence example adds the executor re-implementation beside opsbridge until T8.
- MOD7-1 (R7-8) — §12.8.1: the registry engine, `childop`, `opsmetrics`, `opsv3` and the state-using `opstest` files are re-implementations and get no `origins.txt` line.
- COR7-5 — SC1 and §12.4.2: a family's `legacy` identifiers are deleted in the first release after its cutover that no longer sets the member, a minor.
- FE7-12 — SC8 and §12.5 step 6 cite 07 §7.3.2's one build-order list (`$WS`) instead of restating it.
- FE7-1 — SC4(a): `core/web/.gitignore` travels with the export and into the split repo's `web/`; the run leaves the tree clean.
- MOD7-6 — §12.3.11 `ci` gains `embed-check` over core's own embed list (no Dockerfile cross-check).
- SIMP7-8, R7-2 — SC1 names `legacy.LibraryRootSource` and `legacy.Maintenance`.
- integration pass (round 7): §12.8.1: the whole `pkg/ops/opstest` package is re-implemented (R-H request).

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`; resolutions R8-1…R8-6):

- R8-6 (MOD8-1) — §12.8.1 "Move PRs record their origins": a move may carry 04 §2.2's enumerated mechanical rewrites (logger cut, store-interface cut) and keeps its `origins.txt` line, so all 21 §8.5 rows keep theirs; any other rewrite makes a re-implementation.
- R8-6 (MOD8-2) — §12.8.1: `childop` and `opsmetrics` stay as opsbridge until their last ABO user is native; `opsmetrics`' successor is in `internal/executor`.
- FE8-4 — SC4(a): `core/.gitignore` (S0) ignores `/.ci/readiness/`, `/hello`, `**/playwright-report/`, `**/blob-report/`; `core/web/.gitignore` adds the two Playwright entries; the export becomes a fresh repository before the run and the check asserts `git status --porcelain` is empty; §12.3.11 `readiness` names the ignoring file.
- integration pass (round 8): §12.8.1 cites 04 §2.2 as the one list of the six mechanical rewrites instead of naming two as if they were the rule (R-H request, consistency with 03 R1(c)); SC4's git work tree and `core/.gitignore` (FE8-4) and the 21 §8.5 rows' `origins.txt` lines were already applied.

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; resolutions A-1…A-17):

- A-2 (ADV-3, ADV-S1) — new §12.4.4: the post-S10 identity, secrets and auth-precedence hardening cutovers, what each changes, what holds until it runs, and that none is a split criterion.
- A-4 (ADV-S3) — §12.8.1: the history path file comes from git's rename detection at `F` (no pathspec); `core/scripts/split/origins.txt` and the move/re-implementation rule deleted; move PRs keep the move in a first, building commit; undetected moves keep post-arrival history only, listed in the readiness report.
- ADV-9 — open item 1: PolyForm Noncommercial meets D9; relicensing is an optional reuse decision, confirmed over the full history.
- integration pass (advisor): §12.4.4 names H1–H3 as 09 §9.13 does.

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; resolutions R9-1…R9-11):

- R9-10 (MOD9-4) — §12.8.1: the path file lists every pre-move path `==>` its `core/` path **before** the `core/` line (filter-repo applies directives in file order; Z10); rename chains mapped transitively; an origin path re-created after its move fails `history_paths.py` and is mapped by a `--commit-callback`; the dry run asserts on the rewritten clone that each moved file's `git log --follow` reaches its pre-move first commit (also cutover step 3); the stale `internal/metrics/metrics.go` example replaced by `internal/telemetry/telemetry.go`. Optional item (f), lowering `-M90%`, not taken: MOD9-5's check reports a file below the threshold instead.
- R9-10 (MOD9-3) — SC2 and §12.3.11: `TestCoreImportsOnlyCore` and `TestCoreHoldsNoAppKeyNames` read the tag union; core's `ci` target gains `vet-darwin` and `vet-integration` (root `make ci` the same).
- MOD9-5 — §12.3.11 and §12.8.1: intermediate commits are built for move PRs by `scripts/ci/check_commits_build.py` in `extraction-gates`, which also reports moved files below the similarity threshold.
- R9-11 (SEC9-3, DATA9-8) — §12.4.4 "Their real reach": every owner whose rows a cutover rewrites (`books`, plus `core`) is raised; the first cutover lifts `{core, 0}`; H1/H2 delete the pre-cutover checkpoint and old Pebble files by forced compaction; archives keep the old representation, stated in done-checks and the release note (rotate secrets, re-issue keys).
- DATA9-8 — §12.4.4 H2 row: the four HEAD-encrypted rows move into `c/secrets/` with the plaintext ones, as 05-config §1.12.5 decides.
- R9-5 (SEC9-2, SEC9-7) — §12.4.4 H3 row: only auth-enable precedence moves; a disabling value and every other `BelowStored` flag keep HEAD's precedence.
- SEC9-8 — §12.4.4 H1 row: non-confined roles and existing keys gain the scopes they held only through `LegacyAmbientScopes`; epochs bumped.
- integration pass (round 9): §12.4.4 H3 row: H3 clears `HEADAuthOffWritable` on `enable_auth` only; the Basic gate keys keep HEAD's auth-off write rule. H2's four encrypted rows moving into `c/secrets/` was already stated.

Round 10 (dispositions in `docs/plans/platform-core/reviews/spec-round-10-dispositions.md`; resolutions R10-1…R10-11):

- R10-9 (MOD10-9) — §12.8.1: the history assertion runs `git log --follow` on the filter-repo output of a real rewrite on a throwaway clone, for the fixed sample `core/scripts/split/history_sample.txt`, compared through the saved commit-maps; `--dry-run` is only the collision probe; re-created origins get a pass 0 with no path filter, so the commit callback sees them before the `core/` filter drops them; §12.8.2 step 3 runs the assertion and states the stop.
- R10-9 (MOD10-8, OPS10-8) — §12.3.11 `ci` target: `vet-darwin` runs without `embed_frontend`; the readiness row names the throwaway-clone rewrite.
- R10-2 (SEC10-1) — §12.4.4 H1 row: ambient scopes go into a key iff its effective pre-Ambient set is not `Confined()`, never from its owner; worker-scoped keys stay confined.
- SEC10-7 — §12.4.4 H1 row and "What is not": sessions, invites and one-time tokens deleted, not rehashed; the epoch clause and "re-issue keys" removed; 09 §9.13 owns the release-note text.
- SEC10-5, SEC10-6 — §12.4.4 H3 row: the Basic gate keys keep HEAD's auth-off rule until the settings cutover, which precedes or ships with H3, and lose it together.
- DATA10-7 — §12.4.4 "Level": the first cutover that raises `core` lifts `{core, 0}`.
- R10-11 (DATA10-6) — §12.4.4 "What is deleted": every pre-cutover checkpoint and old Pebble file, not only the attempt's.
