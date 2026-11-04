<!-- file: docs/specs/platform-core/13-toolchain.md -->
<!-- version: 1.10.0 -->
<!-- guid: a1e1b657-1c87-4a3e-8407-43269415455f -->
<!-- last-edited: 2026-11-04 -->

# 13 — Toolchain, language idioms, and dependency policy

Evidence commit: `748fd787a`. Versions re-checked on **2026-11-04** (this file) against the Go module
proxy and the npm registry; the earlier snapshot is [`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md)
(2026-11-03). Build/CI facts: [`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md).
Org rules: `.standards/instructions/go.md` v1.4.0 (submodule commit `16be0c4`), cited as `go.md:<line>`.
Decision: D8 / 02 N11 (latest stable Go and frontend tooling; exceptions recorded here), D9 (nothing paid).

## 13.1 Purpose & scope

Fixes (a) the Go toolchain core and ABO build with and what adopting it costs, (b) the Go idioms core
code is **required** to use and where each is used, (c) frontend versions for `core/web` and ABO's `web/`
with every exception and its exit condition, (d) which third-party Go dependencies core drops in favour
of the standard library and which it keeps, and (e) a release-age policy so "latest" never means
"published yesterday". Risks found are collected in §13.7 for [14-risks-open-questions](14-risks-open-questions.md).

This file is the **single owner** of the release-age rule (§13.6) and of version targets (§13.4 for the
frontend, §13.5 for Go dependencies). Other files, including [07-ui-shell](07-ui-shell.md) §7.3.3, refer
to these tables instead of restating versions, so a target cannot violate the rule in one file while
obeying it in another.

## 13.2 Go toolchain

### 13.2.1 Target: go1.27.3

| Check (2026-11-04) | Result |
|---|---|
| `GOTOOLCHAIN=go1.27.3 go version` | `go version go1.27.3 linux/amd64` |
| Proxy toolchain index `https://proxy.golang.org/golang.org/toolchain/@v/list` | go1.27 entries: `go1.27rc1..rc3`, `go1.27.0`, `.1`, `.2`, `.3`; **no go1.27.4, no go1.28 pre-release** |
| `…/@v/v0.0.1-go1.27.3.linux-amd64.info` | `"Time":"2026-10-19T22:20:36Z"` (16 days before this check) |
| `…/@v/v0.0.1-go1.27.2.linux-amd64.info` | `"Time":"2026-10-02T20:28:03Z"` (the current pin) |
| `…go1.27.4…info`, `…go1.28rc1…info` | HTTP 404 |
| `go.dev/dl/?mode=json` (the `stable` flag) | unreachable from the authoring sandbox (also in `raw/15-dependencies.md` "Go toolchain"); "latest stable" is therefore "newest go1.27.x on the proxy" |
| **HEAD builds and vets on go1.27.3** | `GOTOOLCHAIN=go1.27.3 go build ./... && go vet ./...` at HEAD: both exit 0 (4 m 59 s wall). |
| HEAD short tests on go1.27.3 | exit 0: 183 packages ok, 0 failed (§13.2.4) |

`go.md:23-32` makes the probe "a build, not a guess" and requires the test step because `go test` runs the
`stdversion` vet check. A patch bump within 1.27 changes no `go` directive, so `stdversion` cannot newly
fail; the probe is still run as a precondition of the bump PR.

**Directive policy.** Root `go.mod` stays `go 1.27.0` with no `toolchain` line (`go.mod:3`); `core/go.mod`
uses the same directive and also no `toolchain` line (03 §3.2); the pin lives in `GOTOOLCHAIN`
(`go.md:34-56`). The `go` directive moves only on a Go minor (1.28), never on a patch.

### 13.2.2 What adopting go1.27.3 costs: every copy of the pin

The pin `go1.27.2` appears in **34 machine-checked places** plus **9 prose copies** and **3 image
digests** that must be re-resolved. Counted with `git grep -n -E '1\.27\.2'` excluding `docs/` and
`CHANGELOG.md`, then classified by hand:

| Group | Sites | Anchors | Guarded by `check_toolchain_versions.py` tier |
|---|---|---|---|
| Canonical pin | 1 | `Makefile:43` (`export GOTOOLCHAIN := go1.27.2`) | source of truth |
| direnv | 1 | `.envrc:10` | 1 (exact) |
| Editor | 2 | `.vscode/settings.json:7,10` | 1 |
| Dockerfiles (tag **and** digest) | 2 | `Dockerfile:27`, `Dockerfile.build-cgo:23` (`golang:1.27.2-alpine@sha256:f92b6e…`) | 1 + digest equality |
| GitHub Actions `go-version` | 17 | `ci.yml:129,211,277,328,436,531,690,790,876`; `binary-smoke.yml:33`; `codeql.yml:42`; `e2e.yml:154`; `frontend-ci.yml:66`; `nightly.yml:33`; `ratchet-lower.yml:47`; `security.yml:54`; `vulnerability-scan.yml:28` | 1 (full patch required since 2026-10-10, `scripts/check_toolchain_versions.py:37-40`) |
| Woodpecker `GOTOOLCHAIN` | 9 | `.woodpecker/checks-build.yaml:44`, `checks-lint.yaml:52`, `test-database.yaml:47`, `test-fixtures-{1,2,3}.yaml:61`, `test-native-taglib.yaml:58`, `test-rest.yaml:51`, `test-server-scanner.yaml:44` | 1 |
| Woodpecker image (tag **and** digest) | 1 | `.woodpecker/checks-lint.yaml:50` (`golang:1.27.2-bookworm@sha256:5cf287…`) | 1 + single-digest rule |
| Remote CI script | 1 | `scripts/ci_remote.py:77` | 1 |
| **Machine-checked total** | **34** | | |
| Prose that names the pin | 9 | `Dockerfile:25` and `Dockerfile.build-cgo:21` (the comments "SHA pinned … (golang:1.27.2-alpine manifest-list)" directly above the pinned `FROM` lines, updated with the digest; MOD8-12), `CLAUDE.md:180`, `agents/go-specialist.md:15`, `skills/project-context/SKILL.md:52`, `Makefile.local.example:21`, `.github/codeql/README.md:127,142`, `.woodpecker/checks-build.yaml:21` (comment) | not checked — stale prose is how a wrong pin gets copied |
| Not a pin (leave alone) | — | historical comments that record go1.27.2 *behaviour*: `Makefile:463,507`, `ci.yml:525`, `checks-lint.yaml:73`, `internal/diag/flight/flight.go:628`, `flight_test.go:494`, `internal/pathutil/protected.go:52`, `scripts/check-errcheck-ratchet.sh:121`, `scripts/check-interface-width.sh:128`, `scripts/setup-mockery.sh:11`; file-header versions `1.27.2` in `internal/audiobooks/audiobook_service_unit_test.go:2` and `internal/config/persistence_test.go:2`; the guard's own history and example text, `scripts/check_toolchain_versions.py:39` and `scripts/tests/test_check_toolchain_versions.py:31` (MOD8-12) | — |

Unchanged by a patch bump: `go.mod:3` (`go 1.27.0`, tier 3), `tools/*/go.mod:8` (`go 1.27.0`, **not
guarded today**: the script reads only the root `go.mod`, `scripts/check_toolchain_versions.py:329-351`),
and `.github/repository-config.yml:44` / `.github/workflow-config.yaml:11,44,52` (`'1.27'`, tier 2).

**The guard.** `scripts/check_toolchain_versions.py` (476 lines, v1.3.1) enforces three tiers —
exact patch for every build path, major.minor for `repository-config.yml`, minimum for `go.mod`
(`scripts/check_toolchain_versions.py:14-46`) — and is run by `test-action-integration.yml`
(`raw/14-build-ci.md` §5). Required changes for core:

1. Tier 3 must also read `core/go.mod`, `core/tools/*/go.mod` and the existing `tools/*/go.mod`
   ([12-repo-split](12-repo-split.md) SC10). Today it reads only the root `go.mod`. There is no
   `core/examples/hello/go.mod`: `examples/hello` is a package in the core module (03 §3.3, 12 SC3).
2. Tier 1 must read the `export GOTOOLCHAIN :=` pin in `core/Makefile` (12 §12.3.11) and require it to
   equal the root `Makefile:43` pin. That file is the 35th machine-checked copy; it exists because core
   CI must run from an export of `core/` alone (12 SC4) and the core repo has nothing else after the split.
   The parity rule (core pin = root pin) is a root-side check and is dropped at cutover step 6; inside
   core, `core/scripts/ci/check_core_toolchain.py` checks only the copies core carries (12 §12.3.11).
3. Core's own CI workflow (`core-ci.yml`) adds its `go-version` lines to tier 1 automatically (the
   script already scans every workflow).
4. **No literals outside the guarded copies.** Commands and scripts never repeat the pin: 12's
   measurement and release commands take it from `make -s -C core print-toolchain` (12 §12.3), and the
   guard gains a check that fails on a `go1.N.N` literal in `scripts/**`, `core/scripts/**` and
   workflow `run:` steps (comments that record past behaviour, listed under "Not a pin" above, are
   allowlisted by path and line; the core copies of `scripts/check-errcheck-ratchet.sh` and
   `scripts/check-interface-width.sh` that core's own `ci` target runs from `core/scripts/ci/`
   (12 §12.3.11, MOD5-15) carry the same comments and are allowlisted the same way). Without it, every command copied from 12 into a script would be an
   unguarded 36th copy.

**Who performs the bump, and when.** The bump to go1.27.3 is one PR, `toolchain: go1.27.3`, which is the
**first PR of 09 stage S0**, before `core/` exists: `core/Makefile` is then born with the bumped pin, and
12's commands never meet two pins. It carries every change in this section (the 34 copies, the prose
copies, the image digests, the two stale Dependabot rules) and runs the preconditions of §13.2.4.

**Two stale Dependabot rules must be fixed in the same bump PR**, or Dependabot keeps refusing exactly the
updates D8 asks for:

- `.github/dependabot.yml:30-34` ignores `golang` image versions `>=1.27` with the comment "Go 1.27 does
  not compile this repository" — false since the 2026-09-01 move to 1.27 (`go.md:119-127`) and false
  today (§13.2.1 build). As written it blocks every `golang:1.27.x` digest/patch update.
- `.github/dependabot.yml:80-89` blocks `vite >=8.0.0` and `@vitejs/plugin-react >=5.0.0` "with this
  React 18 + MUI v5 + emotion app" — the app is on React 19.3.1, MUI 9.5.0, Vite 8.3.4 and plugin-react 6.1.0
  (`raw/15-dependencies.md` npm table). These `versions` ranges also block **security** updates within
  Vite 8 (the comment says so: "They ALSO block dependabot SECURITY updates"). This is a stale
  justification of the kind `CLAUDE.md` "Fix It Right" warns about.

Image digests for `golang:1.27.3-alpine` and `golang:1.27.3-bookworm` must be resolved at bump time
(`docker buildx imagetools inspect golang:1.27.3-alpine`); they could not be fetched from the sandbox.

### 13.2.3 Tool compatibility

`Makefile:463,507` and `.woodpecker/checks-lint.yaml:73` record that mockery v3.8.0's bundled x/tools and
golangci-lint < v2.14.0 could not read go1.27.2 export data (version 5). A patch release does not change the
export-data format, so mockery (built with raised x/tools, `tools/mockery/go.mod`), staticcheck
(`tools/staticcheck`) and golangci-lint v2.14.0 are expected to work unchanged; the bump PR runs
`make ci` to prove it rather than assuming it.

### 13.2.4 Short-test probe on go1.27.3

`GOTOOLCHAIN=go1.27.3 go test -short -count=1 -timeout 25m ./...` at HEAD (no `-race`, no fixtures):
**exit 0 — 183 packages `ok`, 0 `FAIL`, 30 without test files.** Together with §13.2.1 this is the full
`go.md:27` probe (build + vet + short tests) passing on the target toolchain. The `-race` run, fixture
tests and `make ci` remain preconditions of the bump PR itself.

## 13.3 Go idioms required in core

Core is new code; `go.md:239-336` and `go.md:426-600` already say these are the current idioms. The table
makes each one a **requirement** for core with the place it is used, and gives the HEAD baseline (files,
including tests, matched by `git grep -l` at HEAD) so reviewers know what ABO code moving into core must be
rewritten to.

| Idiom (Go version) | Requirement in core | Where core uses it | HEAD baseline |
|---|---|---|---|
| Generics | Contracts that differ only by payload type are generic, not `any` | `integrations/metadata.Provider[Q, R]` (03 §3.3); `acquire.Engine[T]`, `Spec[T]`, `Candidate[T]` ([10](10-tv-movies-proof.md) §10.5.3); `tasks.RunItems[T]`; typed storage codecs | ABO `RunItems[T]` (`raw/06-tasks-ops.md` §2) |
| Generic methods (1.27) | Allowed on concrete types; not usable to satisfy interfaces (`go.md:318-321`) | `storage.Keyspace` typed helpers | — |
| `iter.Seq` / `iter.Seq2`, range-over-func | Every scan or stream returns an iterator, never a full slice of an unbounded collection | `storage` scans (03 §3.3 "`iter.Seq2` scans"); `acquire.Formats.Match`, `Engine.Decide`, `Service.History/Queue`; `library` walks; `audit` reads | 0 files use `iter.Seq`; 2 hand-written `func(yield …)` |
| `log/slog` | The only logger; `*slog.Logger` passed in, never `slog.SetDefault` inside core (R5; `go.md:362-369`) | `log` package builds per-module child loggers; `slog.NewMultiHandler` fans out to stderr + the UI log ring | 590 files import `log/slog`; 2 still call `log.Printf` |
| `errors.Join`, `errors.AsType[T]` | Aggregate independent failures; typed matching without a target variable (`go.md:380-395`) | kernel boot validation reports *all* manifest collisions at once; shutdown joins component errors; adapters classify provider errors with `AsType` | 78 / 80 files |
| `context.AfterFunc` | Tie non-context resources to a context instead of a watcher goroutine | close in-flight provider/client response bodies on task cancel (`acquire`, `providerhttp`); unsubscribe realtime streams on request end | 4 files |
| `context.WithoutCancel` | Finishing writes that must outlive a cancelled caller | audit append and outbox commit after the request context ends; terminal task state persistence ([10](10-tv-movies-proof.md) §10.5.6) | 16 files |
| `os.Root` (1.24) | All library file access under a configured root goes through `os.Root`; path-prefix checks are not accepted (`go.md:436`) | `library` (03 §3.3: "`os.Root`-confined filesystem access"); backup staging dirs | 2 files (`internal/metadata/cover.go:126`, `internal/diag/flight/flight.go:601`) |
| `sync.WaitGroup.Go` (1.25) | The only way to start a joined goroutine; the four legitimate `Add` shapes of `go.md:581-596` need a comment naming the shape | kernel supervisor, executor workers, realtime hub, outbox relay | 128 files use `wg.Go`, 64 still `wg.Add(1)` |
| `testing/synctest` + `httptest.NewTestServer` (1.27) | Any test that waits on time uses a bubble, never `time.Sleep` (`go.md:441-470`, `go.md:328-330`) | scheduler, task timeouts/yield, providerhttp throttle and breaker, delay profiles, tracked-download polling, SSE heartbeats | 2 files (`internal/backup/backup_test.go:17`, `internal/realtime/events_test.go:15`) |
| `t.Context()`, `t.Chdir`, `t.ArtifactDir` | Test isolation helpers (`go.md:426-439`; `t.Context` is cancelled before `Cleanup`) | all core tests | 93 files use `t.Context()` |
| `b.Loop()` | Every benchmark (`go.md:438`) | `acquire` decide, storage scan, rbac check benchmarks | 16 files `b.Loop`, 18 still `i < b.N` |
| `encoding/json/v2` + `jsontext` (GA in 1.27, `go.md:206-237`) | New core wire types and stored values use v2 with `omitzero` for scalars; v1 `encoding/json` only where a legacy ABO wire shape must be byte-compatible (09 compatibility windows) | `httpapi` envelopes, `config` file decoding, `storage` value codecs for new `c/` keyspaces, `acquire.ConditionSpec.Params` (`jsontext.Value`) | 15 files v2, 1 384 files v1 |
| `unique` | Interning of small, hot, repeated strings compared by identity | `rbac` permission scopes and `entity.Ref` module/kind strings on the authorization hot path | 0 files |
| `weak` + `runtime.AddCleanup` (1.24) | Only for caches whose entries may be dropped under memory pressure; never for correctness; `runtime.SetFinalizer` banned | compiled custom-format regex cache in `acquire` (shared across profiles; entries collectable when no snapshot references them) | 0 files |
| `net/http` ServeMux patterns (1.22) + `http.CrossOriginProtection` (1.25) + `MaxHeaderValueCount` (1.27) | Core HTTP is stdlib (02 N7); method+wildcard patterns; CSRF via `CrossOriginProtection` as its own stage of the fixed middleware chain ([05-http-api](05-http-api.md) §4.2, which owns the stage); every `http.Server` sets the timeout trio and `MaxHeaderValueCount` (`go.md:331-333`) | `httpapi`, `realtime`, `ui` | 13 files `http.NewServeMux`; 446 files import gin; 0 `CrossOriginProtection` |
| stdlib `uuid` (1.27) | New random identifiers (`uuid.NewV7()`/`NewV4()`); ULIDs (`oklog/ulid`) stay where time-sortability matters (`go.md:325-327`) | request/correlation IDs, API-key IDs | 0 files (`github.com/google/uuid` only indirect, `go.mod:108`) |
| `new(expr)` (1.26), `min`/`max`, `range n`, `slices`/`maps` | `go.md:300-317` | everywhere | — |
| `go vet` (with `stdversion` in `go test`) and `go fix` modernizers | Core CI runs `go vet ./...` and **`go fix -diff ./...` must be empty**. `go vet` also runs, as a required PR job over **both** modules, for `GOOS=darwin CGO_ENABLED=0 -tags embed_frontend` (goreleaser's darwin set, `.goreleaser.yml:23-36`) and with `-tags integration` (`vet-darwin` and `vet-integration` in root `make ci` and in core's `ci` target, 12 §12.3.11): at the evidence commit no PR job compiled either set (`Makefile:362-369`'s `vet-integration` is called by neither `make ci`, `Makefile:785`, nor a workflow), so a darwin twin left behind by a move or an `integration` test broken by one surfaced only at release (03 §3.5.1 item 4; R9-10, MOD9-3) (`-diff` exits non-zero on any pending fix; verified in `go help fix` on go1.27.3). The 26 analyzers on go1.27.3 (`go tool fix help`): `any atomictypes buildtag embedlit errorsastype forvar hostport inline mapsloop minmax newexpr omitzero plusbuild rangeint reflecttypefor slicesbackward slicescontains slicessort stditerators stringsbuilder stringscut stringscutprefix stringsseq testingcontext unsafefuncs waitgroupgo` — identical to `go.md:263-268`. When ABO code moves into core, the per-class review rules of `go.md:275-287` apply to the resulting hunks, and the move PR applies `go fix` to the files it moves in a commit after its move commit, so the move commit changes only what it needs to build and git's rename detection still pairs each moved file with its source, which is where the split's history comes from (12 §12.8.1, A-4); every commit of the PR builds and no rename-only commit lands on `main`, which `scripts/ci/check_commits_build.py` enforces for every PR that renames a file into `core/`, reporting any moved file whose first-commit similarity falls below 90 % (12 §12.3.11; MOD9-5). | `core-ci.yml` | ABO does not gate on `go fix`, and gains no such gate (03 §3.2 item 4): at HEAD `go fix -diff ./...` in the root exits 1 with pending fixes in 245 files (round-3 X20), so a root gate would be red from S0 |

## 13.4 Frontend versions (`core/web` workspace and ABO `web/`)

Checked with `npm view <pkg> version` and `npm view <pkg> time` on **2026-11-04**. This table is the
single source of frontend version targets; 07 §7.3.3 points here (FE-21). A target in the "Core `core/web` target" column obeys §13.6 on the day it is adopted, not on the
day it is written down.

| Package | ABO installed (`raw/15`) | Latest | Published | Core `core/web` target | Note |
|---|---|---|---|---|---|
| react / react-dom | 19.3.1 | 19.3.1 | 2026-10-14 | `^19.3.1` peer | |
| @mui/material / icons-material | 9.5.0 | 9.5.0 | 2026-10-09 | `^9.5.0` peer of `@mediacore/ui-kit` **and** `@mediacore/shell` | 23 of the 35 shell- and ui-kit-bound files import `@mui/icons-material` (07 §7.11 move list, `web/src/components/layout/TopBar.tsx` and others @748fd787a); apps already depend on it (`web/package.json:29`); contributed nav icons are typed `ComponentType<SvgIconProps>` (07 §7.3.1; FE7-11) |
| @emotion/react / styled | 11.14.0 / 11.14.1 | same | 2024-12 / 2025 | peer via MUI | |
| vite | 8.3.4 | 8.3.5 | 2026-10-21 | `^8.3.5` | |
| react-router | — (ABO uses `react-router-dom` 7.18.5) | **8.5.1** (dist-tag `latest`; `version-7` = 7.18.5) | 8.0.0: 2026-06-16; 8.5.1: 2026-10-27 | **`react-router@^8.5.1` peer** | see exception/migration E4 |
| react-router-dom | 7.18.5 | 7.18.5 (no v8: package removed) | 2026-10-27 | not used | |
| zustand | 5.0.16 | 5.0.16 | 2026-10-22 | `^5.0.16` | |
| typescript (TS 7 native, alias `@typescript/native`) | 7.0.2 | 7.0.2 (`next` 7.1.0-dev) | 2026-07-08 | type-check with TS 7 | E1 |
| typescript (JS API, alias `@typescript/typescript6`) | 6.0.2 | 6.0.2 | 2026-07-06 | kept for tooling | E1 |
| @babel/core | 7.29.7 | **8.1.0** | **2026-11-02** | 7.29.x | E2 |
| vitest / @vitest/coverage-v8 | 5.0.5 | 5.1.0 | **2026-11-02** | `^5.0.5` until soak ends | release-age rule §13.6 |
| @vitejs/plugin-react | 6.1.0 | 6.2.1 | **2026-11-02** | `^6.1.0` until soak ends | release-age rule |
| eslint | 10.14.0 | 10.14.0 | 2026-10-30 | same | |
| typescript-eslint | 8.72.1 | 8.72.1 (peer `typescript >=4.8.4 <6.1.0`) | 2026-10-26 | same | why E1 exists |
| @playwright/test | 1.64.1 | 1.64.1 | 2026-10-14 | same | |
| jsdom | 30.0.1 (exact pin) | 30.1.3 | 2026-10-15 | 30.0.1 | E3 |
| globals | 17.13.0 | 17.14.0 | **2026-11-01** | after soak | |
| knip | 6.44.1 | 6.44.1 | 2026-10-31 | same | |
| prettier | 3.9.10 | 3.9.10 | 2026-10-10 | same | |
| babel-plugin-react-compiler | 1.0.0 | 1.0.0 (dep `@babel/types ^7.26.0`) | 2025-10-07 | same | E2 |
| @rolldown/plugin-babel | 0.3.0 | 0.3.0 (peer `@babel/core ^7.29.0 \|\| ^8.0.0-rc.1`) | 2026-10-29 | same | |
| esbuild | 0.28.2 (exact) | 0.28.2 | 2026-08-08 | same | |
| openapi-typescript | — (not used) | 7.13.0 (peer `typescript ^5.x`) | 2026-02-11 | `7.13.0` exact, in every package that runs the generator ([05-http-api](05-http-api.md) §7) | runs on the TS 6 alias through an `overrides` entry (E1; [07-ui-shell](07-ui-shell.md) §7.3.3) |
| openapi-fetch | — (not used) | 0.17.0 (no peers) | 2026-02-11 | `0.17.0` exact (pre-1.0, 05-http-api §7) | |
| Node | 26 (`.github/repository-config.yml:19,45`) | — | — | 26 | react-router 8 needs ≥ 22.22.0; Babel 8 needs `^22.18.0 \|\| >=24.11.0` |

The assignment brief named "react-router 7"; the registry shows v8 is the stable major (five months, eleven
releases). Core targets v8.

### 13.4.1 Exceptions (each with reason and exit condition)

| # | Exception | Reason (verified) | Exit condition |
|---|---|---|---|
| E1 | Two TypeScripts: TS 7.0.2 type-checks (`"@typescript/native": "npm:typescript@^7.0.2"`, `web/package.json:53`, scripts `:8-9`), while the `typescript` name resolves to TS 6 (`"npm:@typescript/typescript6@^6.0.2"`, `web/package.json:66`) | TS 7 ships no JS API, and `typescript-eslint` 8.72.1 peers `typescript >=4.8.4 <6.1.0` (`npm view`); rationale recorded in `CHANGELOG.md:2909-2924` | `typescript-eslint` declares TS 7 support → drop the alias, single `typescript@^7`. `core/web` copies the same two-alias layout until then. Every package that runs `openapi-typescript` (peer `typescript ^5.x`; it crashes on TS 7, which has no JS API) carries `"overrides": {"openapi-typescript": {"typescript": "$typescript"}}`, so the generator resolves the TS 6 alias (07 §7.3.3, FE-20). (knip is not a blocker: knip 6.44.1 depends on `oxc-parser`/`oxc-resolver` and declares no `typescript` dependency or peer, `npm view knip@6.44.1 dependencies`, checked 2026-11-04.) |
| E2 | Stay on `@babel/core` 7.29.x | 8.1.0 is two days old (release-age rule); `babel-plugin-react-compiler` 1.0.0 depends on `@babel/types ^7.26.0` (`npm view`), so Babel 8 core would run a compiler plugin built against Babel 7 AST types; the compiler path is the only Babel use (`web/vite.config.ts:8-21`) | react-compiler publishes Babel 8 support **and** 8.x has passed the 14-day soak → bump, run the React Compiler memo tests (`CHANGELOG.md` records them, e.g. `DupesSpine.memo.test.tsx`). |
| E3 | `jsdom` pinned at 30.0.1 | 30.1.0 broke MUI FocusTrap unmount ("5 failed / 7 passed" vs 12/12), `.github/dependabot.yml:90-104` | the 12/12 check in that comment passes on the newest 30.1.x (30.1.3 untested here). |
| E4 | ABO stays on `react-router-dom` 7.18.5 until its own react-router 8 migration | v8 removed `react-router-dom` (react-router 8.0.0 changelog: "Remove `react-router-dom` package", #15076; DOM bits from `react-router/dom`, everything else from `react-router`); ABO imports a react-router package in **122** files, **52 source and 70 test** (the test count includes the helper `web/src/test/renderWithProviders.tsx`), counted with 07 §7.3.3's grep `git grep -lE "from ['\"]react-router(-dom)?['\"]" 748fd787a -- web` (it also covers `web/tests/` and `web/vite.config.ts`); `@mediacore/shell` needs v8 as a peer, so the migration must be **finished before** ABO adopts the shell | A separate ABO PR series migrates to `react-router@8`: 09 stage **UI-0** swaps the imports and the chunk regex (`web/vite.config.ts:81-82`) and keeps the declarative `<BrowserRouter>`; the data router and the test-harness rewrite follow at shell adoption (07 §7.19 steps 4–5). UI-0 ships before S4 and **precedes** shell adoption ([07-ui-shell](07-ui-shell.md) §7.19 step 1, [09-abo-on-core](09-abo-on-core.md) §9.3). Exit condition: `! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts`, reached **before** ABO's first `createShell` call (07 §7.19 step 1). |

## 13.5 Go dependencies: what core drops and keeps

Latest versions from the proxy `@latest` endpoint on 2026-11-04 (same values as `raw/15-dependencies.md`
except where noted).

### 13.5.1 Dropped in core (ABO may keep them until its own migration)

| Dependency (latest) | ABO use at HEAD | Core replacement | Why |
|---|---|---|---|
| `github.com/gin-gonic/gin` v1.12.0 | 446 files; router built at `internal/server/server.go:638` (`gin.New()`) | `net/http` ServeMux patterns in `httpapi` (02 N7) | Stdlib has method+wildcard routing since 1.22; one fewer framework in every app's API; gin's context type would leak into every handler signature of the SDK. ABO keeps gin: its router stays mounted through `httpapi.MountLegacy` **permanently** (02 N7); a books route group moves to native `httpapi` only when it is rewritten for its own reasons. Core never imports gin. |
| `github.com/gin-contrib/gzip` v1.2.9 | `internal/server/server.go` | `compress/gzip` middleware in `httpapi` | Trivial with stdlib. ABO keeps it as long as it keeps gin. |
| `otelgin` v0.72.0 | 1 file | `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` **v0.72.0** (2026-10-02) | Follows the router: core uses `otelhttp`; ABO's gin engine keeps `otelgin` while it exists. **One emitter per family (R9-3, MOD9-2):** HEAD's `http_server_*` families, whose `http_route` label is gin's `c.FullPath()` (`internal/telemetry/contract/testdata/series.golden:85-87`), stay otelgin's for every request served through `MountLegacy`; core's `otelhttp` stage records only spans for those requests and records `http_server_*` metrics for native and `Infra` routes alone, so no request is counted twice into one family and the legacy route label values keep HEAD's series identity, which ABO's HEAD golden checks (05-http-api §4.2, 05-metrics §13 own the stage). |
| `github.com/spf13/viper` v1.21.0 (last release **2025-09-08**) | 31 files | `core/config` layered loader (defaults → file → env → flags → DB overrides, 03 §3.3) decoding into typed per-module sections | Viper's package-level default instance is a process-global mutable registry (R5); per-module typed sections and a UI schema are the core requirement, not key-string lookup. |
| `github.com/go-viper/mapstructure/v2` v2.5.0 | `internal/config/viper_decode.go` | `encoding/json/v2` decode into typed sections | Goes with viper. |
| `github.com/go-playground/validator/v10` v10.30.6 | 1 file (`internal/server/credential_routes.go`) | typed decode + explicit `Validate() error` methods; errors map to the `httpapi` envelope | Struct-tag validation is reflection-heavy and opaque for one call site. |
| `github.com/spf13/cobra` / `pflag` v1.10.2 / v1.0.10 | 9 files (CLI) | none in core; `app.Options` is a struct; apps choose their CLI | A library should not own the process's command line. |
| `gopkg.in/yaml.v3` v3.0.1 (last release **2022-05-27**) | `internal/config/viper_decode.go`, `persistence.go` | `go.yaml.in/yaml/v3` **v3.0.6** (2026-10-13), the maintained continuation by the YAML organisation — only if core config accepts YAML files | Unmaintained import path; `go.yaml.in/yaml/v3` is already in ABO's graph as indirect v3.0.5 (`go.mod:151`). `go.yaml.in/yaml/v4` is `v4.0.0-rc.9` (2026-11-02): **not** stable. |
| `github.com/golang-jwt/jwt/v5` v5.3.1 | ABS token only (`internal/server/absauth/token.go`) | not needed in core: sessions/API keys are opaque tokens; OIDC verification is go-oidc's job ([05-authn](05-authn.md)) | ABS compatibility stays in the `books` module. |
| `github.com/hashicorp/go-memdb` v1.3.5 | 6 files in `internal/database` | not in core ([05-storage](05-storage.md): Pebble + bleve) | ABO in-memory index detail. |
| `github.com/google/uuid` (indirect v1.6.0) | indirect only | stdlib `uuid` | `go.md:325-327`. |

### 13.5.2 Kept in core

| Dependency | Version (latest = current unless noted) | Published | Core package | Note |
|---|---|---|---|---|
| `github.com/cockroachdb/pebble/v2` | v2.1.7 | 2026-08-24 | `internal/pebble` behind `storage` | the release that unblocked Go 1.27 (`go.md:119-127`) |
| `github.com/blevesearch/bleve/v2` | **v2.6.2** (ABO v2.6.1) | **2026-11-03** | `internal/bleveindex` behind `storage/index` | one day old: release-age rule applies (§13.6) |
| `github.com/prometheus/client_golang` / `client_model` | v1.25.0 / v0.6.3 | 2026-10-08 | `metrics` (per-App registry, R5) | |
| `go.opentelemetry.io/otel`, `/sdk`, `/sdk/metric`, `/trace`, `/metric` | v1.47.0 | 2026-10-02 | `metrics` | |
| `go.opentelemetry.io/otel/exporters/prometheus` | v0.69.0 | 2026-10-02 | `metrics` | v0: minor may break → **pinned** (mechanism below the table) |
| OTLP gRPC exporters (`otlpmetricgrpc`, `otlptracegrpc`) + `google.golang.org/grpc` v1.85.0 | v1.47.0 | 2026-10-02 | `metrics` (optional export) | pulls the split `genproto/googleapis/{api,rpc}` modules. The monolithic `genproto` is **present in the module graph** via `cockroachdb/errors@v1.14.0` (`raw/00-baseline-build.md` E7, `go mod graph`) and **pruned from the build list** in single-module mode, so it is absent from `go.mod`/`go.sum` (`raw/15-dependencies.md` "Go module-level notes"). Pebble plus these exporters in one core module is what makes a workspace inside `core/` fail without the exclude (03 §3.2.1 X2), which is why the exclude lives in `core/go.mod` |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | v0.72.0 | 2026-10-02 | `httpapi` | new |
| `github.com/google/jsonschema-go` | v0.4.3 | 2026-04-17 | `internal/jsonschemax` (03 R4: the L0 leaf that owns the one JSON Schema reflector; `config` and `httpapi` contribute overrides for their own types) | **Adopted** by [05-config](05-config.md) §1.10.2 (SIMP-14 made 05-config the owner of that choice). Checked 2026-11-04: MIT licence (module zip `LICENSE`), latest on the proxy (`For[T]`, `ForOptions.TypeSchemas` present, round-2 X15), pre-1.0 → **pinned** (mechanism below the table). |
| `github.com/go-logr/logr` | v1.4.4 | 2026-07-20 | `metrics` (OTel SDK logger bridge only) | `otel.SetLogger` takes a `logr.Logger` (`internal/telemetry/sdklog.go:17,30`); core bridges slog with `logr.FromSlogHandler` and imports logr nowhere else. See O1. |
| `github.com/oklog/ulid/v2` | v2.1.2 | 2026-07-23 | IDs for tasks, history, audit | time-sortable keys |
| `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2` | v3.21.0, v0.37.0 | 2026-09-01, 2026-08-25 | `auth` | |
| `golang.org/x/crypto` | v0.58.0 | 2026-10-09 | `auth`, `secrets` | password hashing, AEAD helpers |
| `golang.org/x/sync` | v0.24.0 | 2026-09-23 | `tasks`, adapters (`errgroup` + `SetLimit`) | CLAUDE.md concurrency rule |
| `golang.org/x/time` | v0.16.0 | 2026-08-19 | `integrations/providerhttp` (token bucket) | |
| `golang.org/x/text` | v0.43.0 | 2026-10-09 | `shared/` normalisers | |
| `github.com/klauspost/compress` | v1.20.3 | 2026-10-12 | `backup` (zstd; `internal/backup/codec.go`) | no zstd in stdlib |
| `github.com/fsnotify/fsnotify` | v1.10.1 | 2026-05-04 | `library` watcher (if [05-library](05-library.md) keeps watching) | |
| `github.com/stretchr/testify` | v1.12.1 | 2026-08-17 | tests only (`go.md:400`) | |
| `github.com/openai/openai-go/v3` | v3.93.0 (ABO v3.91.0) | — | `integrations/ai` OpenAI-compatible adapter only ([05-ai](05-ai.md)) | not in the contract package |

**How "pinned" works.** A Go `require` is a minimum: any module in either build list that requires a
higher version wins silently, and Dependabot's `/core` entry would open bump PRs. A pinned dependency
therefore gets (a) a Dependabot `ignore` entry for all update types in the gomod entry, which covers
both `/` and `/core` through `directories` (03 §3.2 item 6), so it is bumped only by a deliberate PR, and (b) a `--pin module=version` argument to
`scripts/ci/check_module_skew.py`, which asserts the resolved version in both `go list -m all`
outputs and fails when a transitive requirement lifts it. Pinned today: `github.com/google/jsonschema-go`
v0.4.3 (`TestSchema_Golden` would catch drift only after the fact) and
`go.opentelemetry.io/otel/exporters/prometheus` v0.69.0.

Not in core at all: `quic-go` (v0.64.1 → v0.65.0, v0 minor), `taglib`, `dhowden/tag`, `howett.net/plist`,
`chromem-go`, `coder/hnsw`, `lithammer/fuzzysearch`, `x/image`, `pgregory.net/rapid` (test-only; core may
adopt it for property tests) — audiobook-domain or ABO-internal.

## 13.6 Release-age policy ("latest stable", safely)

D8 asks for latest stable versions; a release a day old has had no time for regressions to be reported.
Rule for core and for ABO's bump PRs:

- A dependency or toolchain release is adoptable once it is **≥ 14 days old** and no newer patch of the
  same line exists that fixes a reported regression. Security fixes are exempt (adopt immediately).
- Measured, not remembered: `scripts/check_release_age.py` (plan deliverable) reads publish times from
  `npm view <pkg> time --json` and from the proxy `…/@v/<version>.info` `Time` field (the source used in
  this file) and fails a PR that introduces a younger version without a `security` label.
- Dependabot's `cooldown` option can enforce the same in the bot itself where available; the script is
  authoritative either way and costs nothing.
- **Tools are dependencies too.** Every Go tool core or ABO CI runs comes from a pinned module with a
  `tool` directive, installed with `go -C <dir> install tool` (the existing `tools/*` pattern, e.g.
  `tools/staticcheck/go.mod`): core's are `core/tools/apidiff` (`golang.org/x/exp/cmd/{apidiff,gorelease}`
  at `v0.0.0-20261014143641-f0251eb4aa3e`, 2026-10-14), `core/tools/govulncheck`
  (`golang.org/x/vuln/cmd/govulncheck` from x/vuln v1.9.0, 2026-10-11) and `core/tools/staticcheck`
  ([12-repo-split](12-repo-split.md) §12.3.11). `go run …@latest` and other unpinned forms are not
  used in any Makefile target or workflow; tool versions obey the same 14-day rule.
- This rule is owned here. A version target written anywhere else in the spec (07's frontend targets,
  12's tool versions) is valid only if it satisfies this section on its adoption date.

On 2026-11-04 this holds back: `@babel/core` 8.1.0, `vitest`/`@vitest/coverage-v8` 5.1.0,
`@vitejs/plugin-react` 6.2.1 (all 2026-11-02), `globals` 17.14.0 (2026-11-01), `bleve/v2` v2.6.2 (2026-11-03).
go1.27.3 (2026-10-19) is 16 days old and adoptable.

## 13.7 Risks for 14-risks-open-questions

| Risk | Evidence | Mitigation |
|---|---|---|
| `bleve/v2` v2.6.2 is one day old | proxy `Time` 2026-11-03T16:54:22Z | release-age rule; stay on v2.6.1 until 2026-11-17 |
| `@babel/core` 8 is two days old and React Compiler declares Babel 7 types | §13.4.1 E2 | E2 exit condition |
| vitest 5.1.0, plugin-react 6.2.1 (peer `oxc-transform-react >=0.152.0 <0.157.0`), globals 17.14.0 are days old | npm `time` | release-age rule |
| jsdom 30.1.x has a known MUI FocusTrap regression | `.github/dependabot.yml:90-104` | E3 |
| TS 7 has no JS API; lint tooling capped below TS 6.1 | `typescript-eslint` peer range | E1 |
| react-router 8 removes `react-router-dom`; 122 ABO files (52 source, 70 test) | §13.4.1 E4 | separate migration series (09 UI-0) that ends before `createShell` (E4) |
| `otel/exporters/prometheus` is v0 (minor may break) | v0.69.0 | pinned: Dependabot `ignore` plus `check_module_skew.py --pin` (§13.5.2); bump deliberately |
| Stale Dependabot ignores block golang 1.27.x and Vite 8 updates, including security updates | `.github/dependabot.yml:30-34`, `:80-89` | remove in the toolchain bump PR (§13.2.2) |
| `go.dev/dl` stable flag unverifiable from sandbox | §13.2.1 | re-check on a networked machine before the bump |
| Docker image digests for golang 1.27.3 not resolved | §13.2.2 | resolve at bump time; guard enforces digest equality |
| The monolithic genproto returns through `cockroachdb/errors` whenever a workspace includes core without core's exclude | 03 §3.2.1 X2; round-2 X10, X16 | exclude in `core/go.mod` from S0; `core/scripts/ci/check_genproto_exclude.py` checks, in a temp copy, that the line is present, that the workspaces build with it, and that without it `go mod graph` still shows the requirer **and** the build fails with `ambiguous import` — otherwise the exclude is stale (03 §3.2 item 1). The live graph cannot be probed: an excluded version never appears in it (X10) |
| Root and `core/go.mod` resolve different versions of shared dependencies (the binary ships versions core CI never tested) | 03 §3.2 item 6 | Dependabot gomod entry with `directories: ["/", "/core"]` under the existing `multi-ecosystem-group` (verified to yield one version-update PR; security PRs per directory are fixed up by the root-tidy step and caught by the skew check); `check_module_skew.py` (12 SC4(c)), with `--pin` for pinned modules; per-module `tidy-check` and the tidy-coupling rule (03 §3.2 item 4) |

## Objections to 03

None open. (The former O1 — add `otel.SetLogger`, `otel.SetErrorHandler` and
`otel.SetTextMapPropagator` to R5 and allow one opt-in call site in `app` — is resolved: 03 R5 v1.2.0+
lists all three and names `app.InstallProcessDefaults` as the single exception.)

## Out-of-scope defects found

1. `.standards/instructions/go.md:47` says CI pins `go-version: '1.27'` (minor), while this repo's guard
   requires the full patch and treats a floating minor as an error since 2026-10-10
   (`scripts/check_toolchain_versions.py:37-40`, after runners drifted to 1.27.2 on 2026-10-08). The org
   standard and the repo disagree; the repo's rule is the safer one. Belongs to the standards repo.

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- FE-21 — 13 declared the single owner of the release-age rule and version targets (§13.1, §13.4, §13.6); knip dropped from E1's exit condition with the `npm view` evidence.
- FE-5 — the `CrossOriginProtection` row points at the 05-http-api middleware stage that owns CSRF.
- SIMP-2 — gin, gin-contrib/gzip and otelgin rows: ABO keeps gin under a permanent `MountLegacy`; core never imports it.
- SIMP-14 — `google/jsonschema-go` recorded as a conditional kept dependency (licence, version, date checked); 05-config owns the choice.
- SIMP-25 — E4: the react-router 8 migration is a separate series that precedes shell adoption; counts 121 / 53 corrected with method.
- MOD-4 — §13.5.2 OTLP row corrected: monolithic genproto is in the module graph, pruned from the build list; exclude lives in `core/go.mod`.
- MOD-7 — no `core/examples/hello/go.mod` for the guard to read.
- MOD-11 — skew risk recorded in §13.7.
- MOD-17 — guard extended to `core/Makefile`, `core/go.mod`, `core/tools/*/go.mod`, `tools/*/go.mod`; the "tools are tier 3" sentence corrected (they are not guarded today).
- MOD-19 — pinned tools modules for apidiff/gorelease/govulncheck; no `@latest` (§13.6).

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-G request (FE-2, SIMP-25) — E4 exit condition: `react-router-dom` gone before ABO's first `createShell`; names 09 stage UI-0.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- FE2-19 — E4 and §13.7: 122 files (52 source, 70 test) with 07's grep; UI-0 scope is imports and the chunk regex (data router and harness at 07 §7.19 steps 4–5); exit check covers `web/tests` and `vite.config.ts`.
- SIMP2-3, SIMP2-16 — jsonschema-go row: adopted (no "only if"); lives in `internal/jsonschemax` (L0).
- MOD2-20 — "pinned" given a mechanism: Dependabot `ignore` plus `check_module_skew.py --pin`, for jsonschema-go and `otel/exporters/prometheus`.
- MOD2-5 — §13.7 genproto row: the redesigned exclude check (present; green with it; edge and `ambiguous import` without it).
- MOD2-6 — §13.7 skew row: one-PR Dependabot verification, `tidy-check` and the tidy-coupling rule.
- MOD2-13 — §13.2.2: no toolchain literals in scripts or workflow steps (guard check); the bump is the first PR of S0, before `core/` exists.
- MOD2-9 — §13.2.2 item 2: root-pin parity is root-side; core checks only its own copies.
- MOD2-21 — 12's criteria cited as SC3, SC4, SC4(c), SC10.

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- FE3-20 — §13.4: rows for `openapi-typescript` 7.13.0 and `openapi-fetch` 0.17.0 (published 2026-02-11, checked with `npm view` on 2026-11-04); E1 carries the generator's `overrides` note; react-router stays `^8.5.1` here (07 uses this range).
- MOD3-6 — §13.3 `go fix` row: a move PR applies `go fix` in a separate commit after the rename commit; the root gains no `go fix` gate (245 files pending at HEAD, X20).


Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- MOD4-14 — §13.3 `go fix` row: no rename-only commit; a move PR's history is kept by its `core/scripts/split/origins.txt` lines (12 §12.8.1), so every commit builds.
- MOD4-15 — §13.5.2 "How pinned works" and §13.7 skew row: one gomod entry with `directories: ["/", "/core"]` under the existing `multi-ecosystem-group` (no `applies-to`); security PRs handled by the root-tidy step and the skew check.

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`; resolutions R5-1…R5-7):

- MOD5-15 — §13.2.2 guard item 4: the core copies of the errcheck and interface-width ratchet scripts under `core/scripts/ci/` carry the "not a pin" comments and are allowlisted by path and line like the root originals.

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; resolutions R7-1…R7-8):

- FE7-11 — §13.4: `@mui/icons-material ^9.5.0` is a peer of `@mediacore/ui-kit` and `@mediacore/shell` (the moved shell files import it), matching 07 §7.3.1.

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`; resolutions R8-1…R8-6):

- MOD8-12 — §13.2.2: the two Dockerfile comments naming `golang:1.27.2-alpine` (`Dockerfile:25`, `Dockerfile.build-cgo:21`) join the prose row (9 prose copies, updated with the digest); the guard's history and docstring lines (`scripts/check_toolchain_versions.py:39`, `scripts/tests/test_check_toolchain_versions.py:31`) join "Not a pin".

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; resolutions A-1…A-17):

- A-4 (ADV-S3) — §13.3 `go fix` row: a move PR applies `go fix` in a commit after its move commit, so rename detection keeps the moved files' history; `origins.txt` no longer exists.

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; resolutions R9-1…R9-11):

- R9-10 (MOD9-3) — §13.3 `go vet` row: a required PR job vets `GOOS=darwin CGO_ENABLED=0 -tags embed_frontend` and `-tags integration` over both modules (`vet-darwin`, `vet-integration`).
- MOD9-5 — §13.3 `go fix` row: "every commit of the PR builds" is enforced by `scripts/ci/check_commits_build.py` for move PRs.
- R9-3 (MOD9-2) — §13.5.1 `otelgin` row: one emitter per family; otelgin keeps `http_server_*` metrics and gin's route label for legacy requests, `otelhttp` records spans only for them and metrics for native and `Infra` routes.
