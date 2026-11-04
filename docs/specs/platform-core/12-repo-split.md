<!-- file: docs/specs/platform-core/12-repo-split.md -->
<!-- version: 1.0.0 -->
<!-- guid: eb47baa1-640b-4c96-96cc-f6b2bdeaa57e -->
<!-- last-edited: 2026-11-04 -->

# 12 — Repository split: criteria, versioning, release, cutover, rollback

Evidence commit: `748fd787a`. Normative inputs: [03-architecture](03-architecture.md) §3.2 (nested
module `github.com/falkcorp/mediacore` at `core/`, `require` + `replace`, no `go.work`) and
[02-decision-record](02-decision-record.md) N1/N2 (split only when the measurable criteria here hold).
Raw data: [`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md),
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md).

## 12.1 Purpose & scope

D3: core starts as an in-repo nested module and moves to its own repository (`falkcorp/mediacore`)
**only on measurable criteria**. This file defines those criteria with the exact command that measures
each, the versioning and deprecation policy before and after the split, the release process, how app
repos pin core, cross-repo CI, history preservation, the cutover runbook, and how to undo it.

Out of scope: what is in core (03 §3.3, [04-package-mapping](04-package-mapping.md)); the in-repo build
wiring (`GO_MODULE_DIRS := . core`, 03 §3.2 item 4 — implementation plan wave 0).

## 12.2 Current state at HEAD

| Fact | Evidence |
|---|---|
| One Go module plus four tool modules; no `replace`, no `go.work` | `go.mod:1` (`module github.com/falkcorp/audiobook-organizer`), `go.mod:3` (`go 1.27.0`); `tools/{deadcode,mockery,staticcheck,tygo}/go.mod`; `raw/15-dependencies.md` "No `replace` or `retract` directives"; `go.work` forbidden by `CLAUDE.md` ("Do NOT run `go work init .`") |
| A public-ish seam already guarded by a tool | `pkg/plugin/sdk` + `tools/cmd/sdkguard` (`raw/14-build-ci.md` §9, §10 item 1) — the precedent for an API gate, but it checks *dependencies*, not API compatibility |
| Releases: git tags, RC on every push to main, manual prod release via reusable workflow; goreleaser for binaries | `raw/14-build-ci.md` §8 (`.github/workflows/prerelease.yml:32-58`, `release-prod.yml:43-65`, `.goreleaser.yml:1-60`) |
| Changelog: scriv fragments, CI-enforced | `changelog.d/scriv.ini:8-21`; `changelog-check.yml` (`raw/14-build-ci.md` §6) |
| Dependency updates: Dependabot for gomod and npm (free, already configured) | `.github/dependabot.yml:59-64` (gomod), `:69-79` (npm, majors blocked) |
| Shared CI backbone in an org repo | reusable workflows from `falkcorp/github-common`, ref pinned in `.github/ghcommon-ref.txt` (`raw/14-build-ci.md` §6, §10 item 4) |
| Toolchain drift guard knows only the root `go.mod` | `scripts/check_toolchain_versions.py:44-46` (tier 3: "`go.mod` — the `go` directive is a minimum") |
| Licence | `LICENSE` is PolyForm Noncommercial 1.0.0 (first line) |

## 12.3 Split trigger and readiness criteria

The split happens when **every readiness criterion S1–S10 holds** *and* the **trigger** T holds.

**T — there is a reason to split.** A second app repository (TV or Movies, D5) is being created, or
the owner asks for it. Splitting with a single consumer adds a release step to every core change and
buys nothing; the criteria below are tracked from wave 0 regardless, so readiness is known when T fires.

Measurements are run from the repo root on a clean checkout of `main`. Scripts named here are plan
deliverables (Python per `CLAUDE.md` "Critical Rules" 4); each prints `PASS`/`FAIL` and the measured
value. Results are recorded in `docs/plans/platform-core/split-readiness.md` with date and commit.

### S1 — Public API stable across 3 consecutive core checkpoints

*Definition.* Zero incompatible changes in SDK packages (03 §3.1: every core package outside
`internal/` and `kernel/`) between checkpoint *n−1* and *n*, for the last three checkpoints, spanning
at least 6 weeks.

*Why not `gorelease` yet.* `gorelease -base=vX` fetches the base version through the module proxy.
Before the split, `github.com/falkcorp/mediacore` has no fetchable versions (the repo does not exist
and the root module reaches core only via `replace`), so `gorelease` cannot run. `apidiff` works on
locally written export data and does not need the proxy. Both tools exist at
`golang.org/x/exp/cmd/{apidiff,gorelease}` (installed and run under go1.27.3 while writing this file,
`x/exp` `v0.0.0-20261014143641-f0251eb4aa3e`).

*Command (pre-split)*, wrapped by `scripts/ci/core_api_gate.py`:

```sh
# checkpoints are tags core-v0.Y.Z in this repo (§12.4.1)
prev=$(git tag -l 'core-v*' --sort=-v:refname | sed -n 2p)
git worktree add --detach "$TMP/core-prev" "$prev"
(cd "$TMP/core-prev/core" && GOTOOLCHAIN=go1.27.3 apidiff -m -w "$TMP/prev.api" github.com/falkcorp/mediacore)
(cd core && GOTOOLCHAIN=go1.27.3 apidiff -m -w "$TMP/cur.api" github.com/falkcorp/mediacore)
apidiff -m -incompatible "$TMP/prev.api" "$TMP/cur.api" \
  | grep -v -E '^(Package )?github\.com/falkcorp/mediacore/(kernel|internal)(/|$)' \
  | tee "$TMP/incompatible.txt"
test ! -s "$TMP/incompatible.txt"
git worktree remove --force "$TMP/core-prev"
```

The script repeats this for the last three checkpoint pairs and checks the date span with
`git log -1 --format=%cs <tag>`. (`apidiff` already skips `internal/` by default; the `kernel/` filter is
what excludes the public-but-unstable kernel, 03 §3.3.)

*Command (post-split, in the core repo):* `gorelease -base=$(git describe --tags --abbrev=0)`.

### S2 — R1 guard green and core requires no app module

```sh
cd core && GOTOOLCHAIN=go1.27.3 go test ./archtest/ -run 'TestNoAppRequire|TestCoreImportsOnlyCore|TestNoGlobals' -count=1
cd core && GOTOOLCHAIN=go1.27.3 go list -m all | grep -c 'falkcorp/audiobook-organizer'   # must print 0
```

### S3 — Second consumer builds against core with nothing but a dependency on it

Two consumers:

1. **`core/examples/hello`** — the dummy second module of 02 §2.4 G6, as its own module
   (`core/examples/hello/go.mod`, module `github.com/falkcorp/mediacore/examples/hello`) so it consumes
   core exactly as an external app does, through `require` + `replace => ../..`. It must use only SDK
   packages (`archtest.NoKernelImports`).
2. **Out-of-tree smoke module**, generated in a temp directory by `scripts/ci/core_consumer_smoke.py`:
   a `go.mod` with one `require github.com/falkcorp/mediacore v0.0.0-…` and one `replace` to the
   checkout's `core/`, plus a copy of `examples/hello`'s `main.go`. It proves that nothing in core
   assumes it is built from inside this repo (relative `go:embed`, `../` paths, root-module tools).

```sh
cd core/examples/hello && GOTOOLCHAIN=go1.27.3 go build ./... && go vet ./... && go test -short ./...
python3 scripts/ci/core_consumer_smoke.py --core core   # builds, vets, tests in $TMP; exit 0
```

"Only a version pin" — the form an app uses after the split — cannot be tested before a version is
published; it is the first verification step of the cutover (§12.8 step 8).

### S4 — Core CI is standalone and green

(a) Core builds from an export containing **only** `core/`:

```sh
git archive HEAD core | tar -x -C "$TMP" && cd "$TMP/core" \
  && GOTOOLCHAIN=go1.27.3 go build ./... && go vet ./... && go test -short -race ./...
```

(b) The core workflow (`.github/workflows/core-ci.yml`, path filter `core/**`, runs only `make -C core ci`)
was green on the last 20 runs on `main`:

```sh
gh run list --workflow core-ci.yml --branch main --limit 20 --json conclusion \
  -q '[.[] | select(.conclusion != "success")] | length'      # must print 0
```

### S5 — Coverage

Total ≥ 70 % and every SDK package ≥ 60 % under `-short` (the root module's floor is 30 %,
`.ci/coverage-floor.txt`, `raw/14-build-ci.md` §2 step 12; a library others build on needs more).

```sh
cd core && GOTOOLCHAIN=go1.27.3 go test -short -coverprofile="$TMP/core.out" ./... \
  && go tool cover -func="$TMP/core.out" | tail -1                     # total
python3 scripts/ci/core_coverage_by_package.py "$TMP/core.out" --min 60 --exclude 'kernel/|internal/|examples/'
```

### S6 — Every core contribution point is documented

Every config key, permission, storage keyspace, task kind, event topic, metric and route that core
registers appears in the generated reference under `core/docs/reference/`. Registration is data
(`module.Registrar`, 03 §3.3), so a generator can boot the `coretest` harness with zero modules and
enumerate it:

```sh
cd core && GOTOOLCHAIN=go1.27.3 go run ./internal/cmd/coredoc -check   # non-zero if docs differ from registrations
```

### S7 — No `replace` except the one to core; core has none

```sh
GOTOOLCHAIN=go1.27.3 go mod edit -json        | python3 -c 'import json,sys;r=json.load(sys.stdin).get("Replace") or [];assert [x["Old"]["Path"] for x in r]==["github.com/falkcorp/mediacore"],r'
cd core && GOTOOLCHAIN=go1.27.3 go mod edit -json | python3 -c 'import json,sys;assert not json.load(sys.stdin).get("Replace")'
```

### S8 — npm packages build and pack standalone

`@mediacore/shell`, `@mediacore/api-client`, `@mediacore/ui-kit` (03 §3.3) pack without the root
workspace, and ABO's `web/` builds against the packed tarballs instead of the workspace link:

```sh
cd core/web && npm ci && npm run build --workspaces && npm pack --workspaces --pack-destination "$TMP/pkgs"
python3 scripts/ci/web_build_against_tarballs.py --tarballs "$TMP/pkgs"   # copies web/, rewrites deps to file:<tgz>, npm ci, npm run build
```

### S9 — No known vulnerabilities in core's graph

```sh
cd core && GOTOOLCHAIN=go1.27.3 go run golang.org/x/vuln/cmd/govulncheck@latest ./...   # exit 0
```

### S10 — Toolchain parity

`core/go.mod`'s `go` directive equals the root's and neither has a `toolchain` line (03 §3.2;
`.standards/instructions/go.md` "Keep `toolchain` at or above the `go` line, or omit it").
Measured by `scripts/check_toolchain_versions.py` once it is extended to read `core/go.mod` and
`core/examples/hello/go.mod` (today it reads only the root, `scripts/check_toolchain_versions.py:44-46`;
see [13-toolchain](13-toolchain.md) §13.2).

Every criterion is also a CI job from the wave that introduces it, so readiness is continuously visible
rather than measured once.

## 12.4 Versioning and semver policy

### 12.4.1 In-repo phase (v0, no published versions)

- The root module requires the zero pseudo-version and `replace`s it (03 §3.2). Nothing outside
  this repo can depend on core.
- **Checkpoints.** Every ABO production release that changed `core/` also creates an annotated tag
  `core-v0.Y.Z` on the same commit. The form is deliberate: Go treats `vX.Y.Z` as a root-module
  version and `core/vX.Y.Z` as a version of a module at path
  `github.com/falkcorp/audiobook-organizer/core`, which does not exist; `core-v0.Y.Z` is neither, so Go
  tooling ignores it while `git tag -l 'core-v*'` drives S1.
- **v0 rules.** Incompatible SDK changes are allowed (v0 makes no promise) but must (a) bump the
  checkpoint minor, (b) carry a changelog fragment in category `Removed` or `Changed` whose first line
  starts with `BREAKING:`, and (c) reset the S1 counter. Additive changes bump the minor; fixes bump
  the patch.
- `kernel/` and `internal/` are outside every promise in every phase (03 §3.3).

### 12.4.2 At and after the split

- **First published version.** If S1 holds (it must, for the split to happen) the first tag in the
  new repo is **`v1.0.0`**. The owner may instead publish `v0.(Y+1).0` first and promote to `v1.0.0` after
  one app release cycle on the published module; either way v1 is reached at or after the split, never
  before (there is nothing to publish before).
- **Semver from v1.** Patch: fixes, no API change. Minor: additive API, new contribution points,
  dependency minor/patch bumps, raising the `go` directive (it forces consumers to a newer Go, so it is
  never a patch). Major: any incompatible SDK change — **and in Go a major ≥ 2 changes the module path**
  to `github.com/falkcorp/mediacore/v2`, so every app import changes. Policy: avoid v2; batch any
  unavoidable breaks into one major, announced one minor ahead.
- **Deprecation.** A deprecated identifier gets a `// Deprecated: use X; removed in vN+1.0.0` doc line
  (picked up by `staticcheck` SA1019 in app CI) and stays for **at least two minor releases and 90 days**,
  matching the compatibility window chosen for ABO's own legacy surfaces (02 §2.5 Q5). Removal therefore
  only happens in a major.
- **Bad releases** are withdrawn with a `retract` directive in the next release's `core/go.mod`, never by
  deleting the tag (the module proxy and checksum database keep it forever).
- **npm lockstep.** `@mediacore/*` packages are versioned in lockstep with the Go module (same `X.Y.Z`),
  because `@mediacore/api-client` is generated from the core OpenAPI document of that exact version
  (03 §3.3). A shell-only fix still bumps the Go module's patch (no-op Go release) to keep one number.

## 12.5 Release process (core repo, after the split)

No goreleaser: core ships no binary. Steps (a `make release VERSION=vX.Y.Z` target in the core repo
runs 1–5; 6–8 are the release workflow):

1. `gorelease -base=<previous tag> -version=vX.Y.Z` — fails if the requested version understates the
   API change (post-split form of S1).
2. `scriv collect --version X.Y.Z` with the same `scriv.ini` shape as this repo
   (`changelog.d/scriv.ini:8-21`: Markdown, Keep-a-Changelog categories) → `CHANGELOG.md`; PR; merge.
3. `git tag -a vX.Y.Z -m "mediacore vX.Y.Z"` on the merge commit (signed if the owner's key is configured);
   `git push origin vX.Y.Z`.
4. Warm and verify the proxy: `GOPROXY=https://proxy.golang.org GOTOOLCHAIN=go1.27.3 go list -m github.com/falkcorp/mediacore@vX.Y.Z`.
   (If the repo is private: apps set `GOPRIVATE=github.com/falkcorp/*` and authenticate with a GitHub
   token; the proxy step is skipped. Both are free.)
5. GitHub release with the changelog section as body (`gh release create vX.Y.Z --notes-file …`).
6. Publish npm packages at `X.Y.Z`: public → npm registry (free for public packages); private → GitHub
   Packages (free within the account's included storage/transfer). `npm publish --workspaces --access …`.
7. Dispatch the downstream matrix (§12.7) against the tag.
8. Dependabot in each app repo opens the bump PR (§12.6).

## 12.6 How app repos pin core

```
require github.com/falkcorp/mediacore vX.Y.Z      // no replace
```

```json
"dependencies": { "@mediacore/shell": "X.Y.Z", "@mediacore/api-client": "X.Y.Z", "@mediacore/ui-kit": "X.Y.Z" }
```

Exact npm versions (no `^`), because the Go side and the npm side must move together. Updates come from
**Dependabot** (already configured here, free): add a group so the Go module and the three npm packages
arrive in one PR:

```yaml
# .github/dependabot.yml (app repo) — additions
  - package-ecosystem: gomod
    directory: /
    groups: { mediacore: { patterns: ["github.com/falkcorp/mediacore"] } }
  - package-ecosystem: npm
    directory: /web
    groups: { mediacore: { patterns: ["@mediacore/*"] } }
```

ABO's existing npm rule blocks all majors (`.github/dependabot.yml:76-79`); that is the desired behaviour
for `@mediacore/*` too (a core major is a planned migration, not a bot PR). Renovate (free GitHub app) is an
acceptable alternative if grouping across ecosystems in one PR is wanted; not required.

A local override for co-developing core and an app is a **temporary** `go mod edit -replace
github.com/falkcorp/mediacore=../mediacore` that CI rejects (S7-style check in every app repo); `go.work`
stays banned (03 §3.2 item 1).

## 12.7 Cross-repo CI

| Where | Job | Trigger | What |
|---|---|---|---|
| core repo | `downstream` (matrix over `falkcorp/audiobook-organizer`, the TV app, the Movies app) | every PR and push to `main` that touches an SDK package | check out the app at `main`; `go mod edit -replace github.com/falkcorp/mediacore=$GITHUB_WORKSPACE/core`; `go build ./... && go vet ./... && go test -short ./...`; build the app's `web/` against the PR's packed `@mediacore/*` tarballs (S8 script). **Required** for merge after v1; before v1 a red downstream is allowed only with the `BREAKING:` changelog fragment and a linked app PR. |
| core repo | `core-ci` | every PR | `make ci` (S2, S4, S5, S6, S7, S9, S10 as jobs) |
| each app repo | `core-main` nightly | cron | `go get github.com/falkcorp/mediacore@main && go build ./... && go test -short ./...`; on failure open (or update) one issue labelled `mediacore-main`. Early warning only; never blocks the app. |
| each app repo | normal CI | PRs | builds against the pinned version only |

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
`core/`; code that moved there from `internal/...` (03 §3.2.1 E2 already moves `internal/telemetry`,
`internal/keyedmutex`) would lose its earlier history. So the filter uses a path file generated from
[04-package-mapping](04-package-mapping.md)'s ABO-origin column, with filter-repo's `==>` rename syntax
mapping each origin to its final core location, followed by the subdirectory step:

```text
# core-history-paths.txt (generated by scripts/split/history_paths.py from 04-package-mapping)
core/
internal/keyedmutex/==>core/internal/keyedmutex/
internal/telemetry/==>core/metrics/otel/
...
```

```sh
git clone --no-local --branch main <abo-remote-url> mediacore-split && cd mediacore-split
git filter-repo --paths-from-file ../core-history-paths.txt
git filter-repo --subdirectory-filter core
git log --oneline | wc -l                           # recorded in split-readiness.md
GOTOOLCHAIN=go1.27.3 go build ./... && go test -short ./...
```

Before anything is pushed: run a secret scanner over the **whole rewritten history** (`gitleaks detect
--log-opts=--all`, free) and the repo's confidentiality scrub over every file at `HEAD`. ABO's pre-commit
hook blocks token files going forward (`scripts/setup-git-hooks.sh`, `CLAUDE.md` "Pre-Commit Hook") but
does not prove the past is clean.

Rewritten commits get new SHAs; spec and CHANGELOG references to ABO SHAs stay valid in ABO, and the core
repo's first commit message records the ABO commit the split was taken from.

### 12.8.2 Cutover runbook

Precondition: T and S1–S10 hold, recorded in `split-readiness.md`.

1. **Freeze** `core/` in ABO: a CI check fails any PR touching `core/` (label `core-freeze`), announced.
2. **Re-measure** S1–S10 on the freeze commit `F`.
3. **Filter** (§12.8.1) from `F`; scan; build; test.
4. **Create** `falkcorp/mediacore` (`gh repo create falkcorp/mediacore --private|--public`), push `main`,
   set branch protection, add `core-ci.yml`, `downstream.yml`, `dependabot.yml`, `CODEOWNERS`, the
   reusable-workflow ref, `LICENSE` (§12.10 item 1).
5. **Tag** the first version (§12.4.2) and run the release process (§12.5) including npm publish.
6. **ABO PR** (one PR, so it reverts as one unit):
   - `go mod edit -dropreplace github.com/falkcorp/mediacore && go get github.com/falkcorp/mediacore@vX.Y.Z && go mod tidy`
   - `git rm -r core/`; `GO_MODULE_DIRS := .` in the Makefile; remove core jobs from CI; drop `core/go.mod`
     from the toolchain guard; `web/package.json` workspace links → exact `@mediacore/*` versions;
     `npm ci --prefix web` to refresh the lockfile.
   - Root `internal/arch` keeps calling `archtest` — now from the versioned module.
   - Add a guard that fails if a `core/` directory reappears.
7. **Verify ABO**: `make ci`, e2e, `binary-smoke.yml`; `go version -m ./audiobook-organizer | grep mediacore`
   shows `vX.Y.Z` with a checksum (no `=>` replacement); `go install github.com/falkcorp/audiobook-organizer@<new tag>`
   from outside the repo works again (the cost accepted in 03 §3.2 item 5 is removed).
8. **Verify "version pin only"** (S3's post-split form): `core/examples/hello` in the new repo drops its
   `replace` in a scratch clone and builds against `vX.Y.Z` from the proxy (or with `GOPRIVATE`).
9. **Unfreeze.** All core changes now go to `falkcorp/mediacore`; ABO picks them up through Dependabot.
10. **Docs**: ABO `CLAUDE.md`, `AGENTS.md`, and this spec's README get a pointer to the new repo.

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
   contributions in the rewritten history. Flagged for [14-risks-open-questions](14-risks-open-questions.md).
2. **Public or private core repo.** Decides proxy vs `GOPRIVATE`, npm registry vs GitHub Packages, and the
   Actions-minutes note in §12.7. Free either way (D9).
3. Thresholds in S1 (3 checkpoints, 6 weeks) and S5 (70 %/60 %) are proposals; the owner may tune them
   before wave 0 wires the jobs, not after.
4. `examples/hello` as a separate module adds a third `go.mod` to the toolchain guard and to
   `GO_MODULE_DIRS`; confirm in the implementation plan.

## Objections to 03

None. One clarification 03 implies but does not state: §3.2 item 2 calls the split "a directory move plus
deleting one `replace` line"; for the Go imports that is exact, but the cutover also moves the npm
workspace, CI jobs, Makefile wiring and the toolchain guard (§12.8.2 step 6). None of that touches
application source, so 03's claim about import rewrites stands.

## Out-of-scope defects found

None.
