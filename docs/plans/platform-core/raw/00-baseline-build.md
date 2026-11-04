<!-- file: docs/plans/platform-core/raw/00-baseline-build.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4ee88860-62b7-475d-9e76-5dcf92eb5c1d -->
<!-- last-edited: 2026-11-04 -->

# 00 — Baseline build and module-layout experiments

All experiments ran in a throwaway detached worktree of `748fd787a` that was never committed.
Module-cache paths are shown as `<gomodcache>/`. Machine: 4 CPUs, Linux amd64, CGO enabled.
Run dates 2026-11-03/04. Each experiment started from `git reset --hard HEAD && git clean -fdx`.

> **Discarded runs.** An earlier batch (labelled E2b/E6 in working notes) used a reset step
> (`git checkout -- .`) that does not undo staged `git mv` renames, so those runs started from a
> half-moved tree. Their results are **not used anywhere**. Every result below comes from a run whose
> starting tree was verified clean (0 changed files at `748fd787a`).

## Control (prompt §2.1: build and vet unmodified HEAD first)

| ID | Toolchain | Command | Exit |
|---|---|---|---|
| E0a | `go1.27.0` (`GOTOOLCHAIN=auto`, selected by the `go 1.27.0` directive) | `go build ./...` | 0 (3m58s cold cache) |
| E0a | `go1.27.0` | `go vet ./...` | 0 (5m43s) |
| E0b | `go1.27.2` (the repo pin, `Makefile:43`, `.envrc:10`) | `go build ./...` / `go vet ./...` | 0 / 0 |
| E0c | `go1.27.3` (latest stable, per module-proxy toolchain index; see `15-dependencies.md`) | `go build ./...` / `go vet ./...` | 0 / 0 |

Baseline is **green** on all three toolchains, so the experiments below are meaningful.

## E1 — reproduce the CLAUDE.md `go.work` claim

```
$ GOTOOLCHAIN=go1.27.2 go work init .        # exit 0
$ GOTOOLCHAIN=go1.27.2 go build ./...        # exit 1
<gomodcache>/google.golang.org/grpc@v1.85.0/status/status.go:35:2: ambiguous import: found package google.golang.org/genproto/googleapis/rpc/status in multiple modules:
	google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc
<gomodcache>/github.com/grpc-ecosystem/grpc-gateway/v2@v2.31.0/runtime/handler.go:13:2: ambiguous import: ... googleapis/api/httpbody ...
<gomodcache>/go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc@v1.47.0/client.go:14:2: ambiguous import: ... googleapis/rpc/errdetails ...
```

**Claim confirmed** (`CLAUDE.md`, "Do NOT run `go work init .`").

## E7 — root cause

```
$ go mod graph | grep ' google.golang.org/genproto@'
github.com/cockroachdb/errors@v1.14.0 google.golang.org/genproto@v0.0.0-20230410155749-daa745c078e1
$ go mod why -m google.golang.org/genproto
(main module does not need module google.golang.org/genproto)
```

The single requirer is `github.com/cockroachdb/errors@v1.14.0` (a Pebble dependency). Its `go.mod`
line 30 requires the monolithic `google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1`
(fetched from `https://proxy.golang.org/github.com/cockroachdb/errors/@v/v1.14.0.mod`). v1.14.0 is
the latest release (`@latest` → v1.14.0, 2026-06-18), so there is no upstream fix to take.
In single-module mode, module-graph pruning drops that requirement (no package needs it); in
workspace mode it is loaded, and both the monolithic module and the split
`genproto/googleapis/{api,rpc}` modules then provide the same package paths.

## E8 / E9 — does one `exclude` line fix it, with no side effects?

Added to the root `go.mod`:
`exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1`

| ID | Setup | Command | Exit | Notes |
|---|---|---|---|---|
| E8 | HEAD + exclude, single-module | `go mod tidy` | 0 | diff: `go.mod` +2 lines (the exclude and a blank line); `go.sum` **unchanged** |
| E8 | same | `go build ./...` / `go vet ./...` | 0 / 0 | |
| E9 | HEAD + exclude + `go work init .` | `go build ./...` / `go vet ./...` | 0 / 0 | the exact scenario `CLAUDE.md` warns about now works |

## E2 / E2c — nested core module with `require` + `replace`

Setup: create `core/go.mod` = `module github.com/falkcorp/mediacore` / `go 1.27.0`; `git mv`
`internal/keyedmutex` → `core/keyedmutex` and `internal/telemetry` (excluding its `contract/`
sub-package) → `core/telemetry`; rewrite imports; in the root:
`go mod edit -require=github.com/falkcorp/mediacore@v0.0.0-00010101000000-000000000000 -replace=github.com/falkcorp/mediacore=./core`.
`internal/telemetry` was chosen because it imports otel + grpc exporters (the genproto-sensitive
graph); `keyedmutex` as a pure leaf. Both have zero in-module imports in non-test code.

| Step | Command (dir) | Exit | Notes |
|---|---|---|---|
| core tidy | `go mod tidy` (`core/`) | 0 | `core/go.mod` does **not** mention the app module |
| core build/vet | `go build ./...` / `go vet ./...` (`core/`) | 0 / 0 | |
| core test | `go test -count=1 ./...` (`core/`) | 1 | `keyedmutex` ok; `telemetry` **`TestRatchet_ScansRealTree`: "scanned only 0 .go files; the walk is not reaching the tree"** — a ratchet test that walks the *repo's* source tree by relative path. It is an ABO repo-policy test, not a telemetry unit test; it must stay in the root module (finding F3). |
| root tidy | `go mod tidy` (root) | 0 | `go.mod` +8/−5 (require + replace; otel/grpc direct deps demoted to indirect); no monolithic genproto in `go.sum` |
| root build/vet | `go build ./...` / `go vet ./...` (root) | 0 / 0 | |
| root test | `go test -count=1 ./internal/telemetry/...` | 0 | the `contract` package left behind in the root passes against core |
| `./...` reach | `go list ./... \| grep -c mediacore` (root) | — | **0**: root `./...` does not include `core/` (finding F4) |
| -mod=readonly | `GOFLAGS=-mod=readonly go build ./...` (root, E5) | 0 | `go.sum` complete |
| layering guard | `go test ./internal/arch/ -count=1` (root, E4) | 1 | `layerOf lists "internal/keyedmutex", which is not a package in the module` (and the same for `internal/telemetry`, `internal/telemetry/contract` in the first, uncareful E2). Exactly the predicted break (`03-layering-guard.md` §3) |
| E10 workspace | root exclude + `go work init . ./core`; `go build ./... ./core/...`; `go vet ./... ./core/...` | 0 / 0 | workspace mode over both modules works once the exclude is present |

### E2 (first attempt) — the hazard it exposed

In the first attempt the whole `internal/telemetry` tree, including `contract/` (whose
`series_contract_test.go:51` imports `internal/ai`), was moved. `go mod tidy` in `core/` then
**silently resolved `github.com/falkcorp/audiobook-organizer v0.222.2` from the public module proxy**
and added it to `core/go.mod` (122 lines), and `go vet` failed with
`use of internal package github.com/falkcorp/audiobook-organizer/internal/ai not allowed`. Go permits
module dependency cycles, so nothing but an explicit check stops core from depending on the app
(finding F2).

## Findings

| ID | Finding | Consequence for the spec |
|---|---|---|
| F1 | `go.work` fails only because of one stale transitive requirement (`cockroachdb/errors@v1.14.0` → monolithic genproto 2023-04-10); one `exclude` line in the root `go.mod` makes workspace mode build and vet green with no `go.sum` change. | The nested-module + `replace` mechanism needs no `go.work`; a `go.work` becomes an **optional** developer convenience once the exclude lands. The `CLAUDE.md` warning becomes "add the exclude before using go.work" (filed as follow-up; outside this run's write fence). |
| F2 | `go mod tidy` in `core/` will add the app module from the proxy if any core file (including a test) imports app code. | Rule R1 needs `archtest.TestNoAppRequire` (reads `core/go.mod`) in addition to the import check. |
| F3 | Some tests walk the repo tree by relative path (e.g. telemetry's ratchet test). | Each package move must classify its tests: unit tests move; repo-policy ratchets stay in the root (or take an explicit root). |
| F4 | `./...` from the root excludes `core/`. | Every Go-running Makefile target and CI job iterates `GO_MODULE_DIRS := . core`. |
| F5 | The layering guard fails on stale `layerOf` entries as soon as one package moves. | The guard redesign (spec 03 §3.5.1) is the first code task of the extraction, before any package move. |

COMPLETED: 12 experiments with valid results — E0a, E0b, E0c, E1, E2 (first attempt, hazard run), E2c, E4, E5, E7, E8, E9, E10
REMAINING: 0 —
BLOCKED: 0 —
