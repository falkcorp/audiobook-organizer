<!-- file: docs/specs/platform-core/03-architecture.md -->
<!-- version: 1.15.9 -->
<!-- guid: 8af6d935-7143-4d06-8562-b04a18504d81 -->
<!-- last-edited: 2026-11-05 -->

# 03 — Architecture

This section is **normative**: every other spec file uses the names, paths and rules defined
here. If another file disagrees with this one, this one wins until it is amended.

**How to read the rules.** Finding and resolution IDs in parentheses (`R4-1`, `SEC6-3`, `A-2`) are
provenance, not part of a rule. Provenance-free normative digests, one per release, are the first artefact of
Phase 6, under `docs/plans/platform-core/digests/` (ADV-12). Counts quoted from the evidence commit (572
routes, 269 op IDs, 213 packages and the like) are **informative**; a done-check or test that asserts a
count generates it at its PR's base commit (ADV-11).

**Owner decisions D10–D13 (02 §2.6) govern everything below.** There is no backward compatibility: stored
formats, key names, HTTP paths, metric names, config keys, env names and CLI flags may change, and the dashboards,
rules, scripts and skills that name them change in the same PR. The one frozen external contract is the
Audiobookshelf-compatible API of the phone apps (§3.8 "The frozen contract"). The extraction ships in four releases,
**R0–R3** (§3.8); R2 is the one-way cutover in which every core-service family moves from ABO's HEAD rows into its
native `c/<subsystem>/` keyspace, through books-run migration steps that call each subsystem's **importer** (§3.3
"Store interfaces"). Native stores have the hardened design from their first write, ABO's included.

Evidence commit: `748fd787a`. Raw data: [`docs/plans/platform-core/raw/`](../../plans/platform-core/raw/00-index.md).
Round-1 review experiments X1–X8 are in
[`reviews/spec-round-1-modules-build.md`](../../plans/platform-core/reviews/spec-round-1-modules-build.md)
("Experiments run for this review"); round-2 experiments X9–X15 are in
[`reviews/spec-round-2-modules-build.md`](../../plans/platform-core/reviews/spec-round-2-modules-build.md);
X16 was run for the round-2 revision (§3.2.1). All were run in scratch directories, never in the repo.

## 3.1 Three layers

| Layer | What it is | Where it lives (in-repo phase) | Where it lives (after split) |
|---|---|---|---|
| **Core** | Platform services every app needs: storage, tasks, events, config, secrets, logging, metrics, HTTP framework, realtime, authN, RBAC, audit, library roots, integration contracts and adapters, backup, updater, health, UI shell. | Nested Go module at `core/`, module path **`github.com/falkcorp/mediacore`**, plus npm workspace `core/web/` | Its own repo `falkcorp/mediacore` |
| **SDK** | The *public, versioned subset* of core that an app is allowed to import: `module`, the capability contracts, `tasks`, `storage` (with its leaves `storage/keys` and `storage/codec`), `httpapi`, `rbac`, `config`, `events`, `log`, `metrics`, `coretest`, `archtest`, the store conformance-suite packages `<pkg>/<pkg>test` (§3.3), `entity`, `lifecycle`, `version`, `integrations/*`, and the npm packages `@mediacore/shell`, `@mediacore/api-client` and `@mediacore/ui-kit`. The SDK is not a separate module; it is the set of core packages **outside** `core/internal/` and `core/kernel/`, and is what the API-stability gate (§12) measures. Packages marked **deferred** in §3.3 are SDK paths whose contract is specified but not yet built. | same module | same module |
| **App** | A deliverable product = one `main` + one or more **modules** (a module is a self-contained domain: manifest, routes, jobs, migrations, permissions, settings schema, UI contribution, adapters). ABO is the first app; its domain becomes module `books` (§3.6). | Root module `github.com/falkcorp/audiobook-organizer` (this repo) | This repo; future `falkcorp/tv-*`, `falkcorp/movies-*` repos |

"Plugin" is **not** used as an architectural term in this spec (the 2026-09-18 evaluation's
guardrail, `raw/08-prior-art.md` §1). The words are: *core*, *module* (a compiled-in domain),
*adapter* (an implementation of a capability contract, e.g. the Deluge download client),
*app* (a shipped binary).

## 3.2 Module mechanism (D3: in-place module first)

The core is a **nested Go module with a `replace` directive**, under the module path it will keep
after the repo split:

```
core/go.mod:        module github.com/falkcorp/mediacore
                    go 1.27.0            # same directive as the root; no toolchain line
                    exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1
                    ignore ./web                 # npm packages ship .go files (see go.mod:166)
                    ignore ./examples/hello/web
./go.mod (root):    require github.com/falkcorp/mediacore v0.0.0-00010101000000-000000000000
                    replace github.com/falkcorp/mediacore => ./core
                    # no exclude: the genproto exclude lives only in core/go.mod (item 1)
```

Why this mechanism, and why this path:

1. **The build never depends on `go.work`.** Re-tested at HEAD (`raw/00-baseline-build.md` E1):
   `go work init .` fails with `ambiguous import` exactly as `CLAUDE.md` says. Root cause (E7): the
   single stale requirement `github.com/cockroachdb/errors@v1.14.0` → monolithic
   `google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1`, which module-graph pruning hides
   in single-module mode and workspace mode loads. v1.14.0 is the latest release, so there is no
   upstream fix. One `exclude` of that version makes workspace mode build and vet green with no
   `go.sum` change. **Where the exclude lives** was settled by two rounds of experiments:
   - E8–E10 put it in the **root** `go.mod`; with E2c's small core (no Pebble) that made
     `go work init .` and `use . ./core` green.
   - Round-1 X2: once core holds Pebble (`storage`) *and* the OTLP/grpc exporters (`metrics`) —
     13 §13.5.2's kept set — a workspace created inside `core/` (or core plus another module, or
     post-split `use . ../mediacore`) fails with the same ambiguous import even with the root
     exclude in place, because a workspace only applies the excludes of its own member modules.
   - Round-1 X3: with the exclude **only in `core/go.mod`**, `use . ./core` builds and vets green, a
     workspace inside `core/` is green, and the root's single-module build and `go mod tidy` are
     unchanged (a dependency's exclude is ignored outside workspace mode).

   Therefore: `require` + `replace` is the mechanism (it works with no workspace, E2c); the exclude
   lands in **`core/go.mod`** in the first core PR of R1 (§3.8) and in no other `go.mod`, so the root — and every future app
   repo — keeps a `go.mod` that `go install …@version` accepts after the split (item 5). It is needed
   from that first PR although core's own graph reaches `cockroachdb/errors` only later in R1, when Pebble enters
   core (04 §8.1): the supported workspace pulls genproto in through the root, and a workspace applies
   its members' excludes. The
   supported developer workspace is **`go work init . ./core`**, uncommitted (`go.work` and
   `go.work.sum` are gitignored, `.gitignore:36-37`); a workspace that uses the root without
   `./core` is unsupported (it reproduces E1). No Makefile target or CI job builds through a
   `go.work`, except the throwaway workspaces of the check below. **While ABO's `CLAUDE.md` forbids
   `go work init` in the repository and its worktrees, nobody creates this workspace there** (CLAUDE.md wins over
   this file): ABO builds in single-module mode with `require` + `replace`, developers accept gopls' workspace
   warnings and verify with `go build`/`go vet`, and the only workspaces that exist are the check's throwaway ones,
   outside the repository. The developer workspace becomes usable in a checkout only once 12 §12.8.2 step 10 has
   changed that guidance.

   **The exclude check.** Probing the live graph cannot work: an excluded version is dropped from the
   module graph, so `go mod graph` never lists it while the exclude is present (round-2 X10), and a
   requirer edge alone does not prove the exclude is load-bearing (X16: a workspace whose build list
   holds no split `genproto/googleapis/*` module builds green without it). So
   `core/scripts/ci/check_genproto_exclude.py` (a core-owned script, 12 §12.3.11) works on a temp copy
   of the checkout, made in a temporary directory outside the repository and every worktree (`mktemp -d`),
   never in the checkout itself, and fails unless all of these hold:
   - **present**: `core/go.mod` carries exactly the exclude line above, and no other `go.mod` in the
     tree carries a genproto exclude (otherwise it reports *missing* or *misplaced*);
   - **green with it**: every probed workspace builds — root mode `go work init . ./core && go build
     ./... ./core/...`, and core mode `(cd core && go work init . && go build ./...)`;
   - **load-bearing without it**: after `go mod edit -dropexclude` in the copy, **at least one**
     applicable probe's `go mod graph` lists an edge `<requirer> google.golang.org/genproto@v0.0.0-20230410155749-daa745c078e1`
     (the script prints the requirer, today `github.com/cockroachdb/errors@v1.14.0`) **and** that
     probe's `go build` fails with `ambiguous import`. If no applicable probe does both, the exclude is
     **stale** and the check fails, naming the requirer that disappeared. (A probe that is not
     applicable, or that builds green without the exclude, is reported, not failed.)

   The check has two targets, because one of its modes needs the root. **Core mode** —
   `make -C core exclude-check` — runs the core-mode probe and the *present/misplaced* scan of `core/`
   only, so it runs in core's merge gate and from an export of `core/` alone (12 SC4(a)). It applies once
   `(cd core && go mod graph)` in the copy without the exclude lists the requirer (once Pebble is in core, R1); before that it
   reports "not applicable", which is correct, because until then only the root can pull genproto in.
   **Root mode** — the root `make ci` job `genproto-exclude-root` — runs the root-mode probe and the
   tree-wide "no other `go.mod` carries a genproto exclude" scan; it is a root-side job, dropped at
   cutover step 6 (12 §12.8.2). Both targets run from the first core PR (09's R1 table lists them as
   two rows), so the check holds from that PR on.
2. **The future path from day one.** Because every import already says `github.com/falkcorp/mediacore/...`,
   the repo split (§12) is a directory move plus deleting one `replace` line — no import rewrite in
   any app. The alternative (`github.com/falkcorp/audiobook-organizer/core`) would force a codemod
   across every app at split time and a second module path in history.
3. **Go's `internal/` rule does part of the enforcement for free.** Packages under
   `core/internal/...` have import path `github.com/falkcorp/mediacore/internal/...`, which is
   importable only from `github.com/falkcorp/mediacore/...`; the ABO root module cannot import them.
   The rule is path-based, not module-based: any package whose path is under
   `github.com/falkcorp/mediacore/` may import `internal/` (X5), so `examples/...` is guarded by
   `archtest` instead (R2).
4. **Cost accepted: two modules in every build path.** `go build ./...` / `go vet ./...` /
   `go test ./...` from the repo root do **not** descend into `core/` (a nested `go.mod` ends the
   `./...` pattern). A single Makefile variable `GO_MODULE_DIRS := . core` drives the targets that are
   genuinely per-module (build, vet, test, staticcheck, `fmt-check`, and `tidy-check` = `go mod tidy
   -diff`). `go fix -diff` is **core-only** (core's `fix-check`, 13 §13.3); the root has no `go fix`
   gate and gains none. Per-module does **not** mean "run everything twice": each of the 13 `make ci`
   prerequisites (`Makefile:785`) and every CI job is classified as per-module, root-only or
   core-only, with per-module baseline files (`core/.errcheck-baseline`,
   `core/.interface-width-baseline`; core's errcheck and interface-width ratchets, `mocks-check`,
   `pprof-check` and the `core/web` tests run in core's own `ci` target, with their scripts under
   `core/scripts/ci/`, so an export of `core/` and the post-split repo keep them, 12 §12.3.11), a core coverage **ratchet floor** in core's merge gate (the
   70 %/60 % thresholds of 12 SC5 are a readiness report, not a merge gate, 12 §12.3.11), a core
   mockery config, and core gates registered in `ci/gates.txt` with a `core-job` value. Core's
   deadcode ratchet is **not** per-module: deadcode reports what no `main` reaches, and a library's
   exported API is reached by apps, not by core's own two mains (the root excludes its own `pkg/`
   tree for the same reason, `scripts/deadcode-ratchet.py:64`); it runs from the root over ABO's and
   hello's mains, filtered to `github.com/falkcorp/mediacore/(internal|kernel)`. The classification
   table and the checklist (shard weight files, `.mockery.yaml`, `tygo.yaml`, `ci/gates.txt`
   rows such as `store-width`/`store-references`) are owned by 09's R1 table.

   **Tidy coupling.** With `replace => ./core`, the root's `go.mod` and `go.sum` must carry the
   `// indirect` requirements and `go.sum` entries of every module that core's imported packages
   need. A core-only `go mod tidy` therefore builds green in core and leaves the root red
   (round-2 X11: `missing go.sum entry`). Rule: **a PR that changes `core/go.mod` carries the root
   `go mod tidy` result in the same PR.** Root `make ci` runs `tidy-check` in both modules, and
   `core-ci.yml` also runs the root `go build ./...` whenever `core/go.mod` or `core/go.sum` changes
   (12 §12.3.11).

   Build entry points outside Make must also learn about `core/`:
   - **Docker.** `Dockerfile:54-55` and `Dockerfile.build-cgo:52-53` copy only `go.mod go.sum` and run
     `go mod download && go mod verify`; with the `replace` and no `core/go.mod` in that layer both
     exit 1 (X1). Both Dockerfiles copy `core/go.mod core/go.sum` into `./core/` before
     `go mod download`; `.dockerignore` adds `**/node_modules` and `core/web/packages/*/dist`. Today it
     excludes far more than build output (`.dockerignore:5-32`): `.git`, `.github`, **`docs`** (`:11`),
     root-level `*.md` (`:30`; the pattern has no `**/`, so a Markdown file below the root stays in the
     context), `testdata`, `dist`, root `node_modules`,
     `web/node_modules` and `web/dist`, among others. So **every `//go:embed` input of the root module and
     of core lies inside the Docker build context or is produced in the image by a declared earlier
     stage**. The second case exists on purpose: under `embed_frontend`, `web_embed.go:11`
     embeds `web/dist`, which `.dockerignore:20` excludes, and the Go stage receives it from
     `COPY --from=frontend-builder /build/web/dist ./web/dist` (`Dockerfile:68`; MOD6-3). The in-image
     outputs are **one committed list**, `scripts/ci/embed_build_outputs.txt` (`web/dist` from R1; with the shell
     each app's `web/dist`, for ABO still that one path, 07 §7.6.3), which the check reads and compares with
     the Dockerfiles' `COPY --from` lines **into a stage that runs `go build`** (the `go-builder` stages)
     whose destination lies under that stage's `WORKDIR`: a listed path with no such line, or such a line
     with no listed path, fails. A runtime-stage copy of the binary (`Dockerfile:90` into `/app/`,
     `Dockerfile.build-cgo:75`) is not an embed output and is outside the comparison; the check's fixture
     test carries one, and it must pass (MOD7-5). Hello's `core/examples/hello/web/dist` is **not** on this
     list, because no ABO Dockerfile produces it: core's packages are judged against **core's own list**,
     `core/scripts/ci/embed_build_outputs.txt` (hello's output from the shell work; 07 §7.6.3), which only tolerates an
     unbuilt output and has no Dockerfile cross-check, because core builds no image. Core's own `ci` target
     runs that half from an export of `core/` alone (`core/scripts/ci/check_embed_outputs.py`, 12 §12.3.11;
     MOD7-6). A path filter cannot check this rule, because the change that breaks it (a new
     `//go:embed docs/x.json` directive) touches only a `*.go` file. The check is
     `scripts/ci/check_embed_context.py`, which runs in root `make ci` on every PR: it runs `go list -e
     -json` over the root module and over core for every (GOOS, tag set) of §3.5.1 item 4's one list,
     takes every package's `EmbedFiles` and fails when one of them matches a `.dockerignore`
     pattern (moby pattern semantics) and is not a listed in-image output. A `pattern …: no matching files
     found` package error for a listed output (a tree with no frontend build; for a core package, an
     output on core's list) is ignored; the same error for any other path fails. The pattern is normalised before
     that comparison, the same way in `check_embed_context.py` and `check_embed_outputs.py`: an `all:` prefix is
     stripped and the pattern is joined with the package's directory relative to its module root, so hello's
     `pattern all:web/dist` in package `examples/hello` matches core's entry `examples/hello/web/dist`; a fixture
     carries an `all:` pattern in a non-root package (MOD8-11). 09's R1 table owns its done-check (MOD5-7). The PR image job, which also fails on
     an unlisted excluded input ("no matching files found"), is the backstop.
     `.dockerignore` keeps its exclusions. A PR job builds the image. These land in the same PR as the `replace` (R1). At that point core holds only the
     stdlib-only `archtest` engine (`internal/arch/layering_test.go:20-31`), and `go mod tidy` writes
     no `go.sum` for a module without requirements (round-2 X12), so that PR commits an **empty
     `core/go.sum`** (tidy keeps it) and `check_module_skew.py` asserts that it exists; the literal
     `COPY core/go.mod core/go.sum` then never fails.
   - **Web.** Once `web/` depends on `@mediacore/*` (whose `exports` point at built `dist/`), every
     `cd web && npm ci && npm run build` that does not first build `core/web` breaks. One
     implementation, `scripts/ci/build_web.sh` (core web build, then `web/`), is called by
     `make web-deps`, by every workflow and by both Dockerfiles; path filters gain `core/web/**`,
     `core/go.mod`, `core/go.sum`; Dependabot npm gains `/core/web`. The full list of entry points has one
     owner, 09 §9.1's list of `npm ci` sites (07 §7.3.2 cites it rather than keeping a second list;
     MOD5-13). The script does **not** generate the UI build ID: the npm build itself does, so every
     build path stamps it (§3.6 "Build identity", R5-7); `build_web.sh` only passes a caller-set
     `BUILD_ID` through.
   - **Core's own Makefile.** `core/Makefile` (12 §12.3.11) carries core's `ci` target (the merge
     gate) and its `readiness` target (report-only), so that core CI runs from an export containing
     only `core/` (12 SC4). Every script those targets call lives under `core/scripts/ci/`, so the
     export and the post-split repo carry it; the two checks that compare core with the root
     (`check_module_skew.py`, toolchain parity with the root pin) are root-side jobs dropped at cutover
     step 6. Core's toolchain pin is checked by the root toolchain guard until the split
     (13 §13.2.2).
5. **Cost accepted:** `go install github.com/falkcorp/audiobook-organizer@...` from outside the
   repo stops working while the `replace` exists: `cmd/go` refuses a providing module whose `go.mod`
   contains `replace` **or `exclude`** directives for `go install pkg@version` (X4). Because the
   exclude lives in `core/go.mod` (item 1), the root regains an installable `go.mod` as soon as the
   split drops the `replace` (12 §12.8.2 step 7). Nothing in the repo's release path uses that form
   (`raw/14-build-ci.md`: releases are built by goreleaser and the Docker builders from a checkout);
   this is listed in risks (14 K7).
6. **Cost accepted: dependency skew.** The root build takes the MVS maximum over the root and
   `core/go.mod`, while core CI tests only core's requirements, and Dependabot's gomod entry
   targets `directory: /` only (`.github/dependabot.yml:59-60`). So the binary can ship dependency
   versions core CI never ran. Mitigation: the gomod entry gains `directories: ["/", "/core"]` under the
   top-level `multi-ecosystem-group: dependencies` it already joins (`.github/dependabot.yml:8-20,59-64`;
   the repo uses no `groups` key, and `applies-to` is a single-valued key of a `groups` entry, so no one
   group could cover both update kinds), and `scripts/ci/check_module_skew.py`, which fails when any
   module path in `(cd core && go list -m all)` resolves to a different version in the root's
   `go list -m all` (12 SC4(c)). R1 verifies that version updates arrive as **one** PR spanning `/` and
   `/core`; if they do not, a workflow step runs the root `go mod tidy` on Dependabot `/core` PRs and
   pushes the result (tidy coupling, item 4). Security updates are opened per directory whatever the
   grouping, so a `/core` security PR is made consistent by that same root-tidy step and, failing that,
   caught by the blocking skew check. Dependencies that must not float are pinned by a Dependabot `ignore` plus
   `check_module_skew.py --pin` (13 §13.5.2). `check_module_skew.py` carries three guarantees (no skew,
   the empty `core/go.sum` of item 4 exists, the `--pin` pins hold), so it is a **blocking** root-side
   check: it runs in root `make ci` (and therefore in the merge requirement of §3.8: every merge passes `make ci`
   and e2e) from R1, not only as the reported readiness criterion 12 SC4(c). The root-tidy step pushes with the PAT,
   as HEAD's `auto-revert.yml` pushes, because a `GITHUB_TOKEN` push triggers no workflow and the PR's required
   checks would not re-run on the tidy commit.

### 3.2.1 Experiment results (control and mechanism)

Summarized from `raw/00-baseline-build.md` (E rows) and the round-1 modules-build review (X rows);
those files have exact commands and outputs.

| Experiment | Toolchain | `go build ./...` | `go vet ./...` |
|---|---|---|---|
| E0 control, HEAD unmodified | go1.27.0 (auto), go1.27.2 (pin), go1.27.3 (latest) | 0 / 0 / 0 | 0 / 0 / 0 |
| E1 `go work init .` at HEAD | go1.27.2 | **1** (`ambiguous import`, genproto) | — |
| E8 HEAD + **root** `exclude` of monolithic genproto | go1.27.2 | 0 | 0 |
| E9 E8 + `go work init .` | go1.27.2 | 0 | 0 |
| E2c nested `core/` (`internal/keyedmutex`, `internal/telemetry` minus `contract/`) + `require`/`replace` — in `core/` | go1.27.2 | 0 | 0 |
| E2c — in root | go1.27.2 | 0 | 0 (`-mod=readonly` build also 0) |
| E10 E2c + **root** exclude + `go.work use . ./core` | go1.27.2 | 0 | 0 |
| E4 E2 + unmodified `internal/arch` layering test | go1.27.2 | test **fails**: stale `layerOf` entries (predicted, §3.5.1) | — |
| X1 root `require`+`replace`; only `go.mod`/`go.sum` copied (the Dockerfile layer); `go mod download && go mod verify` | go1.27.0 (auto) | **1** (`reading core/go.mod: no such file or directory`) | — |
| X2 mock core with Pebble v2.1.7 + `otlptracegrpc` v1.47.0; root-only exclude; `go work init` inside `core/` | go1.27.0 (auto) | **1** (`ambiguous import`, genproto) | — |
| X3 exclude **only in `core/go.mod`**; `go.work use . ./core` | go1.27.0 (auto) | 0 | 0 |
| X3 — workspace inside `core/`; root single-module build and tidy | go1.27.0 (auto) | 0 / 0 | — |

Round-2 experiments (modules-build review X10–X12, and X16 run for this revision):

| Experiment | Toolchain | Result |
|---|---|---|
| X10 module requiring `cockroachdb/errors` v1.14.0; `go mod graph \| grep -c 'google.golang.org/genproto@'` before and after `go mod edit -exclude=…daa745c078e1` | go1.27.0 | 1 line without the exclude, **0 with it**: an excluded version never appears in the graph |
| X11 root `require`+`replace => ./core`; core adds a dependency; `go mod tidy` in core only; root `go build ./...` | go1.27.0 | **exit 1** (`missing go.sum entry`); a root `go mod tidy` adds the `// indirect` requirement |
| X12 module with no third-party imports: `go mod tidy`; then `touch go.sum; go mod tidy` | go1.27.0 | no `go.sum` written; an empty committed `go.sum` is kept |
| X16 workspace `go work init . ./core`: root requires `cockroachdb/errors` v1.14.0, `core/go.mod` carries the exclude; then the exclude is dropped | go1.27.0 | workspace `go mod graph` works: **0** genproto lines with the exclude, **1** without (`github.com/cockroachdb/errors@v1.14.0 google.golang.org/genproto@v0.0.0-20230410155749-daa745c078e1`); `go build ./... ./core/...` exit 0 both ways, because this build list holds no split `genproto/googleapis/*` module — so a requirer edge alone does not prove the exclude is needed (item 1) |

E2c's package choice tested the mechanism, not a placement: [04](04-package-mapping.md) is
authoritative for where each package goes (it keeps `internal/keyedmutex` in ABO, and core gets its
own `core/internal/keyedmutex` copy in the PR in which a core package first needs one, 04 §8.1 (config, R2); the
`internal/telemetry/contract` suite and `internal/telemetry/ratchet_test.go` stay in ABO because they
import or walk ABO code).

Hazards the experiments exposed, each turned into a rule or plan task: core `go mod tidy` silently
adds the app module from the proxy if any core file — even a test — imports app code (R1(a), R1(b)
with test imports); repo-walking ratchet tests break when moved (they stay in the root); `./...` from
the root does not reach `core/` (item 4); the Docker module-download layer lacks `core/go.mod` (X1,
item 4); a root-only exclude does not cover workspaces created inside core (X2, item 1); the
`internal/` rule does not stop a package under the core path from importing core internals (X5,
item 3); the layering guard must be redesigned before the first move (§3.5.1).

## 3.3 Core package tree

All paths are relative to `core/` (import prefix `github.com/falkcorp/mediacore/`). **Public**
packages are the SDK. `kernel/` holds the composition root's machinery that apps never touch
directly; `internal/` holds implementations.

The **Build** column applies the promotion rule of §3.4: **ABO** = ABO consumes it in one of the releases
R0–R3 of §3.8 ([09](09-abo-on-core.md) §9.4); **core** = core itself needs it;
**deferred** = the package is described here and in its spec file as a **design sketch (non-normative)**
at proof depth (D5), the plan builds none of it, the SDK freezes none of it, and it is designed for real
and built when the first TV/Movies app starts (A-15).

| Package | Visibility | Build | Purpose | Spec |
|---|---|---|---|---|
| `app` | public | ABO | Composition root. `app.New(ctx, app.Options, ...module.Module) (*app.App, error)`; `(*App).Run(ctx) error`. The **only** way to assemble a process; `app.OpenOffline` (returning an `app.Offline`) is the app-side wrapper over `kernel.OpenOffline`, the read-write open for offline commands (every CLI write subcommand, `restore-offline` among them, §3.8). It runs the verdicts a serve boot runs — the staged swap, the per-owner level guard, the upgrade floor — and refuses every store a serve boot would refuse (06 §6.5.1). | [06-app-sdk](06-app-sdk.md) |
| `module` | public | ABO | `Module` interface, `Manifest`, `Registrar` (routes, tasks, migrations, permissions, settings, events, UI, adapters), `Deps` handed to a module; `module.ReservedIDs()` (§3.6). | [06-app-sdk](06-app-sdk.md) |
| `entity` | public | core | Leaf package (L0): `entity.Ref{Module, Kind, ID string}` — **the one** polymorphic reference to any module-owned entity. Every file uses this type: tasks resources (`tasks.Resource` is `entity.Ref`), outbox/notify payloads, download grabs (multi-entity grabs carry `[]entity.Ref`), acquisition history/blocklist/queue, audit targets. No other `EntityRef`/`Resource{Module, Kind, ID}` struct exists; an `archtest` check fails any core identifier named `EntityRef` outside `entity`. Never a shared media row (decision 6). | [10](10-tv-movies-proof.md), [05-tasks](05-tasks.md) |
| `lifecycle` | public | core | Leaf package (L0): `Component{Start, Stop}` ordering contract and shutdown budget types used by kernel, tasks, realtime, outbox relay; the public constant `lifecycle.NotifySocketEnv` (the systemd notify-socket variable name, read by apps that strip it from subprocess environments; 04 §8.3, from R1). | [05-tasks](05-tasks.md), [05-health](05-health.md) |
| `version` | public | core | Leaf package (L0): `const Version = "0.Y.Z"`, the core version; it lands in R1 as `"0.1.0"`, so the `hello` skeleton's `MinCoreVersion: "0.1.0"` passes P1 before any checkpoint exists. **Never** derived from `debug.ReadBuildInfo` (under the `replace` that reports the zero pseudo-version, which is below every `MinCoreVersion`). Read by the kernel's `MinCoreVersion` check, `GET /api/core/v1/modules` and the backup manifest; CI keeps it **≥** the newest checkpoint (12 §12.4.3: the one "core version" PR bumps it above the newest checkpoint before the release that records the next one). | [12](12-repo-split.md) |
| `buildid` | public | ABO | Leaf package (L0), dependency-free: `buildid.Binary(version string) (string, error)` (the version, `+`, and the SHA-256 of the running executable) and `buildid.Commit() string` (`vcs.revision`), the binary ID of §3.6 "Build identity". Lands in the first core PRs of R1, so ABO's store-format guard and `--version-json` compute the binary ID before `app` exists; once the kernel exists `app.BinaryID` delegates to it (06 §6.4.5; FE6-3, 04 §8.1). | [06-app-sdk](06-app-sdk.md), [05-updater](05-updater.md) |
| `storage` | public | ABO | Pebble-backed ordered KV: `DB`, `Keyspace`, `Batch`, `Snapshot`, `iter.Seq2` scans, per-keyspace migrations and legacy-prefix claims (books' domain families, which stay in place). No core service reads or writes an app's range: each service that persists state does so through its **store interface** over its own `c/<subsystem>/` keyspace, and an app with pre-core rows hands them to that service's **importer** from its own migration step (table "Store interfaces and importers" below; 02 decision 13). There are no cross-owner grants. **Write checking:** storage-enforced `CheckedWrites` (`Expect` and `Serialized` checks at commit) apply to every native keyspace; books' domain families keep books' own locking (its keyed mutexes, and `apiKeyMu` with early fix 6 until the identity families leave in R2) and may stage an `Expect` or `GetForUpdate` as a voluntary compare-and-set that storage honours but never requires (05-storage §5S.3.2). **Claims are strict from R1**, and the full test suite and e2e run under strict claims; `core.storage.claims.mode=shadow` stays as an emergency dial, and a violation is logged, never persisted (05-storage §5S.3.4). A row a migration step cannot decode is **quarantined**: its raw bytes are copied under `c/storage/quarantine/` in the step's batch and reported in health, and it is never dropped in silence; a row of a secret-bearing family is sealed with the keyring before it is stored there, never kept raw (05-storage §5S.3.13). **Identity rows are the exception (D13):** a user, or one of its keys or identities, that the identity step cannot convert stops the upgrade instead (§3.8 M3). `storage.ErrSwapPending` is the one sentinel for an occupied swap slot of any kind (restore, revert, checkpoint restore, reset), defined here at L1 so the staging paths can return it (R8-1, COR8-6). | [05-storage](05-storage.md) |
| `storage/keys` | public | ABO | Leaf package (L0): pure tuple-key functions (the core key encoding). Lands in the first core PRs of R1, so the store-format guard writes its `c/storage/` records in core's encoding (04 §8.1). | [05-storage](05-storage.md) |
| `storage/codec` | public | ABO | Leaf package (L0): generic value codecs for stored values. | [05-storage](05-storage.md) |
| `storage/restorejournal` | public | ABO | Leaf package (L0) that imports nothing from core: the record types of the **one** journaled directory swap, whose kinds are 05-backup's staged restore, revert and checkpoint restore, the factory reset, `KindReset` with its two reset kinds `ResetStore` and `ResetFactory` (R8-1, SIMP8-1), and M8's store rewrite, `KindRewrite`, from F3 (05-backup §5B.3.4; SEC-5). They are written to `restore/<id>/journal.json`, which `internal/restoreswap` (L1) writes and P0a reads, so neither storage nor restoreswap reaches an L4 type; `backup` imports this leaf directly and re-exports nothing (COR7-10, SIMP8-8). Every kind, `KindRewrite` included, uses one intent slot, `restore/pending.json` with its `kind`, created atomically, so a second staging of either kind fails with `storage.ErrSwapPending`, and one rejection rule (`expires_at`, `staged_by_build`). A swap in flight of any kind is marked by `restore/<id>/inflight.json`, created before the first rename of every kind and every file (R9-7; DATA9-3) and for `KindReset` kept until `hook_done`; health's token `restore_inflight` (beside `restore_pending`, whose message names the kind) and the one in-flight probe shared by `make rollback` and `make rollback-restore` (§3.8) read exactly that path (05-storage §5S.3.7). Lands in R1 with the kernel's journaled swap (04 §8.1). | [05-storage](05-storage.md) §5S.3.15, [05-backup](05-backup.md) |
| `storage/index` | public | ABO | Full-text search index contract (bleve implementation in `internal/bleveindex`). Depends on `storage`, never the reverse; index wiring lives in `kernel`; each index keeps its own durable dirty set under `c/index/` (`Index.MarkDirty`, in the entity's batch), which no app supplies (05-storage §5S.3.8). | [05-storage](05-storage.md) |
| `storage/datamigrate` | public | ABO | Declarations of large, resumable data migrations (`DataMigration` with a required `tasks.Parallelism`, shard plan, dry run); imports `storage` and `tasks`, so it sits at L4, not with `storage`. Modules list them in `module.Manifest.DataMigrations`; the runner is `internal/storagemigrate`. Every data migration of R0–R3, the R2 cutover steps included, is `BootBlocking`; the `Online` mode (with `storage.ReadAdapter` and the kernel-synthesised `<ID>.datamigrate.<name>` kind) has no consumer and is a deferred-build member that lands with its first consumer (05-storage §5S.3.14; SIMP5-7). It lands at **F2**, R2's foundation PRs, together with `internal/storagemigrate` and the `tasks` surface they import (`Parallelism`, `Sequential`, `CPU`, `ForEach`, `ItemsError`), so the cutover steps of F3 and F4 run on it (§3.8; IMPL-2, XFILE-4). | [05-storage](05-storage.md) §5S.3.6 |
| `library` | public | ABO | Media roots, `os.Root`-confined filesystem access, path mapping, safe move/copy/hardlink, hashing, quarantine, free-space checks. Roots are the `core.library.roots` and `core.library.mappings` config entries for every app; there is no root store. ABO's HEAD import roots become `core.library.roots` entries in R2, IDs kept, and the books root is an ordinary root entry (§3.8 M6; 05-library §5L.10). | [05-library](05-library.md) |
| `tasks` | public | ABO | Durable task/job system: `Definition`, `Run`, `RunItems`, `Parallelism` (required), `Schedule` (the one schedule mechanism), `Resource` (= `entity.Ref`, namespaced write-sets), checkpoints, preview/dry-run, cancellation; runs, queues, checkpoints, schedule clocks, the pause marker and the stand-down markers persist through the `tasks.Stores` interfaces (below; SIMP7-10). The package, its executor and `ForEach` land dark at F2, with no books kind registered; every ABO definition is converted to a native definition before the R2 tasks flip F5, which switches ownership and deletes HEAD's engine (05-tasks §5T.8; §3.8). | [05-tasks](05-tasks.md) |
| `events` | public | ABO | Notify-only in-process bus with typed, namespaced topics; the one topic catalog. | [05-events](05-events.md) |
| `outbox` | public | ABO | Durable, at-least-once, same-batch outbox for correctness-critical cross-module work. | [05-events](05-events.md) |
| `log` | public | ABO | `log/slog` construction, per-module child loggers, redaction, and the `Record`/`Tap` types of the **system log journal** (persisted, queryable operational log; implementation `internal/logjournal`, persisting through the journal store interface below; the books activity feed stays in the books module). | [05-logging](05-logging.md) |
| `metrics` | public | ABO | Per-`App` Prometheus registry and OTel providers (never the process globals); every series core and the app's modules emit is named under the app's metric namespace, `app.Options.MetricNamespace` (§3.6 "Metric name"). | [05-metrics](05-metrics.md) |
| `config` | public | ABO | Layered config: defaults → file → env → flags → DB overrides. A value set by the operator (file, env or flag) **locks** its key; a DB override applies only to an unlocked key and is stored natively in `c/config/` through `config.OverrideStore`. **One fail-closed rule:** for the two keys declared `FailClosed`, the auth-enable switch `core.auth.mode` and the Basic-gate switch `core.http.basic_gate.enabled` (no other `core.http.basic_gate.*` key), the more restrictive of the operator value and the stored value wins and health reports the conflict, so a stale `ENABLE_AUTH=false` or compose's always-set `BASIC_AUTH_ENABLED=false` never switches off what the UI switched on (05-config §1.3.5). **Canonical names only:** each type has one strict decoder, and there are no alias names. A **retired name** (a HEAD env or YAML name that the rename table maps to a canonical key) is never read: boot refuses while a retired `ProtectAdmin`-or-higher or secret name is set, naming its canonical key, and storage refuses to create a store while a retired DB-path name is set, so an ignored `DATABASE_PATH` can never open an empty store at the default path (05-config §1.3.6). The **rename table** is generated once (HEAD name → canonical key, env name and blob field); it drives the R2 config step (§3.8 M2) and the R2 release note. Core writes no gap-fill `config.yaml`: M2 imports that file's keys once and then strips its managed keys (M2b, 05-config §1.12.2). Per-module typed sections declared in the manifest; JSON Schema for the UI; change notification. Holds no integration instances (those are `integrations` records). Until R2 ABO's config stays HEAD's own code: R1 hands the listener and perimeter values it holds to core as plain `app.Options` fields (06 §6.4.5). Every core config section (auth, tasks, backup, updater, ai, integrations, library, notify, health, metrics and the rest), each with its owner spec's key table, is registered dark at F2, so every key the rename table targets exists when F3's M2 imports it (§3.8; IMPL-1). | [05-config](05-config.md) |
| `secrets` | public | ABO | Encrypted-at-rest secret store (`*secrets.Store`) with one keyring; config fields reference secrets by name, and every secret is encrypted, ABO's included. The R2 config step re-encrypts into `c/secrets/` both ABO's plaintext blob secret fields and HEAD's four already-encrypted secret rows (OpenAI key, Google Books key, Hardcover token, Basic password; `internal/config/persistence.go:1955-1958` blanks them in the blob, `:1989-2003` writes them as encrypted setting rows @748fd787a), through `secrets.Importer`, with the keyring that `secrets.Open` creates after the pre-migration checkpoint and before the migration list (06 §6.5.1 P4; §3.8 M2). A secret row that cannot be decrypted (its `.encryption_key` missing or regenerated) becomes a "re-enter" item in health: never fatal and never dropped silently. `.encryption_key` stays in the state directory until R3's boot step deletes it (§3.8 R3), so a rollback restore still reads the old rows; the keyring file is part of every rollback restore from R2 on, and the owner exports it after R2's first boot, which creates it (§3.8). | [05-config](05-config.md) |
| `httpapi` | public | ABO | `net/http` (Go 1.22+ `ServeMux` patterns) API framework: typed routes, mandatory permission per route, error envelope, pagination, versioning, OpenAPI 3.1 generation, core perimeter middleware, and `MountLegacy` — a **permanent, supported** way to mount an app's existing router (ABO's gin engine) behind core's perimeter; it never returns a chain-less group (R6). | [05-http-api](05-http-api.md) |
| `realtime` | public | ABO | SSE hub with resumable, permission-filtered streams. In R2 HEAD's two operation streams are deleted by the tasks flip (F5), and `/api/events` by the events flip (F6), in which the SPA's realtime client moves to `/api/core/v1/events` (05-realtime §11). | [05-realtime](05-realtime.md) |
| `auth` | public | ABO | Identity: users, sessions, API keys, bootstrap, OIDC/OAuth, credential epochs; middleware. Persists through `auth.Store` (below), natively in `c/auth/`: hashed sessions and one-time tokens, and `mck_` keys with native scopes. The R2 identity step imports ABO's users, OAuth identities, the invite allow-list and API keys with every ID kept; an `abk_` key keeps its SHA-256 hash and therefore keeps verifying, while every new key is `mck_` (D12 (b)). Web sessions, invites and one-time tokens are not imported: everyone signs in once after R2 (D12 (b)). A new API key always has explicit, non-empty scopes (R5-6). The Audiobookshelf-compatible auth (its HS256 JWTs, its `abs_sess:` refresh sessions and the value of its signing secret) stays books' and is not migrated; from R2 its logins resolve users through core auth, by books' method-bound `auth.Compat` handle (`VerifyPassword`, `AuthenticateKey`, `PrincipalFor`, `CheckCredentialFor`; 05-authn §4.10; §3.8 "The frozen contract"). The six early fixes ship in R0 (05-authn §11.1); the tests of those whose code R2 deletes are ported as property tests onto core auth in R2. | [05-authn](05-authn.md) |
| `rbac` | public | ABO | Permission scopes (`<ns>:<action>`), roles as data, module-declared permissions, enforcement helpers. Roles persist through `rbac.Store` (below), natively in `c/rbac/` with native scopes, ABO's from R2. The R2 identity step maps every stored HEAD permission atom to native scopes through **one books-owned table** (code, not stored data); the gin routes keep calling `s.perm(atom)`, and the same table resolves an atom to the scopes the route requires (05-http-api §8.3). A principal that is not `Confined()` — judged on a key's **effective** set, role set ∩ key scopes, never on its owner — gets `core:self`, `core:metrics.read` and `books:reading` stored by that step, HEAD's ambient reach for the profile, `/metrics` and reading state, so a worker-confined key never gains them (05-rbac §6; R10-2). Seed roles are core's; HEAD's rewrite-at-boot behaviour is kept. The full `core:*` scope catalog — every core permission any flip, seed role or atom-table row uses, with its class and flags — is declared at F2, whichever subsystem is wired yet (§3.8; IMPL-4). | [05-rbac](05-rbac.md) |
| `audit` | public | ABO | Append-only audit log (auth events, permission denials, admin actions, task approvals); targets are `entity.Ref`. Its store and the normative action table (the `core.storage.*` actions and `core.settings.update` included) land at F2, so F3's settings writes and every cutover step's record have a writer (§3.8; IMPL-3). | [05-rbac](05-rbac.md) |
| `integrations` | public | ABO | Root of the integration contracts and **the one registry for integration instances** (native records in `c/integrations/instances/`; ABO's from R2, when the integrations step moves the instance fields of HEAD's config blob there with their secret refs, IDs kept, §3.8 M6; the native rules — `Local` with confirmation, origin binding, per-instance limits — apply to every instance, 05-metadata M1): `integrations.Ref{Kind, Instance}`, instance records (kind, instance, settings, `secrets.Ref`s), the shared error taxonomy (retryable/permanent/auth/rate-limited), `Factory`/`Descriptor` types modules use to contribute adapters (`Registrar.Adapter`). Imports nothing in `module` (R4). | [05-metadata](05-metadata.md) |
| `integrations/ai` | public | ABO | Provider-neutral AI contract (text/structured generation, embeddings, transcription) plus routing/health. v1 records usage metrics only; the usage ledger and monthly cap are deferred. | [05-ai](05-ai.md) |
| `integrations/download` | public | ABO (contract, `download/deluge`, what ABO uses today); deferred (other adapters; grab/monitor/pending-recovery/`MarkImported`) | Download-client contract (`Client`, `GrabRequest`, `Item` status) + adapters `download/deluge`, `download/qbittorrent`, `download/transmission`, `download/sabnzbd`, `download/nzbget`. Owns client polling, item state and completion events; the grab pipeline belongs to `acquire`. | [05-download](05-download.md) |
| `integrations/indexer` | public | **deferred** | Indexer/search contract + Newznab/Torznab adapter. | [05-indexer](05-indexer.md) (design sketch, non-normative) |
| `integrations/metadata` | public | ABO | Generic metadata-provider contract `Provider[Q, R any]` + resilience wiring. | [05-metadata](05-metadata.md) |
| `integrations/notify` | public | ABO | Notification contract + adapters (mail, webhook) with global routing. Inbox, per-user subscriptions, personal channels and templating are deferred. | [05-notify](05-notify.md) |
| `integrations/providerhttp` | public | ABO | Outbound HTTP for every adapter: per-provider throttle, daily quota, circuit breaker, retry, user agent — **instance-scoped**, not package globals. | [05-metadata](05-metadata.md) |
| `acquire` | public | **deferred** | Media-neutral acquisition engine (quality definitions, profiles, custom formats, decision engine, grab decisions, tracked-download records, import hand-off, history/blocklist/queue); consumes `integrations/download` events, never polls clients itself. | [10](10-tv-movies-proof.md) (design sketch, non-normative) |
| `backup` | public | ABO | Consistent backup/restore of every core-owned store plus module-declared artifacts; one archive format, in core's layout. Archives HEAD wrote (`legacy-abo/0`) stay restorable, and the next boot migrates the restored store (05-backup B1). | [05-backup](05-backup.md) |
| `backup/journal` | public | ABO | Leaf of `backup` (L4; imports `entity` and `storage` only): the out-of-DB revocation and restore journal and its types (`Revocation`, `RevocationJournal`, `RestoreContext`, `RestoreHook`). Lands in R2 with `auth` and `rbac`, which import it instead of `backup`; `rbac` cannot take these types from `auth`, which imports `rbac` (COR2-9). | [05-backup](05-backup.md) §5B.3.6 |
| `updater` | public | ABO | Self-update check/apply, parameterized by the app's release source. R0 hotfixes HEAD's updater (exit 75, the `.prev` slot, semver comparison with local-build detection, check-only unattended mode), and R2 **moves** that updater here rather than rebuilding it. It runs no pre-swap level or floor check: a store the new binary cannot take is refused by that binary's own boot (exit 78, §3.7). Signed manifests, the boot-attempt counter, automatic rollback and the unit's binary-layout migration are deferred-build members (A-6; 05-updater). | [05-updater](05-updater.md) |
| `health` | public | ABO | Liveness/readiness, component health, diagnostics bundle. The health registry and its route land at F2, so the tokens F3 raises (`storage_quarantine`, `secrets_undecryptable`, `secrets_keyring_not_exported`) surface from F3 on (§3.8; IMPL-12). | [05-health](05-health.md) |
| `auth/oidc`, `auth/password`, `health/diagnostics` | public | ABO | Public sub-packages that are not conformance suites (COR6-9): `auth/oidc` (OIDC/OAuth providers from `internal/oauth`, public because ABO's `internal/cfaccess` uses them) and `auth/password` (password hashing and policy) land with `auth` in R2; `health/diagnostics` (the diagnostics bundle, itself a task) lands with `health`. Each takes its parent's layer (R4). | [05-authn](05-authn.md), [05-health](05-health.md) |
| `arrfacade` | public | **deferred** | Extension point for an *arr-v3-compatible API adapter (D2). During the extraction only the `/api/v3/` path reservation exists (05-http-api §4.1). | [08-arr-facade](08-arr-facade.md) (design sketch, non-normative) |
| `ui` | public | ABO (from the release in which ABO adopts the shell build, R2 or R3; `ui.ReservedSegments` from R1) | Go side of the UI shell: serves an app's embedded **shell** SPA build with the core's headers/CSP/caching; `ui.ReservedSegments` (§3.6). `ui.ReservedSegments()` is a static function with no dependencies that lands in R1 together with `module`, so `module.ReservedIDs()` reserves the shell segments and its "equals §3.6's row" test passes from then on. The dependency-free leaf types `ui.ModuleManifest`, `UIIDs` and `Redirect` land with it, because `internal/modulehttp` (L6) reports `modules[].ui` from R2 and cannot name the L7 `module.UIDescriptor` (FE4-6); the rest of `ui` lands with the shell (04 §8.1). Until an app ships a shell build, `ui.Handler` is not mounted and the legacy mount owns `/`. | [07-ui-shell](07-ui-shell.md) |
| `coretest` | public | core | Test kit: in-memory `App` harness, fake clock, fake adapters, permission assertions; sub-packages `coretest/fakes` (in-memory implementations of every built capability contract and store interface, 06 §6.4.8), `coretest/crashmatrix` (from R1: the public crash-matrix kit of 05-storage §5S.3.16, which kills a boot at every durable write, rename and fsync of 06 §6.5.1's boot-writer table and checks the cross-mechanism invariants; core runs it over the kernel's writers with hello, and ABO runs the same kit over its full boot, books' writers and every R2 migration step included; R9-7, COR9-5) and, deferred with `acquire`, `coretest/acquiretest` (10). Test-support (R4). | [06-app-sdk](06-app-sdk.md) |
| `archtest` | public | core | Reusable architecture guards an app runs in its own tests (§3.5); sub-package `archtest/tasklint` (the task-bundle analyzer from `tools/cmd/oplint`, R2, 04 §8.1). Test-support (R4). | §3.5 |
| `<pkg>/<pkg>test` | public | with its store interface | Store conformance suites and fakes, one package per store interface of the table below (`config/configtest`, `auth/authtest`, `rbac/rbactest`, `tasks/taskstest`, `log/logtest`, `integrations/ai/aitest`, `integrations/providerhttp/providerhttptest`, `integrations/integrationstest`, `integrations/notify/notifytest`): every implementation runs its suite — the native one and the in-memory one in `coretest/fakes` — and the suite of a store with an importer carries the importer's cases (imported records read back equal through the store, every record ID is kept, an interrupted import re-run on the same input is idempotent). Test-support like `coretest` (may import any core package; no non-test core package imports them); part of the SDK and frozen with it. Each lands with its interface (04 §8.1; MOD5-6). | the interface's spec file |
| `shared/...` | public | none yet | Cross-app *domain* packages that are not platform concerns but are used by ≥2 apps (§3.4). Zero promoted (04 §9). | [04-package-mapping](04-package-mapping.md) |
| `kernel/...` | public-but-unstable | core | Module catalog, manifest validation, lifecycle supervisor, and the **typed services struct** that builds core services in a fixed order with explicit constructors. There is **no generic DI container** (no `Get[T]`-of-`any`). Imported only by `app`. Excluded from the API-stability gate; apps that import it fail `archtest`. | [06-app-sdk](06-app-sdk.md) |
| `examples/hello` | public (example) | core | The minimal "hello" app from 06 §hello: a **package in the core module** (not a separate module), built and tested in core CI and importable by ABO's G6 test; the in-tree second consumer for the split criteria (12 SC3) and the producer of the core OpenAPI document `core.json` (07 §7.8). It lands in R1 as a skeleton and grows with the releases, using only what has been built so far (04 §8.1; rule (b) of §3.4). | [06-app-sdk](06-app-sdk.md), [12](12-repo-split.md) |
| `internal/<capability>http` | internal | ABO (`internal/acquirehttp`: deferred) | **Core capability HTTP routes** (`/api/core/v1/...`): one package per capability — e.g. `internal/taskhttp`, `internal/authhttp`, `internal/rbachttp`, `internal/audithttp`, `internal/confighttp`, `internal/libraryhttp`, `internal/backuphttp`, `internal/updaterhttp`, `internal/healthhttp`, `internal/eventhttp`, `internal/storagehttp` (lands with the R2 identity flip, when core routes authenticate natively: one route, the factory-reset route `POST /api/core/v1/system/factory-reset`, `core:admin` + interactive + owner, over a staging func the kernel hands it as a storagehttp-local interface, `storagehttp.Stager` (the kernel's `stageReset`), never `*storage.Admin` or the kernel's swapper, the way `modulehttp` receives `ModulesView`. HEAD's two gin reset routes, `POST /system/factory-reset` and `POST /system/reset`, are deleted in R1, whose only reset path is the offline `<app> storage reset` (the SPA hides its reset control in R1); the identity flip adds this route and switches the SPA and the first-party scripts to it (05-storage §5S.3.15). The request is the one struct `storage.ResetRequest` (`{kind, confirm: "RESET", delete_library_files?, library_root?, library_file_count?}`, owned by 05-storage, whose `kernel.stageReset` compares the root and count with a fresh preflight), and a `delete_library_files` request whose root or count does not match a fresh preflight count answers 422 `library_files_confirm_required` with nothing staged (R9-6, COR9-4, SEC9-4; 05-storage §5S.3.15, R7-2, R8-1, COR8-1, COR8-2)), and `internal/modulehttp` (`GET /api/core/v1/modules`; the kernel hands it a read-only `ModulesView` interface — catalog snapshot, `restart_required`, and the `ui_build_id`, which is the served **shell** bundle's UI build ID (`ui.Bundle.BuildID()`, read from that bundle's meta tag) and `""` while a legacy mount owns `/`; the `X-App-Build` header carries the legacy mount's bundle ID meanwhile (07 §7.5; FE5-7), and so does `--version-json`'s `ui_build_id`, which is the `X-App-Build` value, not this field (FE7-13). There is no ldflags value and no boot-time comparison (05-http-api §4.2), and the UI build ID is never the binary's identity (§3.6 "Build identity", R5-7) — so it imports nothing above L6). Each imports its capability package(s) and `httpapi` (and `realtime` for stream routes), sits at L6 (R4), registers on `httpapi`'s core group, and is imported **only by `kernel`**. | per capability |
| `internal/keyedmutex` | internal | core | **L0** leaf (standard library only): core's own per-key mutex, a copy of ABO's `internal/keyedmutex`, which ABO keeps (§3.2.1). Lands in R2, when `config` first needs it (04 §8.1, §9); imported by `config`, `integrations/download` and `library` (05-config, 05-download, 05-library). It implements no public package, so R4 names its layer. | [05-config](05-config.md) |
| `internal/...` | internal | per contract | Implementations: `internal/pebble`, `internal/executor`, `internal/scheduler`, `internal/sse`, `internal/bleveindex`, `internal/openapi`, `internal/storagemigrate`, `internal/logjournal`, `internal/jsonschemax` (the one JSON Schema reflector, R4), … | per contract |

**Store interfaces and importers (R4-1; D10).** Every core service that persists state defines a **store
interface** in its SDK package and ships a **native implementation** over its own `c/<subsystem>/` keyspace. The
native implementation is the only one any app runs, ABO's included; the interface stays so that `coretest/fakes`
can supply an in-memory implementation and one conformance suite tests both. It lands with its subsystem (rule (b)
of §3.4). `library` has no store: its roots are config entries (row `library`).

Beside its store, each subsystem whose state ABO already holds exports an **importer**, `<pkg>.Importer` (table
below). An importer stages decoded records into the **caller's** batch through the native store's own validation,
keeps every record ID, and knows nothing about where the records came from. **One exception:** an importer of an
L0 package cannot name a storage type (R4), so it commits its own writes and is **idempotent** instead, re-importing
the same input to the same keys, which gives the calling step the same exactly-once result; `log.Importer` is the
one such importer (05-logging §3). ABO's HEAD rows reach core only this
way: in R2, one **books** migration step per family decodes HEAD's rows, calls the importer and deletes the HEAD
rows, in one batch per chunk (§3.8 M2–M6). The kernel hands each step the importers of the families it moves, as
`module.Importers` (06 §6.4.2), so core holds no app key name or row format (R1(c)) and no core service ever opens
an app's range. Every ID that books' own rows reference survives unchanged: user, role, API-key, import-root,
instance and run IDs. A row the step cannot decode is quarantined (row `storage`), except an identity row, which stops the upgrade (D13; §3.8 M3). The decoders of HEAD's rows are
books code in `internal/books/cutover` (§3.5.1 item 1), deleted in R3 together with books' legacy steps (§3.8).
Core enforces its invariants above the interface (never-empty API-key scopes, staging rules) and ships one
conformance suite per interface, in its `<pkg>/<pkg>test` package (row above), which covers the importer too.

This table is the **one list** of store interfaces and importers: other files cite "03 §3.3 'Store interfaces'"
instead of restating it (SIMP5-8).

| Package | Store interface (named in its spec file) | Importer | What it persists | Native keyspace | ABO's HEAD rows that the R2 step imports (§3.8) | Lands |
|---|---|---|---|---|---|---|
| `config` (with `secrets`) | `config.OverrideStore` ([05-config](05-config.md) C4); secrets in `*secrets.Store` (05-config Part 2) | `config.Importer`, `secrets.Importer` | DB overrides; encrypted secrets | `c/config/`, `c/secrets/` | M2: the config blob's fields under canonical keys (rename table), its plaintext secret fields and HEAD's four encrypted secret rows, re-encrypted, with the residual blob rewritten and kept as an active books claim; the gap-fill `config.yaml` keys, once, then the file stripped of its managed keys (M2b); the blob's integration-instance fields are left for M6, which retires and deletes the blob; and in M6 the import roots and the books root as `core.library.roots` entries | R1 has config stage 0 only (05-config); store, importers and ABO's use in R2 (flip F3) |
| `auth` | `auth.Store` ([05-authn](05-authn.md) §3.4) | `auth.Importer` | users, API keys, OAuth identities, invites, sessions, one-time tokens | `c/auth/` | M3: users, OAuth identities, the invite allow-list and API keys (`abk_` hashes kept); sessions, invites and one-time tokens are deleted, not imported (D12 (b)) | R2 (F4) |
| `rbac` | `rbac.Store` ([05-rbac](05-rbac.md) §3.4) | `rbac.Importer` | roles | `c/rbac/` | M3: roles, with their atoms mapped to native scopes by books' table | R2 (F4) |
| `tasks` | `tasks.Stores` (`RunStore`, `ClockStore`, `PauseStore`, `StandDownStore`; [05-tasks](05-tasks.md) §5T.3.13) | `tasks.Importer` | runs, queues, checkpoints, schedule clocks, the pause marker, the stand-down markers | `c/tasks/` | M4: open, paused and finished runs with their checkpoints, chunk ledgers and HEAD's repairs rows (`opv2:row:`, `opv3:rowmark:`; history kept, D12 (b)), every stored kind renamed through books' kind-rename map; the schedule clocks (the maintenance-window clock `maintenance_window_last_run` among them), so the first boot fires no catch-up storm; the pause marker, the scan stand-down marker `registry.scan_standdown` and the `Requires` stamps | R2 (F2 dark; ABO's use F5) |
| `log` | `log.JournalStore` ([05-logging](05-logging.md) §3) | `log.Importer` (idempotent, not staged into the caller's batch: the exception above) | the system log journal | `c/log/` | M5: the `syslog:` rows, converted (D12 (b)) | R2 (F5) |
| `integrations/ai` | `ai.JobStore` ([05-ai](05-ai.md)) | `ai.Importer` | AI batch jobs | `c/ai/` | M6: the `aijob*` rows (books' AI result journal is plain books code over its own family, not a store; SIMP5-3) | R2 (F7) |
| `integrations/providerhttp` | `providerhttp.GateStore` ([05-metadata](05-metadata.md) M2) | none | throttle holds, daily quotas | `c/providerhttp/` | none: the holds are transient and reset at R2 (D12 (b)); M6 deletes HEAD's throttle and budget rows | R2 (F7) |
| `integrations` | `integrations.InstanceStore` ([05-metadata](05-metadata.md) M1.3) | `integrations.Importer` | integration instance records | `c/integrations/` | M6: the instance fields of the config blob (plugin and provider settings) with their secret refs | R2 (F7) |
| `integrations/notify` | `notify.RuleStore` ([05-notify](05-notify.md)) | `notify.Importer` | global notification rules | `c/notify/` | M6: HEAD's webhook settings as one native rule over the `notify/webhook-<n>` instances that `integrations.Importer` writes, each with the HMAC secret by value; deliveries use core's native format from then on (05-notify §3.1, §10) | R2 (F7) |

The full-text index has no row and no importer: books' index is **rebuilt** at a new path under the native
`storage/index` contract at the first R2 boot, M6 deletes HEAD's dirty-set rows, and HEAD's `library.bleve`
directory, left for a rollback restore to the pre-freeze release, is deleted by R3's boot step (§3.8 R3; 05-storage §5S.3.8). Derived indexes and caches are
rebuilt, not imported (D12 (b)).

npm workspace `core/web/` (D6):

| Package | Purpose |
|---|---|
| `@mediacore/shell` | React shell: auth, layout/nav, settings frame, tasks/jobs, logs, users/roles, notification admin pages for the v1 scope — the notify contract, the mail and webhook channels and global routing rules — plus toasts (no inbox, bell, per-user subscriptions or personal channels: deferred, 05-notify §1), system health; the `createShell({ modules })` extension API. |
| `@mediacore/api-client` | Generated TypeScript client for **core** routes (from the core OpenAPI document). Apps generate their own client package the same way. |
| `@mediacore/ui-kit` | Shared presentational components and theme tokens (MUI-based) used by shell and apps. It imports neither `@mediacore/api-client` nor `@mediacore/shell` (a `core/web` ESLint rule): a component that needs data or a toast takes it as a prop (`ServerFileBrowser`'s `browse`, `PathLinks`' `aliases` and `onCopied`; 07 §7.11, FE7-12). |

## 3.4 What is core, what is shared, what is app

**Promotion rule (non-circular).** Being listed in §3.3 is not a reason to build a package. A
package is **built** in core only if

- (a) ABO consumes it in one of the releases R0–R3 (§3.8; [09](09-abo-on-core.md)), or
- (b) a core package built under (a) or (b) needs it — the kernel, the composition root, the
  architecture guards and the in-tree second consumer `examples/hello` count as core. `examples/hello`
  never pulls a package forward: it uses only what the releases have built so far and grows with them
  (04 §8.1), so it cannot force `ui` or `tasks` into existence earlier than ABO needs them.

A package that meets neither keeps a **design sketch (non-normative)** in the spec at proof depth (D5)
and is marked **deferred** in §3.3: the plan builds none of it, the SDK freezes none of it, and it is
designed and built when the first TV/Movies app starts (A-15). Today that is `acquire`, `arrfacade`,
`integrations/indexer`, the download grab/monitor pipeline, the download adapters other than Deluge, and
the AI usage ledger; inside built packages it is also the `Online` data-migration mode and its read adapters
(no consumer in R0–R3; 05-storage §5S.3.14, SIMP5-7), backup encryption (deferred for scope; 05-backup B3), the
seed-role merge diff (05-rbac §4.8) and root read shares (`library.RootSpec.ReadShares`: no app of R0–R3 has two
modules that share a root; ABO has one module; 05-library, SIMP7-2, SIMP8-5).
**Deferral applies to features inside built packages as well as to packages**: a built package carries
no member whose only consumer is deferred — for example the facade-only key machinery
(`KeySpec.Purpose`/`Module`, `PurposeFacade`, query-key extraction, `Router.FacadeGroup`, the facade
route kind and its stage-13 clause) lands with `arrfacade`, not in `auth`, `rbac` or `httpapi` in R1 or R2.
Every spec file that has deferred members in a built package lists them, informatively, under a heading
`Deferred-build members`; an identifier is listed under one heading only, and another file that defers it
cites that heading. No generated list or absence test checks these headings (A-4): a PR that builds a
deferred member is reviewed against its heading and deletes that member's entry.
Cross-app domain helpers enter `shared/` only when they have **two real consumers**; until a second
app exists they stay in ABO unless a built core package needs them. The guard against the 2026-09-18
adversarial review's residual risk "small core becoming a god package" (`raw/08-prior-art.md` §2) is
therefore the consumer test, not the list. The Build column is re-checked for every release
(14 K6). The package mapping ([04](04-package-mapping.md)) applies the rule to all 213 packages.

**Visibility rule.** A package that still has an ABO importer moves to a **public** core path, or
stays in ABO; it never moves to `internal/` or `kernel/`, which ABO cannot import (Go's `internal/`
rule; R2). It may move to `internal/`/`kernel/` only in the PR in which its **last** ABO importer has
been rewired to a public SDK surface (for example `sdnotify` behind `health`/`lifecycle` readiness
hooks, `compactprogress` behind a `storage` statistics type, `oauth` behind the `auth` OIDC API,
`scheduler`/`executor` behind `tasks`, startup steps only through `app`). A package whose replacement
public surface lands later stays in ABO until then: e.g. `internal/startup` stays in ABO
until `Registrar.StartupSteps` exists (06 §6.4.10), because `cmd/root.go:274` builds the `serve` step
runner from it (R3-3, MOD3-1). 04 derives its internal/public column from the import graph under this
rule (informative planning data). Go's `internal/` rule and `archtest.NoKernelImports` enforce it in the CI of the PR
that rewires or moves a package, under **every** (GOOS, tag set) of §3.5.1 item 4's list, because a required PR job
compiles each of them (the darwin and `integration` vet job, §3.5.1 item 4; R9-10, MOD9-3); no predictive gate
evaluates later releases ahead of time (A-4).

| Bucket | Rule | Examples (decided in 04) |
|---|---|---|
| core kernel/SDK | built under (a) or (b) above | storage, tasks, auth, rbac, httpapi, config, log, metrics |
| core, deferred | contract only; built with the first TV/Movies app | `acquire`, `arrfacade`, `integrations/indexer` |
| core `shared/` | domain logic with ≥2 real consumers across apps, or needed by a built core package | text normalization (`titleutil`, `personname`) once TV/Movies parsers need them |
| app (ABO) | everything audiobook-specific | scanner, organizer, dedup, itunes, ABS compatibility surface, narrators, the R2 cutover steps that decode ABO's HEAD rows into core's importers, the atom → scope table and the kind-rename map (§3.3, §3.8) |
| delete/merge | dead or duplicated mechanisms | v3/v2/v1 op duplication collapsed into `tasks`; legacy `internal/plugin` |

## 3.5 Dependency direction rules and their mechanical enforcement

| # | Rule | Enforced by |
|---|---|---|
| R1 | Core never imports app code, and holds no app key name or row format. | (a) `core/go.mod` must not `require` any app module — checked by `core/archtest.TestNoAppRequire` reading `core/go.mod`; (b) `core/archtest.TestCoreImportsOnlyCore`: `go list -e -deps -test -json` of every core package (an unbuilt listed embed output is tolerated, any other error fails: §3.5.1 item 4; R10-9), run once per (GOOS, tag set) of §3.5.1 item 4's one list and read as the **union** of the file lists (honouring `core/go.mod`'s `ignore`, so `core/web/node_modules` is never scanned), contains no path outside `github.com/falkcorp/mediacore/...`, stdlib, and the allowlisted third-party set (test imports included, because the E2 hazard came from a test file; the synthetic `.test` and `[pkg.test]` packages are filtered); runs in core's own `go test ./...`. A default-tag read would miss a darwin- or `integration`-tagged core file that imports an ABO non-internal path, which the root's own darwin build accepts and links into the release binary (R9-10; MOD9-3, round-9 modules-build Z3). Even on default tags, R1 rests on **core's** CI: the root's build, vet and `go mod tidy -diff` all stay green with a core → ABO import (round-9 Z5), so `core-ci` is a required check (12 SC4(b)). Note that Go *does* allow module dependency cycles, so (a) is required, not redundant. (c) Core never decodes an app's rows: an app hands its pre-core records to a core importer from its own migration step (§3.3 "Store interfaces and importers", R4-1). Because a **copied** format passes (a) and (b), the root module's `TestCoreHoldsNoAppKeyNames` (`internal/arch`; this is the test's one name, DATA5-16) scans every string literal in a non-test `core/` file of the same tag union. It lands in root `make ci` in the first core PR of R1, before any file moves into core (MOD9-6). Its name set is the claim generator's **output** for owner books, read through the exported `KeyFamilies()` at its current path (`internal/database/keyfamilies.go:330` @748fd787a, `internal/books/store/keyfamilies.go` from R1) with core's reserved `c/` and `m/` excluded, plus ABO's setting and preference sub-keys (generated from the `GetSetting`/`SetSetting` call sites, which add `setting:` themselves, so a moved file can carry one with no family prefix) and HEAD's 269 op IDs, read from the source side of books' kind-rename map (MOD5-4), plus a reviewed list of ABO row-type and param-key literals that no family prefix carries (`legacy_op_id`; MOD7-2). **One match rule** — this is its only text; other files, 05-storage's `TestCoreHoldsNoAppKeyNames` row among them, cite it instead of restating it (MOD6-7, MOD7-7): a literal fails when it **begins with a family prefix** and the character after the prefix is not a space (a key, not a Go `pkg: message` error string such as core `library`'s `"library: …"` sentinels, although `library:` is an ABO family, `keyfamilies.go:126`); when it **equals a setting or preference sub-key, a listed literal or an op ID, or begins with one followed by `.` or `:`**; or when it **begins with a constant prefix folded from a constant + variable key** (`"scheduled_task_last_tick." + kind` folds to `scheduled_task_last_tick.`, which a literal matches by begins-with). A **struct tag** is matched per key: each `key:"value"` pair's value, up to its first `,`, is matched as a literal, because a param key travels as a struct tag in most of its ABO occurrences (MOD8-4). Core-owned task kinds never spell an app's op ID: books' kind-rename map maps a former op ID onto a core kind itself (`diagnostics.export` → `core.diagnostics.export`; the core kinds that replace HEAD definitions are `core.storage.compact`, `core.storage.census`, `core.log.journal.prune`, `core.integrations.release-holds`, `core.diagnostics.export`, `core.window.nightly` and the new `core.tasks.prune`, 05-tasks §5T.8.2), so a core literal naming an ABO op ID fails this test (MOD6-6). Exceptions live in one reviewed allowlist file, not inline. Cases: `"library: root escapes"` passes, `"library:" + id` fails, `"c/tasks/"` passes (MOD5-3), `tasks.Kind("diagnostics.export")` fails (MOD6-6), `"<op-id>.x"` (e.g. `"library.organize.extra"`) fails while `"library.organize-when-scanned"` is judged as its own key, not as the op ID `library.organize` (MOD7-7), `"scheduled_task_last_tick.x"` fails (folded prefix), `` `json:"legacy_op_id,omitempty"` `` fails (struct tag, MOD8-4). |
| R2 | Apps import only core's public packages. No PR rewires an ABO import onto `internal/` or `kernel/` (visibility rule, §3.4). | Go's `internal/` rule for `core/internal/...`; `archtest.NoKernelImports(t, "github.com/falkcorp/audiobook-organizer/...")` for `kernel/...` (called from the root module's `internal/arch`); `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` for the examples (both read the tag union of R1(b), R9-10), which the path-based `internal/` rule does not cover (X5). A wrong-way rewire surfaces when that PR builds (A-4). |
| R3 | A module never imports another module's packages; modules talk through core contracts (events, outbox, tasks, capability lookups). | `archtest.ModulesIsolated(t, map[moduleID]pkgPrefix)`; for ABO there is one module, so the rule is vacuous until a second exists, but the test is wired from R1 so the second module cannot land without it. |
| R4 | Core layers (a package may import only its own or lower layers): **L0** `entity`, `lifecycle`, `version`, `buildid`, `log`, `metrics`, `storage/keys`, `storage/codec`, `storage/restorejournal` → **L1** `storage`, `storage/index` → **L2** `secrets` → **L3** `config` → **L4** `events`, `outbox`, `tasks`, `storage/datamigrate`, `library`, `auth`, `rbac`, `audit`, `integrations`, `integrations/*`, `backup`, `backup/journal`, `updater`, `health` → **L5** `acquire` → **L6** `httpapi`, `realtime`, `arrfacade`, `ui`, `internal/<capability>http` (including `internal/modulehttp`) → **L7** `module` → **L8** `kernel` → **L9** `app`, `examples/*`. Deferred packages keep their layer so their contracts are checked when built. A public sub-package `p/q` takes the layer of `p` unless this list names it: `auth/oidc` and `auth/password` are L4 with `auth`, and `health/diagnostics` is L4 with `health` (its import of `tasks` is a same-layer edge; COR6-9). `coretest` and its sub-packages (`coretest/fakes`, `coretest/crashmatrix`, `coretest/acquiretest`), `archtest` and `archtest/tasklint`, and the store conformance-suite packages `<pkg>/<pkg>test` are test-support packages (may import any core package; no core non-test package imports them; MOD5-6). An `internal/*` package takes the layer of the public package it implements; an `internal/*` package that **bridges** capabilities (imports a package above the one it implements) takes the layer of its **highest import**, is never imported by a lower-layer package, and is wired by `kernel` — e.g. `internal/storagemigrate` (storage migrations run as tasks: L4), `internal/logjournal` (persisted journal over storage and tasks: L4), `internal/<capability>http` (L6). `internal/restoreswap` (the one journaled directory swap: restore, revert, checkpoint restore and `KindReset`; R8-1) is **L1**, imports only `storage` and its L0 leaves, and is imported only by `kernel`. The direction is `restoreswap` → `storage`, never the reverse: `storage.Admin` carries no staging or swap method, and the kernel holds the swapper, staging through it (for a reset through `kernel.stageReset`, for both reset kinds `ResetStore` and `ResetFactory`, with 05-storage's `storage.ResetRequest`; COR8-2, R9-6) and running it at P0a (COR8-5). The one sentinel of an occupied slot, `storage.ErrSwapPending`, lives in `storage`, so the staging paths and L1 code can return it, and `backup`'s restore sentinel is its alias (COR8-6). The restore-journal record type that restoreswap and storage's open read is defined in the L0 leaf `storage/restorejournal` (05-storage §5S.3.15), which `backup` imports directly without an alias, so `storage` never reaches an L4 type (COR7-10, SIMP8-8); `core/internal/archtest_layers_test.go` checks the edge. `internal/keyedmutex` is an **L0** leaf (standard library only; §3.3 row). `internal/jsonschemax` is an **L0** leaf (it imports only `github.com/google/jsonschema-go`): it owns the one JSON Schema reflector wrapper, the tag pass and an extensible override table (`type Overrides map[reflect.Type]*jsonschema.Schema`, `For[T any](o Overrides)`); each package contributes the overrides for its own types at its own layer, through a function so no exported package-level map exists (R5): `jsonschemax.Base()`: `time.Time`; `config.Overrides()`: `config.Duration`, `secrets.Ref`; `httpapi`: `rbac.Permission` — so `config` (L3) never names `rbac` (L4). There is one composition, `jsonschemax.Base().With(config.Overrides()).With(<httpapi's own>)`, used by both the settings schema and OpenAPI: `httpapi` imports the static `config.Overrides()` value (L6 may import L3), never a `*config.Store` (SIMP2-3, SIMP3-1). | `core/internal/archtest_layers_test.go` — the redesigned successor of `internal/arch/layering_test.go` for core (§3.5.1). |
| R5 | No process-global mutable state **in core**. No `func init()` in core except allowlisted pure ones (none at start); no exported package-level variables of map, slice, func or pointer type; core packages never call `prometheus.DefaultRegisterer`/`MustRegister` on the default registry, `otel.SetMeterProvider`, `otel.SetTracerProvider`, `otel.SetLogger`, `otel.SetTextMapPropagator`, `otel.SetErrorHandler`, `slog.SetDefault`, `log.SetOutput`, `http.DefaultServeMux`, `http.DefaultClient`/`http.DefaultTransport` mutation, `signal.Notify` or `signal.NotifyContext` (which installs its handler through `signal.Notify`, so a scan keyed on the one name would miss it). The single exception is `app.InstallProcessDefaults`, an opt-in helper a `main` may call; a `package main` is a process owner like that helper, so `TestNoGlobals` skips `main` packages (`core/examples/*/cmd/`), where hello's `main` calls `signal.NotifyContext` (COR6-11); until it exists (R1), the process-global installs that ABO's metrics need stay in ABO (a thin `internal/telemetry` façade or `cmd/root.go`), and core `metrics` only returns providers (04). R5 covers the test-support packages `coretest` and `archtest` too: `coretest` captures logs through an injected handler (it hands the test a `*slog.Logger`) and never calls `slog.SetDefault`, so core tests stay parallel-safe; a helper that sets the process default for ABO code stays in ABO (MOD3-3). R5 binds core only: an app may keep façades over core handles (ABO keeps `config.AppConfig` and its logger shim as books-module values); they are per-`App` values, so several apps in one binary stay possible, and removing them is an optional ratchet, not part of any release. | `core/archtest.TestNoGlobals` (AST scan). Rationale: several apps in one binary, and test isolation (`raw/07-plugin-registry-sdk.md` §6, `raw/12-cross-cutting.md` §9). |
| R6 | Every route declares a permission (or explicit `rbac.Public` / `rbac.AnyAuthenticated`; `AnyAuthenticated` never admits a confined credential, 05-rbac). Routes served through `httpapi.MountLegacy` (ABO's gin engine, mounted permanently behind core's perimeter middleware) are covered by a reviewed legacy route manifest in which every route names its permission. The manifest registers each legacy route as its own `ServeMux` pattern, except that each connected set of same-owner routes that conflict as `ServeMux` patterns (HEAD has such sets, e.g. `GET /api/v1/operations/{id}/activity` beside `GET /api/v1/operations/v2/{id}`) registers as **one generalised pattern**, and only when every member carries the same `Perm` (resolved through books' atom → scope table, 05-http-api §8.3), `Guards`, `Public`, `RateClass`, `OwnAuth` with its `AuthMethod`, `BasicExempt` and `NoCompress` (05-http-api §4.8 rule 4's list; COR9-9); any other conflicting set fails manifest generation, and the post-gin check accepts the `c.FullPath()` of any member (A-1; 05-http-api §4.8, §8.2). **Every conflict set has one owner (R9-2, R10-1; COR9-2, COR10-1):** a set that holds routes of two owners fails `Build`, so a route never moves to core without its conflict partners. The one such set at the evidence commit, `GET /api/v1/operations/{id}/{activity,changes,result,results}` with `GET /api/v1/operations/v2/{id}`, stays books' until F5, the tasks flip, which deletes HEAD's engine, moves the Activity page onto core's task routes (`GET /api/core/v1/tasks/runs/{id}` and its siblings, 05-tasks §5T.3.10) and deletes the whole set together with HEAD's `/api/v3/ops/*` routes; the first-party scripts switch in the same PR (§3.8). **No API returns a chain-less route group or mount** (the G8 failure mode was a missing chain, not a missing permission): a non-Public `Router.Infra` route runs the authenticated stages like any other route (05-http-api §4.2); `MountLegacy`'s `LegacyAuthGin` mode (gin's own auth chain instead of core's stages 12–13) is accepted only for the module named in the compile-time `app.Options.LegacyAuthGinUntil`, which ABO removes in the R2 identity flip (F4, §3.8); and a legacy route marked `OwnAuth` (the Audiobookshelf-compatible routes) is accepted only when it names an auth method registered through `Registrar.AuthMethod` (non-interactive, never `MayChangeCredentials`). | Registration-time error in `httpapi` (fail-closed) **and** `archtest.RoutesHavePermissions` over the built router and the legacy manifest — a permanent test, not a migration test; `TestOwnAuthRoutesGolden` records every `OwnAuth` route; an archtest fails if `LegacyAuthGinUntil` is still set after the identity flip (05-http-api §8.2). |
| R7 | Every library-scale loop declares parallelism. | `tasks.Parallelism` has no valid zero value. `Batch`/`Fixer` parallelism is a definition field validated at **registration** (a missing value fails it). `tasks.RunItems`, `tasks.RunSeq` and `tasks.ForEach` (the same loop for code that holds no `*RC`) take a required positional `tasks.Parallelism`; a zero value returns `*tasks.ItemsError` wrapping `tasks.ErrNoParallelism` before any item runs (fail-closed). `archtest.RunItemsDeclareParallelism` (AST) accepts, for that argument and for the `BatchSpec`/`FixerSpec` field: (a) a call to a `tasks` constructor (`CPU`, `Fixed(n, reason)`, `Sequential(reason)`); (b) a `tasks.Parallelism`-typed variable, parameter or field whose every assignment in the analysed packages is (a) — so passed-through values are legal; (c) a `//mediacore:required` options field whose owner calls `Parallelism.Validate` at construction. It flags zero literals and empty reasons (05-tasks §5T.3.2). Replaces `internal/plugins/runitems_concurrency_lint_test.go`. |
| R8 | Every module storage key lives in the module's keyspace or a declared legacy claim. | `storage` refuses **reads and writes** outside the handed `Keyspace` and claims (`ErrOutsideKeyspace`; DATA-17); claim modes (read-write, read-only, aging-out, retired) and the same-owner nesting rule are 05-storage §5S.3.4's; claims are of three kinds — prefix, exact key, and prefix-with-exceptions (sub-prefixes or exact keys carved out as the same owner's nested claims with their own mode, as books' settings and index families do in 05-storage §5S.9.1's table) — resolved by longest match; boot fails on any overlap between owners (05-storage). No owner reaches another owner's range: core services receive an app's pre-core records only through the importers, which the app's own migration step calls (§3.3, R4-1). R2 drops books' claims on the families it moves and splits the shared `idx:` and `setting:` families by explicit sub-prefix lists, checked byte for byte on every range outside the lists (05-storage §5S.9.1). Claims are strict from R1, with `core.storage.claims.mode=shadow` as the emergency dial; storage-enforced write checks (`CheckedWrites`) cover every native keyspace, and books' domain families keep books' own locking (§3.3 `storage`). |

### 3.5.1 Redesign of `internal/arch/layering_test.go`

Verified assumptions that the extraction breaks (`raw/03-layering-guard.md` §2–§4):

- the **package floor** `minModulePackages = 150` (`internal/arch/layering_test.go:38`). The root has
  213 packages today. Under 04's mapping 49 packages leave (`tools/cmd/opsgen` stays in ABO, and R3 deletes it
  together with `web/src/generated/ops.ts` and `internal/operations/state`, the Activity page having stopped reading
  `ops.ts` at F5, XFILE-2; until then `internal/operations/state` stays as books'
  HEAD run-state package — HEAD's v2 table, V3 constants, transition machine, `Classify*`,
  `FromV2`/`ToV2` — which the tasks cutover step and `opsgen` import, while core's executor state machine is a
  re-implementation over `tasks.RunState`; R6-2, MOD6-1; the registry engine, `childop`, `opsmetrics`, `opsv3` and
  the whole of `pkg/ops/opstest` (into `taskstest`) are re-implemented in core, recorded as 04 §8.6 planning rows:
  core's executor is written fresh against `tasks.RunStore`/`RunState`, with `rc.Await` in place of
  `childop.Follow` and `opsmetrics`' one core successor in `internal/executor`, and every ABO definition is
  converted to a native definition before the R2 tasks flip, which deletes the ABO engine files; MOD7-1, MOD8-2)
  and 5 new paths appear
  (`internal/books`, the composition root; `internal/books/contract`, the books helper package below
  it, item 1 and R4-5; `internal/books/cutover` (name open), books' R2 migration steps that decode HEAD's rows of
  the core-service families and call core's importers, which need `internal/config`, the store residue and
  `internal/operations/state` and so cannot live in the residue that `internal/config` imports (MOD5-2, R5-2);
  `internal/cfaccess`; `internal/server/legacyauth`, the bridge of 05-http-api §8.3 that replaces the gin auth chain
  inside the legacy mount from the R2 identity flip, a package of its own because the handler packages call
  `legacyauth.Can` and cannot import `internal/server`; it imports only core `auth` and `rbac`, gin and books' atom →
  scope table, so its computed layer is the floor and every ABO layer may import it), so
  213 − 49 + 5 = **169** remain: a margin of 19 that further moves consume (04 §8.4 note 6;
  `raw/03-layering-guard.md` §3's 154 predates the new paths and the 8 packages 04 keeps in ABO, among them
  `internal/logger/logtest` (MOD3-3), `internal/telemetry` (MOD4-4) and `internal/ai/resultjournal` (plain books
  code over books' own family, not a store implementation; SIMP5-3, COR5-10)). The count is informative and is
  re-derived whenever 04's moves change;
- the **positive-control edge** `internal/server -> internal/database` (`layering_test.go:40`): both
  packages are split by the extraction;
- **`layerOf` completeness** keyed on root-module paths;
- the shrink-only **`allowed`** map (5 of its 12 edges are `config` reaching up).

The guard is **redesigned, not deleted**:

1. **Root module (ABO) guard** keeps its rule and its `layerOf` map, but:
   - the loader-health floor becomes a *ratio* check: `len(packages) >= 0.8 * len(layerOf)`
     instead of a literal 150, so moving packages out cannot trip it while a broken loader still does;
   - the **in-module positive control** becomes "the root `main` package (key `.`) imports `cmd`"
     (`main.go:12`). The CLI and cobra stay in ABO (13 §13.5.1), so the edge exists before, during
     and after the extraction. (`internal/server` is not used: `.` imports it today only for the
     process-global setters that 07 and the R1 composition-root work remove.) A PR that ever removes the edge must name a
     replacement in-module edge in the same change;
   - imports of `github.com/falkcorp/mediacore/...` are classified onto ABO's layer scale (0 leaf,
     1 config, 2 storage, 3 domain, 4 jobs, 5 transport, 6 entry; `layering_test.go:46-48`) by core's
     R4 layer, so core imports keep the domain/transport/entry split instead of hiding wrong-way edges:
     core **L0–L5** → **−1** (platform, legal from every ABO layer); core **L6** (`httpapi`,
     `realtime`, `ui`) → ABO layer **5**; core **L7–L9** (`module`, `app`, `examples/*`) → ABO layer
     **6**, with no allowance. ABO's composition root `internal/books` implements `module.Module` and
     makes the `Registrar` calls, so it imports `module`/`app` and every books package it registers; its
     computed layer is 6, and only `.` and `cmd` import it (non-test imports: the G6 test
     `TestSecondModuleNoAudiobookEdit` lives in `internal/books` itself, MOD5-11). Its constructor is
     `books.Module()` in every file (MOD5-11; `books.Module(store)` only on the intermediate `main`s of R1 before the PR in which the kernel opens the store, 06 §6.11.1). The shared books helpers (`PageBooks`, the
     books `Writer` contract, selection helpers, task `Resource` declarations and resolvers, the kind-rename
     map) live in a package **below** it (`internal/books/contract`, name open, 04 §2.4; layer 3) that
     imports neither `module`/`app` nor anything that imports the composition root; otherwise a plugin
     that imports a helper closes the cycle `internal/books` → plugin → `internal/books` (R4-5). Below
     layer 6 an ABO package never imports `module` or `app`: it receives what it needs (`ExtendStart`,
     loggers, scopes) as funcs or interfaces that `internal/books` wires. A guard assertion fails when any
     package other than `.`, `cmd` and `internal/books` imports `mediacore/module` or `mediacore/app`
     (non-test imports). Every new path enters `layerOf` in the PR that creates it — `internal/books` in R1;
     `internal/books/contract` in R2, when the sources of its content (`pkg/ops`, `pkg/plugin/sdk`,
     `internal/operations`, `internal/opscatalog`, the resolvers of `internal/operations/registry/deps.go`) leave;
     `internal/books/cutover` with the first cutover step (the config flip, R2); and `internal/server/legacyauth` in
     the identity flip, the PR that deletes the gin auth chain — so no stale key and no placeholder file is needed
     (MOD5-9, MOD6-14; 04 §2.4, §8.1, §8.6). `internal/books/cutover` imports downward only (`internal/config`, the
     store residue, `internal/operations/state`; MOD6-1), and only `internal/books` imports it (R5-2, MOD5-1;
     04 §2.4). A core import is never counted as an in-module edge, but it is
     checked against the importer's layer like one: an ABO domain package that imports
     `mediacore/realtime` is a wrong-way edge exactly as an import of today's `internal/realtime` is,
     so an `allowed` entry such as `plugins/* → itunes/service` is re-evaluated against the mapped
     layer when its target moves to core, never dropped (04 §8.4 note 3). Mapped core imports also
     **feed the computed layer**: the layer the classification rule computes for an ABO package is the
     maximum of its in-module imports' layers **and** the mapped layers of its core imports (floored as
     today), so `layerOf` follows a core import the same way it follows an in-module one. Consequence in
     R1: `internal/httputil` (declared layer 0, `layering_test.go:82`) keeps `respond.go`, which builds
     the envelope types that move to `mediacore/httpapi` (mapped layer 5), so its computed and declared
     layer become **5**; that is legal, because all 19 of its importers are layer 5 (round-3 X17), and it
     needs no new `allowed` entry. The same computation makes `internal/cfaccess` layer **5** from R2 (its `owner_guard.go` imports
     `internal/httputil`), so its layer-4 importer `internal/plugins/maintenance` (`layering_test.go:227`)
     does not import it: `internal/books` injects the owner check as a func, the MOD4-18 rule above, and 04 §2.4 states cfaccess's `layerOf` (MOD7-4). Packages with no R4 row are mapped explicitly: `coretest`, `archtest`,
     the `<pkg>/<pkg>test` suites, `storage/*` → **−1**; `shared/...` → **3** (domain). `archtest.NoKernelImports` is unchanged. This needs a **loader change**: `loadGraph` (`layering_test.go:339-381`) drops
     every import outside the module today; it keeps imports with the `github.com/falkcorp/mediacore/`
     prefix and hands them to the classifier;
   - a **classifier positive control**, effective from the first core PR of R1, the first in which ABO imports
     core (the root guard itself calls `archtest.Layering`): at least one root package imports a
     `github.com/falkcorp/mediacore/...` path, and the guard classified that import onto the mapped
     layer. (A cross-module edge cannot serve as the in-module control, because by the previous bullet
     it is never an in-module edge.)

   A rule against "smuggling audiobook types into core through a callback" was considered and
   dropped: callbacks are values, not import edges, so no import-graph check can see them, and
   R1(a)/(b) already stop core from naming any ABO type.
2. **Core guard** (`core/internal/archtest_layers_test.go`) is a new instance of the same algorithm
   with the R4 layer table, its own `layerOf` and its own ratio floor. It is registered in
   `ci/gates.txt` as a core gate (§3.2 item 4). Its in-module positive control grows in three
   steps inside R1, because core has no in-module import edge in its first PR and `app`/`kernel` arrive later:
   - **first core PR:** the engine's own self-test on a synthetic graph in `archtest` (a known violation must be
     reported, and an empty graph must fail); the core guard asserts only that the loader returned at
     least the packages core holds;
   - **once core has leaves:** the PR that moves them names a real edge it creates (e.g. `health` → `log` if the
     flight recorder takes a `log` child logger, or `coretest` → `log`);
   - **once the composition root exists:** `app` imports `kernel`.

   A PR that removes the current control names its replacement in the same change (the ABO rule).
3. **Shared engine**: the classification/compute code moves to `core/archtest` as a library
   (`archtest.Layering(t, archtest.LayerSpec{...})`) so both modules — and every future app repo —
   run the same algorithm. This is what "reusable guard" in R2/R3 means.
4. **The one tag-set list (A-4).** The (GOOS, tag set) pairs that CI or goreleaser
   builds, taken mechanically from the workflows and the Makefile, are **one list** (MOD7-14), which the embed
   check of §3.2 item 4 and 04 cite: default, `bench`, `integration`, `pprof`, `race`, `native_taglib`,
   `embed_frontend fts5 native_taglib` (the image build's combined set, `Dockerfile:74-75`), `GOOS=darwin
   CGO_ENABLED=0` with `embed_frontend` (exactly as goreleaser builds it, `.goreleaser.yml:23-36`; MOD9-3) and
   `GOOS=windows` **for `./cmd/mtls-bridge` only** (`build-mtls-bridge-windows`). A tool
   that needs every file's imports takes the union of the file lists of `go list -e -deps -test -json` over all of
   them, because a default-tag `go list` reports a build-tagged file only under `IgnoredGoFiles`, without its
   imports (round-4 Y2); no file parses `IgnoredGoFiles` entries itself (MOD5-16). R1(b)'s
   `TestCoreImportsOnlyCore`, `archtest.NoKernelImports`, `archtest.SDKOnly` and `TestCoreHoldsNoAppKeyNames` are
   such tools (R9-10). **They run on any tree (R10-9; MOD10-8, OPS10-8):** under `embed_frontend` an unbuilt
   `web/dist` (or hello's `examples/hello/web/dist` in core) makes `go list` report `pattern …: no matching files
   found`, so the tools pass `-e`, ignore exactly that error on a package whose embed pattern names an output on
   the module's committed in-image list (§3.2 item 4's rule and its pattern normalisation), and fail on every other
   `Error` or `DepsErrors` entry; a fixture with no built frontend must pass and one with any other package error
   must fail.
   **Every set is compiled on every PR (R9-10, MOD9-3).** At the evidence commit no PR job compiles `GOOS=darwin`
   (only goreleaser at release) or `integration` (`Makefile:362-369`'s `vet-integration` is called by neither
   `make ci` nor a workflow; round-9 modules-build Z6), so a darwin twin left behind, or an `integration`-tagged test
   broken by a move, surfaced only after merge. A required PR job therefore runs `go vet` for `GOOS=darwin
   CGO_ENABLED=0` and with `-tags integration`, over **both** modules: root `make ci` gains the darwin vet
   (`vet-darwin`) and the existing `vet-integration`, and core's `ci` target gains both (12 §12.3.11, 13 §13.3); both
   are registered in `ci/gates.txt` (09's R1 classification table). `vet-darwin` runs **without** `embed_frontend`,
   because `go vet` has no `-e` and the embed directive fails on every tree with no built frontend (`web_embed.go:11`
   @748fd787a; root `make ci`, `Makefile:785`, builds none), so it would be red on every PR (R10-9; MOD10-8,
   OPS10-8). The files only `embed_frontend` selects carry no GOOS constraint: at `748fd787a` they are `web_embed.go`
   (which imports only `embed`) and `internal/server/static_embed.go` with its recovery test
   `static_embed_recovery_test.go` (which import `embed`, the standard library, gin and ABO packages, so "import
   only `embed`" was too strong; VER11 note, R11-5), and in core hello's `ui_embed.go`. The image job compiles the
   non-test ones on every PR, and the tag-union tools above still read the darwin `embed_frontend` set.
   There is no stage marker and no release-gating commit trailer (§3.8). No tool evaluates later releases ahead of
   time: a package rewired or moved too early fails in the CI of the PR that does it, under every (GOOS, tag set) of
   this list, through Go's `internal/` rule and `archtest.NoKernelImports` (R2), and 04 stays the informative
   per-package map (rows, destinations, releases, LOC), which feature PRs never regenerate (A-4, A-11).

## 3.6 Naming conventions

| Thing | Convention | Example |
|---|---|---|
| App ID | `^[a-z][a-z0-9]{1,15}$` (anchored, FE8-7), field `app.Options.AppID` | `abo` |
| Module ID | lowercase `[a-z][a-z0-9]{1,15}`; **reserved** — the first path segment of every route or page core serves and every namespace core owns, each name assigned to the one package that serves it: `httpapi.ReservedSegments()` (server paths) = `api`, `v` followed by digits, `auth`, `events`, `health`, `livez`, `readyz`, `metrics`, `assets`, `static`, `ui`; `ui.ReservedSegments()` (shell routes) = `activity`, `invite`, `login`, `settings`, `setup`, `system`, `users`; `rbac.ReservedNamespaces()` = `core`, `custom`. An app with a legacy mount adds inputs with different meanings: `LegacyMount.TopSegments` (top-level server paths of a legacy mount, e.g. ABO's `ping`, `status`, `logout`, `socket.io`, `public`; they feed both the reserved IDs and `ui`'s non-SPA predicate), `LegacyMount.SPAShared` (top-level segments where the mount's routes share a path with a shell page, e.g. ABO's `login`: the Audiobookshelf-compatible `POST /login` while `GET /login` is the page; reserved IDs, but **never** non-SPA) and `app.Options.ReservedAPINamespaces` (second segments under `/api/`, e.g. ABO's ABS-compatible unversioned `/api/<seg>` names; reserved IDs only, because a native module API lives at `/api/<moduleID>/v1`). The first segment of a manifest's `LegacyPrefixes` is a reserved ID only; the non-SPA set is exactly `TopSegments` minus `SPAShared` (05-http-api §3.5, §8.2). `module.ReservedIDs()` is **static**: it returns the union of the three owner lists (all three exist from R1, §3.3 `ui`), and a test asserts that it equals this row exactly. The kernel validates module IDs at 06 P1 against that set plus `app.Options.ReservedAPINamespaces`, and re-checks them at P7 against every recorded mount's `TopSegments` and `SPAShared`, still before listeners open; a kernel boot check fails when a `ui.ReservedSegments()` name is a non-SPA segment (a shell page that a full page load could never reach), and passes for a `SPAShared` segment. | `books`, `tv`, `movies` |
| Core namespace | the reserved module ID `core` | `core:admin` |
| Permission | `<moduleID>:<action>[.<sub>]` | `books:edit`, `tv:request`, `core:users.manage` |
| Task kind | `<moduleID>.<segment>(.<segment>)*`, every segment matching `[a-z][a-z0-9-]*` (dots allowed after the module ID; no `_` and never `:`, the storage separator — none of ABO's 269 op IDs has either, `internal/server/testdata/op_ids.golden`); `tasks.ParseKind` enforces exactly this (05-tasks §5T.3.1) | `books.scan`, `books.dedup.full-scan`, `core.backup` |
| Event topic | `<moduleID>.<noun>[.<qualifier>].<verb-past>.v<N>`: segments `[a-z][a-z0-9-]*`, at most one qualifier segment on the noun, `N` ≥ 1 (05-events §5E.3.1) | `books.book.changed.v1`, `core.integration.health.changed.v1` |
| Storage keyspace | `m/<moduleID>/` for modules, `c/<subsystem>/` for core | `m/tv/series/…`, `c/tasks/run/…`, `c/config/…` |
| Native API path | `/api/core/v1/...`, `/api/<moduleID>/v1/...` | `/api/books/v1/books/{id}` |
| Config section | `core.*`, `modules.<moduleID>.*` | `modules.books.scan.workers` |
| Schedule key | `<owner>.<subsystem>.schedules.<name>`, where `<owner>` is the owner's config namespace (`core`, or `modules.<moduleID>` per the Config section row); `<subsystem>`, `schedules` and `<name>` segments match `[a-z][a-z0-9_]{0,47}`. Every schedule key is declared in its owner's config section and registered through `tasks.ScheduleFromConfig`, which accepts any declared key of the registering owner and refuses all others (R2-7; 05-tasks) | `core.backup.schedules.create`, `modules.books.scan.schedules.full_sweep` |
| Role ID | core seed roles: bare names (`admin`, `editor`, `viewer`, kept from ABO); module seed roles: `<moduleID>.<name>`; custom roles: `custom.<slug>` | `admin`, `tv.admin`, `books.editor`, `custom.night-shift` |
| Audit action | `<moduleID>.<noun>.<verb>`: exactly three segments `[a-z][a-z0-9_]*`, the verb in the present tense (an action, not an event; no version suffix, because records are stored, not consumed); core's actions are the normative table of 05-rbac §7.1 | `core.apikey.create`, `core.role.update`, `core.restore.stage`, `books.user.merge` |
| Metric name | One namespace per app, `app.Options.MetricNamespace` (`[a-z][a-z0-9_]*`, default `mediacore`; A-5). Core's series are `<MetricNamespace>_<subsystem>_*`; module series are `<MetricNamespace>_*` while the app **ships** one module and `<MetricNamespace>_<moduleID>_*` when it ships two or more — counted over the modules compiled into the app (the `app.New` module list, enabled or not), so enabling or disabling a module never renames another module's series. Core's task subsystem scope is `ops` for every app (05-tasks 5T.11), so a native app's task series are `<MetricNamespace>_ops_*`. Standard OTel semantic-convention names (e.g. `http_server_*`) are used unchanged. **Names may change (D10):** ABO sets `MetricNamespace` to `audiobook_organizer`, so a books series keeps its name and an emitter that moves into core keeps it too where core's subsystem name already matches; otherwise the series takes core's name, and `deploy/prometheus/*.yml` and `deploy/grafana/dashboards/*.json` change in the same PR. A rename that buys nothing is not made. **Every family has exactly one owner (R9-3; MOD9-1, MOD9-2):** two emitters of one name and label set make every scrape fail, so the PR that gives a family a core emitter deletes its ABO emitter; a test builds the app's real recorders with the kernel's Provider, gathers, and fails on an error or a duplicate family, and a second test fails when a series that a file under `deploy/prometheus/` or `deploy/grafana/dashboards/` names is not served (05-metrics §12). **HTTP series have one recorder:** core's perimeter stage 2 records every request, the ones the legacy gin mount serves included, and `otelgin` leaves the mount in R1; `httpapi.MatchedPattern(ctx)` stays only for the SEC2-22 post-match check (05-http-api §4.2, §8.2). Core's histogram buckets apply to every series. | `mediacore_ops_runs_total` (default namespace), `audiobook_organizer_ops_runs_total` (ABO), `audiobook_organizer_books_total` |
| UI route | `/<moduleID>/...` for module pages; shell pages under the segments of `ui.ReservedSegments`; one-time tokens travel in the URL fragment, never in the path (05-authn §4.13), from R2, when the link forms change together with the SPA and the one-time tokens HEAD issued are reset (D12 (b)); before R2 invite links keep HEAD's `/invite/<token>` path form (`web/src/App.tsx:201-206`; FE4-2). Optional static client redirects may map HEAD's SPA paths to the new pages; they carry no counter and no window | `/books/library`, `/settings`, `/activity`, `/invite#token=…` |
| Build identity | Two values, never interchanged (R5-7). The **UI build ID** names one built UI bundle: `BUILD_ID` when it is non-empty, and it must match `^[A-Za-z0-9._-]{1,64}$` and must not be `dev` in any case (reserved: a `dev` ID never triggers the reload rule), or `vite build` fails (an empty value falls through; FE6-7, FE7-6); else, in a clean checkout (`git status --porcelain --untracked-files=no` prints nothing, so an untracked file never changes the rule; FE7-1), `<short sha>-<commit unix time>`; else (dirty tree, or no `.git`) `<UTC timestamp>-<8 random hex>`. The npm build itself generates it (the Vite config: ABO's from R1, the `@mediacore/shell/vite` preset with the shell) and writes it into the bundle's meta tag, so `make build`, goreleaser, the Docker frontend stage and a bare `npm run build` all stamp it with no side script (07 §7.6.3 owns the text; FE5-1, FE5-2). The meta tag is the one source: the server's `X-App-Build` reads it, and the client reads it once at page load through `readPageBuildId()` of `@mediacore/api-client`, the only meta reader, and passes it to `checkBuild(response, { buildId, shell })` (07 §7.5.2; FE7-5); no Vite config, ABO's pre-shell one included, writes the UI build ID into a `define`, and no `core/web` package reads one, because a library built to `dist/` before the app would bake its own value (FE6-1, FE7-3). It serves only `X-App-Build`, `ui_build_id` and the stale-build reload rule. The **binary ID** names the running executable: `app.Options.Version`, `+`, and the SHA-256 of the executable, computed once per process before the store opens by `buildid.Binary`, the dependency-free L0 leaf `core/buildid` that lands in the first core PRs of R1 (§3.3) — called by ABO's store-format guard and `--version-json` until the kernel exists, and then by the kernel before P−1, where `app.BinaryID` delegates to it (FE6-3); it needs no ldflags and no `.git`. The updater, backup's staged-by and source fields and `--version-json` use the binary ID — never the UI build ID, which is `dev` for every API-only build, and never `version.Version`, which is the core constant (FE5-3) | UI: `3f9c2ab-1762214400`; binary: `<version>+<sha256 hex>` |

ABO's un-namespaced names are mapped onto these conventions **once, in R2**, with no alias, window or second
spelling (D10): the 13 permission atoms through books' atom → scope table (§3.3 `rbac`), the flat config keys and
env names through the config rename table (§3.3 `config`), HEAD's op IDs through books' kind-rename map (§3.3
`tasks`), and the core-service families into `c/<subsystem>/` (§3.8). Books' own domain families (about 240 of the
258) stay in place under books' legacy claims, because renaming them buys nothing. ABO's routes stay on its gin
engine under `MountLegacy`, so `/api/v1/...` is a permanent books prefix; the core subsystems' HEAD routes on it are
replaced by `/api/core/v1/...` routes in the R2 flip of their subsystem and deleted, and the SPA and the first-party
scripts switch in the same PR.

## 3.7 Process shape

One `main` per app calls `app.New` with the modules it ships and the adapters it enables, then
`Run`. The kernel validates the module graph (duplicate or reserved IDs, `MinCoreVersion` against
`version.Version`, permission/route/task-kind/keyspace collisions, migration order) **before**
opening listeners, runs migrations, constructs services, starts the task executor and scheduler,
mounts routes, and serves. Module enablement is **restart-bound** (kept from old decision 10) and is
read before module registration (06): a disabled module contributes no route, task, schedule,
worker, nav entry or filesystem mutation, and its data is retained.

The order of boot phases is defined **once**, in 06 §6.5.1's boot-phase table; every other file
references it. Four properties of that order are architectural and hold for every app:

- **Core services own their state natively.** Every core service persists in its own `c/<subsystem>/` keyspace
  through its native store (§3.3 "Store interfaces and importers"), for every app; core holds no app key name and
  no storage grant. For ABO the core-service families — users, OAuth identities, the invite allow-list, API keys,
  roles, task runs and their checkpoints, the clock, pause and stand-down keys, the config blob and secret rows,
  system log rows, import roots, AI batch jobs, integration instances and the webhook rule — move there **once**,
  in R2, through books' cutover steps and core's importers (§3.8). Sessions, one-time tokens, throttle and quota
  holds, the full-text index, derived indexes and caches are reset or rebuilt instead (D12 (b)).
  Books' own domain families stay in place under books' legacy claims.
- **Checkpoint before the first shape-changing write.** A boot that will run a migration step, a level ratchet or
  a marker write takes the one pre-migration checkpoint, after the disk headroom check, before its first write
  (05-storage §5S.3.12); a boot with none of these takes none, and a boot that cannot take it halts before writing
  anything. Every checkpoint is deleted automatically 7 days after it stops being needed (D12 (d); CONFORM-2,
  DATA-8): a completed checkpoint 7 days after its attempt completed; an open one is kept while the live store
  is still at its pre-migration levels, because it is the rollback, and its 7-day clock starts once the store has
  completed a migration or been restored by any path (the checkpoint, an archive, a failed restore's `failed/` copy,
  `restore_left`); health raises `storage_checkpoint_open` meanwhile. The sweeper and `storage prune-checkpoints`,
  which deletes a checkpoint earlier, exist from R1, whose first boot on a non-empty store takes one. The checkpoint
  directory is mode 0700 and its files 0600 (05-storage §5S.3.12).
- **Migrations are one-way and stamped.** Opening the store runs the single per-owner level guard (05-storage
  §5S.3.7): it refuses a store stamped above the build's level. A real format change raises a level: books' own
  migrations, and R2's level step, which raises `books` and `core` and sets books' projection onto HEAD's format
  stamp to `LegacyMaxFormat + 1`, so HEAD (R0) and R1 binaries refuse an R2 store (`TestCutoverRefusedByOlderBuilds`).
  Rollback across a level step is never a binary swap: it is an offline restore of the checkpoint or of an
  off-host archive plus the previous binary (§3.8 "Rollback").
- **Legacy migrations first; one floor, in R3.** Books' existing legacy migrations 1..66 run in the phase before
  every core and module migration, on books' own claims, and are kept until R3. R3 deletes them, the HEAD-format
  readers and the cutover steps, and declares books' one upgrade floor, `UpgradeFloor{InstallFirst: R2}`: a store
  that has not run R2's level step is refused at boot, before any write, with exit 78 and the last log line
  `install <R2 release> first`, and `--print-upgrade-floor` prints the requirement (02 N14). Nothing checks the
  floor before a binary swap: neither the updater nor the deploy recipe reads the store. An **empty store** is
  exempt: its fresh-install batch records every books legacy step 1..`LegacyCap` as applied and every step ID that a
  declared `UpgradeFloor.Requires` names (M2–M6) as satisfied, whether or not the build still registers that step, and
  books' versioned migrations are numbered from `LegacyCap+1`, so a fresh install or a factory reset passes the floor
  on its first boot and every later one, in R3 too, which no longer registers M2–M6 (R7-2, DATA7-7; IMPL-7).

```
main ──► app.New(opts, books.Module(), …)
           │  kernel: (phases P−1–P8 of 06 §6.5.1)
           │          the one staged swap, restore or reset (P0a) → config boot → validate manifests
           │          → open storage (per-owner level guard, upgrade floor)
           │          → R2 cutover preflight (read-only, only while the cutover has not started: books' level below M7's target and
           │            no cutover step with a progress record in c/storage/dm/; D13: identity rows, account merges)
           │          → disk headroom check → pre-migration checkpoint (before the first shape-changing write)
           │          → secrets.Open (the keyring; R2 on)
           │          → M1: books legacy migrations 1..66 (first, on books' own claims; until R3)
           │          → versioned core and module steps (05-storage cross-keyspace DAG), M7 among them
           │          → R2 cutover M2–M6 (books' decoders → core importers)
           │          → compaction M8 (06 §6.5.1 P4b)
           │          → build the typed services struct (explicit constructors, no init();
           │            each store interface bound to its native implementation)
           │          → register tasks/schedules
           │          → mount httpapi: core routes (internal/<capability>http), module routes,
           │            legacy mount (httpapi.MountLegacy, permanent for ABO's gin engine),
           │            arrfacade (deferred) → ui.Handler (only for an app that ships a shell
           │            build; until then the legacy mount owns `/`)
           ▼
         App.Run(ctx): start everything in the single start order of 06 §6.5.2,
                       stop in its reverse within core.shutdown.budget (05-health §5H.4)
```

## 3.8 Releases and the cutover (D10–D13)

This section is the one definition of the release keys **R0–R3** that every spec file uses. 09 owns each
release's content table (§9.4) and the per-subsystem flip details; the owner files own their migration steps.

**How work lands (D11).** Trunk-based: code lands **dark** on `main`, and each subsystem is wired in **one flip
PR**. Every merge passes `make ci` and the e2e suite; the named required checks stay (14 Q-I). There are no soaks,
no soak ledger, no stage marker and no release-gating commit trailer; an optional 1-day smoke run after a deploy
is the owner's choice. Feature work keeps its move windows (14 K24). From the first R2 flip (F3) until R2 ships,
automatic promotion and auto-update are paused, so R2 reaches production as **one** cutover release (D12 (a)). A lead
extension of D12 (a), which the owner may override (IMPL-9), pauses promotion the same way from R1's first core
PR until R1 ships, so R1 and R2 each reach production as exactly one release. **The pre-freeze release** is the last
production release before F3 merges; it is also the `.prev` binary once R2 is deployed. A hotfix during the R2 freeze
is cut from it, the restore drill boots its binary, and `TestCutoverRefusedByOlderBuilds` runs it (09 §9.2.1).
D12's four owner answers are cited as D12 (a)–(d) everywhere, so they never clash with the flip labels F2–F9 below.

**Releases.**

| Key | Content | Migrations | Rollback |
|---|---|---|---|
| **R0** — hotfixes | The six early fixes of 05-authn §11.1; the toolchain bump (13); the updater hotfix (exit 75, the `.prev` slot, semver with local-build detection, check-only unattended apply; 05-updater). | none | binary swap |
| **R1** — core runtime, no user-data change | The nested module and its CI (§3.2: guards, the image job, `build_web.sh`, `restore-offline`); the leaves, metrics construction and config stage 0; the composition root `app.New`, core listeners, the perimeter (with `otelgin` removed) and logging; core storage opening the store, books' claims strict, the staged restore through the one journaled swap, the offline reset (`storage reset`, R1's only reset path: HEAD's two gin reset routes are deleted and the SPA hides its reset control until the core route lands in F4), offline opens, the crash matrix and the re-home of `internal/database`; the checkpoint sweeper and `storage prune-checkpoints` (the first R1 boot on a non-empty store takes a checkpoint, §3.7; CONFORM-6); the first UI steps (07 §7.19 steps 1–2: react-router 8, the api-client); the HTTP metric changes of core's perimeter with their dashboards and rules, the `http_route` value change in R1's release note (05-metrics §13). | none on user data: core writes only its `c/storage/` bookkeeping (install marker, levels at 0), which HEAD ignores, after the pre-migration checkpoint on a non-empty store | binary swap to R0, which loses nothing (one R1-only test: the R0 binary opens an R1 store); or a restore |
| **R2** — the cutover | The foundation PRs F2, dark; then every core-service family, in the flip order below: config and secrets (with the operator precedence and the fail-closed rule), identity (hashed sessions and one-time tokens, `mck_` keys, native scopes), tasks (every definition native, schedules, the log journal, the Activity page on core's task routes), events, outbox and realtime (the SPA's realtime client), integrations, library roots, the rebuilt index and notify, backup in core's layout, the moved updater, health and the kernel `/metrics`; the generated clients and identity UI; the shell if it is ready; every first-party consumer of a renamed thing (the SPA, scripts, the repo's agent skills, deploy files, the Dockerfile `HEALTHCHECK`, `deploy-wait-ready.sh`, dashboards and rules) switched in the flip PR that renames it; the operator runbook. | M1, M7, M2–M6, M8 below, one ordered `BootBlocking` list behind the checkpoint | offline restore of the checkpoint or of the owner's off-host archive, plus the pre-freeze release (below) |
| **R3** — cleanup and floor | The shell flip if it was not in R2; deleting `internal/serviceregistry`, `pkg/plugin/sdk`, the no-op `--db-type` and SQLite flags, books' legacy steps 1..66, the HEAD-format readers, `internal/books/cutover`, and together `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state` (XFILE-2); books' start hook `legacy-file-cleanup`, which deletes `.encryption_key` and HEAD's `library.bleve` directory, skipping only `.encryption_key` while health raises `secrets_keyring_not_exported` (05-storage §5S.3.8), crash-labelled in 06 §6.5.1's writer table (CONFORM-4, DATA-16); `UpgradeFloor{InstallFirst: R2}` (§3.7). After R3 a pre-R2 archive is restored by installing R2 first, with the archive's off-host `.encryption_key` (09 §9.2.2). | no migration step; the start hook deletes the two files | binary swap to R2 (R2 reads neither file on a migrated store) |

**Flips inside R2.** R2 opens with **F2, the foundation PRs**, landed dark before F3: they build every core
surface that more than one flip, or a cutover step, needs, so that each later flip only switches ownership (IMPL-1–IMPL-5,
IMPL-12, XFILE-4). The flips that follow are serialized, because they share `main.go`, the kernel
wiring and `internal/server`; each carries the migration step of its families and the UI calls their routes serve, so
every intermediate `main` boots and passes e2e. The labels follow the subsystem order.

| Flip | Subsystem | Migration step it carries |
|---|---|---|
| **F2** | the foundation PRs, dark, in R2 before F3: `storage/datamigrate` and `internal/storagemigrate`; `tasks` with its executor and `ForEach` (no books kind registered yet, and no `ops` instrument created before F5); the `audit` store and the normative action table, `core.storage.*` and `core.settings.update` included; the full `core:*` scope catalog, every scope any flip uses declared here; every core config section (backup, auth, tasks, updater and the rest) registered, dark; the health registry and its route, with the kernel's own components `core.storage` and `core.secrets` registered on it kernel-side (the module handles land at F5), so the tokens F3 raises surface; the minimal owner registrations of the three core kinds whose owners land later (below) | none |
| **F3** | config and secrets | M2 |
| **F4** | identity (`auth`, `rbac`, `audit` serving; the gin auth chain replaced by `legacyauth`; the core reset route); the `events` bus, on which the kernel publishes `core.rbac.changed.v1`; the native settings routes (`/api/core/v1/settings`, `/settings/schema`), with the gin settings handlers of the `k` and `s` keys deleted and the SPA settings page switched (05-config §1.3.4) | M3 |
| **F5** | tasks and the log journal; HEAD's engine, its task routes (the books operations routes and `/api/v3/ops/*`) and the two operation streams deleted; the Activity page on core's task routes, polling `GET /api/core/v1/tasks/runs` until F6, and no longer reading `ops.ts`; HEAD's `operations_*`, `operation_*` and `op_*` families deleted and the `ops_*` label changes made, with their dashboards, rules and rule tests (05-metrics §13); the three late-owner core kinds registered | M4, M5 |
| **F6** | outbox and realtime (the bus is live from F4), with the SPA's realtime client, which the Activity page uses from then on; `/api/events` deleted | none |
| **F7** | integrations, AI jobs, provider gates, library roots, notify; HEAD's AI families renamed with their dashboards and rules (05-metrics §13) | M6 |
| **F8** | backup, the updater move, health, the kernel `/metrics` mount | none |
| **F9** | the shell (in R2 if ready, else R3) | none |
| — | the runbook and the R2 release note (last PR of R2); every first-party caller and deploy file has already changed in the flip PR that renamed what it uses | none |

Metric renames land only where their emitter moves, each with its dashboards and rules in the same PR: R1 (the HTTP
series), F5 (the ops families) and F7 (the AI families); F8 moves only the kernel `/metrics` mount (05-metrics §13;
XFILE-1).

**The three late-owner kinds.** HEAD's throttle reset, diagnostics export and AI batch poller become the core kinds
`core.integrations.release-holds`, `core.diagnostics.export` and `core.ai.batch-poll`, whose owner packages land at
F7 (`integrations/providerhttp`, `integrations/ai`) and F8 (`health/diagnostics`). F2 creates a minimal owner
registration for each, F5 registers the three kinds on core's executor when it deletes HEAD's engine, so no
intermediate `main` lacks a definition and M4 renames runs to registered kinds, and F7 and F8 extend the
registrations when their owners land (05-tasks §5T.8.2).

M7 and M8 land with F3 and run in every build from then on. Each later flip that adds a step raises M7's target
levels in the same PR, so every build stamps exactly the steps it has applied, and an older `main` refuses a store
that a newer `main` migrated. M8's step ID names the last step it compacts after, so a flip that adds a step also
registers a new M8 and a store migrated by an earlier `main` is compacted again (05-storage §5S.3.13; IMPL-13).

**Migration steps (R2, `BootBlocking`, resumable).** They run in the order of this table: **M1, M7, M2, M3, M4,
M5, M6, M8**. M7 runs before any family row is deleted, so a boot that stops between two steps leaves a store that
every older binary refuses. Each step from M2 to M6 is a books migration that, in one batch per chunk, decodes
HEAD's rows, writes them through the importers (§3.3) and deletes them.

| Step | What it migrates |
|---|---|
| **M1** | Books' legacy steps 1..66, which run only on older stores and already exist. |
| **M7** | Level step, in one batch before M2's first chunk: `books` and `core` raised, and books' projection onto HEAD's format stamp set to `LegacyMaxFormat + 1`. HEAD, R0 and R1 refuse the store from this batch on. |
| **M2** | Config and secrets: the fields of `setting:config_blob` into `c/config/` under canonical keys (the rename table); the plaintext blob secrets and HEAD's four encrypted rows re-encrypted into `c/secrets/` with the keyring that `secrets.Open` created after the checkpoint (06 §6.5.1 P4); M2 moves only the blob's fields and rewrites the residual blob, which stays an active books claim until M6 retires it, and its `Verify` checks that the moved keys are absent, not that the range is empty (DATA-1); the gap-fill `config.yaml` keys imported once, then the file stripped of its managed keys and deleted if empty (M2b, 05-config §1.12.2; the checkpoint already holds a copy). The blob's integration-instance fields are left for M6. `.encryption_key` stays until R3. |
| **M3** | Identity: users, OAuth identities, the invite allow-list, API keys and roles into `c/auth/` and `c/rbac/`, every ID kept; `abk_` keys keep their SHA-256 hashes; atoms mapped to native scopes by books' table, with the ambient rule of §3.3 `rbac`, and HEAD's `admin` seed and every key whose owner holds HEAD's `admin` role (the bootstrap key, an admin's every-atom or empty-list key) mapped to `core:admin` plus every native scope of the 13 atom rows, task-run approval included, so the reach oracle shows no loss; every other holder of every atom (a custom role, or a key owned by a non-admin) gets the union of the 13 rows, a key then intersected with its owner's role set, never `core:admin` (05-rbac §6.1; SEC-12); web sessions, invites and one-time tokens deleted (D12 (b)). Only the auth sub-prefixes of the shared `idx:` family move. The ABS families (`abs_sess:`, progress, bookmarks) stay books'. **D13:** a user, or one of its keys or identities, that cannot be converted stops the upgrade (boot refuses, the report names the row, and an offline command fixes or deletes it), and an account merge in progress refuses the migration until it is finished; both are checked by the read-only cutover preflight before the checkpoint, on a store whose cutover has not started (the cutover has started when books' level has reached M7's target or any cutover step has a progress record in `c/storage/dm/`; the preflight runs if and only if it has not started, and checkpoint reuse is not part of that condition), so the store is untouched when boot refuses; a boot resuming a started cutover skips the preflight, and M3's own check applies only to the rows it has not yet imported; its refusal names `make rollback-restore`, since
`storage cutover-blockers` no longer opens a store whose cutover has started (05-storage §5S.3.13; 09 §9.2.3). Finished merges become permanent: no merge is undone after R2. |
| **M4** | Tasks: every run, open, paused and finished, with its checkpoint, the schedule clocks, the pause and stand-down markers and the `Requires` stamps into `c/tasks/`; stored kinds renamed through books' kind-rename map, in the books families that 05-storage §5S.9.1 lists as rewritten in place too (DATA-5); HEAD's repairs rows (`opv2:row:`, `opv3:rowmark:`) imported with their runs (DATA-3). Only the run keys of the shared `setting:` family move. |
| **M5** | The `syslog:` rows into `c/log/`, converted (D12 (b)). |
| **M6** | Integrations: the blob's instance fields with their secret refs into `c/integrations/instances/`, then the blob deleted; `aijob*` rows into `c/ai/`; HEAD's webhook settings into native notify instances and one rule in `c/notify/`; import roots into `core.library.roots` entries, IDs kept; HEAD's search dirty-set rows, throttle and quota rows deleted (D12 (b)). |
| **M8** | After M3, the store rewritten into a fresh directory (full compaction, flush, then a Pebble checkpoint or copy) and swapped in through the journaled swap, the old directory deleted, so no WAL, recycled log or MANIFEST of the live DB directory keeps a raw token or a HEAD row (SEC-5). The checkpoint stays until its 7-day deletion (D12 (d)). |

A row a step cannot decode is quarantined (§3.3 `storage`), never dropped, except an identity row, which stops the
upgrade (D13, M3); the referential `Verify` excludes quarantined IDs and reports each dangling reference in health
instead of failing boot (DATA-2). Every step has a dry run that reports per-family counts, quarantined rows and the
preflight's blocking rows. The verification of R2 is automated: a fixture corpus written by the R0 binary itself,
plus a separate, labelled, hand-built overlay for the cases R0's API cannot write (corrupt rows, unknown blob fields,
custom roles), each case listed (09 §9.3; IMPL-8); a production-shaped store; per-family count, ID and reference checks;
the config, reach, schedule and search oracles (effective result before = after, with R0, HEAD plus the six early
fixes, as the reach baseline); the parity suites; the Audiobookshelf conformance suite on the migrated fixture; the
crash matrix over every step; the no-raw-token and no-plaintext-secret canaries over the whole live DB directory,
WAL, MANIFEST and OPTIONS files included (the retained checkpoint excepted); `TestCutoverRefusedByOlderBuilds`, run
with the pre-freeze release's binary; a restore drill (the checkpoint restored, then the pre-freeze release's binary
boots it); and e2e. The owner may rehearse on a restored production
archive in a `--network none` container with `core.integrations.disabled=true`.

**What survives R2 (D12 (b)).** API keys (`abk_`) and the Audiobookshelf phone apps' sessions and tokens survive.
Web sessions and one-time tokens are reset, so every user signs in once and open invites are re-sent. Task-run and
system-log history is kept, converted. Throttle and quota holds, derived indexes and caches are reset or rebuilt.

**The frozen contract (D12 (c)) and what stays unchanged because it is cheaper.** The **only** frozen external contract is the Audiobookshelf-compatible API used by
the phone apps: the unprefixed root routes `/ping`, `/status`, `/login`, `/logout`, `/auth/refresh`,
`/auth/openid*`, `/api/authorize`, `/api/me*`, `/api/libraries*`, `/api/items*`, `/api/session*`,
`/public/session/:id/track/:index`, `/api/playlists*`, `/api/collections*` and the authors, series and narrators
routes; the value of its JWT signing secret; and its auth semantics. HEAD's OAuth start path `/api/v1/auth/oauth/{provider}/start` is kept at its exact path as part of it, because ABS
clients such as AudioBooth start a sign-in there: `CoreExactPatterns()` lists it as a second exact pattern beside the
callback, served by core's start handler with the app hand-off deep-link rules (05-http-api §8.1). Its conformance suite pins it and runs on the
migrated fixture, because from R2 its logins resolve users through core auth, by books' `auth.Compat` handle (05-authn §4.10). If the secret's env
name changes, a set retired name refuses boot (§3.3 `config`). Webhooks are not part of it: from F7 every delivery uses core's
native webhook format (05-notify §3.1), the R2 release note says so, and the owner reconfigures each receiver at the
R2 deploy (09 §9.4 R2). Everything else may change. These stay unchanged only because that is cheaper; they are not
contracts, and each has one test (CONFORM-12):

- the OAuth/OIDC callback path `/api/v1/auth/oauth/:provider/callback`: core serves it from F4 (05-authn §4.13), so
  no identity provider needs re-registering, and keeps HEAD's native-app hand-off on it (deep-link start parameters,
  books' hook that mints the phone app's authorization code, the exact-match redirect allowlist, S256-only PKCE;
  SEC-2);
- the remote fingerprint-worker API: it stays a books route on the permanent gin mount, and its `abk_` key survives;
- `/health` (with `ready`; it carries no version, which only the authenticated status endpoints report, 05-health §5H.3.3) and the `/metrics` path;
- the shipped CLI flags (the no-op `--db-type` and SQLite flags are deleted in R3).

Renamed env names, metric names and HTTP paths are listed in the R2 release note, generated from the rename table,
the metric renames and the route deletions; dashboards, rules, scripts and skills change in the PR that renames. The
UI shell builds no old-URL redirects (D13): a former bookmark lands on the shell's not-found page, and the release note
lists the new paths.

**Safety kept (D11).** The early fixes ship first, in R0, and the tests of those whose code R2 deletes are ported
as property tests onto core auth (05-authn §11.1). Every migrating boot takes the pre-migration checkpoint
(§3.7). Before deploying R2 the owner finishes any account merge in progress and clears the dry run's blocking rows
(D13), and takes an off-host archive together with `.encryption_key` and the gap-fill `config.yaml`; after R2's first boot, which creates the keyring, the owner exports the keyring off the host
(05-config §2.4). Older binaries refuse a migrated store (M7). The first R2 boot reports each step to systemd (`EXTEND_TIMEOUT` per step), so a long
migration is not killed.

**Rollback.** R0 and R1 roll back by binary swap. From R2 on, rollback is `make rollback-restore`: stop the
service; compare the source's levels (the checkpoint record's `from_vector`, or an archive's levels) with the levels
`.prev --print-storage-levels` reports, and refuse, naming the release to install (the checkpoint record's
`from_release`: the pre-freeze tag the cutover build embeds, since HEAD's store records no app version; on the archive branch, the release that produced the archive, from its
manifest), when they differ, so a rollback never re-migrates (DATA-9); after an R2 hotfix, `make rollback-restore
RELEASE=<pre-freeze tag>` fetches the named release from the configured release source (`updater fetch-release`,
05-updater §5U.4; apply's trust level: HTTPS from the source, plus the checksum check apply performs if the source
publishes one), checks that its levels equal the source's levels before renaming it to `.prev`, and proceeds, which
is not an arbitrary-binary override (05-storage §5S.3.12); restore the pre-migration checkpoint or an off-host archive **offline** with `restore-offline` — the DB
directory together with the state-directory files its source holds (`.encryption_key`, the gap-fill `config.yaml`
and, for a source taken after R2's first boot, `secrets.keyring`); a restore replaces only the state files its source
holds and never removes a keyring the source lacks (DATA-11) — reinstall the previous binary from `.prev`, and
start; its one probe refuses while a swap is in flight (§3.3 `storage/restorejournal`), and it has no override.
Everything written after the upgrade is lost, listening progress synced by phone apps included, and so is every
revocation made on R2: a key revoked, a user disabled or a password changed after the upgrade is live again (SEC-10),
so fixing forward is the default and the runbook says so. The pre-migration checkpoint holds plaintext secrets and
raw tokens; it is deleted automatically 7 days after it stops being needed (§3.7; D12 (d)), and the release note says
so.

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- SIMP-1 — promotion rule made non-circular (§3.4); Build column in §3.3; `acquire`, `arrfacade`, `integrations/indexer`, download grab/monitor and non-Deluge adapters marked deferred.
- SIMP-2 — `MountLegacy` permanent and supported (§3.3 `httpapi`, R6, §3.6 note, §3.7); no gin retirement.
- SIMP-3 — R5 binds core only; ABO façades allowed and per-App.
- SIMP-4/SIMP-5/SIMP-6/SIMP-7/SIMP-18 — `integrations` is the one instance store; `events` the one topic catalog; `tasks.Schedule` the one schedule mechanism; one compat window (09 §9.2.1); merged compat/storage-format guard (§3.3 rows).
- SIMP-13 — metric row: `MetricPrefix` (ABO `audiobook_organizer`), books families unchanged, core `mediacore_*`.
- SIMP-16/COR-17 — one `entity.Ref`; `tasks.Resource` is `entity.Ref`; multi-entity grabs carry `[]entity.Ref`.
- SIMP-22 — "declared dependencies" removed from §3.7 (`DependsOn` dropped); `MinCoreVersion` checked against `version.Version`.
- SIMP-26 — §3.7 diagram says "typed services struct", not "container".
- COR-2 — `storage/index` depends on `storage`, never the reverse (§3.3).
- COR-3 — `integrations/download` owns polling/completion; `acquire` consumes its events (§3.3).
- COR-16 — Role ID row: `<moduleID>.<name>`, `custom.<slug>`, bare core seed roles.
- COR-18 — `internal/<capability>http` route packages at L6, imported only by `kernel` (§3.3, R4).
- COR-19 — bridge rule for `internal/*` packages (R4); `storagemigrate`, `logjournal` at L4.
- COR-23 — R7 AST rule accepts typed pass-through of `tasks.Parallelism`.
- COR-26 — task-kind segment grammar `[a-z][a-z0-9-]*`; `app.Options.AppID`; metric prefix named.
- FE-1 — `ui.Handler` mounts only for shell builds; the legacy mount owns `/` until then (§3.3, §3.7).
- FE-23 — reserved module IDs cover every shell route segment (`invite`, `activity`, `tasks`, …); one `module.ReservedIDs()` union.
- SEC-3 — R6: `AnyAuthenticated` never admits a confined credential.
- DATA-1 — config overrides live under `c/config/` (§3.3 `config`, §3.6 keyspace example). (Narrowed in round 3 by R3-1: only native-only keys live there during the extraction.)
- MOD-1 — Docker layers, `.dockerignore` and PR image job in §3.2 item 4.
- MOD-2 — visibility rule (§3.4) and R2: ABO never imports moved `internal/`/`kernel/` packages.
- MOD-3/MOD-4 — genproto exclude moved to `core/go.mod`; X2/X3 recorded; supported workspace command; exclude checks (§3.2 items 1 and 5, §3.2.1).
- MOD-6 — E2c's package choice is not a placement; 04 authoritative (§3.2.1).
- MOD-7 — `examples/hello` is a package in the core module; `archtest.SDKOnly` covers it (R2).
- MOD-8 — `GO_MODULE_DIRS` scope and per-target classification (§3.2 item 4).
- MOD-9 — one web build implementation, path filters, Dependabot (§3.2 item 4).
- MOD-10 — `ignore ./web`, `ignore ./examples/hello/web` in the `core/go.mod` template.
- MOD-11 — dependency skew cost and check (§3.2 item 6).
- MOD-12 — public L0 `version` package (§3.3, R4).
- MOD-14 — sound positive controls: `. → cmd` and a classifier control (§3.5.1).
- MOD-15 — callback bullet dropped; floor count stated once with derivation (§3.5.1).
- MOD-16 — R1(b) uses `go list -deps -test`.
- MOD-17 — `core/Makefile` referenced (§3.2 item 4; specified in 12).

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-C request (COR-23) — R7 names `RunSeq`/`ForEach`, the three accepted argument forms (constructor, assigned-only-from-constructor pass-through, validated required field) and the `*ItemsError` return, as 05-tasks §5T.3.2 states them.
- integration pass: R-C request (COR-26) — task-kind row states the `:` exclusion and points at `tasks.ParseKind`; it already used 05-tasks' segment grammar.
- integration pass: R-C request — event-topic row allows one noun qualifier segment (`core.download.grab.added.v1`), matching 05-events.
- integration pass: R-B request (SIMP-17, COR-19) — `storage/datamigrate` row in §3.3 and placed at L4 in R4.
- integration pass: R-B request (DATA-16, DATA-17) — R8 says reads and writes and defers claim modes and nesting to 05-storage §5S.3.4.
- integration pass: R-G request (COR-8) — §3.7 `App.Run` line points at 06 §6.5.2's single start order and `core.shutdown.budget` instead of listing an order.
- integration pass: R-G / 07 request — §3.6 UI-route example `/system/tasks` → `/activity`.
- integration pass: R-H request — stage range S0–S10 (09 v1.1.0 numbering with S2b and UI-0); package count 55 leaving / 160 remaining (04 §8 note 6).

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-1 — §3.7: rollback floor = the Teach build, enforced by the level guard at storage open; `compat` row points at it. (Superseded in round 3 by R3-1.)
- R2-4 — §3.7: books legacy migrations run before core claims are enforced; minimum-upgrade-from release (02 N14).
- R2-8 — §3.7: one boot-phase table (06 §6.5.1) referenced; checkpoint before any write; diagram follows the phases.
- MOD2-5 — §3.2 item 1: exclude check redesigned (present; green with it; edge still in `go mod graph` and `ambiguous import` without it, in a temp copy); needed from S0; X10/X16 recorded in §3.2.1.
- MOD2-6 — §3.2 item 4 tidy coupling rule and per-module `tidy-check`; item 6 one-PR Dependabot verification.
- MOD2-7 — §3.5.1: core layers mapped onto ABO's scale (L0–L5 → −1, L6 → 5, L7–L9 → 6, `internal/books` allowance); `loadGraph` keeps mediacore imports.
- MOD2-8 — §3.2 item 4: core coverage is a ratchet floor in the merge gate; SC5 thresholds are a readiness report.
- MOD2-9 — §3.2 items 1 and 4: core-owned scripts live under `core/scripts/ci/`; root-comparison checks are root-side jobs.
- MOD2-10 — §3.3 `examples/hello` row (lands at S2, grows with the stages, produces `core.json`) and §3.4 rule (b) (never pulls a package forward).
- MOD2-15 — §3.2 item 4: empty `core/go.sum` committed at S0 (X12).
- MOD2-18 — §3.2 item 4: `go fix -diff` is core-only; per-module list gains `tidy-check`.
- MOD2-19 — §3.2 item 4: core deadcode ratchet runs from the root over app mains, filtered to `internal|kernel`.
- MOD2-21 — 12's criteria cited as SC1–SC10; every "wave" replaced by the 09 stage.
- COR2-16 — §3.2.1: core gets its own `core/internal/keyedmutex` copy (04 §9); ABO keeps its package.
- COR2-19 — §3.6 audit-action grammar: three segments, present-tense verb, examples fixed.
- DATA2-29 — R8 example: carve-outs in `idx:`, `setting:`, `preference:` (no `pref:` carve-out).
- FE2-6 — §3.6: liveness probe is `livez` (not `healthz`).
- FE2-7 — §3.6: `logout` is no longer a core reservation (ABO's `TopSegments` keeps it); boot check that no shell route is a non-SPA segment.
- FE2-8, SEC2-6 — §3.6 UI-route example `/invite#token=…`; tokens in the fragment.
- FE2-11 — §3.6 Module ID row: every reserved name assigned to one owner list (`assets`, `static`, `ui` to `httpapi`; `admin`, `tasks` dropped); `LegacyMount.TopSegments` vs `app.Options.ReservedAPINamespaces`.
- SEC2-14 — R6: non-Public `Router.Infra` routes run the authenticated stages.
- SEC2-16, SEC2-17 — R6 and §3.3 `httpapi`: no chain-less group; `LegacyAuthGin` only until S4 via `app.Options.LegacyAuthGinUntil`; `OwnAuth` only with a `Registrar.AuthMethod` compat method; `TestOwnAuthRoutesGolden` (05-http-api's objection resolved here).
- SIMP2-3 — R4 and §3.3: `internal/jsonschemax` is the L0 reflector; overrides contributed per package.
- SIMP2-6 — §3.4: deferral applies to members of built packages (facade-only key machinery).
- SIMP2-10 — §3.6 event-topic example is a built topic (`core.integration.health.changed.v1`).
- SIMP2-11 — §3.3 `@mediacore/shell` row: v1 notification scope (contract, mail, webhook, global routing) admin pages and toasts; no inbox or personal channels.
- R2-7 (COR2-6, SIMP2-1) — §3.6 Schedule key row: `<owner>.<subsystem>.schedules.<name>`, declared, via `tasks.ScheduleFromConfig`.
- SIMP2-13 — §3.6 metric row: segment decided by the modules the app ships, not the enabled ones.

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): §3.3: `backup/journal` row (L4, lands at S4; COR2-9) and in R4's L4 list; `lifecycle` row adds `lifecycle.NotifySocketEnv` (MOD2-1).
- integration pass (round 2): §3.6: `module.ReservedIDs()` is static; the kernel adds both compatibility inputs in 06 P1. Audit-action segments `[a-z][a-z0-9_]*` with 05-rbac §7.1 examples; schedule-key and topic examples are declared names.
- integration pass (round 2): §3.7: adopt steps stamp `Migration.Floor`; only contracts raise a level beyond it. (Superseded in round 3 by R3-1.)

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- OPS3-7 — §3.7: the minimum-upgrade-from requirement is checked on the store's applied steps (02 N14).
- R3-1 — §3.7: "rollback floor" property replaced by "one stored format per family; the level guard only for real format changes"; cutover deferred (02 N15); diagram no longer names a Teach build. §3.3: `storage` names storage grants, `config` stores existing keys in place through its adapter (native-only keys in `c/config/`), `secrets` takes plaintext blob secrets on first write, `auth` uses legacy rows and core-only hashed sessions; `compat` row drops the Teach-build floor.
- R3-3 (MOD3-1) — §3.4 visibility rule: a package stays in ABO until its replacement surface exists (`internal/startup` until `Registrar.StartupSteps`); R2 and new §3.5.1 item 4: the stage-visibility gate reads non-test imports of every package, including `cmd/*` and the root `main`.
- R3-5 (FE3-1) — §3.6 Module ID row: `LegacyMount.SPAShared`; non-SPA = `TopSegments` minus `SPAShared`; `LegacyPrefixes` first segments are reserved IDs only; P1/P7 split as 05-http-api states.
- MOD3-2 — §3.5.1 item 4: the generator is committed, runs in root `make ci` on every PR touching root Go code, evaluates the next unshipped stage point; stage PRs regenerate 04's tables.
- MOD3-3 — R5: process-global installs stay in ABO until `app.InstallProcessDefaults` (S2); `coretest` captures logs through an injected handler, never `slog.SetDefault`.
- MOD3-5 — §3.5.1 item 1: mapped core layers feed the computed layer; `internal/httputil` becomes layer 5 at S2; `coretest`, `archtest`, `storage/*` → −1, `shared/...` → 3.
- MOD3-8 — §3.5.1 item 2: core guard's positive control staged (S0 synthetic self-test, S1 a named real edge, S2 `app` → `kernel`).
- MOD3-9 — §3.1, §3.3, R4: `storage/keys` and `storage/codec` added as public L0 leaves.
- MOD3-12 — §3.2 item 1: pass rule "at least one applicable probe"; core mode in `make -C core exclude-check`, root mode in root job `genproto-exclude-root`.
- MOD3-16 — §3.2 item 6: `check_module_skew.py` is blocking in root `make ci`; Dependabot group `applies-to` version and security updates.
- MOD3-19 — §3.3 `version`: S1 lands `"0.1.0"`.
- MOD3-20 — §3.2.1: keyedmutex copy cited as 04 §8.1 (S3).
- COR3-3, FE3-8 — §3.3 `ui`: `ui.ReservedSegments()` is a static leaf landing at S2 with `module`; §3.6: the equality test holds from S2.
- FE3-16 — §3.3 and R4: `internal/modulehttp` (L6) serves `/api/core/v1/modules` from a kernel-supplied `ModulesView`.
- SEC3-16 — §3.6: ABO's `SPAShared` includes `invite` (legacy 302) as well as `login`.
- SIMP3-1 — R4: one override composition `Base.With(config.Overrides).With(httpapi's)`; `httpapi` imports the static `config.Overrides`, never a store; `time.Time` belongs to `jsonschemax.Base`.
- SIMP3-2 — §3.4: the deferral check is the permanent `archtest.DeferredMembersAbsent` from S2, not a per-file grep at S7.
- SIMP3-3 — §3.3 `compat`: ledger supplied by the app (`app.Options.CompatLedger`) and merged with core's `core/compat/core-ledger.md`.
- SIMP3-9 — §3.6 Config section example is a non-timing key (`modules.books.scan.workers`).

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): §3.5.1 package count 54 leave / 161 remain (`internal/logger/logtest` stays, 04 §8.4 note 6, MOD3-3); R8 claim modes are read-write, read-only, aging-out, retired — `migrating`/`read-share` dropped, another owner's range only through a `storage.Grant` (`GrantMode` read, read-delete, read-write, optionally `Exclusive`; R3-1); R4 `jsonschemax.Base()` and `config.Overrides()` are functions (R5).

Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`; R4-1 refines R3-1):

- SIMP4-2, SIMP4-3, COR4-4, DATA4-2 (R4-1) — §3.3 new "Store interfaces" table: SDK store interface + native `c/<subsystem>/` implementation per persisting service (landing with its subsystem) + books-supplied legacy implementation; `storage` row: grants are deferred-build members of the cutover; `auth`, `rbac`, `config`, `tasks`, `log`, `library`, `storage/index` rows point at their store; R1 gains (c) "no app key name or row format" with `TestCoreHoldsNoAppKeyNames`; R8: no cross-owner reach during the extraction; §3.4 bucket "app" lists the legacy store implementations; §3.7 first property rewritten (no legacy-format adapters, no grants; extension family with legacy-row hash).
- SEC4-3 (R4-1) — `rbac` row: legacy role rows carry legacy atoms only; namespaced scopes live in books' extension family, because HEAD's worker confinement is a "nothing else" test (`auth.go:271-297`).
- SEC4-1, DATA4-1, OPS4-1 (R4-2) — `secrets` row: the first core write replaces the legacy row and the `config.yaml` copy with a never-matching sentinel; rollback fails closed; roll-forward re-imports a re-entered value.
- MOD4-1 (R4-4) — §3.2 item 4 quotes `.dockerignore` as it is (`docs`, root `*.md`, `.git`, `.github`, `testdata`, `dist`, …) and adds the rule that every `//go:embed` input lies in the Docker context; `compat` row: generated ledger copy `internal/books/compat-surfaces.md` with a CI freshness check.
- MOD4-2 (R4-5) — §3.5.1 item 1: the `internal/books` allowance is deleted; composition root (layer 6, imported only by `.`/`cmd`) vs helper package below it (layer 3); guard assertion on `module`/`app` importers; package count 162 (3 new paths).
- COR4-1 (R4-6) — `config` row: ABO's settings load, encryption init and key-regeneration guard run inside the books legacy config store through the one DB handle.
- MOD4-7 — §3.5.1 item 4: generator unions `go list` over every CI tag set, drops `ForTest` variants and `.test` mains, evaluates every remaining stage point after the committed `shipped_stage:` marker.
- MOD4-13 — `version` row: CI keeps it ≥ the newest checkpoint.
- MOD4-15 — §3.2 item 6: Dependabot via `directories` under the existing `multi-ecosystem-group`; the `applies-to` sentence deleted; security PRs handled by the root-tidy step and the skew check.
- MOD4-18 — §3.5.1 item 1: below layer 6 no ABO package imports `module` or `app`; values are injected by `internal/books`.
- SIMP4-1, COR4-19 — §3.4: `Deferred-build members` headings in every file, package-qualified identifiers, list generated from every heading, per-member deletion, test deleted only when the list is empty.
- SIMP4-4, SIMP4-5, SIMP4-6 — §3.4 deferral list names the cutover-only members, backup encryption and the seed-merge diff.
- SIMP4-10 — `compat` row: core's fragment holds only shared permanent names (`OTEL_*`); every window alias is the app's; gate test.
- SIMP4-12 — `integrations` row: "the one registry for integration instances", native vs legacy-backed (resolves 05-metadata's objection to 03).
- Lead decision (R4-1) — legacy store implementations are supplied on `module.Manifest.LegacyStores`, not `app.Options` ("Store interfaces" paragraph).
- FE4-1, MOD4-9, COR4-18 — `internal/<capability>http` row: `ui_build_id` comes from the embedded bundle's meta tag only; no ldflags value, no boot comparison.
- FE4-6 — `ui` row: `ui.ModuleManifest`, `UIIDs`, `Redirect` land as leaf types with `ReservedSegments` at S2.
- FE4-2 (disposition note) — §3.6 UI-route row: fragment tokens from the shell stage; `/invite/<token>` until then.
- DATA4-18, OPS4-5 — §3.7: "floor release" and "stepping-stone release" named distinctly; the updater refusal is scoped to S0+ updaters; what pre-S0 installs see.
- DATA4-20 — §3.7 lists the families core services use (config blob and secret rows, clock and pause keys, system log rows), not "settings" and "activity".
- DATA4-21 — §3.7: checkpoint only for a boot that runs a step, ratchet or marker write.

Integration pass (round 4):

- integration pass (round 4): §3.3 store-interface table re-cut to the owner files' names and the `module.Manifest.LegacyStores` field per row (`config.OverrideStore`, `auth.Store`, `rbac.Store`, `tasks.Stores`, `log.JournalStore`, `library.RootSource` incl. the books root, `index.DirtySet`, `ai.JobStore`, `providerhttp.GateStore`, plus new `integrations.InstanceStore` and `notify.RuleStore` rows); `storage` row and R8 name only `Grant`/`GrantMode` as deferred cutover members (no `Exclusive`, resolves 05-storage's objection); §3.5.1 count 51 leave / 4 new paths / 166 remain (resolves 04's objection 2); ABO key names (`syslog:`, `setting:`, `idx:`) removed from §3.3/R8/§3.7. `rbac` row names `rbac.Store`.

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`; resolutions R5-1…R5-7):

- R5-1 (OPS5-1, OPS5-2) — `config` row: an app's legacy env alias whose key HEAD let a stored value override feeds the default layer and locks nothing, `""` is unset (HEAD precedence until cutover); `secrets` row: the four secrets HEAD already stores encrypted are not moved (`persistence.go:1955-1958`, `:1989-2003`), only plaintext secrets move and are sentinelled; store table `config` row says the same.
- R5-2 (MOD5-1, MOD5-2) — §3.5.1: `internal/books/configstore` (name open) is a fifth new path (213 − 51 + 5 = 167, margin 17); legacy store packages import downward only (taskstore → residue, shared legacy steps wired by `internal/books`, unresolvable legacy-step preconditions fail a test); store paragraph states the rule.
- R5-6 (DATA5-2, DATA5-3, SEC5-1, SEC5-2) — "Extension records" paragraph: `legacy_ref.fp` covers security-relevant fields only (never last-use times); older-build revocations are monotone; `auth` row: a key with no legacy-equivalent scope gets a grants-nothing legacy scope, never an empty list; §3.7 first property follows.
- R5-7 (FE5-1, FE5-3) — §3.6 new "Build identity" row: UI build ID generated by the npm build itself on every build path, binary ID (`app.Options.Version` + executable SHA-256, `app.BinaryID`) for updater, storage and backup; §3.2 item 4 web bullet: `build_web.sh` only passes `BUILD_ID` through.
- FE5-2 — "Build identity" row: one generator text (`BUILD_ID`; clean checkout `<short sha>-<commit time>`; else timestamp + random).
- FE5-7 — `internal/<capability>http` row: `ui_build_id` is the served shell bundle's ID, `""` while a legacy mount owns `/`; `X-App-Build` carries the legacy mount's ID meanwhile.
- MOD5-3 — R1(c): name set is the claim generator's output via `KeyFamilies()` at its current path, `c/`/`m/` excluded; a family prefix followed by a space is an error string, not a key; one reviewed allowlist file; test cases.
- MOD5-4 — R1(c) input adds ABO's setting/preference sub-keys and the 269 op IDs; store table `tasks` row: clock-key and pause-marker formats divided out of HEAD's scheduler and pause-gate files.
- MOD5-6 — §3.3 row `<pkg>/<pkg>test` (conformance suites, test-support, SDK); §3.1 SDK list; R4 test-support sentence; §3.5.1 classifier maps them to −1.
- MOD5-7 — §3.2 item 4: `scripts/ci/check_embed_context.py` in root `make ci` is the embed-context check (`EmbedFiles` over every CI tag set against `.dockerignore`); the image job is the backstop.
- MOD5-9 — §3.5.1 item 1: every new path enters `layerOf` in the PR that creates it.
- MOD5-11 — §3.5.1: the guard assertion reads non-test imports; the G6 test lives in `internal/books`; constructor `books.Module(…)` (also §3.7 diagram); `opsbridge` is the existing registry path, not a new package.
- MOD5-13 — §3.2 item 4: 09 §9.1's `npm ci` list is the one owner of the web entry points.
- MOD5-15 — §3.2 item 4: core's errcheck and interface-width ratchets, `mocks-check`, `pprof-check` and `core/web` tests run in core's own `ci` target.
- MOD5-16 — §3.5.1 item 4: `go list` per (GOOS, tag set) taken from the workflows and Makefile, incl. `race` and `GOOS=windows`; no `IgnoredGoFiles` parsing.
- MOD5-17 — §3.5.1: `internal/books/rbacreach/` is a data directory, not a package.
- COR5-10, SIMP5-3 — §3.5.1 and the `ai` table row: `internal/ai/resultjournal` is plain books code, no store interface.
- COR5-13 — §3.5.1 count uses 04 §2.4's package names (no `internal/books/legacystores`).
- COR5-14 — store table `config` row "Lands": interface S2, native implementation and `LegacyStores.Config` S2b, write members S3.
- COR5-16, SIMP5-6 — §3.4: heading matched after an optional section number; first column package-qualified identifiers only, everything else in `Also deferred (not checked)`; generator self-test.
- DATA5-4 — "Extension records": only records with a Widening field are suspended; others are re-stamped by their next write; confirmation path per store.
- DATA5-10 — `secrets` row: the sentinel carries a fixed prefix, so an orphaned one reads as unset.
- DATA5-16 — R1(c) keeps the one test name `TestCoreHoldsNoAppKeyNames`; `legacy_ref.fp` naming; the S2 `app.Options.LegacyConfigStore` exception.
- SIMP5-1 — store paragraph: exclusive vs routed models, one `Covers(id string) bool` predicate; table "Model" column; `library` native half is its config entries, no `c/library/` records.
- SIMP5-2 — store table: the `storage/index` row is removed; a note below the table says the dirty set is supplied per index through `index.Spec.Dirty`, with no `LegacyStores.Dirty` field.
- SIMP5-4 — store paragraph: SDK interfaces carry only the native contract; legacy-only members are optional interfaces detected at P4c and deprecated with each family's cutover.
- SIMP5-7 — §3.3 `storage/datamigrate` row and §3.4: the `Online` mode is a deferred-build member; extraction data migrations are `BootBlocking`.
- SIMP5-8 — the §3.3 table is the one list; `secrets.LegacyStore` named in the `config` row.
- SIMP5-10 — store paragraph: S2's `app.Options.LegacyConfigStore` is the sole exception to "never through `app.Options`".
- OPS5-4 — §3.4 and `compat` row: spec Markdown and both ledger copies are Go inputs for CI's `changes` classifier.
- integration pass (round 5): §3.5.1 counts follow 04 §8.4 note 6 (50 leave, `tools/cmd/opsgen` staying, FE5-12; 5 new; 213 − 50 + 5 = 168, margin 18); `internal/books/contract` enters `layerOf` at S5 with `taskstore` (MOD5-9); §3.3 `auth` row names ABO's `["none"]` scope.

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`; resolutions R6-1…R6-7):

- SIMP6-2, SIMP6-3 (R6-3) — §3.1 SDK definition and new §3.3 row and paragraph "Legacy-only identifiers": one package `legacy` (suites in `legacy/legacytest`) holds every identifier that exists only for an app's legacy stores (capabilities, `legacy.SecretsStore`, `legacy.Stores`, `legacy.RootSource`, capability suites), at L5, excluded from the API-stability gate like `kernel/`, so cutover removals are not breaking; owners name capabilities through unexported mirror interfaces checked by `export_test.go`; record-type legacy state and `Manifest.LegacyStores` stay in v1; store table header, `config` and `library` rows; library root source legacy-only (option (b)); R2 `SDKOnly` keeps `legacy` out of hello; R4 places `legacy` at L5.
- MOD6-1 (R6-2) — §3.5.1: `internal/operations/state` stays in ABO as books' legacy run-state package (taskstore and opsgen import it; core's executor state machine is a re-implementation); 213 − 49 + 5 = 169, margin 19; taskstore imports it downward.
- SEC6-1 (R6-6) — §3.3 `config` row: an operator env value or flag that enables auth always takes effect, legacy alias included; a disabling value keeps HEAD's precedence.
- COR6-9 — §3.3 rows for `auth/oidc`, `auth/password`, `health/diagnostics` and the `coretest`/`archtest` test-support sub-packages; R4 sub-package rule (a sub-package takes its parent's layer unless listed); `indextest` named as `index.DirtySet`'s suite.
- COR6-10 — §3.7 diagram: phases P−1–P8 and the served-ledger commit at Run (3b).
- COR6-11 — R5: `signal.NotifyContext` listed beside `signal.Notify`; `TestNoGlobals` skips `package main` (process owners).
- DATA6-14, SIMP6-11 — "Extension records": confirmation paths are `ConfirmAPIKey`, `ReconfirmRole` and the instance and root `confirm` routes; users are never suspended.
- SIMP6-1 — §3.4 deferred list gains the config basis mechanism (05-config §1.12.3).
- SIMP6-4 — capability list: extension confirmation is one capability, `legacy.ExtConfirmer`, for every owner.
- SIMP6-5 — selection models: only a routed store's legacy half implements `Covers`; without it the legacy half covers everything and the ledger records `legacy`.
- SIMP6-6 — one conformance pattern: `<pkg>/<pkg>test` for the native contract, one `legacytest.<Pkg><Interface>Suite` per store interface with a case per capability.
- SIMP6-7 — legacy-only state on a record type is one `Legacy *<Type>LegacyState` pointer field, nil from native stores.
- Lead decision (round 6) — "Legacy-only identifiers": `module.Manifest.LegacyStores` remains a `module.Manifest` field of type `legacy.Stores`; cutovers delete the struct's fields, never the `Manifest` field (consistent with 14 K23's residual). R4's sub-package rule lists `health/diagnostics` at L4 with `health` (COR6-9, confirmed).
- integration request (R-E, 05-config v1.7) — "Legacy-only identifiers": the config capabilities are `legacy.Config*`; the identifiers that cannot move gain `secrets.LegacyCopies`, `secrets.LegacyCheck` (result types of `legacy.SecretsStore`) and `secrets.ReadLegacyKey` (reads the keyring file format); §3.4's deferred list already names the config basis mechanism (SIMP6-1).
- SIMP6-8 — §3.4 grammar: `Also deferred (not checked)` in any position; `—` for a row with nothing to check; one heading per identifier; self-test fails on duplicates.
- MOD6-3 — §3.2 item 4: an embed input is in the context or a listed in-image output (`scripts/ci/embed_build_outputs.txt`, checked against the Dockerfiles' `COPY --from` lines); "no matching files" ignored only for listed outputs.
- MOD6-6 — R1(c): core-owned kinds never spell an app op ID; the app supplies former IDs (`app.Options.LegacyTaskAliases`); a core `FormerKinds` literal fails.
- MOD6-7 — R1(c): one match rule (family prefixes by begins-with plus the space rule; setting keys and op IDs exact or followed by `.`/`:`) that 04 (h) and 05-storage cite.
- MOD6-13 — §3.5.1 item 4: the type-checking half of the gates is `tools/stagecheck`, its own module, so no root package and no count change.
- MOD6-14 — §3.5.1 item 1: contract's last source, `registry/deps.go`'s resolvers, joins at T8.
- FE6-1, FE6-7 — §3.6 "Build identity": `BUILD_ID` only when non-empty and matching `^[A-Za-z0-9._-]{1,64}$`; the meta tag is the one source and `checkBuild(response, { buildId, shell })` reads it at runtime, no build-ID define in `core/web`.
- FE6-3 — §3.6: the binary ID is computed before the store opens by one S0 leaf function, called by ABO's S0 guard until the kernel exists.
- OPS6-1 — §3.7: the S0 updater hotfix and the `deploy` recipe compare the new binary's `--print-upgrade-floor` with the store's sidecar; the S8 updater also checks the signed manifest.
- integration pass (round 6): §3.3 new row `buildid` (L0 leaf `core/buildid`, lands at S0; FE6-3) and R4 lists it at L0; §3.6 "Build identity" names `buildid.Binary`; `legacy.TaskRecoverer` spelling (05-tasks owns it; was `TasksRecoverer`); the root source's helper is `legacy.LibraryCoverID` and its legacy state the record-type field `library.RootSpec.Legacy` (no legacy record type); the cannot-move list gains the owner entry points `integrations.Registry.Confirm` and `providerhttp.Options.Legacy` (four kinds); each confirmation path calls the one `legacy.ExtConfirmer`. Already applied: `Manifest.LegacyStores` typed `legacy.Stores` (lead decision), R4's `health/diagnostics` at L4 (COR6-9).

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; resolutions R7-1…R7-8):

- R7-7 (SIMP7-1, COR7-2) — §3.3 "Legacy-only identifiers": the mirror test stated once (the owner's `export_test.go` exports test-only aliases; `legacy_mirror_test.go` in `package <owner>_test` asserts both directions; the executor's lives in `internal/executor`'s external test package).
- R7-7 (SIMP7-4) — §3.3: an owner's legacy-only input with no SDK interface is typed `any` (`secrets.Options.LegacyStore`, `library.Options.LegacySource`, `library.New(opts Options)`), asserted at construction, boot fails with `ErrLegacyStoreType`.
- R7-8 (MOD7-1, MOD7-2) — R1(c) "Moves and re-implementations": a file whose ABO import has no import-path-only replacement is a re-implementation with a 04 §8.6 row, its ABO formats stay in ABO, the generator fails without the row; §3.5.1 names the registry engine, `childop`, `opsmetrics`, `opsv3` and the state-using `opstest` files (opsbridge until T8, no `origins.txt` line); R1(c)'s name set adds the reviewed row-type/param-key list (`legacy_op_id`).
- R7-8 (MOD7-3) — store table `tasks` row: `maintenance_window_last_run` is the maintenance-window kind's `ClockStore` with HEAD's date codec.
- R7-2 (COR7-1) — §3.3: new `internal/storagehttp` (L6, imported only by `kernel`, lands at S4 r2; the legacy path becomes its `Alias`); `legacy.Maintenance` (`module.StorageHandles.Maintenance`, from S2b) in the `legacy` row and paragraph; §3.7: empty store exempt from the floor check, books' migrations numbered from `LegacyCap+1` (DATA7-7); diagram names the staged-reset swap.
- R7-1 (SEC7-1, SEC7-2) — §3.3 `config` row: the legacy config store writes the narrowing `enable_auth: true` (and `basic_gate.enabled` with a stored password) at P4d step 1, audited `core.config.legacy_narrowed`; stored default beats a `BelowStored` legacy value.
- SEC7-13 — `config` row: "env value or operator-file value", no flag.
- MOD7-7 — R1(c) is the one match rule, with the folded-prefix clause and the `"<op-id>.x"` and folded-prefix cases.
- MOD7-14 — §3.5.1 item 4 holds the one tag-set list (`embed_frontend fts5 native_taglib`, `GOOS=windows` for `./cmd/mtls-bridge` only, 18 files out); §3.2 item 4 cites it.
- MOD7-5 — §3.2 item 4: the `COPY --from` cross-check reads only copies into a `go build` stage under its `WORKDIR`; runtime-stage binary copies pass (fixture).
- MOD7-6 — §3.2 item 4: hello's dist is off the root list; core's own list (`core/scripts/ci/embed_build_outputs.txt`, no Dockerfile cross-check) and core's `ci` half.
- MOD7-15 — §3.5.1 item 4: root `make ci` vets and tests `tools/stagecheck` with negative-control fixtures; `ci/gates.txt` row.
- MOD7-4 — §3.5.1 item 1: `internal/cfaccess` computes to layer 5; `internal/plugins/maintenance` gets the owner check injected.
- COR7-11 — §3.5.1 item 4: S4 r1 and S4 r2 are separate stage points of the mapping input.
- COR7-10 — R4: `internal/restoreswap` is L1; the restore-journal record type sits at storage's level, re-exported by `backup/journal`.
- COR7-3, SIMP7-5, COR7-4 — §3.3: the kernel asserts only `legacy.ConfigOpener`, `legacy.ConfigSecrets` (driven) and `legacy.Covering` (P4c's backend comparison; R-G request); every other capability is detected once by its owner.
- R7-4 (DATA7-11) — `app` row: `app.OpenOffline` / `app.Offline`, the app-side wrapper over `kernel.OpenOffline` (R-G request).
- COR7-5 — §3.3 `legacy` row and paragraph: identifiers deleted in the first release after the cutover that no longer sets the member.
- COR7-6, SIMP7-10 — `tasks` package row, store table and §3.7 family list: `StandDownStore` and the scan stand-down marker `registry.scan_standdown`.
- SIMP7-3 — §3.3: the "stays in SDK packages" list is 05-storage §5S.3.11's one table; this file keeps one example per reason (`storage.LegacyGuardProjection` as the permanent guard, SIMP7-7).
- SIMP7-8 — §3.3 naming rule: owner-specific identifiers take the Go-initialism owner prefix, cross-owner ones none; `legacy.AtomWriter` → `legacy.RBACAtomWriter`, `legacy.RootSource` → `legacy.LibraryRootSource`, `legacy.TaskRecoverer` → `legacy.TasksRecoverer`, `legacy.LocalCandidate` → `legacy.IntegrationsLocalCandidate`, `RootLegacyState` → `RootSpecLegacyState` (rule adopted; lead-approved R-F request).
- SIMP7-2 — "Extension records": only integration instances have a `confirm` route; import-root suspension is a 05-library deferred-build member, added to §3.4's list.
- SIMP7-9 — §3.4 cites 05-config §1.12.5.
- FE7-3, FE7-5, FE7-6, FE7-1 — §3.6 "Build identity": no build-ID `define` in any Vite config; `readPageBuildId()` is the only meta reader; `dev` reserved; "clean" is `git status --porcelain --untracked-files=no`.
- FE7-12 — npm table: `@mediacore/ui-kit` imports neither `api-client` nor `shell`; data and toasts are props.
- FE7-13 — `internal/<capability>http` row: `--version-json`'s `ui_build_id` is the `X-App-Build` value, not `/modules`'.
- integration pass (round 7): §3.3 new row `storage/restorejournal` (L0 leaf, S2b; the swaps' in-flight files `restore/<id>/inflight.json`, `restore/<id>/inflight-checkpoint.json`, `reset/<id>/inflight.json` and the four `CheckReader` tokens the S0 probe reads) and R4 lists it at L0, `internal/restoreswap` cites it (R-B, R-H request); `storage.ErrLegacyStoreType` is the one sentinel, defined in 05-storage; §3.5.1: the whole of `pkg/ops/opstest` is re-implemented into `taskstest` (04 §8.6); the naming rule's examples start from the pre-`legacy` names (`rbac.LegacyAtomWriter`, `library.RootSource`, `integrations.LocalCandidate`), so no round-6 draft name remains in the body.

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`; resolutions R8-1…R8-6):

- R8-6 (MOD8-1) — R1(c) "Moves and re-implementations": a move changes each ABO import by import path alone or by one of 04 §2.2's enumerated mechanical rewrites (logger cut, store-interface cut) and keeps its `origins.txt` line, so all 21 §8.5 rows are moves; anything else (row formats, run vocabulary, 04 §2.1 (i)) is a re-implementation with a §8.6 row; generator fixtures for both sides.
- R8-6 (MOD8-2) — §3.5.1: `childop` and `opsmetrics` stay as opsbridge until their last ABO user is native (`childop` latest T7); `opsmetrics`' one core successor is in `internal/executor`; `rc.Await` replaces `childop.Follow`.
- R8-6 (MOD8-3) — R1(c): the file set is the tag union, so build-tag twins travel with their partner (`reflink_darwin.go` with `reflink.go`, `static_embed.go` named in its cell); an unnamed twin fails 04 §2.1 (b).
- R8-1 (SIMP8-1, COR8-1) — `storage/restorejournal` row: one swap with kinds (`KindReset`, reset kinds `ResetStore` and `ResetFactory`), one slot `restore/pending.json` with `kind`, one rejection rule, tokens `restore_pending`/`restore_inflight`; `reset/`, `reset_pending`, `reset_inflight` and `ROLLBACK_IGNORE_RESET` removed; `legacy` row and paragraph: `legacy.Maintenance.StageFactoryReset(ctx, requestedBy, kind)` serves both legacy reset routes; `storagehttp` row: one route; both legacy paths become its `Alias`es (`/system/reset` with `reset_kind=store`, R-D request); §3.7 diagram: the one staged swap.
- COR8-2 — `storagehttp` row: the route receives `storagehttp.Stager` (the kernel's `stageReset`), never `*storage.Admin`; the aliases keep HEAD's `{"confirm":"RESET"}` request and `{data: {message}}` shape.
- COR8-5 (R8-1) — R4: `restoreswap` → `storage`, never the reverse; `storage.Admin` carries no staging or swap method; the kernel holds the swapper (staging and P0a); archtest checks the edge.
- COR8-6 (R8-1) — `storage` row and R4: `storage.ErrSwapPending` defined in `storage` (L1); `backup`'s restore sentinel is its alias.
- SIMP8-3, COR8-2 — "Layer and detection": the one list of kernel-asserted capabilities, four (`ConfigOpener`, `ConfigSecrets`, `ConfigResetArgs` at staging, `Covering`); owner files cite it.
- SIMP8-4, SIMP8-9 — `legacy` row and paragraph: `legacy.Maintenance` has only `StageFactoryReset`, is nil offline and is deleted in the S4 r2 PR.
- SIMP8-5 — "Extension records" and §3.4: `library.RootSpec.ReadShares` is a deferred-build member with root suspension and confirmation.
- SIMP8-7 — naming paragraph and `legacy` paragraph: the hook carrier is `legacy.ConfigResetRun` (with `legacy.ConfigResetArgs`).
- SIMP8-8 — `storage/restorejournal` row and R4: `backup` imports the leaf directly; the `backup/journal` aliases are gone.
- R8-2 (DATA8-3, SEC8-4) — `app` row: `OpenOffline` computes the boot verdicts and stages the owed entries before promoting, so it never launders a foreign open.
- R8-4 (SEC8-1, SEC8-6) — `config` row: the Basic-gate narrowing applies whenever the effective value is `true` (fail-closed without a readable password; release note), `core.http.rate_limit.enabled` narrows, and every boot from S3 writes `operator-names.json`, reported as the advisory token `canonical_only_operator_values`.
- MOD8-4 — R1(c): a struct tag is matched per key (value up to the first `,`); new case.
- MOD8-10 — §3.5.1 item 4: `tools/stagecheck` keeps the `ForTest` entries and maps imports through `ImportMap`; only the counting parse drops them; new fixture.
- MOD8-11 — §3.2 item 4: embed patterns are normalised (strip `all:`, join with the package directory relative to its module root) before the list comparison; fixture.
- FE8-7 — §3.6: the App ID pattern is anchored (`^[a-z][a-z0-9]{1,15}$`).
- integration pass (round 8): §3.3 `storage/restorejournal` row: the sentence naming the deleted reset-only path, tokens and override removed (sweep (a)); §3.3 `legacy` paragraph and "Layer and detection": `legacy.ConfigResetArgs` (with `legacy.ConfigResetPlan`) is called by `kernel.stageReset` for both reset kinds, the plan carrying the factory-default rows for either and the hook arguments for `ResetFactory` only (R-B request); R1(c): 04 §2.2 is cited as the one list of the six mechanical rewrites instead of two examples (R-H request).

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; resolutions A-1…A-17):

- A-1 (ADV-1) — R6: same-owner conflicting legacy routes register as one generalised pattern when every member carries the same requirements; any other conflicting set fails generation.
- A-2 (ADV-3, ADV-S1) — introduction, §3.3 `config`, `secrets`, `auth`, `rbac` rows, the store-interface table and §3.7: the extraction is representation-neutral for ABO; the extension family, its fingerprints and suspension, `["none"]`, the moved-secret sentinels, R6-6's auth-enable exemption, the narrowing writes and `operator-names.json` are deleted from this file; the hardening becomes the identity, secrets and auth-precedence cutovers.
- A-2 — "Legacy-only identifiers": the extension, sign-in-deletion and sentinel identifiers (`legacy.ExtConfirmer`, `Extension`, `ExtRecord`, `legacy.AuthSignIns`, the secret-copy store) and `integrations.Registry.Confirm` removed from the legacy list and the stays-in-SDK examples; "Extension records" paragraph deleted.
- A-3 (ADV-S2) — §3.3 `storage` and R8: storage-enforced `CheckedWrites` cover native keyspaces and core-written rows only; ABO's legacy families keep HEAD's lock discipline; claims keep shadow → strict.
- A-4 (ADV-S3) — §3.4, R1(c), R2 and §3.5.1: the move/re-implementation taxonomy, `origins.txt`, `archtest.DeferredMembersAbsent`, the stage-visibility generator and `tools/stagecheck` deleted; `TestCoreHoldsNoAppKeyNames`, the one tag-set list and a plain `shipped_stage:` marker kept; 04 is informative.
- A-5 (ADV-S4) — §3.3 `metrics`, `compat` and §3.6 "Metric name": core's series use the `app.Options.MetricNamespace` value; ABO keeps its metric names; no metric aliases.
- A-6 (ADV-S5) — §3.3 `updater` and §3.7: S8 moves the S0-hotfixed updater; signed manifests and automatic rollback are deferred-build.
- A-7 (ADV-2) — §3.3 `config`: legacy names decode with HEAD's per-type semantics; a fail-open legacy auth bool is a boot error.
- A-15 (ADV-S9) — §3.3 Build column and §3.4: deferred packages are non-normative design sketches that the SDK does not freeze.
- ADV-8 — §3.3 `secrets`: the `config.yaml` sentinel clause deleted; no moved secret has a file copy at HEAD; core writes only keys HEAD's `SaveConfigToFile` manages.
- ADV-11 — introduction: evidence-commit counts are informative; asserted counts are generated at each stage's base commit.
- ADV-12 — introduction: IDs are provenance; per-stage normative digests under `docs/plans/platform-core/digests/` (Phase 6).
- integration pass (advisor): introduction and §3.7 name the hardening cutovers H1–H3 as 09 §9.13 does; `storage` row and R8 state the lead's A-3 ruling (voluntary compare-and-set inside a `Serialized` legacy claim); `config` row states the one storage rule (legacy name → HEAD's row only; no legacy name → `c/config/`, on ABO too), HEAD's auth-enable rule with auth off, `ABO_CORE_AUTH_MODE` (09 §9.2.1 row 3) and ABO's any-credential `/metrics`; §3.3 legacy list adds `legacy.TasksWriteLock`, drops `legacy.IntegrationsLocalCandidate` and the deleted `RootSpecLegacyState`/`RoleLegacyState` (a legacy-served root's other fields are fixed, `ErrRootFieldFixed`); §3.5.1 item 4 names `ci/stage.txt`; deferred rows cited as design sketches (non-normative).

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; resolutions R9-1…R9-11):

- R9-1 (COR9-1, DATA9-1, DATA9-2, DATA9-7) — §3.3 `storage` row, "Legacy-only identifiers", naming paragraph, store table `auth`/`tasks` rows and R8: `legacy.WriteLock` replaces `legacy.TasksWriteLock` for every legacy store with a HEAD mutex (identity `apiKeyMu`/`inviteMu`/`counterMu`, tasks `opsMu`); core commits through `storage.CommitLocked`, which holds the locks from staging through commit (`Executor.WriteRuns`, `Stager.WriteRuns`); exactly one instance per legacy store family per process (`TestLegacyStoresAreSingletons`); `ReplaceOpRowsPrefix`'s lock rule stated once in 05-tasks.
- COR9-8 — "Legacy-only identifiers": the write lock is asserted on the built store value, never on the `legacy.Stores` struct; a method staged inside the hold never re-takes it.
- R-F request (lead-approved, R9-1) — "Layer and detection": `legacy.WriteLock` is a kernel-asserted capability (five, not four); the kernel hands the locks to the owner with the store.
- R9-2 (COR9-2) — R6: every conflict set has one owner at every stage; the T0+T1 PR moves the four `…/operations/{id}/…` routes with `…/operations/v2/{id}` to core as ledger-listed aliases; no core alias registers beside a books pattern.
- COR9-9 — R4: `kernel.stageReset` calls `legacy.ConfigResetArgs` for both reset kinds; R6 copies 05-http-api §4.8 rule 4's full field list (`Public`, `AuthMethod`, `NoCompress` added).
- R9-3 (MOD9-1, MOD9-2) — §3.6 "Metric name": one owner per family; T0+T1 deletes ABO's `opsmetrics` emission of the executor's families; `TestABOMetricsGatherNoDuplicateOrMissing` from S5 against a HEAD golden with label values.
- MOD9-8 — §3.6: 74 `audiobook_organizer_*` families (87 rows); examples `mediacore_ops_runs_total` / `audiobook_organizer_ops_runs_total`; the task subsystem scope is `ops` for every app.
- R9-4 (COR9-3) — §3.3 `config` row: `LegacyAmbientScopes` belong to the principal and are added after the key-scope intersection, so scoped `abk_` keys keep HEAD's reach.
- R9-5 (SEC9-1, SEC9-2, DATA9-6) — introduction and `config` row: H3 moves only auth-enable precedence; the auth-off owner may store the Basic gate keys into HEAD's fields; `Alias.LegacyYAML`/`LegacyBlob`, routing follows `LegacyBlob`, HEAD's file-only keys are never stored.
- COR9-11 — "Two selection models" and the store table: config is exclusive per key (`Alias.LegacyBlob`), ledger value `legacy+native`, excepted from the native-only-data rule.
- COR9-10 — "Legacy-only identifiers" and naming paragraph: `legacy.AuthAtomWriter` named.
- R9-6 (COR9-4) — `internal/storagehttp` row: one request struct `storage.ResetRequest` through `Stager.StageReset`, `kernel.stageReset` and `ResetArgs`; 422 `library_files_confirm_required`.
- R9-7 (COR9-5, DATA9-3) — `coretest` row and R4: `coretest/crashmatrix` is a public test kit run by core over hello and by ABO over its full boot; `storage/restorejournal` row: `inflight.json` is created before the first rename of every kind.
- R9-8 — §3.5.1 item 4: the soak gate also reads the `Stage-Release:` trailer.
- R9-10 (MOD9-3) — R1(b), R1(c), R2, §3.4 and §3.5.1 item 4: the guards read the tag union of the one list; darwin entry written as goreleaser builds it; a required darwin and `integration` vet job over both modules; R1 rests on core's CI (Z5).
- MOD9-6 — R1(c): `TestCoreHoldsNoAppKeyNames` lands at S0 and is in S1's done-check.
- R9-11 (DATA9-8) — introduction: H1–H3 raise every owner whose rows they rewrite (`books`, plus `core`); H1/H2 delete the pre-cutover checkpoint and old Pebble files by forced compaction; archives keep the old representation.
- R-E requests (lead-approved) — `config` row: config's routing model stated once (legacy name → HEAD's row or file; none → `c/config/`), "Two selection models" and the store table cite it; `legacy.WriteLock` is 05-storage's `WriteLocks(names []string) []storage.Lock`, and `storage.CommitLocked` takes `storage.Lock` values.
- integration pass (round 9): `storage` row and "Write locks" paragraph: `storage.CommitLocked(ctx, b, d, stage, locks...)` takes each distinct lock once in ascending `LockRank`, a nested call joins held locks, `WriteRuns(ctx, open, stage)` with `ErrWriteRunsNested`; the identity, run and config lock lists match 05-storage (not `counterMu`); naming paragraph no longer names the former run-store lock; `auth` store row; `storagehttp` row: `storage.ResetRequest` carries `LegacyConfirm` and `kernel.stageReset` compares the preflight.

Round 10 (dispositions in `docs/plans/platform-core/reviews/spec-round-10-dispositions.md`; resolutions R10-1…R10-11):

- R10-1 (COR10-1) — R6: the five operations routes stay books' legacy routes at every stage (four folded into one generalised pattern, `…/v2/{id}` kept, `library.view`); their handlers call core's tasks client from T0+T1; core's run route `GET /api/core/v1/tasks/runs/{id}` is a different path and no alias; the R9-2 move is gone.
- R10-3 (COR10-3, MOD10-1 to MOD10-6) — §3.6 "Metric name": the legacy gin mount keeps `otelgin` as at HEAD and is the sole recorder of requests that reach gin; core's stage 2 records only core and module routes; one owner per series, scopes told apart by `otel_scope_name`; pre-mount rejections on legacy paths are a 09 §9.2.1 observable change; `app.Options.MetricViews` carries HEAD's bucket layouts; `httpapi.ReportLegacyRoute` deleted.
- MOD10-7 (R10-3) — §3.6 "Metric name": `ops_abandoned_calls` changes owner in exactly one PR.
- R10-4 (COR10-2, DATA10-1, DATA10-3) — §3.3 `storage` row and "Write locks": `CommitLocked` marks the batch with its held set and `storage.LocksHeld(b, l)` reads it; one total rank order over every lock a commit path may take, core-side mutexes ordered (taken before `CommitLocked`, never inside a `stage`; secrets' keyed mutex the one exception); no `stage` calls a books method that takes a covered HEAD mutex; `TestLockRanksTotalOrder`.
- R10-5 (DATA10-2) — §3.3 `storage` row and "Write locks": `CommitLocked` runs the store's post-commit step (the gap-fill write) before it releases the locks.
- COR10-5 — "Write locks" and "Layer and detection": the kernel resolves the locks at P4c and hands owner-typed values (`auth.Options.Locks`, the executor's `RunLocks`, `SettingsLocks`); owners never call `WriteLocks`.
- COR10-10 — store table: the `config`, `integrations` and `integrations/notify` rows name `configBlobSaveMu` as their `legacy.WriteLock`.
- R10-9 (MOD10-8, OPS10-8) — R1(b) and §3.5.1 item 4: the tag-union tools run `go list -e` and tolerate only an unbuilt listed embed output; `vet-darwin` runs `GOOS=darwin CGO_ENABLED=0` without `embed_frontend`, the image job compiling the embed-only files.
- R10-7 (OPS10-1) — §3.5.1 item 4: the trailer is `Stage-Release: <stage> <n|flip>`, carried by every PR that ships stage behaviour and required on every PR that touches a package of a not-yet-flipped stage.
- integration pass (round 10): §3.3 "Write locks" names `auth.Deps.Locks`/`auth.RestoreDeps.Locks`, `storage.OnCommitted(b, storage.CommitStep{Key, Run})` and the `storage.ErrNotLocked`-wrapping refusal, and builds one legacy store instance per family once per App (COR10-10); R6 row and R10-1 history line spell core's run route `GET /api/core/v1/tasks/runs/{id}`; §3.6 "Metric name" (LEAD correction to R10-3): HEAD's exporter uses `WithoutScopeInfo`, so the two HTTP recorders are not told apart by `otel_scope_name`, pre-mount refusals on legacy paths are recorded with no `http_route` (09 §9.2.1 row 12), no route-report identifier named; §3.3 `config` row: ambient scopes never reach a worker-confined key, H1 by the effective set (R10-2). The R10-3 and COR10-5 history lines above keep their round-10 wording; this line supersedes them.

Round 11:

- VER11-1 (R11-1) — §3.6 "Metric name": the wrapper clears `r.Pattern` before gin and the SEC2-22 check reads `httpapi.MatchedPattern(ctx)`; stage 2 records into the same OTel instruments as otelgin (`app.Options.HTTPMetricsScope`), so identical label sets aggregate and no duplicate family can reach a scrape; `WithoutScopeInfo` stays; otelgin's chain position cited from 05-http-api.
- VER11-2 (R11-2) — §3.2 item 6: the Dependabot and root-tidy bot commits need no `Stage-Release:` trailer under check (d)'s bot rule (09 §9.2.1); the item holds.
- R11-5 note (R10-9 justification) — §3.5.1 item 4: the `embed_frontend`-only files are named with their real imports (`static_embed.go` and its test import more than `embed`); the conclusion (no GOOS constraint) is unchanged.

Final verification:

- FIN12-1 — §3.2 item 6: the bot rule admits the paths of all five Dependabot ecosystems (09 §9.2.1's table); the root-tidy commit carries its own `Stage-Release: none` and is pushed with the PAT.

Plan-phase consistency:

- PC-9 — §3.3 `legacy` row: `legacy.Maintenance.StageFactoryReset(ctx, requestedBy, req storage.ResetRequest)`, 05-storage §5S.3.15's one request type (R9-6), replacing the round-8 `kind` argument.
- PC-10 — §3.2 item 1: CLAUDE.md wins: nobody runs `go work init` in the repository or a worktree; the exclude check's throwaway workspaces live in a temp directory outside the repository; the developer workspace waits for 12 §12.8.2 step 10.
- PC-20 — §3.3 new row and R4: `internal/keyedmutex` is an L0 leaf, landing at S3 (04 §8.1 S3); §3.5.1: `internal/server/legacyauth` is the sixth new ABO path (S4, 04's `legacyauth` row), 213 − 49 + 6 = 170, margin 20, `layerOf` entry at S4.

Owner decisions D10–D12:

- D10–D12 revision (2026-11-04): no backward compatibility, no soaks, one R2 cutover. Intro: the representation-neutral paragraph replaced by the D10–D12 statement. §3.1: `compat` and `core/legacy/` leave the SDK definition. §3.2: stage keys re-keyed to R1; the compat-ledger embed and the soak-gate bot rule deleted. §3.3: rows `compat` and `legacy` deleted; "Two selection models" and "Legacy-only identifiers" (write locks and one instance per family, naming, lifetime, layer and detection, the mirror test, legacy-only inputs, what stays in SDK packages) deleted; "Store interfaces" rewritten as "Store interfaces and importers" (native implementations only, one `<pkg>.Importer` per migrated subsystem, books' decoders in `internal/books/cutover`, `module.Importers`); rows `app`, `storage` (strict claims from R1 with the shadow dial, quarantine, no grants, no `CommitLocked`), `storage/restorejournal`, `library`, `tasks`, `metrics`, `config` (operator lock, the fail-closed rule, canonical names, retired-name refusal, rename table), `secrets`, `auth`, `rbac`, `realtime`, `integrations`, `backup`, `updater`, `ui`, `coretest`, `<pkg>/<pkg>test`, `internal/storagehttp` (`module.StorageHandles.Maintenance` as a `module.Maintenance`, R1 only) rewritten. §3.4: the cutover-only members and the config basis (already deleted from 05-config by A-2) leave the deferred list. §3.5: R1(c) cites importers and the kind-rename map; R4 drops `compat` and `legacy`; R6: the operations conflict set is deleted in flip F6; R8 rewritten (no grants, strict claims, shared-family split). §3.5.1: `configstore` and `taskstore` replaced by `internal/books/cutover` (169 packages, margin 19); the stage marker and trailer deleted. §3.6: metric names under one `MetricNamespace` (`MetricPrefix`, otelgin co-recording, `MetricViews` and the HEAD goldens deleted); UI route and closing paragraph rewritten. §3.7 rewritten (native ownership, 7-day checkpoint deletion, the level step, one floor in R3). New §3.8: release keys R0–R3, flips F3–F9, migration steps M1–M8, what survives R2, kept contracts, safety and rollback. Consistency pass: D12's answers cited as D12 (a)–(d); M7 runs right after M1 (order M1, M7, M2–M6, M8) behind `secrets.Open`; tasks routes and the Activity page move in F5; the dirty set, `CheckReader`, `module.Maintenance` and the R1 HTTP reset are gone (offline `storage reset` only); the webhook format changes while the OAuth callback path stays; consumers change in the renaming PR; the keyring is exported after R2's first boot; `log.Importer` is the idempotent L0 exception. Final consistency sweep: §3.3's fail-closed rule is limited to the two `FailClosed` keys `core.auth.mode` and `core.http.basic_gate.enabled`, and a risk citation reads 14 K7.

Round 13 (2026-11-04): R1 (IMPL-1, IMPL-2, IMPL-3, IMPL-4, IMPL-5, IMPL-12, XFILE-4: F2, the foundation PRs, in §3.3's `storage/datamigrate`, `tasks`, `config`, `rbac`, `audit` and `health` rows, the stores table and §3.8's flip table, with the three late-owner kinds), R2 (CONFORM-1: the pre-freeze release replaces R1 in the R2 rollback, the drill and `TestCutoverRefusedByOlderBuilds`), R3 (IMPL-9: the R1 freeze as a lead extension of D12 (a)), R4 (CONFORM-2, CONFORM-6, DATA-8: checkpoint retention, the R1 sweeper and prune command), R5 (CONFORM-3, XFILE-3: M2b in the stores table, `config` row and M2 row), R6 (CONFORM-4, DATA-16, IMPL-7: R3's `legacy-file-cleanup` boot step, the fresh-install batch records the floor's `Requires`), R7 (DATA-1), R8 (DATA-2), R9 (DATA-3), R10 (DATA-5), R11 (DATA-9: the `from_vector` check), R13 (IMPL-8), R14 (XFILE-1), R15 (XFILE-2: §3.5.1 and R3), R17 (IMPL-11, XFILE-6, XFILE-7, XFILE-9, XFILE-11), R19 (SEC-2; the OAuth start path deleted with no alias), R21 (SEC-5), SEC-1 (ABS logins through `auth.Compat`), lead note (the executor creates no `ops` instrument before F5; the R1 note's `http_route` line), IMPL-10, IMPL-13, DATA-11, SEC-9, SEC-10, SEC-13, CONFORM-12 (heading renamed "The frozen contract"), D13 (identity rows stop the upgrade, the read-only cutover preflight, permanent merges, no old-URL redirects).

Round 14 (2026-11-05): V1 (the cutover preflight runs only before the cutover starts; a resumed cutover skips it: §3.7 boot diagram, M3 row), V10 (`make rollback-restore RELEASE=<pre-freeze tag>` through `updater fetch-release`, the tag from the checkpoint's `from_release`, in "Rollback"), V11 (M3's every-atom mapping states SEC-12's owner condition as 05-rbac §6.1), V13 (F2 row: `core.storage` and `core.secrets` registered kernel-side), V14 (`/health` carries no version), V16 (`legacy-file-cleanup` is a start hook and skips only `.encryption_key` while `secrets_keyring_not_exported` is raised), V18 (§3.3 `storage/restorejournal` row lists `KindRewrite` and its intent slot), V22 (HEAD's OAuth start path kept at its exact path as a second `CoreExactPatterns()` entry; the deletion text and its release-note mention removed), V23 (round-13 ruling numbers replaced by the finding IDs they disposed: §3.5.1, §3.7, §3.8).
Round 15 (2026-11-05): W4 (§3.7 boot diagram and the M3 row: the cutover has started when books' level has reached M7's target or any cutover step has a progress record in `c/storage/dm/`; the preflight runs if and only if it has not started; checkpoint reuse is not part of the condition), W8 ("Rollback": `from_release` is the app version HEAD's store recorded for its last boot before the cutover, with the pre-freeze tag the cutover build embeds as the fallback; on the archive branch the refusal names the release that produced the archive), W9 ("Rollback": the fetched binary's levels are compared with the source's levels, the checkpoint's `from_vector` or the archive's), W10 ("Rollback": the fetch has apply's trust level, HTTPS from the release source plus the checksum check apply performs if the source publishes one). W2, W3, W5-W7 checked: this file states none of them. Lead fix: `from_release` is the embedded pre-freeze tag (HEAD records no app version).
