<!-- file: docs/specs/platform-core/03-architecture.md -->
<!-- version: 1.12.1 -->
<!-- guid: 8af6d935-7143-4d06-8562-b04a18504d81 -->
<!-- last-edited: 2026-11-04 -->

# 03 — Architecture

This section is **normative**: every other spec file uses the names, paths and rules defined
here. If another file disagrees with this one, this one wins until it is amended.

**How to read the rules.** Finding and resolution IDs in parentheses (`R4-1`, `SEC6-3`, `A-2`) are
provenance, not part of a rule. Provenance-free normative digests, one per stage, are the first artefact of
Phase 6, under `docs/plans/platform-core/digests/` (ADV-12). Counts quoted from the evidence commit (572
routes, 269 op IDs, 213 packages and the like) are **informative**; a done-check or test that asserts a
count generates it at its stage's base commit (ADV-11).

**Representation-neutral extraction (A-2).** During S0–S10 the extraction moves code, not stored
representation. ABO's legacy stores keep HEAD's representation: raw `sess:` session rows and HEAD's
one-time tokens, `abk_` keys holding legacy permission atoms, roles holding legacy atoms only, plaintext
blob secrets (the four HEAD already encrypts stay as they are) and HEAD's auth-enable precedence. The
hardening of those representations is three named post-S10 **hardening cutovers** on 02 N15's one-way,
floored machinery, named as in 09 §9.13: **H1 — identity cutover** (hashed sessions and one-time tokens,
`mck_` keys, native scopes), **H2 — secrets cutover** (encrypt the plaintext blob secrets) and **H3 —
auth-precedence cutover** (an operator value that enables auth beats a stored `false`) (12 §12.4.4; the
residual risk is 14 K25). Native stores (hello, TV, Movies) have the hardened design from their first write.

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
| **SDK** | The *public, versioned subset* of core that an app is allowed to import: `module`, the capability contracts, `tasks`, `storage` (with its leaves `storage/keys` and `storage/codec`), `httpapi`, `rbac`, `config`, `events`, `log`, `metrics`, `coretest`, `archtest`, the store conformance-suite packages `<pkg>/<pkg>test` (§3.3), `entity`, `lifecycle`, `version`, `integrations/*`, `compat`, and the npm packages `@mediacore/shell`, `@mediacore/api-client` and `@mediacore/ui-kit`. The SDK is not a separate module; it is the set of core packages **outside** `core/internal/`, outside `core/kernel/` and outside `core/legacy/` (the identifiers that exist only for an app's pre-core data, §3.3 "Legacy-only identifiers"; R6-3), and is what the API-stability gate (§12) measures. Packages marked **deferred** in §3.3 are SDK paths whose contract is specified but not yet built. | same module | same module |
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
   lands in **`core/go.mod`** in S0 (09) and in no other `go.mod`, so the root — and every future app
   repo — keeps a `go.mod` that `go install …@version` accepts after the split (item 5). It is needed
   from S0 although core's own graph reaches `cockroachdb/errors` only from S2b, when Pebble enters
   core (04 §8.1): the supported workspace pulls genproto in through the root, and a workspace applies
   its members' excludes. The
   supported developer workspace is **`go work init . ./core`**, uncommitted (`go.work` and
   `go.work.sum` are gitignored, `.gitignore:36-37`); a workspace that uses the root without
   `./core` is unsupported (it reproduces E1). No Makefile target or CI job builds through a
   `go.work`, except the throwaway workspaces of the check below.

   **The exclude check.** Probing the live graph cannot work: an excluded version is dropped from the
   module graph, so `go mod graph` never lists it while the exclude is present (round-2 X10), and a
   requirer edge alone does not prove the exclude is load-bearing (X16: a workspace whose build list
   holds no split `genproto/googleapis/*` module builds green without it). So
   `core/scripts/ci/check_genproto_exclude.py` (a core-owned script, 12 §12.3.11) works on a temp copy
   of the checkout and fails unless all of these hold:
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
   `(cd core && go mod graph)` in the copy without the exclude lists the requirer (S2b); before that it
   reports "not applicable", which is correct, because at S0–S2 only the root can pull genproto in.
   **Root mode** — the root `make ci` job `genproto-exclude-root` — runs the root-mode probe and the
   tree-wide "no other `go.mod` carries a genproto exclude" scan; it is a root-side job, dropped at
   cutover step 6 (12 §12.8.2). Both targets run from S0 (09 S0's classification table lists them as
   two rows), so 09 S0's "Done when" holds from the first core PR.
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
   table and the per-stage checklist (shard weight files, `.mockery.yaml`, `tygo.yaml`, `ci/gates.txt`
   rows such as `store-width`/`store-references`) are owned by 09 S0.

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
     root-level `*.md` (`:30`; the pattern has no `**/`, so a Markdown file below the root, such as
     `core/compat/core-ledger.md`, stays in the context), `testdata`, `dist`, root `node_modules`,
     `web/node_modules` and `web/dist`, among others. So **every `//go:embed` input of the root module and
     of core lies inside the Docker build context or is produced in the image by a declared earlier
     stage**, and the compat ledger is embedded from a generated copy outside `docs/` for this reason
     (§3.3 `compat`, R4-4). The second case exists on purpose: under `embed_frontend`, `web_embed.go:11`
     embeds `web/dist`, which `.dockerignore:20` excludes, and the Go stage receives it from
     `COPY --from=frontend-builder /build/web/dist ./web/dist` (`Dockerfile:68`; MOD6-3). The in-image
     outputs are **one committed list**, `scripts/ci/embed_build_outputs.txt` (`web/dist` from S0; from S9
     each app's `web/dist`, for ABO still that one path, 07 §7.6.3), which the check reads and compares with
     the Dockerfiles' `COPY --from` lines **into a stage that runs `go build`** (the `go-builder` stages)
     whose destination lies under that stage's `WORKDIR`: a listed path with no such line, or such a line
     with no listed path, fails. A runtime-stage copy of the binary (`Dockerfile:90` into `/app/`,
     `Dockerfile.build-cgo:75`) is not an embed output and is outside the comparison; the check's fixture
     test carries one, and it must pass (MOD7-5). Hello's `core/examples/hello/web/dist` is **not** on this
     list, because no ABO Dockerfile produces it: core's packages are judged against **core's own list**,
     `core/scripts/ci/embed_build_outputs.txt` (hello's output from S9; 07 §7.6.3), which only tolerates an
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
     carries an `all:` pattern in a non-root package (MOD8-11). 09 S0 owns its done-check (MOD5-7). The PR image job, which also fails on
     an unlisted excluded input ("no matching files found"), is the backstop.
     `.dockerignore` keeps its exclusions. A PR job builds the image. These land in the same stage as the `replace` (09 S0). At S0 core holds only the
     stdlib-only `archtest` engine (`internal/arch/layering_test.go:20-31`), and `go mod tidy` writes
     no `go.sum` for a module without requirements (round-2 X12), so S0 commits an **empty
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
   this is listed in risks (§14 K7).
6. **Cost accepted: dependency skew.** The root build takes the MVS maximum over the root and
   `core/go.mod`, while core CI tests only core's requirements, and Dependabot's gomod entry
   targets `directory: /` only (`.github/dependabot.yml:59-60`). So the binary can ship dependency
   versions core CI never ran. Mitigation: the gomod entry gains `directories: ["/", "/core"]` under the
   top-level `multi-ecosystem-group: dependencies` it already joins (`.github/dependabot.yml:8-20,59-64`;
   the repo uses no `groups` key, and `applies-to` is a single-valued key of a `groups` entry, so no one
   group could cover both update kinds), and `scripts/ci/check_module_skew.py`, which fails when any
   module path in `(cd core && go list -m all)` resolves to a different version in the root's
   `go list -m all` (12 SC4(c)). S0 verifies that version updates arrive as **one** PR spanning `/` and
   `/core`; if they do not, a workflow step runs the root `go mod tidy` on Dependabot `/core` PRs and
   pushes the result (tidy coupling, item 4). Security updates are opened per directory whatever the
   grouping, so a `/core` security PR is made consistent by that same root-tidy step and, failing that,
   caught by the blocking skew check. Dependencies that must not float are pinned by a Dependabot `ignore` plus
   `check_module_skew.py --pin` (13 §13.5.2). `check_module_skew.py` carries three guarantees (no skew,
   the empty `core/go.sum` of item 4 exists, the `--pin` pins hold), so it is a **blocking** root-side
   check: it runs in root `make ci` (and therefore in the merge requirement of 09 §9.2.1) from S0, not
   only as the reported readiness criterion 12 SC4(c).

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
own `core/internal/keyedmutex` copy in the stage in which a core package first needs one, 04 §8.1 (S3); the
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

The **Build** column applies the promotion rule of §3.4: **ABO** = ABO consumes it during the
extraction stages of [09](09-abo-on-core.md) (S0–S10, including the inserted S2b and UI-0; 09 §9.3); **core** = core itself needs it;
**deferred** = the package is described here and in its spec file as a **design sketch (non-normative)**
at proof depth (D5), the plan builds none of it, the SDK freezes none of it, and it is designed for real
and built when the first TV/Movies app starts (A-15).

| Package | Visibility | Build | Purpose | Spec |
|---|---|---|---|---|
| `app` | public | ABO | Composition root. `app.New(ctx, app.Options, ...module.Module) (*app.App, error)`; `(*App).Run(ctx) error`. The **only** way to assemble a process; `app.OpenOffline` (returning an `app.Offline`) is the app-side wrapper over `kernel.OpenOffline`, the guard-aware read-write open for offline commands. It computes the boot verdicts a serve boot computes; when it finds a foreign open, a missing or unreadable open-stamp record or a behind lineage, its close batch first stages the owed entries a serve boot would stage, and only then promotes its own open stamp and sidecar identity at a clean close, so it is never a foreign open and never launders one (R7-4, R8-2; DATA7-11, DATA8-3, SEC8-4). | [06-app-sdk](06-app-sdk.md) |
| `module` | public | ABO | `Module` interface, `Manifest`, `Registrar` (routes, tasks, migrations, permissions, settings, events, UI, adapters), `Deps` handed to a module; `module.ReservedIDs()` (§3.6). | [06-app-sdk](06-app-sdk.md) |
| `entity` | public | core | Leaf package (L0): `entity.Ref{Module, Kind, ID string}` — **the one** polymorphic reference to any module-owned entity. Every file uses this type: tasks resources (`tasks.Resource` is `entity.Ref`), outbox/notify payloads, download grabs (multi-entity grabs carry `[]entity.Ref`), acquisition history/blocklist/queue, audit targets. No other `EntityRef`/`Resource{Module, Kind, ID}` struct exists; an `archtest` check fails any core identifier named `EntityRef` outside `entity`. Never a shared media row (decision 6). | [10](10-tv-movies-proof.md), [05-tasks](05-tasks.md) |
| `lifecycle` | public | core | Leaf package (L0): `Component{Start, Stop}` ordering contract and shutdown budget types used by kernel, tasks, realtime, outbox relay; the public constant `lifecycle.NotifySocketEnv` (the systemd notify-socket variable name, read by apps that strip it from subprocess environments; 04 §8.3, from S2). | [05-tasks](05-tasks.md), [05-health](05-health.md) |
| `version` | public | core | Leaf package (L0): `const Version = "0.Y.Z"`, the core version; S1 lands it as `"0.1.0"`, so the S2 `hello` skeleton's `MinCoreVersion: "0.1.0"` passes P1 before any checkpoint exists. **Never** derived from `debug.ReadBuildInfo` (under the `replace` that reports the zero pseudo-version, which is below every `MinCoreVersion`). Read by the kernel's `MinCoreVersion` check, `GET /api/core/v1/modules` and the backup manifest; CI keeps it **≥** the newest checkpoint (12 §12.4.3: the one "core version" PR bumps it above the newest checkpoint before the release that records the next one). | [12](12-repo-split.md) |
| `buildid` | public | ABO | Leaf package (L0), dependency-free: `buildid.Binary(version string) (string, error)` (the version, `+`, and the SHA-256 of the running executable) and `buildid.Commit() string` (`vcs.revision`), the binary ID of §3.6 "Build identity". Lands at **S0**, so ABO's S0 guard and `--version-json` compute the binary ID before `app` exists; from S2b `app.BinaryID` delegates to it (06 §6.4.5; FE6-3, 04 §8.1 S0). | [06-app-sdk](06-app-sdk.md), [05-updater](05-updater.md) |
| `storage` | public | ABO | Pebble-backed ordered KV: `DB`, `Keyspace`, `Batch`, `Snapshot`, `iter.Seq2` scans, per-keyspace migrations and legacy-prefix claims. During the extraction no core service reaches an app's legacy family: each service that persists state uses its **store interface** and the app supplies the implementation over its own claims (table "Store interfaces" below; 02 decision 13). Cross-owner reach (`Grant`, `GrantMode`) exists only as a deferred-build member of the per-family cutover (02 N15, 05-storage §5S.3.13), the only step in which core reads an app's rows. **Write checking (A-3):** storage-enforced `CheckedWrites` (`Expect` and `Serialized` checks at commit) apply to native keyspaces and to rows core newly writes; ABO's legacy families keep HEAD's lock discipline through the extraction — its keyed mutexes, `apiKeyMu` (with early fix 6) and the single-writer AST ratchet — and get no storage-enforced checks; a legacy writer that stages into a caller's batch may stage an `Expect` or `GetForUpdate` on a key of a `Serialized` legacy claim it read, as a **voluntary compare-and-set** that storage honours but never requires (05-storage §5S.3.2); claims (key ownership) keep their shadow → strict rollout (05-storage §5S.9.4). `storage.ErrSwapPending` is the one sentinel for an occupied swap slot of any kind (restore, revert, checkpoint restore, reset), defined here at L1 so the S2b staging paths can return it (R8-1, COR8-6). | [05-storage](05-storage.md) |
| `storage/keys` | public | ABO | Leaf package (L0): pure tuple-key functions (the core key encoding). Lands at S0, so S0's guard writes its `c/storage/` records in core's encoding (04 §8.1). | [05-storage](05-storage.md) |
| `storage/codec` | public | ABO | Leaf package (L0): generic value codecs for stored values. | [05-storage](05-storage.md) |
| `storage/restorejournal` | public | ABO | Leaf package (L0) that imports nothing from core: the record types (`Record`, `SidecarIdentity`) of the **one** journaled directory swap, whose kinds are 05-backup's staged restore, revert and checkpoint restore and the factory reset, `KindReset` with its two reset kinds `ResetStore` and `ResetFactory` (R8-1, SIMP8-1). They are written to `restore/<id>/journal.json`, which `internal/restoreswap` (L1) writes and P0a reads, so neither storage nor restoreswap reaches an L4 type; `backup` imports this leaf directly and re-exports nothing (COR7-10, SIMP8-8). Every kind uses one intent slot, `restore/pending.json` with its `kind`, created atomically, so a second staging of either kind fails with `storage.ErrSwapPending`, and one rejection rule (`expires_at`, `staged_by_build`, a moved open stamp). A swap in flight is marked by `restore/<id>/inflight.json` (for `KindReset` kept until `hook_done`) or `restore/<id>/inflight-checkpoint.json` (a `KindCheckpoint` restore, so the rollback tooling applies its exception from the path alone); `CheckReader` (tokens `restore_pending` and `restore_inflight`, whose message names the kind) and the one S0 sudoers probe read exactly these paths (05-storage §5S.3.7). Lands at **S2b** with the kernel's journaled swap (04 §8.1 S2b). | [05-storage](05-storage.md) §5S.3.15, [05-backup](05-backup.md) |
| `storage/index` | public | ABO | Full-text search index contract (bleve implementation in `internal/bleveindex`). Depends on `storage`, never the reverse; index wiring lives in `kernel`; the full-text dirty set behind it is a store interface (below). | [05-storage](05-storage.md) |
| `storage/datamigrate` | public | ABO | Declarations of large, resumable data migrations (`DataMigration` with a required `tasks.Parallelism`, shard plan, dry run); imports `storage` and `tasks`, so it sits at L4, not with `storage`. Modules list them in `module.Manifest.DataMigrations`; the runner is `internal/storagemigrate`. Extraction-era data migrations are `BootBlocking`; the `Online` mode (with `storage.ReadAdapter` and the kernel-synthesised `<ID>.datamigrate.<name>` kind) has no S0–S10 consumer and is a deferred-build member that lands with its first consumer (05-storage §5S.3.14; SIMP5-7). | [05-storage](05-storage.md) §5S.3.6 |
| `library` | public | ABO | Media roots, `os.Root`-confined filesystem access, path mapping, safe move/copy/hardlink, hashing, quarantine, free-space checks; native roots are the `core.library.roots` and `core.library.mappings` config entries, and an app's pre-core import roots come from `legacy.LibraryRootSource` (below; SIMP6-3, SIMP7-8). | [05-library](05-library.md) |
| `tasks` | public | ABO | Durable task/job system: `Definition`, `Run`, `RunItems`, `Parallelism` (required), `Schedule` (the one schedule mechanism), `Resource` (= `entity.Ref`, namespaced write-sets), checkpoints, preview/dry-run, cancellation; runs, queues, checkpoints, schedule clocks, the pause marker and the stand-down markers persist through the `tasks.Stores` interfaces (below; SIMP7-10). | [05-tasks](05-tasks.md) |
| `events` | public | ABO | Notify-only in-process bus with typed, namespaced topics; the one topic catalog. | [05-events](05-events.md) |
| `outbox` | public | ABO | Durable, at-least-once, same-batch outbox for correctness-critical cross-module work. | [05-events](05-events.md) |
| `log` | public | ABO | `log/slog` construction, per-module child loggers, redaction, and the `Record`/`Tap` types of the **system log journal** (persisted, queryable operational log; implementation `internal/logjournal`, persisting through the journal store interface below; the books activity feed stays in the books module). | [05-logging](05-logging.md) |
| `metrics` | public | ABO | Per-`App` Prometheus registry and OTel providers (never the process globals); core's series are named under the app's metric namespace, an `app.Options` value (§3.6 "Metric name"; A-5). | [05-metrics](05-metrics.md) |
| `config` | public | ABO | Layered config: defaults → file → env → flags (operator-set values **lock** their key) → DB overrides (apply only to unlocked keys; stored through `config.OverrideStore`: natively in `c/config/`, and for ABO in place in its config blob and secret rows by the books legacy implementation, which also runs ABO's settings load, settings-encryption init and key-regeneration guard through the kernel's one DB handle at the boot phase 06 assigns — never a second Pebble open, never defaults written over the stored blob (R4-6)). **One storage rule (A-2; 05-config C4, §1.12.2):** a key that has a legacy name is stored only in HEAD's legacy row and format; a key with no legacy name is stored natively in `c/config/`, on ABO installs too, through 05-config's routing store, and no older build reads it. An app's legacy env alias whose key HEAD let a stored value override (for ABO every `AutomaticEnv`/`BindEnv` name outside HEAD's env-locked and env-authoritative lists, e.g. compose's always-set `BASIC_AUTH_ENABLED`) feeds the **default** layer and locks nothing, and an env value set to `""` is unset, so the books legacy config store keeps HEAD's precedence for **every** legacy key, the auth-enable switch included, until the auth-precedence cutover H3: a value persisted through the UI wins over an environment default exactly as at HEAD, and with auth off the owner UI may still store `enable_auth` under HEAD's `!prior.EnableAuth` rule; the canonical `ABO_CORE_AUTH_MODE` is an additional operator name that locks the key and that older builds do not read (09 §9.2.1 row 3) (R5-1, A-2, lead decision; 05-config §1.3.7). ABO's `/metrics` keeps HEAD's any-credential check through S10: every `AnyAuthenticated` principal holds `core:metrics.read` through `app.Options.LegacyAmbientScopes` (05-rbac §6.3). A stored value equal to the default still beats a `BelowStored` legacy value, and a value staged as an unset writes the default explicitly (SEC7-2). **Legacy decoding (A-7):** a legacy env or file name decodes with HEAD's per-type semantics (`strconv.ParseBool` spellings, whitespace-split slices, Go durations, `cast` integers) and canonical names keep the strict decoder; a legacy bool on an auth or `ProtectAdmin` key that HEAD would silently read as `false` is a boot error (fail-closed, with a release note), and `false` elsewhere (05-config §1.3.6). Per-module typed sections declared in the manifest; JSON Schema for the UI; change notification. Holds no integration instances (those are `integrations` records). | [05-config](05-config.md) |
| `secrets` | public | ABO | Encrypted-at-rest secret store with one keyring; config fields reference secrets by name. A native store keeps every secret here, encrypted. For an app with pre-core secrets **nothing moves during the extraction** (A-2): the books legacy config store reads and writes ABO's plaintext blob secret fields in place, as HEAD does, and reads HEAD's four already-encrypted secret rows (OpenAI key, Google Books key, Hardcover token, Basic password; `internal/config/persistence.go:1955-1958` @748fd787a blanks them in the blob, `:1989-2003` writes them as encrypted setting rows, `SetSetting(…, true)`) in place, so a rollback reads every secret exactly as before (R5-1). Encrypting the plaintext ones is the post-S10 **secrets cutover** (02 N15). No moved secret has a file copy at HEAD: HEAD's gap-fill `config.yaml` beside the database holds only kept keys (`internal/config/persistence.go:168-178`), and a test asserts that no core build writes a key into it that HEAD's `SaveConfigToFile` does not manage (ADV-8). | [05-config](05-config.md) |
| `httpapi` | public | ABO | `net/http` (Go 1.22+ `ServeMux` patterns) API framework: typed routes, mandatory permission per route, error envelope, pagination, versioning, OpenAPI 3.1 generation, core perimeter middleware, and `MountLegacy` — a **permanent, supported** way to mount an app's existing router (ABO's gin engine) behind core's perimeter; it never returns a chain-less group (R6). | [05-http-api](05-http-api.md) |
| `realtime` | public | ABO | SSE hub with resumable, permission-filtered streams. | [05-realtime](05-realtime.md) |
| `auth` | public | ABO | Identity: users, sessions, API keys, bootstrap, OIDC/OAuth, credential epochs; middleware. Persists through its identity store interface (below): natively in `c/auth/` (hashed sessions and one-time tokens, `mck_` keys with native scopes); for ABO the books legacy implementation keeps users, API keys, sessions and one-time tokens in ABO's rows in HEAD's representation — raw `sess:` rows, HEAD's one-time tokens, `abk_` keys holding legacy atoms — so every row stays readable by an older build (A-2). A new API key always has explicit, non-empty scopes (R5-6); on ABO's legacy store they are legacy atoms, as at HEAD, so a scope with no legacy atom cannot be stored there until the identity cutover (02 N15). | [05-authn](05-authn.md) |
| `rbac` | public | ABO | Permission scopes (`<ns>:<action>`), roles as data, module-declared permissions, enforcement helpers. Roles persist through `rbac.Store` (below): natively in `c/rbac/` with native scopes; for ABO in ABO's role rows holding **legacy atoms only**, never an extra string in a legacy row, because HEAD's worker confinement is a "nothing else" test that an extra string defeats (`internal/server/middleware/auth.go:271-297`). Core-namespaced scopes are an **API-level mapping** over those atoms (`rbac.LegacyAtom`), so the API speaks namespaced scopes for ABO too and D7 holds (A-2; 02 decision 13). | [05-rbac](05-rbac.md) |
| `audit` | public | ABO | Append-only audit log (auth events, permission denials, admin actions, task approvals); targets are `entity.Ref`. | [05-rbac](05-rbac.md) |
| `compat` | public | ABO | Compatibility primitives for strangler migrations: alias tables (paths, config keys, env vars, permission atoms) registered against one compatibility window (09 §9.2.1), Deprecation/Sunset headers and the process-local legacy-use counter `<namespace>_compat_legacy_use_total` (the app's metric namespace, §3.6); metric names have no alias tables, because ABO keeps its metric names (A-5). Every alias needs a row in the compatibility ledger, which `compat.Ledger` merges from two sources: the **app's** ledger (`app.Options.CompatLedger []byte`; a core `//go:embed` cannot reach outside `core/`. ABO's source of truth stays `docs/compat-surfaces.md`, but the Docker build context excludes `docs` (§3.2 item 4), so the books composition root `internal/books` embeds a generated copy, `internal/books/compat-surfaces.md`, which a CI freshness check keeps byte-equal to the source, and `main` passes it on; R4-4; CI's `changes` classifier counts both copies and `core/compat/*.md` as Go inputs, so a ledger-only PR runs the ledger tests, 09 S0, OPS5-4) and a core-owned fragment `core/compat/core-ledger.md` embedded by `compat`, which holds only names every app shares — the permanent `OTEL_*` env aliases (05-config C7); every window alias, of a config key or an env name, is the app's, and a test asserts that every core-fragment row has gate `permanent`. A row ID in both is a boot error. The store-level compatibility guard is the merged storage-format/compat-level mechanism owned by 05-storage (§3.7). | [09-abo-on-core](09-abo-on-core.md) |
| `integrations` | public | ABO | Root of the integration contracts and **the one registry for integration instances** (native ones in `c/integrations/instances/`; during the extraction ABO's existing instances are legacy-backed in their blob fields, 05-metadata M1): `integrations.Ref{Kind, Instance}`, instance records (kind, instance, settings, `secrets.Ref`s), the shared error taxonomy (retryable/permanent/auth/rate-limited), `Factory`/`Descriptor` types modules use to contribute adapters (`Registrar.Adapter`). Imports nothing in `module` (R4). | [05-metadata](05-metadata.md) |
| `integrations/ai` | public | ABO | Provider-neutral AI contract (text/structured generation, embeddings, transcription) plus routing/health. v1 records usage metrics only; the usage ledger and monthly cap are deferred. | [05-ai](05-ai.md) |
| `integrations/download` | public | ABO (contract, `download/deluge`, what ABO uses today); deferred (other adapters; grab/monitor/pending-recovery/`MarkImported`) | Download-client contract (`Client`, `GrabRequest`, `Item` status) + adapters `download/deluge`, `download/qbittorrent`, `download/transmission`, `download/sabnzbd`, `download/nzbget`. Owns client polling, item state and completion events; the grab pipeline belongs to `acquire`. | [05-download](05-download.md) |
| `integrations/indexer` | public | **deferred** | Indexer/search contract + Newznab/Torznab adapter. | [05-indexer](05-indexer.md) (design sketch, non-normative) |
| `integrations/metadata` | public | ABO | Generic metadata-provider contract `Provider[Q, R any]` + resilience wiring. | [05-metadata](05-metadata.md) |
| `integrations/notify` | public | ABO | Notification contract + adapters (mail, webhook) with global routing. Inbox, per-user subscriptions, personal channels and templating are deferred. | [05-notify](05-notify.md) |
| `integrations/providerhttp` | public | ABO | Outbound HTTP for every adapter: per-provider throttle, daily quota, circuit breaker, retry, user agent — **instance-scoped**, not package globals. | [05-metadata](05-metadata.md) |
| `acquire` | public | **deferred** | Media-neutral acquisition engine (quality definitions, profiles, custom formats, decision engine, grab decisions, tracked-download records, import hand-off, history/blocklist/queue); consumes `integrations/download` events, never polls clients itself. | [10](10-tv-movies-proof.md) (design sketch, non-normative) |
| `backup` | public | ABO | Consistent backup/restore of every core-owned store plus module-declared artifacts; one archive format. | [05-backup](05-backup.md) |
| `backup/journal` | public | ABO | Leaf of `backup` (L4; imports `entity` and `storage` only): the out-of-DB revocation and restore journal and its types (`Revocation`, `RevocationJournal`, `RestoreContext`, `RestoreHook`). Lands at 09 S4 with `auth` and `rbac`, which import it instead of `backup` (S8); `rbac` cannot take these types from `auth`, which imports `rbac` (COR2-9). | [05-backup](05-backup.md) §5B.3.6 |
| `updater` | public | ABO | Self-update check/apply, parameterized by the app's release source. S8 **moves** the S0-hotfixed updater here rather than rebuilding it: exit 75, the `.prev` slot, semver comparison, the level and floor checks and check-only unattended mode. Signed manifests, the boot-attempt counter, automatic rollback and the unit's binary-layout migration are deferred-build members (A-6; 05-updater). | [05-updater](05-updater.md) |
| `health` | public | ABO | Liveness/readiness, component health, diagnostics bundle. | [05-health](05-health.md) |
| `auth/oidc`, `auth/password`, `health/diagnostics` | public | ABO | Public sub-packages that are not conformance suites (COR6-9): `auth/oidc` (OIDC/OAuth providers from `internal/oauth`, public because ABO's `internal/cfaccess` uses them) and `auth/password` (password hashing and policy) land with `auth` at S4; `health/diagnostics` (the diagnostics bundle, itself a task) lands with `health`. Each takes its parent's layer (R4). | [05-authn](05-authn.md), [05-health](05-health.md) |
| `arrfacade` | public | **deferred** | Extension point for an *arr-v3-compatible API adapter (D2). During the extraction only the `/api/v3/` path reservation exists (05-http-api §4.1). | [08-arr-facade](08-arr-facade.md) (design sketch, non-normative) |
| `ui` | public | ABO (from the stage in which ABO adopts the shell build; `ui.ReservedSegments` from S2) | Go side of the UI shell: serves an app's embedded **shell** SPA build with the core's headers/CSP/caching; `ui.ReservedSegments` (§3.6). `ui.ReservedSegments()` is a static function with no dependencies that lands at **S2** together with `module`, so `module.ReservedIDs()` reserves the shell segments and its "equals §3.6's row" test passes from S2. The dependency-free leaf types `ui.ModuleManifest`, `UIIDs` and `Redirect` land with it at S2, because `internal/modulehttp` (L6) reports `modules[].ui` from S4 and cannot name the L7 `module.UIDescriptor` (FE4-6); the rest of `ui` lands at S9 (04 §8.1). Until an app ships a shell build, `ui.Handler` is not mounted and the legacy mount owns `/`. | [07-ui-shell](07-ui-shell.md) |
| `coretest` | public | core | Test kit: in-memory `App` harness, fake clock, fake adapters, permission assertions; sub-packages `coretest/fakes` (in-memory implementations of every built capability contract, 06 §6.4.8) and, deferred with `acquire`, `coretest/acquiretest` (10). Test-support (R4). | [06-app-sdk](06-app-sdk.md) |
| `archtest` | public | core | Reusable architecture guards an app runs in its own tests (§3.5); sub-package `archtest/tasklint` (the task-bundle analyzer from `tools/cmd/oplint`, S5, 04 §8.1). Test-support (R4). | §3.5 |
| `<pkg>/<pkg>test` | public | with its store interface | Store conformance suites and fakes for the **native contract**, one package per store interface of the table below (`config/configtest`, `auth/authtest`, `rbac/rbactest`, `tasks/taskstest`, `log/logtest`, `integrations/ai/aitest`, `integrations/providerhttp/providerhttptest`, `integrations/integrationstest`, `integrations/notify/notifytest`, and `storage/index/indextest` for `index.DirtySet`, 05-storage §5S.3.8): every implementation, native or an app's legacy one, runs its suite. The capability cases and the suite of the legacy-only root source live in `legacy/legacytest` instead (row `legacy`; SIMP6-6). Test-support like `coretest` (may import any core package; no non-test core package imports them); part of the SDK and frozen with it. Each lands with its interface (04 §8.1; MOD5-6). | the interface's spec file |
| `legacy` (with `legacy/legacytest`) | public, outside the API promise | ABO | **Every identifier that exists only to serve an app's legacy stores** (R6-3): the optional legacy-only capability interfaces, the legacy secrets store, the `legacy.Stores` struct whose constructor fields an app sets on `module.Manifest.LegacyStores`, the legacy-only `legacy.LibraryRootSource`, the narrow pre-core maintenance handle `legacy.Maintenance` (from S2b, handed to the module that declares legacy claims as `module.StorageHandles.Maintenance`, nil in an offline session; its one method, `StageFactoryReset(ctx, requestedBy, kind)`, is the kernel's reset staging, which books' gin handlers for `POST /system/factory-reset` (`ResetFactory`) and `POST /system/reset` (`ResetStore`) call behind HEAD's chain; the handle and the field are deleted in the S4 r2 PR, when both legacy paths become `Alias`es of the core route; R7-2, R8-1, SIMP8-4, SIMP8-9), and their conformance suites in the test-support sub-package `legacy/legacytest` (paragraph "Legacy-only identifiers" below). Layer **L5** (R4). Excluded from the API-stability gate like `kernel/` (12 SC1, §12.4), so deleting a family's identifiers here, in the first release after its cutover that no longer sets the member, is not a breaking change (COR7-5). Unlike `kernel/`, apps may import it (ABO's books module does); `examples/hello` may not (`archtest.SDKOnly`, R2), because an app with no pre-core data never needs it. Lands at S2 with the first legacy-only capability (05-config's) and grows with each store's stage. | [05-storage](05-storage.md) §5S.3.11 |
| `shared/...` | public | none yet | Cross-app *domain* packages that are not platform concerns but are used by ≥2 apps (§3.4). Zero promoted (04 §9). | [04-package-mapping](04-package-mapping.md) |
| `kernel/...` | public-but-unstable | core | Module catalog, manifest validation, lifecycle supervisor, and the **typed services struct** that builds core services in a fixed order with explicit constructors. There is **no generic DI container** (no `Get[T]`-of-`any`). Imported only by `app`. Excluded from the API-stability gate; apps that import it fail `archtest`. | [06-app-sdk](06-app-sdk.md) |
| `examples/hello` | public (example) | core | The minimal "hello" app from 06 §hello: a **package in the core module** (not a separate module), built and tested in core CI and importable by ABO's G6 test; the in-tree second consumer for the split criteria (12 SC3) and the producer of the core OpenAPI document `core.json` (07 §7.8). It lands at S2 as a skeleton and grows with the stages, using only what each stage has built (04 §8.1; rule (b) of §3.4). | [06-app-sdk](06-app-sdk.md), [12](12-repo-split.md) |
| `internal/<capability>http` | internal | ABO (`internal/acquirehttp`: deferred) | **Core capability HTTP routes** (`/api/core/v1/...`): one package per capability — e.g. `internal/taskhttp`, `internal/authhttp`, `internal/rbachttp`, `internal/audithttp`, `internal/confighttp`, `internal/libraryhttp`, `internal/backuphttp`, `internal/updaterhttp`, `internal/healthhttp`, `internal/eventhttp`, `internal/storagehttp` (lands at **S4 r2**: one route, the factory-reset route `POST /api/core/v1/system/factory-reset`, `core:admin` + interactive + owner, over a staging func the kernel hands it as a storagehttp-local interface, `storagehttp.Stager` (the kernel's `stageReset`), never `*storage.Admin` or the kernel's swapper, the way `modulehttp` receives `ModulesView`; ABO's two legacy paths become `Alias`es of that one route then, `POST /system/factory-reset` (kind `ResetFactory`, the route's default) and `POST /system/reset` with `reset_kind=store` (kind `ResetStore`), each keeping HEAD's `{"confirm":"RESET"}` request and `{data: {message}}` response through its `LegacyPath.Shape`, and no core route for either exists before S4 r2; 05-storage §5S.3.15, R7-2, R8-1, COR8-1, COR8-2), and `internal/modulehttp` (`GET /api/core/v1/modules`; the kernel hands it a read-only `ModulesView` interface — catalog snapshot, `restart_required`, and the `ui_build_id`, which is the served **shell** bundle's UI build ID (`ui.Bundle.BuildID()`, read from that bundle's meta tag) and `""` while a legacy mount owns `/`; the `X-App-Build` header carries the legacy mount's bundle ID meanwhile (07 §7.5; FE5-7), and so does `--version-json`'s `ui_build_id`, which is the `X-App-Build` value, not this field (FE7-13). There is no ldflags value and no boot-time comparison (05-http-api §4.2), and the UI build ID is never the binary's identity (§3.6 "Build identity", R5-7) — so it imports nothing above L6). Each imports its capability package(s) and `httpapi` (and `realtime` for stream routes), sits at L6 (R4), registers on `httpapi`'s core group, and is imported **only by `kernel`**. | per capability |
| `internal/...` | internal | per contract | Implementations: `internal/pebble`, `internal/executor`, `internal/scheduler`, `internal/sse`, `internal/bleveindex`, `internal/openapi`, `internal/storagemigrate`, `internal/logjournal`, `internal/jsonschemax` (the one JSON Schema reflector, R4), … | per contract |

**Store interfaces (R4-1).** Every core service that persists state an app may already keep in its own
key families defines a **store interface** in its SDK package and ships a **native implementation** over
its own `c/<subsystem>/` keyspace (`library` is the one exception: its native roots are config entries and its root source is legacy-only, table below). The native
implementation is the one `examples/hello`, `coretest` and every new app use, and it lands with its
subsystem (rule (b) of §3.4). An app with pre-core data passes a **legacy implementation** of the same
interface on `module.Manifest.LegacyStores` (a `legacy.Stores` value, 06), never through `app.Options` (sole exception: the
S2-only bridge `app.Options.LegacyConfigStore`, needed because S2 has no kernel storage and deleted in the
S2b PR, 06; SIMP5-10, DATA5-16). The full-text dirty set is the one interface supplied **per index**
instead, through `index.Spec.Dirty`, because a module may open several indexes (05-storage §5S.3.8;
SIMP5-2). For ABO that is code in the `books` module, over ABO's
existing keys and row formats, and it stays in ABO. Each legacy implementation lives in a books package
that may import what it needs (`internal/config`, the store residue, the moved task store) and is never
imported back by the code it uses, so no split creates an import cycle or a capability assertion that
silently skips a step (R5-2; §3.5.1 item 1, 04 §2.4). So core holds no app key name or row format (R1(c))
and holds no storage grant during the extraction. Core enforces its invariants above the interface
(never-empty API-key scopes, staging rules) and ships one conformance suite per interface,
in its `<pkg>/<pkg>test` package (row above), which every implementation runs (ABO's with HEAD fixtures).

**Two selection models (SIMP5-1).** An **exclusive** store (`auth`, `rbac`, `tasks`, `config`, `log`, AI
jobs, the dirty set of one index) is native or legacy for a build: a supplied legacy implementation
replaces the native one. A **routed** store (integration instances, provider gates, notification rules)
always runs its native half, and the legacy implementation additionally serves the records it covers,
decided per record by one predicate, `Covers(id string) bool` (the `legacy.Covering` capability), over the
record ID the owner file defines (instance ref, gate scope and counter, rule ID). Only a routed store's
legacy half implements `Covers`; a legacy half without it covers every record, the native half is never
asked, and the `backends` ledger then records `legacy` (SIMP6-5). Library roots are neither: the native
roots are config entries, not a store, and `legacy.LibraryRootSource` adds an app's pre-core import roots beside
them (SIMP6-3). 05-storage §5S.3.11 owns both models and the `backends` ledger value of a routed store
(`legacy+native`).

**Legacy-only identifiers: the `legacy` package (R6-3; SIMP5-4, SIMP6-2, SIMP6-3).** An SDK store
interface carries only the native contract. Every exported identifier that exists only to serve an app's
legacy stores lives in **one core package, `legacy`** (its suites in the test-support sub-package
`legacy/legacytest`), which the API-stability gate excludes like `kernel/` (12 SC1, §12.4): the
**optional capability interfaces** — reconcile, legacy atom writes, the legacy config open, secrets,
external-writer and commit hooks, run recovery, the run store's pre-core write lock (`legacy.TasksWriteLock`, 05-tasks 5T.3.13) and `Covers`; the legacy secrets store (`legacy.SecretsStore`,
which reads an app's secret rows and blob secret fields in place, A-2); the
**`legacy.Stores`** struct, whose constructor fields an app sets on `module.Manifest.LegacyStores`;
library's root source, which has no native implementation and so is legacy-only as a whole
(`legacy.LibraryRootSource` with its record-ID helper `legacy.LibraryCoverID`; a legacy-served root keeps only HEAD's fields, and every field its row has no place for carries the source's fixed value, so a change to one is refused with `library.ErrRootFieldFixed`, A-2; SIMP6-3 option (b): a later native
implementation would add a `library` interface additively); the pre-core maintenance handle
**`legacy.Maintenance`**, which the kernel builds and hands to the module that declares legacy claims as
`module.StorageHandles.Maintenance` from S2b, so pre-core app code (books' two gin reset handlers until
S4 r2) reaches the kernel's reset staging, `kernel.stageReset`, without the swapper, which stays kernel-held, and the
handle is deleted in the S4 r2 PR (R7-2, R8-1, SIMP8-4; 05-storage §5S.3.11 owns its members and lifetime); the reset's
staging capability `legacy.ConfigResetArgs` with its plan `legacy.ConfigResetPlan` (called for both reset kinds) and the
post-reset hook's carrier `legacy.ConfigResetRun` (handed as `legacy.ConfigOpenEnv.Reset` for a `ResetFactory`; R7-2,
R8-1, SIMP8-7); and their conformance suites.

*Naming (SIMP7-8).* An identifier drops any `Legacy` prefix. An **owner-specific** identifier takes its
former package's name in Go initialism form (`Auth`, `RBAC`, `Tasks`, `Config`, `Secrets`, `Library`,
`Integrations`, `ProviderHTTP`): `rbac.LegacyAtomWriter` → `legacy.RBACAtomWriter`,
`tasks.Recoverer` → `legacy.TasksRecoverer`, `config.LegacyOpener` → `legacy.ConfigOpener` (the config
capabilities are `legacy.Config*`, 05-config §1.3, so the post-reset hook's carrier is `legacy.ConfigResetRun`, SIMP8-7), `secrets.LegacyStore` → `legacy.SecretsStore`,
`library.RootSource` → `legacy.LibraryRootSource`, `legacy.LibraryCoverID`, `legacy.IntegrationsCoverID`,
`legacy.ProviderHTTPCoverID`, and `legacy.TasksWriteLock` (05-tasks 5T.3.13: the run store's pre-core write lock held across the caller's commit, A-3). A **cross-owner** identifier takes none (`Stores`,
`Handles`, `Covering`, `Maintenance`; `storage.Covering` →
`legacy.Covering`, `module.LegacyStores` → `legacy.Stores`). A record type's legacy state is
`<Type>LegacyState` after its exact type name (`InstanceConfigLegacyState`, `KeyLegacyState`); no such state annotates or is stored beside a legacy row (A-2). 05-storage
§5S.3.11 "Legacy capabilities" owns the full list and the owner files own the members.

*Lifetime (COR7-5).* A family's cutover build still sets its `legacy.Stores` member, because it reads the
legacy data through it; the member and the identifiers only it uses are deleted in the **first release
after the family's cutover that no longer sets the member** (behind the cutover's `UpgradeFloor`, 05-storage
§5S.3.13), a minor. That deletion is **not** a breaking change of the SDK and needs no deprecation period and
no major (12 §12.4.2).

*Layer and detection (SIMP7-5, COR7-3).* `legacy` sits at **L5** (R4): it imports the L0–L4 packages whose
types its signatures name, and only `module`, `kernel` and apps import it, so no owner package ever imports
it. **This is the one list of the capabilities the kernel asserts** (SIMP8-3); every owner file cites it
instead of giving a count. There are four. Two it **drives**, `legacy.ConfigOpener` and
`legacy.ConfigSecrets`, whose signatures name `legacy` types (`legacy.ConfigOpenEnv`,
`legacy.SecretsStore`), so no owner can mirror them — the kernel calls `Open` and passes `Secrets()` to
`secrets.Open` (05-config §1.3.3). One it **calls at staging**, `legacy.ConfigResetArgs`: `kernel.stageReset`
calls it for **both** reset kinds, `ResetStore` and `ResetFactory` — the plan carries the factory-default settings rows
the reset boot writes for either kind, and the hook arguments for a `ResetFactory` only (05-storage §5S.3.15) — and
passes the plan to the swapper, so `storage` never names `legacy` (COR8-2, SEC8-5).
And `legacy.Covering`, on a routed store's legacy half, because that store's `backends` ledger value
(`legacy` or `legacy+native`) depends on it and P4c compares that value before P4d (COR7-4; the router
still asserts it through its own mirror). Every other capability is detected once, by its owner, through an
**unexported** interface of its own with the same method set (rbac's legacy atom writes, the router of a
routed store, `internal/executor`'s run recovery), and its signatures name only types of the owner's layer or
lower. The kernel hands each built store to its owner and records nothing else about capabilities.

*The mirror test (R7-7; SIMP7-1, COR7-2).* This is the one statement of the pattern; every owner file
cites it. An in-package test of the owner cannot import `legacy`, because `legacy` imports the owner and Go
refuses the cycle ("import cycle not allowed in test"). So:

- the owner's `export_test.go` (`package <owner>`) only exports each mirror as a test-only alias
  (`type CoveringMirror = covering`) and each unexported record-ID helper as a test-only variable;
- a second file, **`legacy_mirror_test.go`, in the owner's external test package** (`package
  <owner>_test`, which may import `legacy`), asserts both directions, `var _ legacy.X =
  (<owner>.XMirror)(nil)` and `var _ <owner>.XMirror = (legacy.X)(nil)`, and compares the helpers on a
  table; for the executor that is `internal/executor`'s external test package.

A renamed or retyped member therefore fails the owner's tests instead of silently disabling a capability.
Native implementations implement none of the capabilities and carry no stubs. Conformance follows one
pattern (SIMP6-6): `<pkg>/<pkg>test` tests the native contract, and **one legacy suite per store
interface**, `legacytest.<Pkg><Interface>Suite` (e.g. `legacytest.AuthStoreSuite`), runs a case for each
capability the implementation has.

*Legacy-only inputs with no SDK interface (R7-7; SIMP7-4).* An owner that receives a legacy-only value for
which no SDK interface exists types the field **`any`**: `secrets.Options.LegacyStore any` and
`library.Options.LegacySource any` (library's constructor is `library.New(opts Options)`, 05-library
§5L.3). The kernel always passes a value built by a typed `legacy.Stores` constructor; the owner asserts its
mirror at construction and fails the boot with **`storage.ErrLegacyStoreType`** on a mismatch, never a silent skip
(one sentinel for every owner, defined once in 05-storage §5S.3.11).
No exported field is typed by a mirror, so `legacy`'s method sets change freely in a minor (R6-3), and the
field stays in v1, inert (nil) after the cutover.

*What stays in SDK packages (SIMP7-3).* Some legacy-serving identifiers cannot move, because a lower-layer
type names them or the owner's own format or entry point needs them. Their **one list** is 05-storage
§5S.3.11's table "Legacy-serving identifiers that stay in SDK packages" (identifier, why it cannot move,
after the cutover); this file and 14 K23 keep one example per reason: named by a lower-layer type (a
record's `Legacy *<Type>LegacyState` field, nil from a native store; SIMP6-7); the owner's own format
(`secrets.ReadLegacyKey`, which reads `k0-legacy` from the keyring file; 05-config §2.3); a permanent guard
(`storage.LegacyGuardProjection`, because pre-guard builds never go away; SIMP7-7); an owner entry point
(the `any`-typed inputs above).
**Lead decision (round 6):** `LegacyStores` remains a field of `module.Manifest`, and its type is
`legacy.Stores`; only the struct's fields, in `legacy`, are deleted after a family's cutover, while the
`Manifest` field stays. All of these stay in v1 until a major, inert for native apps (14 K23).

This table is the **one list** of store interfaces: other files cite "03 §3.3 'Store interfaces'" instead
of restating it (SIMP5-8).

| Package | Store interface (named in its spec file) | Model | `legacy.Stores` field, set on `module.Manifest.LegacyStores` (06 §6.4.2) | What it persists | Native implementation | ABO's legacy implementation (books module) | Lands (09) |
|---|---|---|---|---|---|---|---|
| `config` (with `secrets`) | `config.OverrideStore` ([05-config](05-config.md) C4); `legacy.SecretsStore`, reached through its legacy secrets capability (05-config) | exclusive | `Config` | DB overrides and, for a legacy store, the secrets it holds in place | `c/config/` (encrypted secrets in `c/secrets/`) | ABO's config blob and secret rows, in HEAD's representation (plaintext blob secret fields, HEAD's four encrypted secret rows read in place; R5-1, A-2); ABO's settings load, settings-encryption init and key-regeneration guard (R4-6); in `internal/books/configstore` (§3.5.1) | interface S2 (the read members the S2 bridge uses); native implementation and `LegacyStores.Config` S2b; write members S3, when core starts writing (COR5-14) |
| `auth` | `auth.Store` ([05-authn](05-authn.md) §3.4) | exclusive | `Auth` | users, API keys, OAuth identities, invites, sessions, one-time tokens | `c/auth/` | ABO's user, API-key (`abk_`, legacy atoms), OAuth-identity, invite, session (raw `sess:`) and one-time-token rows, all in HEAD's representation (A-2) | S4 |
| `rbac` | `rbac.Store` ([05-rbac](05-rbac.md) §3.4) | exclusive | `Roles` | roles | `c/rbac/` | ABO's role rows, legacy atoms only; namespaced scopes are an API-level mapping over them (`rbac.LegacyAtom`) | S4 |
| `tasks` | `tasks.Stores` (`RunStore`, `ClockStore`, `PauseStore`, `StandDownStore`; [05-tasks](05-tasks.md) §5T.3.13) | exclusive | `Tasks` | runs, queues, checkpoints, schedule clocks, the pause marker, the stand-down markers | `c/tasks/` | ABO's ops-history and run families (HEAD's ops store code, in `internal/books/taskstore`) and the clock-key and pause-marker formats, divided out of HEAD's scheduler clock and pause-gate files whose tick logic and gate go to core (04; MOD5-4); the maintenance-window clock `maintenance_window_last_run` is the `ClockStore` of that kind, with HEAD's `YYYY-MM-DD` codec (R7-8, MOD7-3); and the scan stand-down marker, HEAD's `registry.scan_standdown` setting row (MOD6-4; COR7-6, SIMP7-10) | S5 |
| `log` | `log.JournalStore` ([05-logging](05-logging.md) §3) | exclusive | `Logs` | the system log journal | `c/log/` | ABO's system log rows | S5 |
| `library` | `legacy.LibraryRootSource` ([05-library](05-library.md) §5L.3.1; legacy-only, SIMP6-3) | legacy-only, beside the native config entries | `Roots` | import roots (for ABO also the books root, listed read-only: its path changes only through config) | the `core.library.roots` and `core.library.mappings` config entries (no `c/library/` root records; SIMP5-1) | ABO's import-root rows and their counter, and the books root from the config blob | S7 |
| `integrations/ai` | `ai.JobStore` ([05-ai](05-ai.md)) | exclusive | `AIJobs` | AI batch jobs | `c/ai/` | ABO's AI job rows (books' AI result journal is plain books code over its own family, not a store interface; SIMP5-3) | S7 |
| `integrations/providerhttp` | `providerhttp.GateStore` ([05-metadata](05-metadata.md) M2) | routed | `Gates` | throttle holds, daily quotas | `c/providerhttp/` | ABO's provider throttle and budget rows | S7 |
| `integrations` | `integrations.InstanceStore` ([05-metadata](05-metadata.md) M1.3) | routed | `Integrations` | integration instance records | `c/integrations/` | ABO's legacy-backed instances (plugin and provider settings), rendered from the config blob by the same code as books' legacy config store (SIMP4-12) | S7 |
| `integrations/notify` | `notify.RuleStore` ([05-notify](05-notify.md)) | routed | `NotifyRules` | global notification rules | `c/notify/` | the one legacy-backed rule over ABO's webhook settings | S7 |

The full-text dirty set has no row and no `LegacyStores` field: `index.DirtySet` (05-storage §5S.3.8) is
supplied **per index** through `index.Spec.Dirty` (nil = the native set over `c/index/`); ABO passes its
dirty set, drained by both builds, for its index from S7, and it is exclusive per index (SIMP5-2).

npm workspace `core/web/` (D6):

| Package | Purpose |
|---|---|
| `@mediacore/shell` | React shell: auth, layout/nav, settings frame, tasks/jobs, logs, users/roles, notification admin pages for the v1 scope — the notify contract, the mail and webhook channels and global routing rules — plus toasts (no inbox, bell, per-user subscriptions or personal channels: deferred, 05-notify §1), system health; the `createShell({ modules })` extension API. |
| `@mediacore/api-client` | Generated TypeScript client for **core** routes (from the core OpenAPI document). Apps generate their own client package the same way. |
| `@mediacore/ui-kit` | Shared presentational components and theme tokens (MUI-based) used by shell and apps. It imports neither `@mediacore/api-client` nor `@mediacore/shell` (a `core/web` ESLint rule): a component that needs data or a toast takes it as a prop (`ServerFileBrowser`'s `browse`, `PathLinks`' `aliases` and `onCopied`; 07 §7.11, FE7-12). |

## 3.4 What is core, what is shared, what is app

**Promotion rule (non-circular).** Being listed in §3.3 is not a reason to build a package. A
package is **built** in core only if

- (a) ABO consumes it during the extraction stages of [09](09-abo-on-core.md) (S0–S10, with S2b and UI-0), or
- (b) a core package built under (a) or (b) needs it — the kernel, the composition root, the
  architecture guards and the in-tree second consumer `examples/hello` count as core. `examples/hello`
  never pulls a package forward: it uses only what the stages have built so far and grows with them
  (04 §8.1), so it cannot force `ui` or `tasks` into existence earlier than ABO needs them.

A package that meets neither keeps a **design sketch (non-normative)** in the spec at proof depth (D5)
and is marked **deferred** in §3.3: the plan builds none of it, the SDK freezes none of it, and it is
designed and built when the first TV/Movies app starts (A-15). Today that is `acquire`, `arrfacade`,
`integrations/indexer`, the download grab/monitor pipeline, the download adapters other than Deluge, and
the AI usage ledger; inside built packages it is also the members that only the deferred cutover uses
(storage grants and the served-run preconditions, 05-storage §5S.3.13), the `Online` data-migration mode
and its read adapters (no S0–S10 consumer; 05-storage §5S.3.14, SIMP5-7), backup encryption (05-backup B3),
the seed-role merge diff (05-rbac §4.8), the config basis mechanism (`config.Basis`: no key of S0–S10
declares a basis; 05-config §1.12.5, SIMP6-1, SIMP7-9) and root read shares (`library.RootSpec.ReadShares`:
no S0–S10 app has two modules that share a root; ABO has one module; 05-library, SIMP7-2, SIMP8-5).
**Deferral applies to features inside built packages as well as to packages**: a built package carries
no member whose only consumer is deferred — for example the facade-only key machinery
(`KeySpec.Purpose`/`Module`, `PurposeFacade`, query-key extraction, `Router.FacadeGroup`, the facade
route kind and its stage-13 clause) lands with `arrfacade`, not in `auth`, `rbac` or `httpapi` at S2/S4.
Every spec file that has deferred members in a built package lists them, informatively, under a heading
`Deferred-build members`; an identifier is listed under one heading only, and another file that defers it
cites that heading. No generated list or absence test checks these headings (A-4): a PR that builds a
deferred member is reviewed against its heading and deletes that member's entry.
Cross-app domain helpers enter `shared/` only when they have **two real consumers**; until a second
app exists they stay in ABO unless a built core package needs them. The guard against the 2026-09-18
adversarial review's residual risk "small core becoming a god package" (`raw/08-prior-art.md` §2) is
therefore the consumer test, not the list. The Build column is re-checked at every stage gate
(14 K6). The package mapping ([04](04-package-mapping.md)) applies the rule to all 213 packages.

**Visibility rule.** A package that still has an ABO importer moves to a **public** core path, or
stays in ABO; it never moves to `internal/` or `kernel/`, which ABO cannot import (Go's `internal/`
rule; R2). It may move to `internal/`/`kernel/` only in the stage in which its **last** ABO importer has
been rewired to a public SDK surface (for example `sdnotify` behind `health`/`lifecycle` readiness
hooks, `compactprogress` behind a `storage` statistics type, `oauth` behind the `auth` OIDC API,
`scheduler`/`executor` behind `tasks`, startup steps only through `app`). A package whose replacement
public surface lands in a later stage stays in ABO until that stage: e.g. `internal/startup` stays in ABO
until `Registrar.StartupSteps` exists (06 §6.4.10), because `cmd/root.go:274` builds the `serve` step
runner from it (R3-3, MOD3-1). 04 derives its internal/public column from the import graph under this
rule (informative planning data). Go's `internal/` rule and `archtest.NoKernelImports` enforce it when the PR
that rewires or moves a package builds; no predictive gate evaluates later stages ahead of time (A-4).

| Bucket | Rule | Examples (decided in 04) |
|---|---|---|
| core kernel/SDK | built under (a) or (b) above | storage, tasks, auth, rbac, httpapi, config, log, metrics |
| core, deferred | contract only; built with the first TV/Movies app | `acquire`, `arrfacade`, `integrations/indexer` |
| core `shared/` | domain logic with ≥2 real consumers across apps, or needed by a built core package | text normalization (`titleutil`, `personname`) once TV/Movies parsers need them |
| app (ABO) | everything audiobook-specific | scanner, organizer, dedup, itunes, ABS compatibility surface, narrators, the legacy store implementations over ABO's key families (§3.3, R4-1) |
| delete/merge | dead or duplicated mechanisms | v3/v2/v1 op duplication collapsed into `tasks`; legacy `internal/plugin` |

## 3.5 Dependency direction rules and their mechanical enforcement

| # | Rule | Enforced by |
|---|---|---|
| R1 | Core never imports app code, and holds no app key name or row format. | (a) `core/go.mod` must not `require` any app module — checked by `core/archtest.TestNoAppRequire` reading `core/go.mod`; (b) `core/archtest.TestCoreImportsOnlyCore`: `go list -deps -test` of every core package (test imports included, because the E2 hazard came from a test file; the synthetic `.test` and `[pkg.test]` packages are filtered) contains no path outside `github.com/falkcorp/mediacore/...`, stdlib, and the allowlisted third-party set; runs in core's own `go test ./...`. Note that Go *does* allow module dependency cycles, so (a) is required, not redundant. (c) A core service that needs data an app already stores defines a store interface and the app supplies the implementation (§3.3 "Store interfaces", R4-1); because a **copied** format passes (a) and (b), the root module's `TestCoreHoldsNoAppKeyNames` (`internal/arch`; this is the test's one name, DATA5-16) scans every string literal in a non-test `core/` file. Its name set is the claim generator's **output** for owner books, read through the exported `KeyFamilies()` at its current path (`internal/database/keyfamilies.go:330` @748fd787a, `internal/books/store/keyfamilies.go` from S2b) with core's reserved `c/` and `m/` excluded, plus ABO's setting and preference sub-keys (generated from the `GetSetting`/`SetSetting` call sites, which add `setting:` themselves, so a moved file can carry one with no family prefix) and the 269 op IDs of `internal/server/testdata/op_ids.golden` (MOD5-4), plus a reviewed list of ABO row-type and param-key literals that no family prefix carries (`legacy_op_id`; MOD7-2). **One match rule** — this is its only text; other files, 05-storage's `TestCoreHoldsNoAppKeyNames` row among them, cite it instead of restating it (MOD6-7, MOD7-7): a literal fails when it **begins with a family prefix** and the character after the prefix is not a space (a key, not a Go `pkg: message` error string such as core `library`'s `"library: …"` sentinels, although `library:` is an ABO family, `keyfamilies.go:126`); when it **equals a setting or preference sub-key, a listed literal or an op ID, or begins with one followed by `.` or `:`**; or when it **begins with a constant prefix folded from a constant + variable key** (`"scheduled_task_last_tick." + kind` folds to `scheduled_task_last_tick.`, which a literal matches by begins-with). A **struct tag** is matched per key: each `key:"value"` pair's value, up to its first `,`, is matched as a literal, because a param key travels as a struct tag in most of its ABO occurrences (MOD8-4). Core-owned task kinds never spell an app's op ID: an app maps its former op IDs onto core kinds itself (06 `app.Options.LegacyTaskAliases`; books maps `diagnostics.export` onto `core.diagnostics.export`), so a `FormerKinds` literal naming an ABO op ID in a core file fails this test (MOD6-6). Exceptions live in one reviewed allowlist file, not inline. Cases: `"library: root escapes"` passes, `"library:" + id` fails, `"c/tasks/"` passes (MOD5-3), `FormerKinds: []tasks.Kind{"diagnostics.export"}` fails (MOD6-6), `"<op-id>.x"` (e.g. `"library.organize.extra"`) fails while `"library.organize-when-scanned"` is judged as its own key, not as the op ID `library.organize` (MOD7-7), `"scheduled_task_last_tick.x"` fails (folded prefix), `` `json:"legacy_op_id,omitempty"` `` fails (struct tag, MOD8-4). |
| R2 | Apps import only core's public packages. No stage rewires an ABO import onto `internal/` or `kernel/` (visibility rule, §3.4). | Go's `internal/` rule for `core/internal/...`; `archtest.NoKernelImports(t, "github.com/falkcorp/audiobook-organizer/...")` for `kernel/...` (called from the root module's `internal/arch`); `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` for the examples, which the path-based `internal/` rule does not cover (X5), and which counts `legacy/...` as outside the SDK, so `hello` never imports it (R6-3). A wrong-way rewire surfaces when that stage's PR builds (A-4). |
| R3 | A module never imports another module's packages; modules talk through core contracts (events, outbox, tasks, capability lookups). | `archtest.ModulesIsolated(t, map[moduleID]pkgPrefix)`; for ABO there is one module, so the rule is vacuous until a second exists, but the test is wired from S0 so the second module cannot land without it. |
| R4 | Core layers (a package may import only its own or lower layers): **L0** `entity`, `lifecycle`, `version`, `buildid`, `log`, `metrics`, `storage/keys`, `storage/codec`, `storage/restorejournal` → **L1** `storage`, `storage/index` → **L2** `secrets` → **L3** `config` → **L4** `events`, `outbox`, `tasks`, `storage/datamigrate`, `library`, `auth`, `rbac`, `audit`, `compat`, `integrations`, `integrations/*`, `backup`, `backup/journal`, `updater`, `health` → **L5** `acquire`, `legacy` → **L6** `httpapi`, `realtime`, `arrfacade`, `ui`, `internal/<capability>http` (including `internal/modulehttp`) → **L7** `module` → **L8** `kernel` → **L9** `app`, `examples/*`. Deferred packages keep their layer so their contracts are checked when built. A public sub-package `p/q` takes the layer of `p` unless this list names it: `auth/oidc` and `auth/password` are L4 with `auth`, and `health/diagnostics` is L4 with `health` (its import of `tasks` is a same-layer edge; COR6-9). `coretest` and its sub-packages (`coretest/fakes`, `coretest/acquiretest`), `archtest` and `archtest/tasklint`, the store conformance-suite packages `<pkg>/<pkg>test` and `legacy/legacytest` are test-support packages (may import any core package; no core non-test package imports them; MOD5-6). `legacy` imports only L0–L4 and is imported only by `module`, `kernel` and apps (R6-3). An `internal/*` package takes the layer of the public package it implements; an `internal/*` package that **bridges** capabilities (imports a package above the one it implements) takes the layer of its **highest import**, is never imported by a lower-layer package, and is wired by `kernel` — e.g. `internal/storagemigrate` (storage migrations run as tasks: L4), `internal/logjournal` (persisted journal over storage and tasks: L4), `internal/<capability>http` (L6). `internal/restoreswap` (the one journaled directory swap: restore, revert, checkpoint restore and `KindReset`; R8-1) is **L1**, imports only `storage` and its L0 leaves, and is imported only by `kernel`. The direction is `restoreswap` → `storage`, never the reverse: `storage.Admin` carries no staging or swap method, and the kernel holds the swapper, staging through it (for a reset through `kernel.stageReset`, which calls `legacy.ConfigResetArgs` first for a `ResetFactory`; COR8-2) and running it at P0a (COR8-5). The one sentinel of an occupied slot, `storage.ErrSwapPending`, lives in `storage`, so the S2b paths and L1 code can return it, and `backup`'s restore sentinel is its alias (COR8-6). The restore-journal record type that restoreswap and storage's open read is defined in the L0 leaf `storage/restorejournal` (05-storage §5S.3.15), which `backup` imports directly without an alias, and the kernel passes the recorded identity to storage's open, so `storage` never reaches an L4 type (COR7-10, SIMP8-8); `core/internal/archtest_layers_test.go` checks the edge. `internal/jsonschemax` is an **L0** leaf (it imports only `github.com/google/jsonschema-go`): it owns the one JSON Schema reflector wrapper, the tag pass and an extensible override table (`type Overrides map[reflect.Type]*jsonschema.Schema`, `For[T any](o Overrides)`); each package contributes the overrides for its own types at its own layer, through a function so no exported package-level map exists (R5): `jsonschemax.Base()`: `time.Time`; `config.Overrides()`: `config.Duration`, `secrets.Ref`; `httpapi`: `rbac.Permission` — so `config` (L3) never names `rbac` (L4). There is one composition, `jsonschemax.Base().With(config.Overrides()).With(<httpapi's own>)`, used by both the settings schema and OpenAPI: `httpapi` imports the static `config.Overrides()` value (L6 may import L3), never a `*config.Store` (SIMP2-3, SIMP3-1). | `core/internal/archtest_layers_test.go` — the redesigned successor of `internal/arch/layering_test.go` for core (§3.5.1). |
| R5 | No process-global mutable state **in core**. No `func init()` in core except allowlisted pure ones (none at start); no exported package-level variables of map, slice, func or pointer type; core packages never call `prometheus.DefaultRegisterer`/`MustRegister` on the default registry, `otel.SetMeterProvider`, `otel.SetTracerProvider`, `otel.SetLogger`, `otel.SetTextMapPropagator`, `otel.SetErrorHandler`, `slog.SetDefault`, `log.SetOutput`, `http.DefaultServeMux`, `http.DefaultClient`/`http.DefaultTransport` mutation, `signal.Notify` or `signal.NotifyContext` (which installs its handler through `signal.Notify`, so a scan keyed on the one name would miss it). The single exception is `app.InstallProcessDefaults`, an opt-in helper a `main` may call; a `package main` is a process owner like that helper, so `TestNoGlobals` skips `main` packages (`core/examples/*/cmd/`), where hello's `main` calls `signal.NotifyContext` (COR6-11); until it exists (S2), the process-global installs that ABO's metrics need stay in ABO (a thin `internal/telemetry` façade or `cmd/root.go`), and core `metrics` only returns providers (04). R5 covers the test-support packages `coretest` and `archtest` too: `coretest` captures logs through an injected handler (it hands the test a `*slog.Logger`) and never calls `slog.SetDefault`, so core tests stay parallel-safe; a helper that sets the process default for ABO code stays in ABO (MOD3-3). R5 binds core only: an app may keep façades over core handles (ABO keeps `config.AppConfig` and its logger shim as books-module values); they are per-`App` values, so several apps in one binary stay possible, and removing them is an optional ratchet, not an extraction stage. | `core/archtest.TestNoGlobals` (AST scan). Rationale: several apps in one binary, and test isolation (`raw/07-plugin-registry-sdk.md` §6, `raw/12-cross-cutting.md` §9). |
| R6 | Every route declares a permission (or explicit `rbac.Public` / `rbac.AnyAuthenticated`; `AnyAuthenticated` never admits a confined credential, 05-rbac). Routes served through `httpapi.MountLegacy` (ABO's gin engine, mounted permanently behind core's perimeter middleware) are covered by a reviewed legacy route manifest in which every route names its permission. The manifest registers each legacy route as its own `ServeMux` pattern, except that each connected set of same-owner routes that conflict as `ServeMux` patterns (HEAD has such sets, e.g. `GET /api/v1/operations/{id}/activity` beside `GET /api/v1/operations/v2/{id}`) registers as **one generalised pattern**, and only when every member carries the same `Perm`, `Guards`, `RateClass`, `OwnAuth`, `BasicExempt` and `rbac.LegacyAtom`; any other conflicting set fails manifest generation, and the post-gin check accepts the `c.FullPath()` of any member (A-1; 05-http-api §4.8, §8.2). **No API returns a chain-less route group or mount** (the G8 failure mode was a missing chain, not a missing permission): a non-Public `Router.Infra` route runs the authenticated stages like any other route (05-http-api §4.2); `MountLegacy`'s `LegacyAuthGin` mode (gin's own auth chain instead of core's stages 12–13) is accepted only for the module named in the compile-time `app.Options.LegacyAuthGinUntil`, which ABO removes in its S4 PR; and a legacy route marked `OwnAuth` is accepted only when it names a compat method registered through `Registrar.AuthMethod` (non-interactive, never `MayChangeCredentials`). | Registration-time error in `httpapi` (fail-closed) **and** `archtest.RoutesHavePermissions` over the built router and the legacy manifest — a permanent test, not a migration test; `TestOwnAuthRoutesGolden` records every `OwnAuth` route; an archtest fails if `LegacyAuthGinUntil` is still set after S4 (05-http-api §8.2). |
| R7 | Every library-scale loop declares parallelism. | `tasks.Parallelism` has no valid zero value. `Batch`/`Fixer` parallelism is a definition field validated at **registration** (a missing value fails it). `tasks.RunItems`, `tasks.RunSeq` and `tasks.ForEach` (the same loop for code that holds no `*RC`) take a required positional `tasks.Parallelism`; a zero value returns `*tasks.ItemsError` wrapping `tasks.ErrNoParallelism` before any item runs (fail-closed). `archtest.RunItemsDeclareParallelism` (AST) accepts, for that argument and for the `BatchSpec`/`FixerSpec` field: (a) a call to a `tasks` constructor (`CPU`, `Fixed(n, reason)`, `Sequential(reason)`); (b) a `tasks.Parallelism`-typed variable, parameter or field whose every assignment in the analysed packages is (a) — so passed-through values are legal; (c) a `//mediacore:required` options field whose owner calls `Parallelism.Validate` at construction. It flags zero literals and empty reasons (05-tasks §5T.3.2). Replaces `internal/plugins/runitems_concurrency_lint_test.go`. |
| R8 | Every module storage key lives in the module's keyspace or a declared legacy claim. | `storage` refuses **reads and writes** outside the handed `Keyspace` and claims (`ErrOutsideKeyspace`; DATA-17); claim modes (read-write, read-only, aging-out, retired) and the same-owner nesting rule are 05-storage §5S.3.4's; the cross-owner reach of the deferred cutover (`storage.Grant`, `GrantMode`) is a deferred-build member that lands with the first cutover (05-storage §5S.3.13, 02 N15) and is not part of the extraction; claims are of three kinds — prefix, exact key, and prefix-with-exceptions (sub-prefixes or exact keys carved out as the same owner's nested claims with their own mode, as books' settings and index families do in 05-storage §5S.9.1's ABO-migration table) — resolved by longest match; boot fails on any overlap between owners (§05-storage). During the extraction no owner reaches another owner's range: core services reach an app's data only through the app-supplied store implementations, which run on the app's own claims (§3.3, R4-1). Claim enforcement rolls out shadow → strict; storage-enforced write checks (`CheckedWrites`) cover native keyspaces and rows core newly writes only, and ABO's legacy families keep HEAD's lock discipline, with a voluntary `Expect`/`GetForUpdate` compare-and-set inside a `Serialized` legacy claim allowed and never required (§3.3 `storage`; A-3). |

### 3.5.1 Redesign of `internal/arch/layering_test.go`

Verified assumptions that the extraction breaks (`raw/03-layering-guard.md` §2–§4):

- the **package floor** `minModulePackages = 150` (`internal/arch/layering_test.go:38`). The root has
  213 packages today. Under 04's mapping 49 packages leave (`tools/cmd/opsgen` stays in ABO until its
  last importer is native, which may never happen, FE5-12; `internal/operations/state` stays as books'
  legacy run-state package — HEAD's v2 table, V3 constants, transition machine, `Classify*`,
  `FromV2`/`ToV2` — which `internal/books/taskstore` and `opsgen` import, while core's executor state
  machine is a re-implementation over `tasks.RunState` and taskstore maps between the two; R6-2, MOD6-1;
  the registry engine, `childop`, `opsmetrics`, `opsv3` and the whole of
  `pkg/ops/opstest` (into `taskstest`) are re-implemented too, recorded as 04 §8.6 planning rows: core's executor is written fresh against
  `tasks.RunStore`/`RunState`, with `rc.Await` in place of `childop.Follow` and `opsmetrics`' one core successor in
  `internal/executor`; the ABO files stay at their path as opsbridge — the registry engine
  until T8, `childop` and `opsmetrics` until their last ABO user is native (`childop` latest at T7), because v2
  bodies that stay v2 until their T2–T7 batch still call them; MOD7-1, MOD8-2)
  and 5 new paths appear
  (`internal/books`, the composition root; `internal/books/contract`, the books helper package below
  it, created at S5, item 1 and R4-5; `internal/books/taskstore`, books' legacy `tasks.Stores`, 05-tasks 5T.6.2;
  `internal/books/configstore` (name open), books' legacy `config.OverrideStore`, `integrations.InstanceStore`
  and the books-root half of `legacy.LibraryRootSource`, which need `internal/config` and so cannot live in the
  store residue that `internal/config` imports (MOD5-2, R5-2); `internal/cfaccess`), so
  213 − 49 + 5 = **169** remain: a margin of 19 that further moves consume (04 §8.4 note 6;
  `raw/03-layering-guard.md` §3's 154 predates the new paths and the 8 packages 04 keeps in ABO, among them
  `internal/logger/logtest` (MOD3-3), `internal/telemetry` (MOD4-4), `internal/metadata/dailyquota` (books'
  legacy gate store, R4-1) and `internal/ai/resultjournal` (plain books code over books' own family, not a
  store implementation; SIMP5-3, COR5-10)). The `internal/operations/registry` path that stays as the v2
  adapter (`opsbridge` in 05-tasks) is that existing path re-implemented, not a new package, and the frozen
  reach snapshot under `internal/books/rbacreach/` is a data directory read by a test, not a Go package
  (05-rbac §6.3), so neither changes the count (MOD5-11, MOD5-17). The count is re-derived whenever 04's
  moves change;
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
     process-global setters that 07 and 09 S2 remove.) A PR that ever removes the edge must name a
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
     `books.Module(…)` in every file (MOD5-11). The shared books helpers (`PageBooks`, the
     books `Writer` contract, selection helpers, task `Resource` declarations and resolvers, task-kind
     aliases) live in a package **below** it (`internal/books/contract`, name open, 04 §2.4; layer 3) that
     imports neither `module`/`app` nor anything that imports the composition root; otherwise a plugin
     that imports a helper closes the cycle `internal/books` → plugin → `internal/books` (R4-5). Below
     layer 6 an ABO package never imports `module` or `app`: it receives what it needs (`ExtendStart`,
     loggers, scopes) as funcs or interfaces that `internal/books` wires. A guard assertion fails when any
     package other than `.`, `cmd` and `internal/books` imports `mediacore/module` or `mediacore/app`
     (non-test imports). Every new path enters `layerOf` in the PR that creates it — `internal/books`
     and `internal/books/configstore` at S2, `internal/books/contract` and `internal/books/taskstore` at
     S5, when the S5 sources of contract's content (`pkg/ops`, `pkg/plugin/sdk`, `internal/operations`,
     `internal/opscatalog`) leave; its last source, the resolvers of `internal/operations/registry/deps.go`,
     joins at T8 — so no stale key and no placeholder file is needed (MOD5-9, MOD6-14; 04 §2.4, §8.1, §8.6).
     The legacy store packages import downward only: `internal/books/taskstore` imports the store residue
     and `internal/operations/state` (MOD6-1), never the reverse, and the residue reaches the code it shares with the moved task store (books legacy
     steps 62/65/66, the every-boot ops reconcile) only through funcs that `internal/books` wires; a legacy
     step whose precondition cannot resolve on a production store type fails a test instead of logging
     "nothing to do" and being recorded as applied (R5-2, MOD5-1; 04 §2.4). A core import is never counted as an in-module edge, but it is
     checked against the importer's layer like one: an ABO domain package that imports
     `mediacore/realtime` is a wrong-way edge exactly as an import of today's `internal/realtime` is,
     so an `allowed` entry such as `plugins/* → itunes/service` is re-evaluated against the mapped
     layer when its target moves to core, never dropped (04 §8.4 note 3). Mapped core imports also
     **feed the computed layer**: the layer the classification rule computes for an ABO package is the
     maximum of its in-module imports' layers **and** the mapped layers of its core imports (floored as
     today), so `layerOf` follows a core import the same way it follows an in-module one. Consequence at
     S2: `internal/httputil` (declared layer 0, `layering_test.go:82`) keeps `respond.go`, which builds
     the envelope types that move to `mediacore/httpapi` (mapped layer 5), so its computed and declared
     layer become **5**; that is legal, because all 19 of its importers are layer 5 (round-3 X17), and it
     needs no new `allowed` entry. The same computation makes `internal/cfaccess` layer **5** from S4 (its `owner_guard.go` imports
     `internal/httputil`), so its layer-4 importer `internal/plugins/maintenance` (`layering_test.go:227`)
     does not import it: `internal/books` injects the owner check as a func, the MOD4-18 rule above, and 04 §2.4 states cfaccess's `layerOf` (MOD7-4). Packages with no R4 row are mapped explicitly: `coretest`, `archtest`,
     the `<pkg>/<pkg>test` suites, `legacy/legacytest`, `storage/*` → **−1**; `shared/...` → **3** (domain). `archtest.NoKernelImports` is unchanged. This needs a **loader change**: `loadGraph` (`layering_test.go:339-381`) drops
     every import outside the module today; it keeps imports with the `github.com/falkcorp/mediacore/`
     prefix and hands them to the classifier;
   - a **classifier positive control**, effective from S0, the first stage in which ABO imports core
     (the root guard itself calls `archtest.Layering`, 09 S0): at least one root package imports a
     `github.com/falkcorp/mediacore/...` path, and the guard classified that import onto the mapped
     layer. (A cross-module edge cannot serve as the in-module control, because by the previous bullet
     it is never an in-module edge.)

   A rule against "smuggling audiobook types into core through a callback" was considered and
   dropped: callbacks are values, not import edges, so no import-graph check can see them, and
   R1(a)/(b) already stop core from naming any ABO type.
2. **Core guard** (`core/internal/archtest_layers_test.go`) is a new instance of the same algorithm
   with the R4 layer table, its own `layerOf` and its own ratio floor. It is registered in
   `ci/gates.txt` as a core gate (§3.2 item 4). Its in-module positive control is **staged**, because
   core has no in-module import edge at S0 and `app`/`kernel` arrive only at S2:
   - **S0:** the engine's own self-test on a synthetic graph in `archtest` (a known violation must be
     reported, and an empty graph must fail); the core guard asserts only that the loader returned at
     least the packages core holds;
   - **from S1:** the S1 PR names a real edge it creates (e.g. `health` → `log` if the flight recorder
     takes a `log` child logger, or `coretest` → `log`);
   - **from S2:** `app` imports `kernel`.

   A PR that removes the current control names its replacement in the same change (the ABO rule).
3. **Shared engine**: the classification/compute code moves to `core/archtest` as a library
   (`archtest.Layering(t, archtest.LayerSpec{...})`) so both modules — and every future app repo —
   run the same algorithm. This is what "reusable guard" in R2/R3 means.
4. **The one tag-set list and the stage marker (A-4).** The (GOOS, tag set) pairs that CI or goreleaser
   builds, taken mechanically from the workflows and the Makefile, are **one list** (MOD7-14), which the embed
   check of §3.2 item 4 and 04 cite: default, `bench`, `integration`, `pprof`, `race`, `native_taglib`,
   `embed_frontend fts5 native_taglib` (the image build's combined set, `Dockerfile:74-75`), `GOOS=darwin`
   (goreleaser) and `GOOS=windows` **for `./cmd/mtls-bridge` only** (`build-mtls-bridge-windows`). A tool
   that needs every file's imports takes the union of the file lists of `go list -deps -test -json` over all of
   them, because a default-tag `go list` reports a build-tagged file only under `IgnoredGoFiles`, without its
   imports (round-4 Y2); no file parses `IgnoredGoFiles` entries itself (MOD5-16).
   Stage progress is a plain committed `shipped_stage:` marker in `ci/stage.txt` (beside its `release:` line),
   which each stage's flip PR advances and which the soak gate reads (09 §9.2.1 owns `ci/stage.txt`, its ordered
   values and the soak ledger `ci/soak.txt`, A-9). No tool evaluates later stages ahead of time: a package rewired or moved too early fails
   to compile in the PR that does it, under Go's `internal/` rule and `archtest.NoKernelImports` (R2), and 04
   stays the informative per-package map (rows, destinations, stages, LOC), which feature PRs never
   regenerate (A-4, A-11).

## 3.6 Naming conventions

| Thing | Convention | Example |
|---|---|---|
| App ID | `^[a-z][a-z0-9]{1,15}$` (anchored, FE8-7), field `app.Options.AppID` | `abo` |
| Module ID | lowercase `[a-z][a-z0-9]{1,15}`; **reserved** — the first path segment of every route or page core serves and every namespace core owns, each name assigned to the one package that serves it: `httpapi.ReservedSegments()` (server paths) = `api`, `v` followed by digits, `auth`, `events`, `health`, `livez`, `readyz`, `metrics`, `assets`, `static`, `ui`; `ui.ReservedSegments()` (shell routes) = `activity`, `invite`, `login`, `settings`, `setup`, `system`, `users`; `rbac.ReservedNamespaces()` = `core`, `custom`. Apps add compatibility inputs with different meanings: `LegacyMount.TopSegments` (top-level server paths of a legacy mount, e.g. ABO's `ping`, `status`, `logout`, `socket.io`, `public`; they feed both the reserved IDs and `ui`'s non-SPA predicate), `LegacyMount.SPAShared` (top-level segments where the mount's routes share a path with a shell page, e.g. ABO's `login` — ABS `POST /login` while `GET /login` is the page — and `invite` — the legacy `GET /invite/{token}` 302 while `GET /invite` is the page; reserved IDs, but **never** non-SPA) and `app.Options.ReservedAPINamespaces` (second segments under `/api/`, e.g. ABO's ABS-compatible unversioned `/api/<seg>` names; reserved IDs only, because a native module API lives at `/api/<moduleID>/v1`). The first segment of a manifest's `LegacyPrefixes` is a reserved ID only; the non-SPA set is exactly `TopSegments` minus `SPAShared` (05-http-api §3.5, §8.2). `module.ReservedIDs()` is **static**: it returns the union of the three owner lists (all three exist from S2, §3.3 `ui`), and a test asserts that it equals this row exactly. The kernel validates module IDs at 06 P1 against that set plus `app.Options.ReservedAPINamespaces`, and re-checks them at P7 against every recorded mount's `TopSegments` and `SPAShared`, still before listeners open; a kernel boot check fails when a `ui.ReservedSegments()` name is a non-SPA segment (a shell page that a full page load could never reach), and passes for a `SPAShared` segment. | `books`, `tv`, `movies` |
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
| Metric name | core: `<MetricNamespace>_<subsystem>_*`, where `MetricNamespace` is the core metric namespace, an `app.Options` value (`[a-z][a-z0-9_]*`, default `mediacore`; A-5). Module series: `<MetricPrefix>_*` while the app **ships** one module, `<MetricPrefix>_<moduleID>_*` when it ships two or more — counted over the modules compiled into the app (the `app.New` module list, enabled or not), so enabling or disabling a module never renames another module's series. `MetricPrefix` is `app.Options.MetricPrefix` (`[a-z][a-z0-9_]*`, defaults to `AppID`). **ABO keeps its metric names:** it sets `MetricPrefix` to `audiobook_organizer` and passes its own `MetricNamespace`, so its 73 existing families (`internal/telemetry/contract/testdata/series.golden`) keep their names when the code that emits them moves into core, with no rename and no alias at any stage (05-metrics owns how each moved series keeps its exact name). Standard OTel semantic-convention names (e.g. `http_server_*`) are kept unchanged. | `mediacore_tasks_runs_total` (default namespace), `audiobook_organizer_books_total` |
| UI route | `/<moduleID>/...` for module pages; shell pages under the segments of `ui.ReservedSegments`; from the shell stage (S9) one-time tokens travel in the URL fragment, never in the path (05-authn §4.13); until then invite links keep the `/invite/<token>` path form that the legacy SPA routes (`web/src/App.tsx:201-206`; FE4-2) | `/books/library`, `/settings`, `/activity`, `/invite#token=…` |
| Build identity | Two values, never interchanged (R5-7). The **UI build ID** names one built UI bundle: `BUILD_ID` when it is non-empty, and it must match `^[A-Za-z0-9._-]{1,64}$` and must not be `dev` in any case (reserved: a `dev` ID never triggers the reload rule), or `vite build` fails (an empty value falls through; FE6-7, FE7-6); else, in a clean checkout (`git status --porcelain --untracked-files=no` prints nothing, so an untracked file never changes the rule; FE7-1), `<short sha>-<commit unix time>`; else (dirty tree, or no `.git`) `<UTC timestamp>-<8 random hex>`. The npm build itself generates it (the Vite config: ABO's from S0, the `@mediacore/shell/vite` preset from S9) and writes it into the bundle's meta tag, so `make build`, goreleaser, the Docker frontend stage and a bare `npm run build` all stamp it with no side script (07 §7.6.3 owns the text; FE5-1, FE5-2). The meta tag is the one source: the server's `X-App-Build` reads it, and the client reads it once at page load through `readPageBuildId()` of `@mediacore/api-client`, the only meta reader, and passes it to `checkBuild(response, { buildId, shell })` (07 §7.5.2; FE7-5); no Vite config, ABO's pre-shell one included, writes the UI build ID into a `define`, and no `core/web` package reads one, because a library built to `dist/` before the app would bake its own value (FE6-1, FE7-3). It serves only `X-App-Build`, `ui_build_id` and the stale-build reload rule. The **binary ID** names the running executable: `app.Options.Version`, `+`, and the SHA-256 of the executable, computed once per process before the store opens by `buildid.Binary`, the dependency-free L0 leaf `core/buildid` that lands at S0 (§3.3) — called by ABO's S0 guard and `--version-json` until the kernel exists, and by the kernel before P−1 from S2b, where `app.BinaryID` delegates to it (FE6-3); it needs no ldflags and no `.git`. The updater, storage's served-ledger and self-served records, backup's staged-by and source fields and `--version-json` use the binary ID — never the UI build ID, which is `dev` for every API-only build, and never `version.Version`, which is the core constant (FE5-3) | UI: `3f9c2ab-1762214400`; binary: `<version>+<sha256 hex>` |

ABO's existing un-namespaced names (13 permission atoms, 258 key families, `/api/v1/...` paths,
flat config keys, `/api/v3/ops`) are mapped onto these conventions with explicit compatibility
windows in [09-abo-on-core](09-abo-on-core.md); the window itself is defined once, in 09 §9.2.1.
ABO's routes stay on its gin engine under `MountLegacy` unless a route group is rewritten for its
own reasons, so `/api/v1/...` is a permanent books legacy prefix, not a surface with a sunset.

## 3.7 Process shape

One `main` per app calls `app.New` with the modules it ships and the adapters it enables, then
`Run`. The kernel validates the module graph (duplicate or reserved IDs, `MinCoreVersion` against
`version.Version`, permission/route/task-kind/keyspace collisions, migration order) **before**
opening listeners, runs migrations, constructs services, starts the task executor and scheduler,
mounts routes, and serves. Module enablement is **restart-bound** (kept from old decision 10) and is
read before module registration (06): a disabled module contributes no route, task, schedule,
worker, nav entry or filesystem mutation, and its data is retained.

The order of boot phases is defined **once**, in 06 §6.5.1's boot-phase table; every other file
references it. Three properties of that order are architectural and hold for every app:

- **One stored format per family; core reaches app data only through store interfaces; the level
  guard only for real format changes.** During the extraction a core service that persists state an
  ABO family already holds (users, sessions, API keys, roles, ops history and runs, the clock, pause
  and stand-down keys, the config blob and secret rows, system log rows, import roots, AI batch jobs,
  provider throttle rows, legacy integration instances and webhook rules, the full-text dirty set) uses its store interface, and the `books` module supplies the implementation
  over ABO's existing keys and row formats (§3.3 "Store interfaces", R4-1; 02 decision 13). Core holds
  no ABO key name and no storage grant. The extraction is **representation-neutral** (A-2): every ABO
  row keeps HEAD's representation, and no native-only data is written beside a legacy row for ABO, so there
  is nothing an older build could misread and nothing to re-confirm after a rollback. Nothing is copied,
  mirrored, rebuilt at boot or handed over between owners, so
  a rollback lands on the previous release with no floor build in between. Opening the store runs the
  single per-owner level guard (05-storage §5S.3.7): it refuses a store stamped above the build's
  level. Only a real format change raises a level — books' own migrations, and later a per-family
  **cutover** to a native keyspace, which is outside the extraction, one-way and checkpointed, brings
  the storage grants with it, and whose floor is the cutover release itself (02 N15). The three post-S10
  hardening cutovers (H1 identity, H2 secrets, H3 auth precedence; this file's introduction, 09 §9.13) run on the same
  machinery.
- **Checkpoint before the first shape-changing write.** A boot that will run a migration step, a level
  ratchet or a marker write takes the one pre-migration checkpoint before its first write (05-storage
  §5S.3.12); a boot with none of these takes none, and a boot that cannot take it halts before writing
  anything.
- **Legacy migrations first.** Books' existing legacy migrations run in the phase before every core and
  module migration, on books' own claims; no core grant exists over a books family during the
  extraction, so they need none. Their code, and every code path they call, is kept in every release
  before the **floor release**: the first release that deletes it (the 05-tasks T8 deletion) and declares books' upgrade floor.
  The **stepping-stone release** is the release the floor release's manifest names to install first
  (`InstallFirst`); it carries those steps and contains no deletion. The two terms always name these two
  different releases (OPS4-5). The requirement is checked on the store's applied steps, not on the binary
  being replaced: an updater from S0 on refuses to apply the floor release to a store that has not
  applied the steps and offers the stepping stone instead — the S0 updater hotfix and the `deploy` recipe
  compare the new binary's own `--print-upgrade-floor` with the store's applied-steps sidecar before the
  swap, and the S8 updater, which is the S0-hotfixed updater moved into `core/updater` (A-6), runs the
  same check (OPS6-1) — and boot refuses with a
  message naming it (02 N14). A pre-S0 updater swaps any newer binary unchecked (`internal/updater/updater.go:214-280`),
  so such an install meets the boot refusal; the floor release's release note and its last log line
  name the stepping stone (14 K18). An **empty store** is exempt from the floor check: its fresh-install
  batch records every books legacy step 1..`LegacyCap` as applied, and books' versioned migrations are
  numbered from `LegacyCap+1`, so a fresh install or a factory reset passes the floor on its first boot and
  every later one (R7-2, DATA7-7).

```
main ──► app.New(opts, books.Module(…), …)
           │  kernel: (phases P−1–P8 of 06 §6.5.1)
           │          the one staged swap, restore or reset (P0a) → config boot → validate manifests
           │          → open storage (per-owner level guard)
           │          → pre-migration checkpoint (before the first shape-changing write)
           │          → books legacy migrations (first, on books' own claims)
           │          → core and module migrations (05-storage cross-keyspace DAG)
           │          → build the typed services struct (explicit constructors, no init();
           │            each store interface bound to its native or app-supplied implementation)
           │          → register tasks/schedules
           │          → mount httpapi: core routes (internal/<capability>http), module routes,
           │            legacy mount (httpapi.MountLegacy, permanent for ABO's gin engine),
           │            arrfacade (deferred) → ui.Handler (only for an app that ships a shell
           │            build; until then the legacy mount owns `/`)
           ▼
         App.Run(ctx): start everything in the single start order of 06 §6.5.2
                       (served-ledger commit at Run (3b)),
                       stop in its reverse within core.shutdown.budget (05-health §5H.4)
```

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
