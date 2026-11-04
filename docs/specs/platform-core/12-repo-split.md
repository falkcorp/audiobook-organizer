<!-- file: docs/specs/platform-core/12-repo-split.md -->
<!-- version: 1.2.0 -->
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
value. Results are recorded in `docs/plans/platform-core/split-readiness.md` with date and commit.
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
`internal/` and `kernel/`; `examples/` is also excluded) between checkpoint *n−1* and *n*, for the last
three checkpoints, spanning at least 6 weeks.

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
lines for `kernel/` and `examples/` in both forms.

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
    | grep -v -E '^- (\./(kernel|examples)(/[^.: ]+)*\.|package github\.com/falkcorp/mediacore/(kernel|examples)(/|:))' \
    > "$TMP/incompatible.txt" || true          # grep -v exits 1 when nothing is left: the passing case
  git worktree remove --force "$TMP/a"
  git worktree remove --force "$TMP/b"
  test ! -s "$TMP/incompatible.txt"
}
refs=$(git for-each-ref --sort=-v:refname --format='%(refname)' refs/core-checkpoints | head -4)
# pair(n-1, n), pair(n-2, n-1), pair(n-3, n-2), then the date span with git log -1 --format=%cs <ref>
```

`core_api_gate.py` carries a fixture test: a known `kernel/` break must be filtered and a known SDK
break must be reported, so a change in x/exp's output format fails loudly instead of silently passing
or failing every run.

*Command (post-split, in the core repo):* `gorelease -base=$(git describe --tags --abbrev=0)` from the
same pinned tools module.

### SC2 — R1 guard green and core requires no app module

```sh
(cd core && go test ./archtest/ -run 'TestNoAppRequire|TestCoreImportsOnlyCore|TestNoGlobals' -count=1)
! (cd core && go list -m all) | grep -q 'falkcorp/audiobook-organizer'
```

(`grep -c` would print `0` but exit 1 on the passing case; the negated `grep -q` exits 0 exactly when
no line matches.)

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
`go build/vet/test`, so a target that calls a script outside `core/` fails here. `npm ci` runs first, so
the run also proves that `core/go.mod`'s `ignore ./web` keeps npm-shipped `.go` files out of `./...`
(03 §3.2):

```sh
git archive HEAD core | tar -x -C "$TMP"
(cd "$TMP/core/web" && npm ci)
make -C "$TMP/core" ci
```

(b) The core workflow (`.github/workflows/core-ci.yml`, path filter `core/**`, runs `make -C core ci`,
§12.3.11; when `core/go.mod` or `core/go.sum` changes it also runs the root `go build ./...` and
`tidy-check`, because every core dependency change needs the root's `go.mod`/`go.sum` too, 03 §3.2
item 4) was green on the last 20 runs on `main`. This measures the merge gate `ci`, not `readiness`:

```sh
gh run list --workflow core-ci.yml --branch main --limit 20 --json conclusion \
  -q '[.[] | select(.conclusion != "success")] | length'      # must print 0
```

(c) No dependency skew between the binary and core CI (03 §3.2 item 6):

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
python3 core/scripts/ci/core_coverage_by_package.py "$TMP/core.out" --min 60 --exclude 'kernel/|internal/|examples/'
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
`"private": true` and never published (07 §7.3.2):

```sh
WS='-w packages/ui-kit -w packages/api-client -w packages/shell'
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
continuously visible rather than measured once. Only the criteria that must hold at every commit —
SC2, SC7 and SC10 — are also part of the merge gate (`make -C core ci`); SC1, SC5, SC6 and SC9 are
readiness reports (`make -C core readiness`), and SC3, SC4(b), SC4(c) and SC8 run in the root.
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
| `ci` target (merge gate) | `build vet test-short fmt-check fix-check tidy-check staticcheck archtest exclude-check coverage-floor replace-check toolchain-check` — i.e. Go build/vet/short tests with `-race`, `gofmt -l` excluding `web/` and `examples/*/web/`, `go fix -diff ./...` empty (13 §13.3), `go mod tidy -diff`, staticcheck, archtest (R1, R2, R5 and the R4 layer guard: SC2), the genproto exclude check (03 §3.2 item 1), the coverage ratchet floor (SC5), the `replace`/`exclude` check (SC7) and core's own toolchain copies (SC10). Every check here holds at every commit, so a red `ci` always means a defect. SC4 is the workflow running this target. |
| `readiness` target (report-only) | `apidiff-gate coverage coredoc-check vulncheck` — SC1, SC5's thresholds, SC6 and SC9. It never fails a merge; it appends its results, with date and commit, to `docs/plans/platform-core/split-readiness.md` (pre-split: a nightly job). SC3 and SC8 run in the root (pre-split) because they need ABO's `web/` or a temp module; the history dry run (§12.8.1) is also reported here. |
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
  caller workflow, not inside the reusable `falkcorp/github-common` workflow): if
  `git diff <newest checkpoint>..HEAD -- core/` is non-empty it requires `version.Version` to be greater
  than the newest checkpoint (the release fails otherwise), then runs `git update-ref` and pushes the ref
  with the explicit refspec above. The job also runs when `prerelease.yml` auto-promotes an RC (09 §9.1).
- **v0 rules.** Incompatible SDK changes are allowed (v0 makes no promise) but must (a) bump the
  checkpoint minor, (b) carry a changelog fragment in category `Removed` or `Changed` whose first line
  starts with `BREAKING:`, and (c) reset the SC1 counter. Additive changes bump the minor; fixes bump
  the patch.
- `kernel/`, `internal/` and `examples/` are outside every promise in every phase (03 §3.3).

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
- **Kept honest by CI.** Pre-split, `core/scripts/ci/check_core_version.py` asserts that `version.Version`
  is ≥ the newest checkpoint name, and that every `core/web/packages/*/package.json` `version` equals
  `version.Version` (the in-repo `file:` dependencies make the lockstep check of §12.6 a no-op, so this is
  what keeps the shell's reported version, 07 §7.5.2, equal to the Go constant). The `core-checkpoint`
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

## 12.5 Release process (core repo, after the split)

No goreleaser: core ships no binary. Steps (a `make release VERSION=vX.Y.Z` target in the core repo,
§12.3.11, runs 1–5; 6–8 are the release workflow):

1. Assert `version.Version == "X.Y.Z"` (§12.4.3); then `gorelease -base=<previous tag> -version=vX.Y.Z`
   from the pinned tools module — fails if the requested version understates the API change
   (post-split form of SC1).
2. `scriv collect --version X.Y.Z` with the same `scriv.ini` shape as this repo
   (`changelog.d/scriv.ini:8-21`: Markdown, Keep-a-Changelog categories) → `CHANGELOG.md`; PR; merge.
3. `git tag -a vX.Y.Z -m "mediacore vX.Y.Z"` on the merge commit (signed if the owner's key is configured);
   `git push origin vX.Y.Z`.
4. Warm and verify the proxy: `GOPROXY=https://proxy.golang.org GOTOOLCHAIN="$(make -s print-toolchain)" go list -m github.com/falkcorp/mediacore@vX.Y.Z`.
   (If the repo is private: apps set `GOPRIVATE=github.com/falkcorp/*` and authenticate with a GitHub
   token; the proxy step is skipped. Both are free.)
5. GitHub release with the changelog section as body (`gh release create vX.Y.Z --notes-file …`).
6. Publish npm packages at `X.Y.Z`: public repo → the npm registry (free for public packages); private
   repo → GitHub Release tarballs (07 §7.17); GitHub Packages is not used (it needs a token on every
   machine). `npm publish -w packages/ui-kit -w packages/api-client -w packages/shell --access …`
   (explicit order, as in SC8; hello's web package is private).
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
mechanism; GitHub Release tarballs for a private one; GitHub Packages is not used), because the Go side
and the npm side must move together. Before the split ABO's `web/` uses `file:` dependencies on
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
path file with filter-repo's `==>` rename syntax, followed by the subdirectory step.

The path file is generated from **real history at the freeze commit `F`**, not from 04's Split cells.
04's cells cannot express a source file divided between destinations (e.g. `internal/metrics/metrics.go`,
whose `operations_*` series go to `tasks`), give no destination file names (`internal/operations/state.go`
and `internal/operations/state/state.go` both land in `mediacore/tasks`), and cannot express a
copy-then-delete window (05-tasks copies the scheduler at T0 while ABO's `internal/scheduler` lives on to
T7). Mapping two coexisting, diverging files to one path either aborts filter-repo with
`File renaming caused colliding pathnames!` when one commit touches both (round-2 X13), or splices the
ABO copy's later edits into core history. Rule:

- an origin path is mapped **only** for a file whose arrival in `core/` was a **pure rename**: the source
  was deleted in the same commit and `git log --follow`'s similarity is ≥ 90 %;
- a copied or split file keeps only its post-arrival history; the core repo's first commit message
  records its origin path and the ABO commit it was copied from (e.g. the `core/internal/keyedmutex`
  copy, 03 §3.2.1, whose origin stays in ABO);
- files that never arrived in `core/` are not listed, so no ABO-only code (`internal/keyedmutex`, the
  `internal/telemetry/contract/` suite, `internal/telemetry/ratchet_test.go`, `cmd/`) enters core's
  history; core-owned scripts were born under `core/scripts/ci/`, so the `core/` line carries them.

```text
# core-history-paths.txt (generated by core/scripts/split/history_paths.py from `git log -M90% --diff-filter=R` at F)
core/
internal/telemetry/telemetry.go==>core/metrics/telemetry.go
internal/telemetry/resource.go==>core/metrics/resource.go
...
```

A dry run (`git filter-repo --dry-run --paths-from-file …` on a fresh clone of `main`) is part of the
readiness report (§12.3.11), so a collision surfaces long before cutover step 3.

```sh
git clone --no-local --branch main <abo-remote-url> mediacore-split
(cd mediacore-split && git filter-repo --paths-from-file ../core-history-paths.txt)
(cd mediacore-split && git filter-repo --subdirectory-filter core)
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
3. **Filter** (§12.8.1) from `F`; delete tags; scan; build; test.
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
     root-mode genproto probe of 03 §3.2 item 1, the readiness report); drop `core/go.mod`, `core/Makefile` and
     `core/tools/*/go.mod` from the toolchain guard; remove `core/web/**`, `core/go.mod`, `core/go.sum`
     from path filters and `/core` and `/core/web` from Dependabot; `web/package.json` `file:` deps →
     exact `@mediacore/*` versions; `npm ci --prefix web` to refresh the lockfile; the Dockerfiles stop
     copying `core/go.mod`.
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

1. **Licence of the core repo.** History carries PolyForm Noncommercial 1.0.0 (`LICENSE`). That licence is
   source-available, not OSI open source; whether core keeps it or is relicensed (e.g. Apache-2.0/MIT) is
   an owner decision needed before step 4, and relicensing requires the copyright holder's consent for all
   contributions in the rewritten history. Flagged for [14-risks-open-questions](14-risks-open-questions.md) Q-A.
2. **Public or private core repo.** Decides proxy vs `GOPRIVATE`, npm registry vs GitHub Release
   tarballs, and the Actions-minutes note in §12.7. Free either way (D9). 14 Q-B.
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
