<!-- file: docs/specs/platform-core/03-architecture.md -->
<!-- version: 1.6.1 -->
<!-- guid: 8af6d935-7143-4d06-8562-b04a18504d81 -->
<!-- last-edited: 2026-11-04 -->

# 03 — Architecture

This section is **normative**: every other spec file uses the names, paths and rules defined
here. If another file disagrees with this one, this one wins until it is amended.

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
| **SDK** | The *public, versioned subset* of core that an app is allowed to import: `module`, the capability contracts, `tasks`, `storage` (with its leaves `storage/keys` and `storage/codec`), `httpapi`, `rbac`, `config`, `events`, `log`, `metrics`, `coretest`, `archtest`, `entity`, `lifecycle`, `version`, `integrations/*`, `compat`, and the npm packages `@mediacore/shell`, `@mediacore/api-client` and `@mediacore/ui-kit`. The SDK is not a separate module; it is the set of core packages **outside** `core/internal/` and outside `core/kernel/`, and is what the API-stability gate (§12) measures. Packages marked **deferred** in §3.3 are SDK paths whose contract is specified but not yet built. | same module | same module |
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
   `core/.interface-width-baseline`), a core coverage **ratchet floor** in core's merge gate (the
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
     `go mod download`; `.dockerignore` (today only `node_modules`, `web/node_modules`, `web/dist`,
     `.dockerignore:18-20`) adds `**/node_modules` and `core/web/packages/*/dist`; a PR job builds the
     image. These land in the same stage as the `replace` (09 S0). At S0 core holds only the
     stdlib-only `archtest` engine (`internal/arch/layering_test.go:20-31`), and `go mod tidy` writes
     no `go.sum` for a module without requirements (round-2 X12), so S0 commits an **empty
     `core/go.sum`** (tidy keeps it) and `check_module_skew.py` asserts that it exists; the literal
     `COPY core/go.mod core/go.sum` then never fails.
   - **Web.** Once `web/` depends on `@mediacore/*` (whose `exports` point at built `dist/`), every
     `cd web && npm ci && npm run build` that does not first build `core/web` breaks. One
     implementation, `scripts/ci/build_web.sh` (core web build, then `web/`), is called by
     `make web-deps`, by every workflow and by both Dockerfiles; path filters gain `core/web/**`,
     `core/go.mod`, `core/go.sum`; Dependabot npm gains `/core/web`. The full list of entry points is
     owned by 09 S0 and 07 §7.3.2.
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
   versions core CI never ran. Mitigation: Dependabot gomod `directories: ["/", "/core"]` in one
   group, and `scripts/ci/check_module_skew.py`, which fails when any module path in
   `(cd core && go list -m all)` resolves to a different version in the root's `go list -m all`
   (12 SC4(c)). S0 verifies that the Dependabot group produces **one** PR spanning `/` and `/core`;
   if it does not, a workflow step runs the root `go mod tidy` on Dependabot `/core` PRs and pushes
   the result (tidy coupling, item 4). The group sets `applies-to` for **both** version and security
   updates, because a security update is not grouped by a version-update group otherwise and could
   create skew on its own. Dependencies that must not float are pinned by a Dependabot `ignore` plus
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
**deferred** = the contract is specified here and in its spec file at proof depth (D5), the plan
builds none of it, and it is built when the first TV/Movies app starts.

| Package | Visibility | Build | Purpose | Spec |
|---|---|---|---|---|
| `app` | public | ABO | Composition root. `app.New(ctx, app.Options, ...module.Module) (*app.App, error)`; `(*App).Run(ctx) error`. The **only** way to assemble a process. | [06-app-sdk](06-app-sdk.md) |
| `module` | public | ABO | `Module` interface, `Manifest`, `Registrar` (routes, tasks, migrations, permissions, settings, events, UI, adapters), `Deps` handed to a module; `module.ReservedIDs()` (§3.6). | [06-app-sdk](06-app-sdk.md) |
| `entity` | public | core | Leaf package (L0): `entity.Ref{Module, Kind, ID string}` — **the one** polymorphic reference to any module-owned entity. Every file uses this type: tasks resources (`tasks.Resource` is `entity.Ref`), outbox/notify payloads, download grabs (multi-entity grabs carry `[]entity.Ref`), acquisition history/blocklist/queue, audit targets. No other `EntityRef`/`Resource{Module, Kind, ID}` struct exists; an `archtest` check fails any core identifier named `EntityRef` outside `entity`. Never a shared media row (decision 6). | [10](10-tv-movies-proof.md), [05-tasks](05-tasks.md) |
| `lifecycle` | public | core | Leaf package (L0): `Component{Start, Stop}` ordering contract and shutdown budget types used by kernel, tasks, realtime, outbox relay; the public constant `lifecycle.NotifySocketEnv` (the systemd notify-socket variable name, read by apps that strip it from subprocess environments; 04 §8.3, from S2). | [05-tasks](05-tasks.md), [05-health](05-health.md) |
| `version` | public | core | Leaf package (L0): `const Version = "0.Y.Z"`, the core version; S1 lands it as `"0.1.0"`, so the S2 `hello` skeleton's `MinCoreVersion: "0.1.0"` passes P1 before any checkpoint exists. **Never** derived from `debug.ReadBuildInfo` (under the `replace` that reports the zero pseudo-version, which is below every `MinCoreVersion`). Read by the kernel's `MinCoreVersion` check, `GET /api/core/v1/modules` and the backup manifest; CI keeps it equal to the newest checkpoint or release tag (12 §12.4.3). | [12](12-repo-split.md) |
| `storage` | public | ABO | Pebble-backed ordered KV: `DB`, `Keyspace`, `Batch`, `Snapshot`, `iter.Seq2` scans, per-keyspace migrations, legacy-prefix claims, and the explicit, logged **storage grants** through which a core service's legacy-format adapter reads and writes a legacy family in its stored format during the extraction (02 decision 13). | [05-storage](05-storage.md) |
| `storage/keys` | public | ABO | Leaf package (L0): pure tuple-key functions (the core key encoding). Lands at S0, so S0's guard writes its `c/storage/` records in core's encoding (04 §8.1). | [05-storage](05-storage.md) |
| `storage/codec` | public | ABO | Leaf package (L0): generic value codecs for stored values. | [05-storage](05-storage.md) |
| `storage/index` | public | ABO | Full-text search index contract (bleve implementation in `internal/bleveindex`). Depends on `storage`, never the reverse; index wiring lives in `kernel`. | [05-storage](05-storage.md) |
| `storage/datamigrate` | public | ABO | Declarations of large, resumable data migrations (`DataMigration` with a required `tasks.Parallelism`, shard plan, dry run); imports `storage` and `tasks`, so it sits at L4, not with `storage`. Modules list them in `module.Manifest.DataMigrations`; the runner is `internal/storagemigrate`. | [05-storage](05-storage.md) §5S.3.6 |
| `library` | public | ABO | Media roots, `os.Root`-confined filesystem access, path mapping, safe move/copy/hardlink, hashing, quarantine, free-space checks. | [05-library](05-library.md) |
| `tasks` | public | ABO | Durable task/job system: `Definition`, `Run`, `RunItems`, `Parallelism` (required), `Schedule` (the one schedule mechanism), `Resource` (= `entity.Ref`, namespaced write-sets), checkpoints, preview/dry-run, cancellation. | [05-tasks](05-tasks.md) |
| `events` | public | ABO | Notify-only in-process bus with typed, namespaced topics; the one topic catalog. | [05-events](05-events.md) |
| `outbox` | public | ABO | Durable, at-least-once, same-batch outbox for correctness-critical cross-module work. | [05-events](05-events.md) |
| `log` | public | ABO | `log/slog` construction, per-module child loggers, redaction, and the `Record`/`Tap` types of the **system log journal** (persisted, queryable operational log; implementation `internal/logjournal`; the books activity feed stays in the books module). | [05-logging](05-logging.md) |
| `metrics` | public | ABO | Per-`App` Prometheus registry and OTel providers (never the process globals). | [05-metrics](05-metrics.md) |
| `config` | public | ABO | Layered config: defaults → file → env → flags (operator-set values **lock** their key) → DB overrides (apply only to unlocked keys; during the extraction a key that ABO already stores is read and written in place in its legacy row — the config blob or a settings family — through config's legacy-format adapter, and a native-only key lives in the core-only family `c/config/`, 02 decision 13); per-module typed sections declared in the manifest; JSON Schema for the UI; change notification. Holds no integration instances (those are `integrations` records). | [05-config](05-config.md) |
| `secrets` | public | ABO | Encrypted-at-rest secret store with one keyring; config fields reference secrets by name. A plaintext secret in ABO's config blob moves here the first time the new build writes it and is blanked in the blob (a rollback needs it re-entered; 02 decision 13). | [05-config](05-config.md) |
| `httpapi` | public | ABO | `net/http` (Go 1.22+ `ServeMux` patterns) API framework: typed routes, mandatory permission per route, error envelope, pagination, versioning, OpenAPI 3.1 generation, core perimeter middleware, and `MountLegacy` — a **permanent, supported** way to mount an app's existing router (ABO's gin engine) behind core's perimeter; it never returns a chain-less group (R6). | [05-http-api](05-http-api.md) |
| `realtime` | public | ABO | SSE hub with resumable, permission-filtered streams. | [05-realtime](05-realtime.md) |
| `auth` | public | ABO | Identity: users, sessions, API keys, bootstrap, OIDC/OAuth, credential epochs; middleware. During the extraction users and API keys stay in ABO's rows, used through legacy-format adapters; sessions are hashed in a core-only family no older build reads; a new API key always has explicit, non-empty scopes (02 decision 13). | [05-authn](05-authn.md) |
| `rbac` | public | ABO | Permission scopes (`<ns>:<action>`), roles as data, module-declared permissions, enforcement helpers. | [05-rbac](05-rbac.md) |
| `audit` | public | ABO | Append-only audit log (auth events, permission denials, admin actions, task approvals); targets are `entity.Ref`. | [05-rbac](05-rbac.md) |
| `compat` | public | ABO | Compatibility primitives for strangler migrations: alias tables (paths, config keys, env vars, permission atoms, metric names) registered against one compatibility window (09 §9.2.1), Deprecation/Sunset headers, `mediacore_compat_legacy_use_total`. Every alias needs a row in the compatibility ledger, which `compat.Ledger` merges from two sources: the **app's** ledger (`app.Options.CompatLedger []byte`; ABO embeds its root `docs/compat-surfaces.md` in the root `main` package, because a core `//go:embed` cannot reach outside `core/`) and a core-owned fragment `core/compat/core-ledger.md` (core metric and config aliases) embedded by `compat`; a row ID in both is a boot error. The store-level compatibility guard is the merged storage-format/compat-level mechanism owned by 05-storage (§3.7). | [09-abo-on-core](09-abo-on-core.md) |
| `integrations` | public | ABO | Root of the integration contracts and **the one store for integration instances**: `integrations.Ref{Kind, Instance}`, instance records (kind, instance, settings, `secrets.Ref`s), the shared error taxonomy (retryable/permanent/auth/rate-limited), `Factory`/`Descriptor` types modules use to contribute adapters (`Registrar.Adapter`). Imports nothing in `module` (R4). | [05-metadata](05-metadata.md) |
| `integrations/ai` | public | ABO | Provider-neutral AI contract (text/structured generation, embeddings, transcription) plus routing/health. v1 records usage metrics only; the usage ledger and monthly cap are deferred. | [05-ai](05-ai.md) |
| `integrations/download` | public | ABO (contract, `download/deluge`, what ABO uses today); deferred (other adapters; grab/monitor/pending-recovery/`MarkImported`) | Download-client contract (`Client`, `GrabRequest`, `Item` status) + adapters `download/deluge`, `download/qbittorrent`, `download/transmission`, `download/sabnzbd`, `download/nzbget`. Owns client polling, item state and completion events; the grab pipeline belongs to `acquire`. | [05-download](05-download.md) |
| `integrations/indexer` | public | **deferred** | Indexer/search contract + Newznab/Torznab adapter. | [05-indexer](05-indexer.md) |
| `integrations/metadata` | public | ABO | Generic metadata-provider contract `Provider[Q, R any]` + resilience wiring. | [05-metadata](05-metadata.md) |
| `integrations/notify` | public | ABO | Notification contract + adapters (mail, webhook) with global routing. Inbox, per-user subscriptions, personal channels and templating are deferred. | [05-notify](05-notify.md) |
| `integrations/providerhttp` | public | ABO | Outbound HTTP for every adapter: per-provider throttle, daily quota, circuit breaker, retry, user agent — **instance-scoped**, not package globals. | [05-metadata](05-metadata.md) |
| `acquire` | public | **deferred** | Media-neutral acquisition engine (quality definitions, profiles, custom formats, decision engine, grab decisions, tracked-download records, import hand-off, history/blocklist/queue); consumes `integrations/download` events, never polls clients itself. | [10](10-tv-movies-proof.md) |
| `backup` | public | ABO | Consistent backup/restore of every core-owned store plus module-declared artifacts; one archive format. | [05-backup](05-backup.md) |
| `backup/journal` | public | ABO | Leaf of `backup` (L4; imports `entity` and `storage` only): the out-of-DB revocation and restore journal and its types (`Revocation`, `RevocationJournal`, `RestoreContext`, `RestoreHook`). Lands at 09 S4 with `auth` and `rbac`, which import it instead of `backup` (S8); `rbac` cannot take these types from `auth`, which imports `rbac` (COR2-9). | [05-backup](05-backup.md) §5B.3.6 |
| `updater` | public | ABO | Self-update check/apply, parameterized by the app's release source. | [05-updater](05-updater.md) |
| `health` | public | ABO | Liveness/readiness, component health, diagnostics bundle. | [05-health](05-health.md) |
| `arrfacade` | public | **deferred** | Extension point for an *arr-v3-compatible API adapter (D2). During the extraction only the `/api/v3/` path reservation exists (05-http-api §4.1). | [08-arr-facade](08-arr-facade.md) |
| `ui` | public | ABO (from the stage in which ABO adopts the shell build; `ui.ReservedSegments` from S2) | Go side of the UI shell: serves an app's embedded **shell** SPA build with the core's headers/CSP/caching; `ui.ReservedSegments` (§3.6). `ui.ReservedSegments()` is a static function with no dependencies that lands at **S2** together with `module`, so `module.ReservedIDs()` reserves the shell segments and its "equals §3.6's row" test passes from S2; the rest of `ui` lands at S9 (04 §8.1). Until an app ships a shell build, `ui.Handler` is not mounted and the legacy mount owns `/`. | [07-ui-shell](07-ui-shell.md) |
| `coretest` | public | core | Test kit: in-memory `App` harness, fake clock, fake adapters, permission assertions. | [06-app-sdk](06-app-sdk.md) |
| `archtest` | public | core | Reusable architecture guards an app runs in its own tests (§3.5). | §3.5 |
| `shared/...` | public | none yet | Cross-app *domain* packages that are not platform concerns but are used by ≥2 apps (§3.4). Zero promoted (04 §9). | [04-package-mapping](04-package-mapping.md) |
| `kernel/...` | public-but-unstable | core | Module catalog, manifest validation, lifecycle supervisor, and the **typed services struct** that builds core services in a fixed order with explicit constructors. There is **no generic DI container** (no `Get[T]`-of-`any`). Imported only by `app`. Excluded from the API-stability gate; apps that import it fail `archtest`. | [06-app-sdk](06-app-sdk.md) |
| `examples/hello` | public (example) | core | The minimal "hello" app from 06 §hello: a **package in the core module** (not a separate module), built and tested in core CI and importable by ABO's G6 test; the in-tree second consumer for the split criteria (12 SC3) and the producer of the core OpenAPI document `core.json` (07 §7.8). It lands at S2 as a skeleton and grows with the stages, using only what each stage has built (04 §8.1; rule (b) of §3.4). | [06-app-sdk](06-app-sdk.md), [12](12-repo-split.md) |
| `internal/<capability>http` | internal | ABO (`internal/acquirehttp`: deferred) | **Core capability HTTP routes** (`/api/core/v1/...`): one package per capability — e.g. `internal/taskhttp`, `internal/authhttp`, `internal/rbachttp`, `internal/audithttp`, `internal/confighttp`, `internal/libraryhttp`, `internal/backuphttp`, `internal/updaterhttp`, `internal/healthhttp`, `internal/eventhttp`, and `internal/modulehttp` (`GET /api/core/v1/modules`; the kernel hands it a read-only `ModulesView` interface — catalog snapshot, `restart_required`, the `ui_build_id` source — so it imports nothing above L6). Each imports its capability package(s) and `httpapi` (and `realtime` for stream routes), sits at L6 (R4), registers on `httpapi`'s core group, and is imported **only by `kernel`**. | per capability |
| `internal/...` | internal | per contract | Implementations: `internal/pebble`, `internal/executor`, `internal/scheduler`, `internal/sse`, `internal/bleveindex`, `internal/openapi`, `internal/storagemigrate`, `internal/logjournal`, `internal/jsonschemax` (the one JSON Schema reflector, R4), … | per contract |

npm workspace `core/web/` (D6):

| Package | Purpose |
|---|---|
| `@mediacore/shell` | React shell: auth, layout/nav, settings frame, tasks/jobs, logs, users/roles, notification admin pages for the v1 scope — the notify contract, the mail and webhook channels and global routing rules — plus toasts (no inbox, bell, per-user subscriptions or personal channels: deferred, 05-notify §1), system health; the `createShell({ modules })` extension API. |
| `@mediacore/api-client` | Generated TypeScript client for **core** routes (from the core OpenAPI document). Apps generate their own client package the same way. |
| `@mediacore/ui-kit` | Shared presentational components and theme tokens (MUI-based) used by shell and apps. |

## 3.4 What is core, what is shared, what is app

**Promotion rule (non-circular).** Being listed in §3.3 is not a reason to build a package. A
package is **built** in core only if

- (a) ABO consumes it during the extraction stages of [09](09-abo-on-core.md) (S0–S10, with S2b and UI-0), or
- (b) a core package built under (a) or (b) needs it — the kernel, the composition root, the
  architecture guards and the in-tree second consumer `examples/hello` count as core. `examples/hello`
  never pulls a package forward: it uses only what the stages have built so far and grows with them
  (04 §8.1), so it cannot force `ui` or `tasks` into existence earlier than ABO needs them.

A package that meets neither keeps its contract in the spec at proof depth (D5) and is marked
**deferred** in §3.3: the plan builds none of it, and it is built, against that contract, when the
first TV/Movies app starts. Today that is `acquire`, `arrfacade`, `integrations/indexer`, the
download grab/monitor pipeline, the download adapters other than Deluge, and the AI usage ledger.
**Deferral applies to features inside built packages as well as to packages**: a built package carries
no member whose only consumer is deferred — for example the facade-only key machinery
(`KeySpec.Purpose`/`Module`, `PurposeFacade`, query-key extraction, `Router.FacadeGroup`, the facade
route kind and its stage-13 clause) lands with `arrfacade`, not in `auth`, `rbac` or `httpapi` at S2/S4.
Each 05 file lists its deferred members under a "Deferred-build" heading, and a permanent root-module
test, `archtest.DeferredMembersAbsent(t, list)`, fails from **S2** (the first stage that builds a package
with deferred members) if any listed identifier exists in `core/`; its list is taken from those headings,
and the PR that builds the deferred package (e.g. `arrfacade`) deletes its entries (09 S2). One list and one
test replace per-file grep claims, because the Build column alone cannot see inside a built package.
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
rule, and the stage-visibility gate (§3.5.1 item 4) checks it on every PR.

| Bucket | Rule | Examples (decided in 04) |
|---|---|---|
| core kernel/SDK | built under (a) or (b) above | storage, tasks, auth, rbac, httpapi, config, log, metrics |
| core, deferred | contract only; built with the first TV/Movies app | `acquire`, `arrfacade`, `integrations/indexer` |
| core `shared/` | domain logic with ≥2 real consumers across apps, or needed by a built core package | text normalization (`titleutil`, `personname`) once TV/Movies parsers need them |
| app (ABO) | everything audiobook-specific | scanner, organizer, dedup, itunes, ABS compatibility surface, narrators |
| delete/merge | dead or duplicated mechanisms | v3/v2/v1 op duplication collapsed into `tasks`; legacy `internal/plugin` |

## 3.5 Dependency direction rules and their mechanical enforcement

| # | Rule | Enforced by |
|---|---|---|
| R1 | Core never imports app code. | (a) `core/go.mod` must not `require` any app module — checked by `core/archtest.TestNoAppRequire` reading `core/go.mod`; (b) `core/archtest.TestCoreImportsOnlyCore`: `go list -deps -test` of every core package (test imports included, because the E2 hazard came from a test file; the synthetic `.test` and `[pkg.test]` packages are filtered) contains no path outside `github.com/falkcorp/mediacore/...`, stdlib, and the allowlisted third-party set; runs in core's own `go test ./...`. Note that Go *does* allow module dependency cycles, so (a) is required, not redundant. |
| R2 | Apps import only core's public packages. No stage rewires an ABO import onto `internal/` or `kernel/` (visibility rule, §3.4). | Go's `internal/` rule for `core/internal/...`; `archtest.NoKernelImports(t, "github.com/falkcorp/audiobook-organizer/...")` for `kernel/...` (called from the root module's `internal/arch`); `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` for the examples, which the path-based `internal/` rule does not cover (X5); and, for moves before they happen, the stage-visibility gate (§3.5.1 item 4), which reads the **non-test imports of every root package, including `cmd/*` and the root `main`** (R3-3). |
| R3 | A module never imports another module's packages; modules talk through core contracts (events, outbox, tasks, capability lookups). | `archtest.ModulesIsolated(t, map[moduleID]pkgPrefix)`; for ABO there is one module, so the rule is vacuous until a second exists, but the test is wired from S0 so the second module cannot land without it. |
| R4 | Core layers (a package may import only its own or lower layers): **L0** `entity`, `lifecycle`, `version`, `log`, `metrics`, `storage/keys`, `storage/codec` → **L1** `storage`, `storage/index` → **L2** `secrets` → **L3** `config` → **L4** `events`, `outbox`, `tasks`, `storage/datamigrate`, `library`, `auth`, `rbac`, `audit`, `compat`, `integrations`, `integrations/*`, `backup`, `backup/journal`, `updater`, `health` → **L5** `acquire` → **L6** `httpapi`, `realtime`, `arrfacade`, `ui`, `internal/<capability>http` (including `internal/modulehttp`) → **L7** `module` → **L8** `kernel` → **L9** `app`, `examples/*`. Deferred packages keep their layer so their contracts are checked when built. `coretest` and `archtest` are test-support packages (may import any core package; no core non-test package imports them). An `internal/*` package takes the layer of the public package it implements; an `internal/*` package that **bridges** capabilities (imports a package above the one it implements) takes the layer of its **highest import**, is never imported by a lower-layer package, and is wired by `kernel` — e.g. `internal/storagemigrate` (storage migrations run as tasks: L4), `internal/logjournal` (persisted journal over storage and tasks: L4), `internal/<capability>http` (L6). `internal/jsonschemax` is an **L0** leaf (it imports only `github.com/google/jsonschema-go`): it owns the one JSON Schema reflector wrapper, the tag pass and an extensible override table (`type Overrides map[reflect.Type]*jsonschema.Schema`, `For[T any](o Overrides)`); each package contributes the overrides for its own types at its own layer, through a function so no exported package-level map exists (R5): `jsonschemax.Base()`: `time.Time`; `config.Overrides()`: `config.Duration`, `secrets.Ref`; `httpapi`: `rbac.Permission` — so `config` (L3) never names `rbac` (L4). There is one composition, `jsonschemax.Base().With(config.Overrides()).With(<httpapi's own>)`, used by both the settings schema and OpenAPI: `httpapi` imports the static `config.Overrides()` value (L6 may import L3), never a `*config.Store` (SIMP2-3, SIMP3-1). | `core/internal/archtest_layers_test.go` — the redesigned successor of `internal/arch/layering_test.go` for core (§3.5.1). |
| R5 | No process-global mutable state **in core**. No `func init()` in core except allowlisted pure ones (none at start); no exported package-level variables of map, slice, func or pointer type; core packages never call `prometheus.DefaultRegisterer`/`MustRegister` on the default registry, `otel.SetMeterProvider`, `otel.SetTracerProvider`, `otel.SetLogger`, `otel.SetTextMapPropagator`, `otel.SetErrorHandler`, `slog.SetDefault`, `log.SetOutput`, `http.DefaultServeMux`, `http.DefaultClient`/`http.DefaultTransport` mutation, or `signal.Notify`. The single exception is `app.InstallProcessDefaults`, an opt-in helper a `main` may call; until it exists (S2), the process-global installs that ABO's metrics need stay in ABO (a thin `internal/telemetry` façade or `cmd/root.go`), and core `metrics` only returns providers (04). R5 covers the test-support packages `coretest` and `archtest` too: `coretest` captures logs through an injected handler (it hands the test a `*slog.Logger`) and never calls `slog.SetDefault`, so core tests stay parallel-safe; a helper that sets the process default for ABO code stays in ABO (MOD3-3). R5 binds core only: an app may keep façades over core handles (ABO keeps `config.AppConfig` and its logger shim as books-module values); they are per-`App` values, so several apps in one binary stay possible, and removing them is an optional ratchet, not an extraction stage. | `core/archtest.TestNoGlobals` (AST scan). Rationale: several apps in one binary, and test isolation (`raw/07-plugin-registry-sdk.md` §6, `raw/12-cross-cutting.md` §9). |
| R6 | Every route declares a permission (or explicit `rbac.Public` / `rbac.AnyAuthenticated`; `AnyAuthenticated` never admits a confined credential, 05-rbac). Routes served through `httpapi.MountLegacy` (ABO's gin engine, mounted permanently behind core's perimeter middleware) are covered by a reviewed legacy route manifest in which every route names its permission. **No API returns a chain-less route group or mount** (the G8 failure mode was a missing chain, not a missing permission): a non-Public `Router.Infra` route runs the authenticated stages like any other route (05-http-api §4.2); `MountLegacy`'s `LegacyAuthGin` mode (gin's own auth chain instead of core's stages 12–13) is accepted only for the module named in the compile-time `app.Options.LegacyAuthGinUntil`, which ABO removes in its S4 PR; and a legacy route marked `OwnAuth` is accepted only when it names a compat method registered through `Registrar.AuthMethod` (non-interactive, never `MayChangeCredentials`). | Registration-time error in `httpapi` (fail-closed) **and** `archtest.RoutesHavePermissions` over the built router and the legacy manifest — a permanent test, not a migration test; `TestOwnAuthRoutesGolden` records every `OwnAuth` route; an archtest fails if `LegacyAuthGinUntil` is still set after S4 (05-http-api §8.2). |
| R7 | Every library-scale loop declares parallelism. | `tasks.Parallelism` has no valid zero value. `Batch`/`Fixer` parallelism is a definition field validated at **registration** (a missing value fails it). `tasks.RunItems`, `tasks.RunSeq` and `tasks.ForEach` (the same loop for code that holds no `*RC`) take a required positional `tasks.Parallelism`; a zero value returns `*tasks.ItemsError` wrapping `tasks.ErrNoParallelism` before any item runs (fail-closed). `archtest.RunItemsDeclareParallelism` (AST) accepts, for that argument and for the `BatchSpec`/`FixerSpec` field: (a) a call to a `tasks` constructor (`CPU`, `Fixed(n, reason)`, `Sequential(reason)`); (b) a `tasks.Parallelism`-typed variable, parameter or field whose every assignment in the analysed packages is (a) — so passed-through values are legal; (c) a `//mediacore:required` options field whose owner calls `Parallelism.Validate` at construction. It flags zero literals and empty reasons (05-tasks §5T.3.2). Replaces `internal/plugins/runitems_concurrency_lint_test.go`. |
| R8 | Every module storage key lives in the module's keyspace or a declared legacy claim. | `storage` refuses **reads and writes** outside the handed `Keyspace` and claims (`ErrOutsideKeyspace`; DATA-17); claim modes (read-write, read-only, aging-out, retired) and the same-owner nesting rule are 05-storage §5S.3.4's; another owner's range is reached only through a `storage.Grant` its owner declares and boot logs (`GrantMode`: read, read-delete or read-write, optionally `Exclusive`; 05-storage §5S.3.11, R3-1); claims are of three kinds — prefix, exact key, and prefix-with-exceptions (sub-prefixes or exact keys carved out for another owner, e.g. the shared `idx:`, `setting:` and `preference:` families, 05-storage §5S.9.1) — resolved by longest match; boot fails on any overlap between owners (§05-storage). |

### 3.5.1 Redesign of `internal/arch/layering_test.go`

Verified assumptions that the extraction breaks (`raw/03-layering-guard.md` §2–§4):

- the **package floor** `minModulePackages = 150` (`internal/arch/layering_test.go:38`). The root has
  213 packages today. Under 04's mapping 54 packages leave and 2 new paths appear
  (`internal/books`, `internal/cfaccess`), so 213 − 54 + 2 = **161** remain: a margin of 11 that
  further moves consume (04 §8.4 note 6; `raw/03-layering-guard.md` §3's 154 predates the 2 new paths
  and the 5 packages 04 keeps in ABO, the last being `internal/logger/logtest`, MOD3-3).
  The count is re-derived whenever 04's moves change;
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
     **6**, plus one explicit allowance for ABO's module composition package `internal/books` (it
     implements `module.Module`). A core import is never counted as an in-module edge, but it is
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
     needs no new `allowed` entry. Packages with no R4 row are mapped explicitly: `coretest`, `archtest`,
     `storage/*` → **−1**; `shared/...` → **3** (domain). `archtest.NoKernelImports` is unchanged. This needs a **loader change**: `loadGraph` (`layering_test.go:339-381`) drops
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
4. **Stage-visibility gate (MOD-2, R2-5, R3-3).** The visibility rule (§3.4) is checked before a move,
   not discovered by a compile failure after it. A committed generator, `scripts/ci/check_stage_visibility.py`
   (with its mapping input committed next to it), reads `go list -deps -test -json` for the root module
   and classifies every importer from its **non-test** imports — every package, **including `cmd/*` and
   the root `main`** — and its test imports separately, so a non-test user can never be declared
   test-only (MOD3-1: `cmd/root.go:274` is a non-test user of `internal/startup`). On HEAD it evaluates
   the **next unshipped stage point** and fails on any user that 04 §8.2/§8.3 does not declare and
   rewire; each stage PR regenerates those tables from HEAD and commits the diff. It runs in root
   `make ci`, so every PR that touches a root `*.go` file is checked (the merge requirement of 09
   §9.2.1), not only PRs that touch `core/`.

## 3.6 Naming conventions

| Thing | Convention | Example |
|---|---|---|
| App ID | `[a-z][a-z0-9]{1,15}`, field `app.Options.AppID` | `abo` |
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
| Metric name | core: `mediacore_<subsystem>_*`. Module series: `<MetricPrefix>_*` while the app **ships** one module, `<MetricPrefix>_<moduleID>_*` when it ships two or more — counted over the modules compiled into the app (the `app.New` module list, enabled or not), so enabling or disabling a module never renames another module's series. `MetricPrefix` is `app.Options.MetricPrefix` (`[a-z][a-z0-9_]*`, defaults to `AppID`); ABO sets `audiobook_organizer`, so its 73 existing families keep their names (`internal/telemetry/contract/testdata/series.golden`) and need no alias. Standard OTel semantic-convention names (e.g. `http_server_*`) are kept unchanged. | `mediacore_tasks_runs_total`, `audiobook_organizer_books_total` |
| UI route | `/<moduleID>/...` for module pages; shell pages under the segments of `ui.ReservedSegments`; one-time tokens travel in the URL fragment, never in the path (05-authn §4.13) | `/books/library`, `/settings`, `/activity`, `/invite#token=…` |

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

- **One stored format per family; the level guard only for real format changes.** During the
  extraction a core service reaches an existing ABO family (users, roles, API keys, settings and the
  config blob, ops history, activity) through a **legacy-format adapter** over the existing keys, by an
  explicit storage grant that boot logs; native-only data is additive in the legacy row where the
  format tolerates it, or in a core-only family no older build reads (02 decision 13). Nothing is
  copied, mirrored, rebuilt at boot or handed over between owners, so a rollback lands on the previous
  release with no floor build in between. Opening the store runs the single per-owner level guard
  (05-storage §5S.3.7): it refuses a store stamped above the build's level. Only a real format change
  raises a level — books' own migrations, and later a per-family **cutover** to a native keyspace, which
  is outside the extraction, one-way and checkpointed, and whose floor is the cutover release itself
  (02 N15).
- **Checkpoint before any write.** The one pre-migration checkpoint of the boot is taken before the
  first write of that boot, by any subsystem (config overrides, storage bookkeeping, tasks); a boot
  that cannot take it halts before writing anything.
- **Legacy migrations before claims.** Books' existing legacy migrations run in a phase before core
  claims are enforced, under an explicit, logged legacy-migration grant, and their code is kept until
  the declared minimum-upgrade-from release. The requirement is checked on the store's applied steps,
  not on the binary being replaced: a store that has not applied what that release requires must have
  it installed first, the updater refuses to skip it, and boot refuses with a message naming it (02 N14).

```
main ──► app.New(opts, books.Module(), …)
           │  kernel: (phases P0a–P8 of 06 §6.5.1)
           │          pending-restore swap → config boot → validate manifests
           │          → open storage (per-owner level guard; storage grants logged)
           │          → pre-migration checkpoint (before any write)
           │          → books legacy migrations (before core claims are enforced)
           │          → core and module migrations (05-storage cross-keyspace DAG)
           │          → build the typed services struct (explicit constructors, no init())
           │          → register tasks/schedules
           │          → mount httpapi: core routes (internal/<capability>http), module routes,
           │            legacy mount (httpapi.MountLegacy, permanent for ABO's gin engine),
           │            arrfacade (deferred) → ui.Handler (only for an app that ships a shell
           │            build; until then the legacy mount owns `/`)
           ▼
         App.Run(ctx): start everything in the single start order of 06 §6.5.2,
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
