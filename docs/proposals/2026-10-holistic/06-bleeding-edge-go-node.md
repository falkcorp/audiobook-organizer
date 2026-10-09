<!-- file: docs/proposals/2026-10-holistic/06-bleeding-edge-go-node.md -->
<!-- version: 1.3.0 -->
<!-- guid: 3f6e0b52-6c1a-4d77-9b0e-5a2d8c41e906 -->
<!-- last-edited: 2026-10-09 -->

# 06: Bleeding-edge Go and Node features

Analyst: `bleeding`. Researched 2026-10-08. Every version claim below was read
from the repo's pin files, `web/package-lock.json`, `npm view <pkg> dist-tags`
or the upstream release notes linked next to it. Nothing is from memory.

### Round-2 review (r4, 2026-10-09)

Re-verified in the `aorg-review2` worktree at `ebda30d47`. Changes made in this pass:

- **Go 1.27.2 confirmed** from `https://go.dev/dl/?mode=json` on 2026-10-09: the current stable set is `go1.27.2` and `go1.26.9`. The claim stands.
- **P1's file count re-measured: 18**, by `grep -rlF '1.27.1' . --exclude-dir={node_modules,.git,web,docs,.claude,.standards}` minus `CHANGELOG.md` and `TODO.md` (history, not pins): 13 functional + 5 docs, exactly the list in the P1 row. The P1 row said "15 files" in its Files column while the summary said 18; the row now says 18.
- **New finding F24:** `.github/workflows/ci.yml:47` passes `go-version: '1.27'` (a floating minor, not the pinned patch) to the reusable workflow, and the six project jobs pass the same value to `actions/setup-go` (`ci.yml:125,189,265,338,464,529`). It does not contain the string `1.27.1`, so no pin grep finds it, and P2's drift check should assert it equals the Makefile pin. Added to P2.
- **D37–D40 cross-check:** D37 accepted Q7 (the committed `Makefile` owns the generic build flags) but P3 did not carry it; P3 now adds the `GO_BUILD_TAGS` / `GO_LDFLAGS` variables and the `deploy-debug` example calling `make build-linux`. D38, D39, D40 match P4/P12, P9 and S4 as written. **D50** (go fix batches only in freeze window F, regenerated, after 01 tier 2) is now stated in §4's order line.
- **P4 hook points verified:** `internal/operations/registry/watchdog.go:55` `watchdogCycle` and `internal/server/search_reconciler.go:473` `checkSearchIndexStall` exist at HEAD. The route-registration file is now named: `internal/server/wire_handlers.go` (there is no `wire_diagnostics_routes.go`; the handler file is new).
- **TS 7 side-by-side made concrete (S5, P9):** `npm view typescript dist-tags` gives `latest: 7.0.2`, `next: 7.1.0-dev.20261008.1`; `npm view @typescript/typescript6 version` gives `6.0.2`. The TS 7 announcement confirms `@typescript/typescript6` ships its binary as `tsc6` so both can be installed; the exact `package.json` scripts and which binary each runs are in S5. The 7.1 dates in F9 remain unverified third-party figures and are marked so.
- **Over-sold, toned down:** rank 2 "costs nothing at runtime" (importing `net/http/pprof` registers on `http.DefaultServeMux`; the listener must use its own mux, now in S2); PGO "2–14 %" and Vitest "8–25 %" are upstream claims, labelled as such in the table; F14's "unlock rate" stays medium.
- **Not changed:** the go fix census, the react-router file list, the synctest list (all appendices re-read, not re-run; the tree moved only in `docs/` since they were generated).

## 1. Summary

- **The repo is already close to current.** It runs Go 1.27.1 with json/v2 as the default and the Green Tea GC, plus React 19.3, MUI 9.4, Vite 8 with rolldown, React Compiler 1.0, ESLint 10 and Node 26 (§2.0). Most of the value is in the gaps around that.
- **P0: Go 1.27.2 came out today** with security fixes to `net/http`, `os`, `crypto/tls`, `html/template` and others. One PR moves the pins. P1's own list is 18 files: 13 functional files and 5 docs that mention the pin. *(Coordinator: corrected from "15 files: 12 functional plus 6 docs", which did not match P1. Release confirmed on go.dev/doc/devel/release: go1.27.2, released 2026-10-08, with security fixes to the go command, crypto/tls, html/template, net/http, net/textproto and os.)* A second PR makes the drift check cover the 6 Woodpecker files and `ci_remote.py`, which it misses today (F1, F20).
- **Profiling prod means swapping the binary.** Only the owner's private `deploy-debug` carries the `pprof` tag. The committed example is stale: no tag, and `-N -l`. Proposal: put the tag, still gated by an env var, in the normal deploy. That makes the Go 1.27 goroutine-leak profile and PGO profiles (2-14% CPU per the Go docs) one restart away (F2, F4, F5).
- **Add an always-on `runtime/trace.FlightRecorder`.** It should snapshot whenever the existing ops watchdog or the search-stall watchdog fires, because both incident classes went silent before (F3).
- **`go fix` modernizers, measured read-only:** 997 hunks across 364 files, 231 of them test files. They split into 4 batches, and `internal/writeback/` is touched 0 times. `omitzero` is held back because it changes the JSON on the wire (F6, F7).
- **TypeScript 7 typechecks this repo 5.4x faster** (8.74 s to 1.61 s) with 0 errors, and a negative control was checked. Typechecking moves to TS 7 now; TS 6 stays for typescript-eslint until TS 7.1, due 2026-11-24 (F8-F10).
- **Frontend upgrades:** react-router 8.4 (95 files) and Vitest 5 are both unblocked. Vitest 5's `clearMocks` break is already harmless: 1,834 of 1,834 tests pass with it on (F11, F12).
- **Swapping `finally { setLoading(false) }` for React 19 `useTransition` targets the React Compiler bailouts.** The pattern appears 130 times in 61 files and is the try/finally shape behind 84% of bailouts. This is Trial, with `ActionBar.tsx` as the scope guard (F14, F15).
- **Hold:** dropping TS 6, oxlint/Biome, Vite `bundledDev`, the oxc compiler, `omitzero` as a sweep, and SIMD.

## 2. Findings

### 2.0 What is already adopted (not recommendations, kept so nobody re-proposes them)

| ID | Already in place | Evidence | Conf. |
|----|------------------|----------|-------|
| A1 | Go 1.27.1 pinned in every build path | `Makefile:43` `export GOTOOLCHAIN := go1.27.1`; `go.mod:3` `go 1.27.0`; `.envrc`; `.vscode/settings.json:7,10`; `Dockerfile:27`, `Dockerfile.build-cgo:23`; `.woodpecker/checks-lint.yaml:47` | high |
| A2 | `encoding/json` v1 API runs on the v2 engine (GA default in 1.27, opt-out `GOEXPERIMENT=nojsonv2`) | `Makefile:39-42`; [go1.27 notes, Experiments](https://go.dev/doc/go1.27) | high |
| A3 | Green Tea GC is the default since 1.26 (10-40% less GC overhead) | [go1.26 Runtime](https://go.dev/doc/go1.26). No `nogreenteagc` anywhere: `grep -rn greenteagc Makefile* Dockerfile* deploy/` = 0 | high |
| A4 | `sync.WaitGroup.Go` in 32 non-test sites; `errors.AsType` in 27; `testing/synctest` in 6 tests; `b.Loop` in 15; `os.Root` in 2; `omitzero` in 29 | `grep -rnE '[wW]g\.Go\('`, `errors\.AsType`, `synctest\.`, `b\.Loop\(\)`, `os\.OpenRoot`, `omitzero` over `--include=*.go` | high |
| A5 | Frontend on current majors: React 19.3.0, MUI 9.4.0, Vite 8.2.1 (rolldown 1.2.5), Vitest 4.1.11, TypeScript 6.0.3, ESLint 10.11.0, React Compiler 1.0.0 via `@rolldown/plugin-babel`, jsdom 30.0.1 | `web/package-lock.json` resolved versions; `web/vite.config.ts:13-21` | high |
| A6 | Node 26 everywhere (CI `node-version: '26'`, `node:26-alpine` builder stages). Node 26 becomes LTS on **2026-10-28** | `.github/workflows/ci.yml:48`; `Dockerfile:13`; [nodejs/Release schedule.json](https://raw.githubusercontent.com/nodejs/Release/main/schedule.json) | high |
| A7 | Container-aware GOMAXPROCS (Go 1.25) is **not applicable**: prod runs under systemd with `MemoryMax=12G` and **no `CPUQuota`**, so cgroup CPU limits never bind. Do not propose it | `deploy/audiobook-organizer.service` (`GOMEMLIMIT=9GiB`, `GOGC=200`, `MemoryMax=12G`, no CPUQuota) | high |

### 2.1 New findings and candidates

| ID | Finding | Evidence | Conf. | Impact |
|----|---------|----------|-------|--------|
| F1 | **Go 1.27.2 shipped today (2026-10-08) with security fixes to the `go` command and `crypto/tls`, `html/template`, `net/http`, `net/textproto`, `os`.** The repo pins 1.27.1. The server exposes `net/http` (gin) and does path work through `os` | [go.dev/doc/devel/release#go1.27.minor](https://go.dev/doc/devel/release) | high | security |
| F2 | **Profiling prod needs a redeploy, and the committed example recipe is stale.** `pprof_debug.go:7` is `//go:build pprof`. The committed `Makefile` (`build-linux`, :143-150), used by the normal `deploy`, omits the tag, so prod normally has no profiling path. The owner's private, gitignored `Makefile.local` does have a correct `build-linux-debug` (tags include `pprof`, optimizations on) and a `deploy-debug` that turns on the listener at `localhost:6060` and checks it comes up. But the committed `Makefile.local.example:107-116` `deploy-debug` still builds **without** `pprof` and **with** `-gcflags="all=-N -l"` (optimizations and inlining off). So anyone working from the example gets an unoptimized binary with no listener. And getting a profile always means replacing the prod binary first | `grep -rn pprof Makefile Makefile.local.example .github/workflows/*.yml` = 0 tag uses; `grep -n 'pprof\|gcflags\|-tags' Makefile.local` (main checkout, read-only) shows the tag at :70 | high | observability, perf |
| F3 | **No runtime trace or flight recorder anywhere.** `runtime/trace` imports = 0. Two watchdogs already detect the exact moments a trace would explain, then only log: `internal/operations/registry/watchdog.go:55` (`watchdogCycle`, stuck / never_reported strikes) and `internal/server/search_reconciler.go:473` (`checkSearchIndexStall`). The 2026-07-05 dedup 3-hour single-core run and the 2026-09-24/25 search-index wedge (comment at `search_reconciler.go:450-454`) both went silent with no evidence captured. `runtime/trace.FlightRecorder` is stable since Go 1.25 | `grep -rn '"runtime/trace"' --include=*.go` = 0; [go1.25 Runtime](https://go.dev/doc/go1.25); [FlightRecorder pkg doc](https://pkg.go.dev/runtime/trace#FlightRecorder) | high | reliability, MTTR |
| F4 | **Goroutine-leak profile is on by default in Go 1.27** (`runtime/pprof` profile `goroutineleak`, endpoint `/debug/pprof/goroutineleak`), and Go 1.27 tracebacks include pprof goroutine labels. Reachable today only through the private `deploy-debug` (F2). The code has 9 non-test and 57 test `wg.Add(1)` sites (`grep -rnE '[wW]g\.Add\(1\)'`) plus long-lived goroutines (watchdogs, reconcilers, SSE hubs), which is the shape this profile targets | [go1.27 Runtime](https://go.dev/doc/go1.27); [go1.26 Runtime (experiment origin)](https://go.dev/doc/go1.26) | high | reliability |
| F5 | **No PGO.** No `default.pgo` in the main package (`ls default.pgo` = absent). Go's documented PGO gain is 2-14% CPU on typical programs. A profile has to come from an optimized prod binary that has pprof, which today means `deploy-debug` from the private `Makefile.local` (F2) | [go.dev/doc/pgo](https://go.dev/doc/pgo) | high (absence), medium (gain size) | perf |
| F6 | **`go fix` modernizers (Go 1.26 rewrite, 1.27 additions) would rewrite a large share of the tree.** Measured with `go fix -diff -<analyzer> ./...` (read-only), default build tags only, so counts are a lower bound. Table in §2.2 | `go tool fix help`; [go1.26 Tools](https://go.dev/doc/go1.26); [go1.27 Tools](https://go.dev/doc/go1.27) | high | DX, quality |
| F7 | **`omitzero` modernizer is a wire-format change.** The tool itself labels it so: `fix: omitzero: ignoring alternative fix "Replace omitempty with omitzero (behavior change)"` (stderr of `go fix -diff ./...`). It changes JSON the frontend and AudioBooth (external Swift, not in CI) read | `go fix -diff ./... 2>&1 >/dev/null \| grep omitzero` | high | risk |
| F8 | **TypeScript 7.0.2 typechecks this repo with zero errors, 5.4x faster.** Measured on this Mac (M-series, 10 cores) with the lockfile's node_modules: TS 6.0.3 `tsc --noEmit -p web` = 8.74 s total (check 7.53 s, 926 MB); TS 7.0.2 = 1.61 s total (check 1.52 s, 978 MB), exit 0, same 1,614 files. Negative control: a seeded `const x: number = "..."` is reported identically (`TS2322`) by both compilers | commands in §2.3 | high | DX, CI time |
| F9 | **TS 7 cannot replace TS 6 outright yet:** `typescript-eslint@8.71.1` (latest) peers `typescript >=4.8.4 <6.1.0`; TS 7.0 ships no JS API. TS 7.1 (new API) is planned Beta 2026-10-06, RC 2026-11-10, Stable 2026-11-24. Microsoft documents a side-by-side install (`@typescript/native` alias for 7, `typescript` kept at 6 for tools) | `npm view typescript-eslint@latest peerDependencies`; [Announcing TS 7.0](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/); 7.1 dates from a third-party summary of the iteration plan, [ecorpit.com](https://ecorpit.com/typescript-7-migration-readiness-eslint-astro-blockers-2026/), **not verified against a Microsoft source**; `npm view typescript dist-tags` shows `next: 7.1.0-dev.20261008.1`, so 7.1 is in progress | high / medium (dates) | DX |
| F10 | **ESLint here is not type-aware** (`web/eslint.config.mjs:19` uses `tseslint.configs.recommended`, not `recommendedTypeChecked`), takes 5.46 s, and uses the TS API only to parse. So the TS 6 dependency is for parsing alone, and a 7.1-era typescript-eslint release removes it | `time eslint . --quiet` = 5.46 s | high | DX |
| F11 | **react-router is the one frontend major still behind.** Installed `react-router-dom@7.18.4`; latest `react-router@8.4.0`. Its gate (React >= 19.2.7) is met by 19.3.0. **95 files** import `react-router-dom` (46 source, 49 test; list in [appendix](06-bleeding-edge/react-router-files.md)). `web/vite.config.ts` `codeSplitting.groups[1].test` names `react-router-dom`, so that regex must change in the same PR | `npm view react-router dist-tags`; [upgrade report §2](../../2026-08-06-frontend-dependency-upgrade-report.md); `grep -rlE "from ['\"]react-router-dom['\"]" web/src web/tests` = 95 files | high | maintenance |
| F12 | **Vitest 5.0.3 is out** (5.0 GA 2026-09-03). The headline break (`clearMocks` defaults to true) is **already harmless here**: running the current 4.1.11 suite with `clearMocks: true` merged into `vitest.config.ts` gave 185/185 files, 1,834/1,834 tests passing. The probe can fail: `web/src/test/setup.ts` does not clear mocks itself, and a control run of the same merged config with `include: ['__nothing__/**']` selected zero files, so the merged config was applied. The other Vitest 5 break (unawaited async assertions now fail) looks like zero work: a grep for `.resolves`/`.rejects`/`expect.poll` without `await` or `return` on the same line found 8 hits, and the 3 inspected were multi-line awaited or assigned-then-awaited. Vitest 5 also adds `fsModuleCache` (persistent transformed-module cache) and claims 8-25% faster runs. Baseline: 185 files, 1,834 tests, 52 s wall, 393 s user CPU; `import` 167 s and `environment` (jsdom) 78 s dominate | [Vitest 5 blog](https://vitest.dev/blog/vitest-5); `npm view vitest dist-tags` (`latest: 5.0.3`) | high (pass), medium (speed claim) | DX |
| F13 | **Two Vitest configs, one shadowed.** Vitest loads `web/vitest.config.ts` in preference to `web/vite.config.ts`, so the `test:` block in `vite.config.ts` (coverage thresholds 15/10/15/15) is dead config, and the real thresholds are 30/20/20/25 in `vitest.config.ts`. `vitest.config.ts` also does not carry the `@` alias or the React Compiler plugin, so tests run uncompiled components | `web/vite.config.ts` `test:` block; `web/vitest.config.ts:7-29`; [Vitest config docs, "vitest.config.ts takes priority"](https://vitest.dev/config/) | high | quality (hand to 01) |
| F14 | **React Compiler bails on 218 components, 84% because of `try/finally`** (`docs/react-compiler-adoption.md`). Compiler 1.0.0 is still the latest stable (`npm view babel-plugin-react-compiler dist-tags`), and nothing found in this research says try/finally support has shipped (not verified against compiler source). Of 203 `finally {` blocks in 72 source files, **130 (in 61 `.tsx` files) do nothing but reset a boolean** (`setLoading(false)` and similar). React 19's `useTransition` / async Actions give `isPending` for free and delete that `finally` entirely, which is the shape the compiler cannot handle. Only 3 files use `useTransition` today | Python count over `web/src` (non-test); `grep -A1 'finally {'` filter `set[A-Za-z]*\(false\)` = 130; [React 19 Actions](https://react.dev/blog/2024/12/05/react-19#actions) | high (counts), medium (unlock rate) | perf, quality |
| F15 | `web/src/components/review/ActionBar.tsx:9-27` documents why `useOptimistic` is the wrong tool for long, server-side applies and uses `useTransition` only for the bounded part. That reasoning limits F14: transitions fit request/response loads, not multi-minute operations | file cited | high | scope guard |
| F16 | **React 19.3 (2026-09-09) promoted `<ViewTransition>` and Fragment refs to stable**, and added Trusted Types pass-through. None are used (`ViewTransition` = 0). Low payoff for an admin SPA; Trusted Types is the interesting one if a CSP is ever added | [React 19.3 blog](https://react.dev/blog/2026/09/09/react-19-3) | high | assess |
| F17 | **Vite 8.1 experimental `experimental.bundledDev`** claims ~15x faster dev startup on 10k-component apps. This app has 253 non-test source files; payoff small, and it is experimental. Vite latest is 8.3.4 vs 8.2.1 installed | [Announcing Vite 8.1](https://vite.dev/blog/announcing-vite8-1); `npm view vite dist-tags` | high | hold |
| F18 | `@vitejs/plugin-react` 6 also offers the Rust (oxc) React Compiler port behind `compiler: true`, documented experimental. The repo comment at `web/vite.config.ts:14-19` already chose the stable babel path; keep it | file cited | high | hold |
| F19 | **Node 26 runs `.ts` files natively** (type stripping stable, `--experimental-transform-types` removed). The repo has 3 `.mjs` test scripts (`web/tests/e2e/check-spec-discovery.mjs`, `web/tests/smoke/routes.mjs`, `web/tests/visual/compare-layout.mjs`) that could become typechecked `.ts` with no build step. `Temporal` is on by default in Node 26 but only reaches browsers unevenly, so no frontend use | [Node 26.0.0 release](https://nodejs.org/en/blog/release/v26.0.0) | high | DX, small |
| F20 | **The toolchain drift checker misses Woodpecker and `ci_remote.py`.** `scripts/check_toolchain_versions.py` checks the Makefile, `.envrc`, `.vscode/settings.json`, `Dockerfile*`, workflow `go-version:` and `go.mod`. It does not check the 6 `.woodpecker/*.yaml` files, which pin `GOTOOLCHAIN: go1.27.1` (`checks-build.yaml:41`, `checks-lint.yaml:49`, `test-database.yaml:45`, `test-fixtures.yaml:43`, `test-rest.yaml:50`, `test-server-scanner.yaml:42`) and the `golang:1.27.1-bookworm@sha256` image (`checks-lint.yaml:47`). It also misses `scripts/ci_remote.py:76` `GO_TOOLCHAIN = "go1.27.1"`. A bump can leave all of these behind without any check noticing | `grep -rnF '1.27.1' . --exclude-dir={node_modules,.git,web,docs,.claude}` | high | reliability |
| F21 | **Test-time sleeps.** 294 `time.Sleep` calls in 132 `_test.go` files, plus 43 `Eventually(` polls. `testing/synctest` (stable since 1.25; `synctest.Sleep` added in 1.27) is used in only 6 tests. Each converted test becomes deterministic and runs in virtual time. Count-only evidence: which sleeps are bubble-compatible (no real I/O, no Pebble) needs per-file review | `grep -rnE --include=*_test.go 'time\.Sleep\('` = 294 / 132 files | high (count), low (convertible share) | flakiness, CI time |
| F22 | `unique` (Go 1.23) and `weak` (Go 1.24) are unused (the two `"weak"` grep hits are string literals). Interning repeated strings in memdb (genre, codec, format, author names) could cut heap, but there is no heap profile to size it, and F2 blocks getting one | `grep -rnE '"unique"|"weak"' --include=*.go` | high (absence), low (benefit) | assess |
| F23 | Go 1.28 (Feb 2027, draft notes) adds vet analyzers `scannererr` and `sqlrowserr`, `regexp` iterator methods, `testing/synctest.Subtest`. Nothing to do now; noted for the next bump | [go1.28 draft](https://tip.golang.org/doc/go1.28) | medium (draft) | future |
| F24 | *(r4)* **`ci.yml` does not pin the patch.** `.github/workflows/ci.yml:47` passes `go-version: '1.27'` to the reusable workflow, and the six project jobs pass the same floating value to `actions/setup-go` (`ci.yml:125,189,265,338,464,529`). GitHub runners therefore moved to 1.27.2 on 2026-10-08 by themselves while every other path pins 1.27.1, and no grep for `1.27.1` sees the file. `scripts/check_toolchain_versions.py` reads workflow `go-version:` lines but accepts the minor-only form | `grep -n "go-version" .github/workflows/ci.yml` | high | drift (hand to P2) |

### 2.2 `go fix` modernizer census (F6)

Command, per analyzer, in the analysis worktree (read-only; `-diff` applies nothing):
`go fix -diff -<name> ./...`. Counts are files / hunks. Default tags only.

| Analyzer | Files | of which `_test.go` | Hunks | Batch |
|----------|------:|------:|------:|-------|
| `rangeint` | 109 | 96 | 200 | P6 |
| `newexpr` (Go 1.26 `new(expr)`) | 67 | **67** | 381 | P7 (test-only, so low risk) |
| `slicescontains` | 45 | 17 | 87 | P6 |
| `mapsloop` | 30 | 9 | 62 | P6 |
| `errorsastype` | 28 | 7 | 35 | P5 |
| `waitgroupgo` | 27 | 22 | 46 | P8 |
| `stringsseq` | 24 | 9 | 34 | P6 |
| `embedlit` (1.27) | 21 | 18 | 53 | P6 |
| `minmax` | 21 | 6 | 24 | P5 |
| `slicesbackward` (1.27) | 19 | 1 | 32 | P6 |
| `reflecttypefor` | 15 | 11 | 19 | P5 |
| `stditerators` | 13 | 10 | 14 | P5 |
| `omitzero` | 9 | 0 | 10 | **excluded** (wire format, F7) |
| `forvar` | 6 | 4 | 6 | P5 |
| `stringscut` | 6 | 3 | 7 | P5 |
| `any` | 2 | 1 | 3 | P5 |
| `stringscutprefix` | 2 | 0 | 2 | P5 |
| `testingcontext` | 2 | 2 | 2 | P8 |
| `stringsbuilder` | 1 | 0 | 2 | P5 |
| `inline` | 1 | — | 1 | P5 |
| `atomictypes`, `hostport`, `slicessort`, `unsafefuncs` | 0 | 0 | 0 | — |
| **All analyzers together** (`go fix -diff ./...`) | **364** | **231** | **997** | |

- `internal/writeback/`: **0 files** in any analyzer's diff (`grep '^+++ ' *.diff | grep internal/writeback/` = 0). The batches still exclude it by rule.
- Run time: the full `go fix -diff ./...` took 31 s wall (180 s user) on this Mac. The worktree stayed clean afterwards (`git status --short` shows only `docs/proposals/`).
- `testingcontext` only finds 2 sites because it rewrites `context.WithCancel` in tests, not bare `context.Background()` (4,062 of those in 766 test files). Converting those to `t.Context()` is a separate manual job and is not proposed here.
- `newexpr` is entirely test code: it replaces pointer helper calls in fixtures with `new(value)`. P7 should delete the helper functions it makes unused.

### 2.3 Measurement commands

```bash
# F8 baseline (main checkout's node_modules, read-only)
cd web && time ./node_modules/.bin/tsc --noEmit -p . --extendedDiagnostics
# F8 TS 7 (installed into the scratchpad only, never into the repo)
npm i --no-save typescript@7.0.2   # in a scratch dir
<scratch>/node_modules/.bin/tsc --noEmit -p <repo>/web --extendedDiagnostics
# F12
cd web && time ./node_modules/.bin/vitest run --reporter=dot
# F12 clearMocks probe: scratch config = mergeConfig(vitest.config.ts, {test:{clearMocks:true}})
```

## 3. Proposed specification

### 3.1 Ranked recommendations

Ring meanings: **Adopt** = do it now, the evidence is in. **Trial** = do it in one
bounded place, measure, then widen. **Assess** = measure before deciding.
**Hold** = do not do it now; reason given.

| Rank | Ring | Item | Needs | Status upstream | Payoff (measured where possible) | Finding |
|-----:|------|------|-------|-----------------|----------------------------------|---------|
| 1 | **Adopt** | Bump Go 1.27.1 → **1.27.2** | Go 1.27.2 | stable, released 2026-10-08 | security fixes in `net/http`, `os`, `crypto/tls`, `html/template`, `net/textproto`, `go`; bug fixes in `encoding/json`, `encoding/json/v2`, `go fix`, vet | F1 |
| 2 | **Adopt** | Ship the `pprof` tag in the **normal** deploy build. The listener stays off unless `ABK_PPROF_ADDR` is set, and binds to loopback. Bring `Makefile.local.example`'s `deploy-debug` in line with the owner's working private version: `pprof` tag, no `-N -l` | Go ≥1.0; `goroutineleak` needs 1.27 | stable | profiling prod then needs only an env drop-in and a restart, not a new binary; the example stops handing out an unoptimized, listener-less recipe. Runtime cost when the listener is off: the `net/http/pprof` import registers handlers on `http.DefaultServeMux` and nothing else (S2 keeps the listener on its own mux) | F2, F4 |
| 3 | **Adopt** | `runtime/trace.FlightRecorder`, always on, snapshot to disk when the ops watchdog writes a stuck / never_reported strike and when the search index stalls; admin-only download | Go 1.25+ | stable since 1.25 | turns two "went silent" incident classes into a trace file of the last N seconds; one recorder per process, one `WriteTo` at a time ([pkg doc](https://pkg.go.dev/runtime/trace#FlightRecorder)) | F3 |
| 4 | **Adopt** | `go fix` modernizers, in four batches (safe mechanical; `rangeint`/loops; `newexpr`; `waitgroupgo`), never `omitzero` in a batch | Go 1.26+ `go fix`, 1.27 analyzers | stable | see §2.2 for exact counts; every hunk is a semantics-preserving rewrite by construction except `omitzero` | F6, F7 |
| 5 | **Adopt** | Typecheck with **TypeScript 7** (`tsc` from `typescript@7`, aliased `@typescript/native`) in `npm run build` and CI; keep `typescript@6` installed only for typescript-eslint | TS 7.0.2 | stable (no JS API until 7.1) | **8.74 s → 1.61 s** (5.4x) per typecheck, 0 errors, same 1,614 files; negative control agrees | F8, F9, F10 |
| 6 | **Adopt** | react-router 7.18.4 → **8.4.0** (`react-router-dom` → `react-router`) | React ≥19.2.7 (have 19.3.0) | stable | removes the last frontend major lag; import rewrites in 95 files | F11 |
| 7 | **Adopt** | Toolchain drift check covers `.woodpecker/*.yaml` | — | — | closes a silent drift path the rank-1 bump would hit | F20 |
| 8 | **Trial** | PGO: `default.pgo`-style profile from prod, applied to deploy/release builds only via `-pgo=<path>` | Go 1.21+, rank 2 first | stable | **upstream claim, not measured here:** 2-14% CPU on "typical programs" ([go.dev/doc/pgo](https://go.dev/doc/pgo)); the payoff for this binary is unknown until the P12 A/B on the scan and dedup ops | F5 |
| 9 | **Trial** | Vitest 4.1 → **5.0.x**, then turn on `fsModuleCache` | Node ≥22.12, Vite ≥6.4 (have 26 / 8.2) | stable since 2026-09-03 | main break already proven harmless (1,834/1,834 with `clearMocks:true`); **upstream claim, not measured here:** 8-25% faster; `import` (167 s) is the cost centre it targets | F12 |
| 10 | **Trial** | Replace `try { … } finally { setLoading(false) }` with `useTransition` / Actions `isPending`, file by file, starting with the compiler short list | React 19 (have 19.3) | stable | 130 boolean-reset `finally` blocks in 61 files; each one removed deletes the shape behind 84% of compiler bailouts. Re-run the compiler logger from `docs/react-compiler-adoption.md` to count the unlock | F14, F15 |
| 11 | **Trial** | `testing/synctest` for sleep-based tests, starting with the files with the most `time.Sleep` | Go 1.25 (+`synctest.Sleep` 1.27) | stable | 294 sleeps in 132 test files; each converted test is deterministic and runs in virtual time | F21 |
| 12 | **Trial** | Node-native `.ts` for the 3 `.mjs` test scripts | Node 26 | stable | small: those scripts get typechecked | F19 |
| 13 | **Assess** | `unique.Make` interning / `weak` caches in memdb | Go 1.23/1.24 | stable | unknown until a heap profile exists (needs rank 2) | F22 |
| 14 | **Assess** | `os.Root` for the organizer / backup / cover write paths in place of `pathvalidation.SecureJoin` | Go 1.24 (+1.25 methods: `MkdirAll`, `Rename`, `RemoveAll`, `WriteFile`…) | stable | symlink-safe by construction at the syscall layer, which `SecureJoin` is not. **Does CodeQL credit it as a `go/path-injection` barrier? Unknown**, and the memory note says helper swaps have failed 8/8. Run the known-positive probe from that note before any migration | — |
| 15 | **Assess** | React 19.3 Trusted Types support, if a CSP is added | React 19.3 | stable | security hardening only together with a CSP | F16 |
| 16 | **Hold** | Full TS 7 cutover (drop TS 6) | TS 7.1 + a typescript-eslint release that peers it | 7.1 stable planned 2026-11-24 | gated upstream | F9 |
| 17 | **Hold** | oxlint / Biome in place of ESLint | — | stable | ESLint takes 5.46 s here; the react-hooks 7 compiler rules (`web/eslint.config.mjs`) are the value, and replacing them is a regression risk for a 5 s saving | F10 |
| 18 | **Hold** | Vite `experimental.bundledDev`, oxc React Compiler (`compiler: true`), `<ViewTransition>` | — | experimental / no fit | small app (253 source files); the babel compiler path is the stable one (`web/vite.config.ts:14-19`) | F17, F18 |
| 19 | **Hold** | `omitzero` as a sweep | — | — | changes JSON on the wire; per-field review only, with AudioBooth checked | F7 |
| 20 | **Hold** | `GOEXPERIMENT=simd`, `runtimesecret` | Go 1.26/1.27 | experimental | no hot loop here has been shown to be SIMD-bound; fingerprinting runs on the Macs, not the server | — |

### 3.2 Specification details for the Adopt items

**S1. Go 1.27.2 bump (rank 1).** Move every pin together, as `Makefile:36-38` says.
Pull the new `golang:1.27.2-alpine` and `golang:1.27.2-bookworm` digests with
`docker buildx imagetools inspect`. If Docker Hub has not published 1.27.2 images
yet on the day of the PR, the PR waits for them; it does not split pins.

**S2. Profiling on prod (rank 2).**
- Committed `Makefile` `build-linux` (:146-150), which the normal deploy uses, adds `pprof` to `-tags`. The listener is still gated by `ABK_PPROF_ADDR` (`pprof_debug.go:24-35`). The committed unit file does not set it, so a default deploy exposes nothing.
- *(r4, D37 Q7 accepted)* The committed `Makefile` owns the generic flags: two variables, `GO_BUILD_TAGS := pprof` and `GO_LDFLAGS := -s -w` plus `-trimpath`, used by `build`, `build-linux` and `build-api`; P12 later appends `-pgo=pgo/prod.pprof` to the deploy targets only. `Makefile.local.example`'s `deploy` and `deploy-debug` call `make build-linux` and keep only host, ssh and drop-in details, so a build-flag PR never needs a hand edit of the private file.
- `pprof_debug.go` serves the listener from its own `http.NewServeMux()` with the `net/http/pprof` handlers mounted explicitly; the import's side-effect registration on `http.DefaultServeMux` is then inert (the server never serves `DefaultServeMux`; the PR asserts that with a grep for `http.ListenAndServe(` / `http.Handle(` outside the pprof file).
- `Makefile.local.example`: `deploy-debug` copies the shape of the working private recipe (`pprof` tag, optimized, listener set through the local drop-in). Drop `-gcflags="all=-N -l"`, which is a delve setting.
- The private `Makefile.local` is the owner's file, and no PR edits it (Q7).
- `/debug/pprof/goroutineleak` (on by default in Go 1.27) comes with this.

**S3. Flight recorder (rank 3).** New package `internal/diag/flight` (name is a
proposal; workstream 07 owns package layout):
- `Start(cfg)` at server boot: `trace.NewFlightRecorder(trace.FlightRecorderConfig{MinAge: 30 * time.Second, MaxBytes: 32 << 20})`.
  Both numbers are proposals; measure RSS before and after on prod.
- `Snapshot(reason string) (path string, err error)`: one `WriteTo` at a time (the
  API refuses concurrent calls), rate-limited to one per 5 minutes per reason, writes
  `<data dir>/traces/<UTC>-<reason>.trace`, keeps at most 10 files.
- Callers: `registry.watchdogCycle` on a stuck or never_reported strike;
  `Server.checkSearchIndexStall` when it returns true.
- Admin-only `GET /api/v1/diagnostics/traces` (list) and `/traces/:name`
  (download; name validated as a bare filename and opened through `os.DirFS`, the
  pattern the CodeQL memory note says clears `go/path-injection`).
- Off switch: env `ABK_FLIGHT_RECORDER=off`.

**S4. go fix batches (rank 4).** Every batch runs after S1 (1.27.2 fixes `go fix`
itself), excludes `internal/writeback/` by policy even though the default-tag run
touches none of it, and is re-run with `-tags "pprof bench"` to catch tagged files.
`omitzero` is excluded from all batches (`-omitzero=false`).

**S5. TypeScript 7 typecheck (rank 5).** `web/package.json`:
```jsonc
"devDependencies": {
  "@typescript/native": "npm:typescript@^7.0.2",   // tsc 7, used for typecheck
  "typescript": "npm:@typescript/typescript6@^6.0.2" // TS 6 API for typescript-eslint
}
"scripts": { "typecheck": "tsc -p .", "build": "tsc -p . && vite build" }
```
*(r4, verified 2026-10-09)* **Which binary runs what.** npm installs a package's
`bin` entries under the package's own names, not the alias name. `typescript@7.0.2`
declares `tsc` (and `tsserver`); `@typescript/typescript6@6.0.2` declares `tsc6`
(the [TS 7 announcement](https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/):
"This package provides an executable named `tsc6`, so that if needed, you can
install TypeScript 7.0 (which ships its own `tsc` binary) side-by-side without
naming conflicts"). So with the two aliases above, `web/node_modules/.bin/` holds
`tsc` = TS 7 and `tsc6` = TS 6, with no collision. Module resolution is the
other way round: `import 'typescript'` (what `typescript-eslint` and
`@typescript-eslint/parser` do) resolves the alias named `typescript`, which is
the TS 6 package; TS 7.0 has no JS API at all ("it does not ship with an API",
same announcement), so nothing can resolve it by accident.

Scripts, exactly:

```jsonc
"scripts": {
  "build":          "tsc -p . --noEmit && vite build",   // TS 7 typecheck, then Vite (oxc strips types; no TS API)
  "typecheck":      "tsc -p . --noEmit",                 // TS 7, 1.6 s measured
  "typecheck:ts6":  "tsc6 -p . --noEmit",                // TS 6 control, kept one release, run in P9's CI step only
  "lint":           "eslint ."                           // typescript-eslint 8.71.1 → TS 6.0.2 via the `typescript` alias
}
```

Today `"build": "tsc && vite build"` (`web/package.json:8`) runs whichever `tsc`
is installed, so after P9 it is TS 7 without a script change; the explicit `-p .
--noEmit` is added so the intent is visible. Vite 8 and Vitest 4/5 compile TS
with oxc/esbuild and never load the `typescript` module, so they are unaffected
by which alias carries that name. The `@typescript/typescript6` alias resolves to
6.0.2 today, one patch below the installed 6.0.3; fine for parse-only lint, noted
in the PR. TS 7 defaults that bite other repos are already explicit here:
`strict: true` and `types: ["vitest/globals", "node"]` are set, and `baseUrl` is
gone (`web/tsconfig.json:6-8`). Baseline re-measured in the review worktree:
TS 6.0.3 `tsc --noEmit -p web` = 8.9 s wall, 0 errors.

**S6. react-router 8 (rank 6).** Follow the upgrade report §2 (sed over the 95 files in the appendix),
plus the `vendor` regex in `web/vite.config.ts` and the exact-file chunk check the
comment there requires.

**S7. Drift check (rank 7).** `scripts/check_toolchain_versions.py` reads every
`golang:` image in `.woodpecker/*.yaml` and requires it to equal the Makefile pin
(tier 1, digest present).

## 4. Implementation plan

Each PR follows the repo's worktree + PR flow, with a `changelog.d/` fragment and
version-header bumps on every touched non-fragment file.

| PR | Title | Files touched (exact) | Tests / verification | Rollback | Size |
|----|-------|-----------------------|----------------------|----------|------|
| P1 | `chore(go): bump toolchain to go1.27.2` | measured with `grep -rlF '1.27.1' . --exclude-dir={node_modules,.git,web,docs,.claude,.standards}` minus `CHANGELOG.md` and `TODO.md`: **18 files** (r4 re-count 2026-10-09; 13 functional + 5 docs). **Pins (functional):** `Makefile:43`, `.envrc:10`, `.vscode/settings.json:7,10`, `Dockerfile:25,27`, `Dockerfile.build-cgo:21,23`, `.woodpecker/checks-lint.yaml:47,49`, `.woodpecker/checks-build.yaml:18,41`, `.woodpecker/test-database.yaml:45`, `.woodpecker/test-fixtures.yaml:43`, `.woodpecker/test-rest.yaml:50`, `.woodpecker/test-server-scanner.yaml:42`, `scripts/ci_remote.py:76`, `scripts/tests/test_check_toolchain_versions.py` (fixture literals at :85-159). **Docs mentioning the pin:** `CLAUDE.md:180`, `Makefile.local.example:21`, `.github/codeql/README.md:127,142`, `agents/go-specialist.md:15`, `skills/project-context/SKILL.md:52`. Also `.standards/instructions/go.md` (git submodule, empty in the analysis worktree so not read here; if it lists the patch, that is a separate PR to `falkcorp/.github`). Plus `changelog.d/<new>.md` | `python3 scripts/check_toolchain_versions.py`; `python3 -m pytest scripts/tests/test_check_toolchain_versions.py`; `make ci`; Woodpecker green on amd64 and arm64 | revert the commit | S |
| P2 | `ci(toolchain): drift-check Woodpecker, ci_remote.py and the workflow patch` | `scripts/check_toolchain_versions.py`, `scripts/tests/test_check_toolchain_versions.py`, `.github/workflows/ci.yml` (`go-version: '1.27'` → `'1.27.2'` at :47, :125, :189, :265, :338, :464, :529; r4 F24), `changelog.d/<new>.md` | new test cases: a mismatched Woodpecker `GOTOOLCHAIN`, a mismatched `go_image` digest, a mismatched `ci_remote.py` constant, and a minor-only workflow `go-version:` each fail | revert | S |
| P3 | `feat(ops): pprof tag in the deploy build; committed Makefile owns the build flags; fix stale debug example` | `Makefile` (`GO_BUILD_TAGS`, `GO_LDFLAGS` variables; `build`, `build-api`, `build-linux` :143-150 use them), `Makefile.local.example` (`deploy` and `deploy-debug`, :107-116, call `make build-linux`), `pprof_debug.go` (own mux, see S2), `docs/BUILD_TAGS_GUIDE.md`, `changelog.d/<new>.md`. The owner mirrors the change in the private `Makefile.local` once (D37 Q7) | `go build -tags "pprof" .`; start the binary without `ABK_PPROF_ADDR` and confirm no listener (`lsof -i :6060` empty); with it set, `curl localhost:6060/debug/pprof/goroutineleak?debug=1`; grep asserts no `http.DefaultServeMux` use outside `pprof_debug.go` | revert; the listener is opt-in anyway | S |
| P4 | `feat(diag): always-on flight recorder with watchdog snapshots` | new `internal/diag/flight/flight.go`, `internal/diag/flight/flight_test.go`; `internal/operations/registry/watchdog.go` (`watchdogCycle`, :55, the stuck / never_reported strike branch); `internal/server/search_reconciler.go` (`checkSearchIndexStall`, :473, the `return true` path); `internal/server/server.go` (start/stop near `bgCtx` at :599); new `internal/server/handlers/diagnostics_traces.go`; `internal/server/wire_handlers.go` (route registration; r4: verified this is where the diagnostics routes are wired, there is no separate diagnostics wiring file); `web/src/services/api.ts` only if a UI link is wanted (optional, defer); `changelog.d/<new>.md` | unit: snapshot writes a file `go tool trace` can open; rate limit; max-files rotation; concurrent `Snapshot` calls return without blocking; watchdog test asserts a snapshot is requested on a stuck strike (a fake clock already exists: `livenessClock`); a coexistence test runs the recorder and hits `/debug/pprof/trace?seconds=1` under the `pprof` tag (the pkg doc is ambiguous on whether `trace.Start` and an active recorder coexist); CPU and RSS gate, before/after, on a scan of a fixture library | env `ABK_FLIGHT_RECORDER=off`, or revert | M |
| P5 | `refactor(go): go fix batch 1 (any, forvar, minmax, errorsastype, reflecttypefor, stringscut*, stringsbuilder, stditerators, inline)` | **80 files**, listed in [appendix: gofix-batches.md §P5](06-bleeding-edge/gofix-batches.md); re-run at PR time | `make ci`; `go vet ./...`; `-race` on touched packages | revert | M |
| P6 | `refactor(go): go fix batch 2 (rangeint, mapsloop, slicescontains, slicesbackward, stringsseq, embedlit)` | **220 files**, [appendix §P6](06-bleeding-edge/gofix-batches.md); can be split by top-level package if review load is too high | as above | revert | M-L |
| P7 | `refactor(go): go fix batch 3 (newexpr)` | **67 files**, all `_test.go`, 381 hunks, [appendix §P7](06-bleeding-edge/gofix-batches.md) | as above; review that every `new(expr)` replaced a local `ptr`-style helper, then delete now-unused helpers in the same PR | revert | M |
| P8 | `refactor(go): go fix batch 4 (waitgroupgo, testingcontext)` | **29 files**, [appendix §P8](06-bleeding-edge/gofix-batches.md) | `-race -count=3` on touched packages (concurrency shape changes) | revert | M |
| P9 | `build(web): typecheck with TypeScript 7, keep TS 6 for lint` | `web/package.json` (the two aliases and the four scripts in S5), `web/package-lock.json`, `Makefile` (`web-typecheck` target calling `npm run typecheck --prefix web`; `make build` keeps calling `npm run build`), `.github/workflows/frontend-ci.yml` only if needed: it delegates to `falkcorp/github-common/.github/workflows/reusable-ci.yml` (line 64), so which npm script it runs must be read there first, `changelog.d/<new>.md` | `time npm run typecheck` before/after in the PR body (8.9 s → about 1.6 s expected); seeded-error control on both `tsc` and `tsc6`; `npm run lint` still passes on TS 6 (`node -e "console.log(require('typescript').version)"` in `web/` prints 6.0.2); `ls web/node_modules/.bin/tsc web/node_modules/.bin/tsc6` both exist | revert package.json/lock | S |
| P10 | `feat(web): react-router 8` | the 95 files in [appendix: react-router-files.md](06-bleeding-edge/react-router-files.md), `web/vite.config.ts`, `web/package.json`, `web/package-lock.json`, `changelog.d/<new>.md` | `npm run build`; `vitest run`; Playwright e2e chromium + webkit; the exact-file chunk check from the `vite.config.ts` comment | revert | M |
| P11 | `test(web): Vitest 5` | `web/package.json`, `web/package-lock.json`, `web/vitest.config.ts`, (coordinator: the shadowed test block in web/vite.config.ts is deleted by 01 P7, not here), `changelog.d/<new>.md` | full `vitest run` timed before/after; then a second commit enabling `fsModuleCache` timed again | revert | S-M |
| P12 | `perf(go): PGO for deploy builds` | new `pgo/prod.pprof` (merged CPU profiles; committed as a plain file, **not LFS**, because CI never fetches LFS and the LFS budget is exhausted per project memory), `Makefile` (`build-linux`: `-trimpath -pgo=pgo/prod.pprof`), `Makefile.local.example` (same flags on deploy), `docs/BUILD_TAGS_GUIDE.md` (one section on PGO), `changelog.d/<new>.md` | A/B: the same op (e.g. a scan of a fixture library) timed on PGO vs non-PGO binaries; CPU profile diff. Before the first commit, `go tool pprof -raw` on the profile and grep it for home-directory paths: the deploy build must use `-trimpath` first, or the profile carries the builder's absolute paths into a public repo | build with `-pgo=off` | S |
| P13+ | `refactor(web): useTransition for loading flags (N files)` | first batch: the compiler short-list files in `docs/react-compiler-adoption.md` that also have a boolean-reset `finally` (`src/components/ChangeLog.tsx`, `src/components/TagComparison.tsx` confirmed by grep; the rest checked at PR time); then batches of ≤10 files from the 61 | vitest for each file; compiler logger count before/after recorded in `docs/react-compiler-adoption.md` | revert per batch | M each |
| P14+ | `test(go): synctest for sleep-based tests (package X)` | one package per PR, in the order of [appendix: synctest-candidates.md](06-bleeding-edge/synctest-candidates.md) (first: `internal/realtime/events_test.go` 9 sleeps; `internal/operations/registry/{scan_standdown_grace,retry,resume,coverage}_test.go` 7 each) | `go test -count=20 ./pkg/...` before/after: wall time and flake count | revert | S-M each |

Order: P1 → P2 → P3 → P4 (P4 can start in parallel with P3; it does not need the
tag) → P5..P8 sequentially (each touches many files; never two at once) → P12 after
P3 has been live long enough to collect profiles. Frontend: P9 → P10 → P11 → P13+,
independent of the Go chain.

*(r4)* **D50 binds P5–P8:** they run only inside freeze window F, after 01's
tier-2 sweep, and each batch's file list is regenerated with `go fix -diff` at
PR time; the appendix lists are the 2026-10-08 census, not the PR's input. D40
keeps `omitzero` out of every batch (`-omitzero=false`).

## 5. Risks and what must not break

- **P1:** a digest refresh that names an image for the wrong arch breaks the
  arm64 Woodpecker runners (`rpiserv*` are arm64); use the manifest-list digest, as
  the existing pins do.
- **P3:** the pen-test HIGH-1 finding behind `pprof_debug.go:16-21` must stay
  closed: the listener stays opt-in, loopback by default, and no unit file or
  `.env` template sets `ABK_PPROF_ADDR`.
- **P4:** trace files land under `/var/lib`. Prod's datasets are ZFS with
  snapshots, so files we rotate away still occupy space until the snapshot expires.
  Keep `MaxBytes` and the file cap small; never add a "clean up /var/lib" step.
  The snapshot path must never block the watchdog goroutine: `WriteTo` runs in its
  own goroutine.
- **P5-P8:** must not touch `internal/writeback/` (hard ban; the default-tag
  census found 0 files there, the PR re-checks). Must not regenerate mocks by hand;
  `make mocks-check` stays green. `waitgroupgo` and `testingcontext` change
  goroutine and context lifetimes; review every hunk.
- **`omitzero`:** excluded. A changed JSON field would reach AudioBooth, which is
  not in CI.
- **P9:** the build must fail on type errors exactly as before; the seeded-error
  control is part of the PR. ESLint must stay on TS 6.
- **P10:** the 2026-06 Vite 8 incident (React error #130) came from duplicated
  React modules across chunks; run the exact-file chunk check the
  `vite.config.ts` comment demands.
- **P12:** PGO changes inlining and so stack traces; a bad profile cannot break
  correctness but can regress speed, so A/B it. Each new profile invalidates the
  whole build cache for that build mode: on the Mac (cache incidents up to 255 GB,
  memory note) keep PGO off for dev, test and CI and on only for deploy/release.
- Nothing here runs audio decoding on the server, touches iTunes, deletes
  `book_file` rows, splits the scan ConcurrencyKey or uses `go work`.

## 6. Dependencies on other workstreams

- **01 (legacy):** F13 (shadowed `test:` block in `web/vite.config.ts`) is dead
  config and belongs to 01's inventory; P11 can delete it if 01 agrees. The `go fix` batches overlap any 01 PR that edits the same Go files, so the coordinator should serialize them. P5-P8 touch 80, 220, 67 and 29 files, and they overlap each other. The overlap counts are at the end of the appendix.
- **04 / 05 (operations):** P4 edits `internal/operations/registry/watchdog.go`. If
  operations v3 (05) replaces the watchdog, the snapshot hook moves into the v3
  liveness checker as a requirement, not an afterthought.
- **07 (design):** owns where `internal/diag/flight` lives and whether diagnostics
  endpoints get a dedicated admin surface. 07's new CI-throughput set (07 C1–C3)
  changes `.github/workflows/ci.yml`, which P2 also edits (the `go-version` patch
  pin); P2 goes after 07 C1. 07 appendix C picks the server-state library
  (TanStack Query v5) that 07 §3.5 said 06 would choose; 06 has no separate
  opinion.
- **02 (search):** the search-index stall hook (P4) and any memdb interning
  (Assess, rank 13) touch 02's area.

## 7. Open questions for the owner

1. **Ship the `pprof` tag in every deploy build (listener still off unless the env
   var is set)?** Recommended: **yes.** It costs nothing at runtime and is the only
   way to get PGO profiles and the 1.27 goroutine-leak profile from prod.
2. **Drop `-gcflags=all=-N -l` from `deploy-debug`?** Recommended: **yes**, unless
   you attach delve to prod; if you do, keep a separate `deploy-delve` target.
3. **Where does the PGO profile live?** Recommended: commit `pgo/prod.pprof` as a
   plain file (not LFS), built from a `-trimpath` binary, with a short README next
   to it saying when and how it was captured. Refresh it quarterly. After
   `-trimpath` it holds function and module paths only, but grep it for host paths
   before every commit (public-repo rule).
4. **Flight-recorder budget:** 32 MiB ring, 10 files on disk? Recommended:
   **yes**, revisit after a week of RSS data.
5. **TS 7 now (side by side) or wait for 7.1 (2026-11-24)?** Recommended: **now**;
   the side-by-side setup is Microsoft's documented path, and the 5.4x typecheck
   saving applies to every build and agent run.
6. **`omitzero`:** leave out of all sweeps? Recommended: **yes**; adopt per field
   only where an empty value on the wire is a bug.
7. **Should the committed `Makefile` own the deploy recipes?** Today the real
   `deploy`/`deploy-debug` live in your private `Makefile.local`, and the
   committed example has drifted (F2). Recommended: move the generic parts
   (build flags, tags, `-trimpath`, `-pgo`) into committed `Makefile` targets
   that both call, and keep only the host and ssh details private. Otherwise every
   build-flag PR here needs you to hand-edit the private file.
