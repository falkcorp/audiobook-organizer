<!-- file: docs/specs/platform-core/03-architecture.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8af6d935-7143-4d06-8562-b04a18504d81 -->
<!-- last-edited: 2026-11-04 -->

# 03 — Architecture

This section is **normative**: every other spec file uses the names, paths and rules defined
here. If another file disagrees with this one, this one wins until it is amended.

Evidence commit: `748fd787a`. Raw data: [`docs/plans/platform-core/raw/`](../../plans/platform-core/raw/00-index.md).

## 3.1 Three layers

| Layer | What it is | Where it lives (in-repo phase) | Where it lives (after split) |
|---|---|---|---|
| **Core** | Platform services every app needs: storage, tasks, events, config, secrets, logging, metrics, HTTP framework, realtime, authN, RBAC, audit, library roots, integration contracts and adapters, backup, updater, health, UI shell. | Nested Go module at `core/`, module path **`github.com/falkcorp/mediacore`**, plus npm workspace `core/web/` | Its own repo `falkcorp/mediacore` |
| **SDK** | The *public, versioned subset* of core that an app is allowed to import: `module`, the capability contracts, `tasks`, `storage`, `httpapi`, `rbac`, `config`, `events`, `log`, `metrics`, `coretest`, `archtest`, and the npm packages `@mediacore/shell` and `@mediacore/api-client`. The SDK is not a separate module; it is the set of core packages **outside** `core/internal/` and outside `core/kernel/`, and is what the API-stability gate (§12) measures. | same module | same module |
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
./go.mod (root):    require github.com/falkcorp/mediacore v0.0.0-00010101000000-000000000000
                    replace github.com/falkcorp/mediacore => ./core
```

Why this mechanism, and why this path:

1. **The build never depends on `go.work`.** Re-tested at HEAD (`raw/00-baseline-build.md` E1):
   `go work init .` fails with `ambiguous import` exactly as `CLAUDE.md` says. Root cause (E7): the
   single stale requirement `github.com/cockroachdb/errors@v1.14.0` → monolithic
   `google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1`, which module-graph pruning hides
   in single-module mode and workspace mode loads. v1.14.0 is the latest release, so there is no
   upstream fix. One line in the root `go.mod`,
   `exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1`, makes workspace mode
   build and vet green with no `go.sum` change (E8, E9, E10). Therefore: `require` + `replace` is the
   mechanism (it works with no workspace, E2c); the `exclude` line lands in wave 0 so that a
   developer-local, **uncommitted** `go.work` (`use . ./core`) is safe for gopls; `go.work` stays
   gitignored and no Makefile target or CI job uses it. The `exclude` is re-checked (and deleted)
   whenever `cockroachdb/errors` is bumped.
2. **The future path from day one.** Because every import already says `github.com/falkcorp/mediacore/...`,
   the repo split (§12) is a directory move plus deleting one `replace` line — no import rewrite in
   any app. The alternative (`github.com/falkcorp/audiobook-organizer/core`) would force a codemod
   across every app at split time and a second module path in history.
3. **Go's `internal/` rule does the first half of the enforcement for free.** Packages under
   `core/internal/...` have import path `github.com/falkcorp/mediacore/internal/...`, which is
   importable only from `github.com/falkcorp/mediacore/...`; the ABO root module cannot import them.
4. **Cost accepted:** `go build ./...` / `go vet ./...` / `go test ./...` from the repo root do
   **not** descend into `core/` (a nested `go.mod` ends the `./...` pattern). Every Makefile target
   and CI job that runs Go tooling runs it **twice** — once in `.` and once in `core/` — via a
   single Makefile variable `GO_MODULE_DIRS := . core`. This is a plan task, not an afterthought
   (implementation plan wave 0).
5. **Cost accepted:** `go install github.com/falkcorp/audiobook-organizer@...` from outside the
   repo stops working while the `replace` exists (Go refuses `replace` in a non-main module context
   for `go install pkg@version`). Nothing in the repo's release path uses that form
   (`raw/14-build-ci.md`: releases are built by goreleaser and the Docker builders from a checkout);
   this is listed in risks (§14).

### 3.2.1 Experiment results (control and mechanism)

Summarized from `raw/00-baseline-build.md`; that file has exact commands and outputs.

| Experiment | Toolchain | `go build ./...` | `go vet ./...` |
|---|---|---|---|
| E0 control, HEAD unmodified | go1.27.0 (auto), go1.27.2 (pin), go1.27.3 (latest) | 0 / 0 / 0 | 0 / 0 / 0 |
| E1 `go work init .` at HEAD | go1.27.2 | **1** (`ambiguous import`, genproto) | — |
| E8 HEAD + root `exclude` of monolithic genproto | go1.27.2 | 0 | 0 |
| E9 E8 + `go work init .` | go1.27.2 | 0 | 0 |
| E2c nested `core/` (`internal/keyedmutex`, `internal/telemetry` minus `contract/`) + `require`/`replace` — in `core/` | go1.27.2 | 0 | 0 |
| E2c — in root | go1.27.2 | 0 | 0 (`-mod=readonly` build also 0) |
| E10 E2c + exclude + `go.work use . ./core` | go1.27.2 | 0 | 0 |
| E4 E2 + unmodified `internal/arch` layering test | go1.27.2 | test **fails**: stale `layerOf` entries (predicted, §3.5.1) | — |

Hazards the experiments exposed, each turned into a rule or plan task: core `go mod tidy` silently
adds the app module from the proxy if any core file — even a test — imports app code (R1(a));
repo-walking ratchet tests break when moved (they stay in the root); `./...` from the root does not
reach `core/` (item 4 above); the layering guard must be redesigned before the first move.

## 3.3 Core package tree

All paths are relative to `core/` (import prefix `github.com/falkcorp/mediacore/`). **Public**
packages are the SDK. `kernel/` holds the composition root's machinery that apps never touch
directly; `internal/` holds implementations.

| Package | Visibility | Purpose | Spec |
|---|---|---|---|
| `app` | public | Composition root. `app.New(ctx, app.Options, ...module.Module) (*app.App, error)`; `(*App).Run(ctx) error`. The **only** way to assemble a process. | [06-app-sdk](06-app-sdk.md) |
| `module` | public | `Module` interface, `Manifest`, `Registrar` (routes, tasks, migrations, permissions, settings, events, UI, adapters), `Deps` handed to a module. | [06-app-sdk](06-app-sdk.md) |
| `storage` | public | Pebble-backed ordered KV: `DB`, `Keyspace`, `Batch`, `Snapshot`, `iter.Seq2` scans, per-keyspace migrations, legacy-prefix claims. | [05-storage](05-storage.md) |
| `storage/index` | public | Full-text search index contract (bleve implementation in `internal/bleveindex`). | [05-storage](05-storage.md) |
| `library` | public | Media roots, `os.Root`-confined filesystem access, path mapping, safe move/copy/hardlink, hashing, quarantine, free-space checks. | [05-library](05-library.md) |
| `tasks` | public | Durable task/job system: `Definition`, `Run`, `RunItems`, `Parallelism` (required), `Schedule`, `Resource` (namespaced write-sets), checkpoints, preview/dry-run, cancellation. | [05-tasks](05-tasks.md) |
| `events` | public | Notify-only in-process bus with typed, namespaced topics. | [05-events](05-events.md) |
| `outbox` | public | Durable, at-least-once, same-batch outbox for correctness-critical cross-module work. | [05-events](05-events.md) |
| `log` | public | `log/slog` construction, per-module child loggers, redaction. | [05-logging](05-logging.md) |
| `metrics` | public | Per-`App` Prometheus registry and OTel providers (never the process globals). | [05-metrics](05-metrics.md) |
| `config` | public | Layered config (defaults → file → env → flags → DB overrides), per-module typed sections, schema for the UI, change notification. | [05-config](05-config.md) |
| `secrets` | public | Encrypted-at-rest secret store; config fields reference secrets by name. | [05-config](05-config.md) |
| `httpapi` | public | `net/http` (Go 1.22+ `ServeMux` patterns) API framework: typed routes, mandatory permission per route, error envelope, pagination, versioning, OpenAPI 3.1 generation. | [05-http-api](05-http-api.md) |
| `realtime` | public | SSE hub with resumable, permission-filtered streams. | [05-realtime](05-realtime.md) |
| `auth` | public | Identity: users, sessions, API keys, bootstrap, OIDC/OAuth, credential epochs; middleware. | [05-authn](05-authn.md) |
| `rbac` | public | Permission scopes (`<ns>:<action>`), roles as data, module-declared permissions, enforcement helpers. | [05-rbac](05-rbac.md) |
| `audit` | public | Append-only audit log (auth events, permission denials, admin actions, task approvals). | [05-rbac](05-rbac.md) |
| `integrations/ai` | public | Provider-neutral AI contract (text/structured generation, embeddings, transcription) plus routing/health. | [05-ai](05-ai.md) |
| `integrations/download` | public | Download-client contract (`Client`, `GrabRequest`, `Item` status) + adapters `download/deluge`, `download/qbittorrent`, `download/transmission`, `download/sabnzbd`, `download/nzbget`. | [05-download](05-download.md) |
| `integrations/indexer` | public | Indexer/search contract + Newznab/Torznab adapter. | [05-indexer](05-indexer.md) |
| `integrations/metadata` | public | Generic metadata-provider contract `Provider[Q, R any]` + resilience wiring. | [05-metadata](05-metadata.md) |
| `integrations/notify` | public | Notification contract + adapters (mail, webhook). | [05-notify](05-notify.md) |
| `integrations/providerhttp` | public | Outbound HTTP for every adapter: per-provider throttle, daily quota, circuit breaker, retry, user agent — **instance-scoped**, not package globals. | [05-metadata](05-metadata.md) |
| `acquire` | public, later | Media-neutral acquisition engine (quality definitions, profiles, custom formats, decision engine, tracked downloads, history/blocklist/queue). Built when the first TV/Movies app starts; designed in [10-tv-movies-proof](10-tv-movies-proof.md). | [10](10-tv-movies-proof.md) |
| `backup` | public | Consistent backup/restore of every core-owned store plus module-declared artifacts. | [05-backup](05-backup.md) |
| `updater` | public | Self-update check/apply, parameterized by the app's release source. | [05-updater](05-updater.md) |
| `health` | public | Liveness/readiness, component health, diagnostics bundle. | [05-health](05-health.md) |
| `arrfacade` | public | Extension point for an *arr-v3-compatible API adapter (D2). | [08-arr-facade](08-arr-facade.md) |
| `ui` | public | Go side of the UI shell: serves an app's embedded SPA build with the core's headers/CSP/caching. | [07-ui-shell](07-ui-shell.md) |
| `coretest` | public | Test kit: in-memory `App` harness, fake clock, fake adapters, permission assertions. | [06-app-sdk](06-app-sdk.md) |
| `archtest` | public | Reusable architecture guards an app runs in its own tests (§3.5). | §3.5 |
| `shared/...` | public | Cross-app *domain* packages that are not platform concerns but are used by ≥2 apps (§3.4). | [04-package-mapping](04-package-mapping.md) |
| `kernel/...` | public-but-unstable | Module catalog, lifecycle supervisor, service container. Imported only by `app`. Excluded from the API-stability gate; apps that import it fail `archtest`. | [06-app-sdk](06-app-sdk.md) |
| `internal/...` | internal | Implementations: `internal/pebble`, `internal/executor`, `internal/scheduler`, `internal/sse`, `internal/bleveindex`, `internal/openapi`, … | per contract |

npm workspace `core/web/` (D6):

| Package | Purpose |
|---|---|
| `@mediacore/shell` | React shell: auth, layout/nav, settings frame, tasks/jobs, logs, users/roles, notifications, system health; the `createShell({ modules })` extension API. |
| `@mediacore/api-client` | Generated TypeScript client for **core** routes (from the core OpenAPI document). Apps generate their own client package the same way. |
| `@mediacore/ui-kit` | Shared presentational components and theme tokens (MUI-based) used by shell and apps. |

## 3.4 What is core, what is shared, what is app

Promotion rule (from the 2026-09-18 adversarial review's residual risk "small core becoming a god
package", `raw/08-prior-art.md` §2): **a package enters core only if it is a platform concern
(the list in §3.3) or if it has two real consumers.** Until a second app exists, cross-app domain
helpers stay in ABO unless they are needed by core itself. The package mapping
([04](04-package-mapping.md)) applies this rule to all 213 packages.

| Bucket | Rule | Examples (decided in 04) |
|---|---|---|
| core kernel/SDK | platform concern | storage, tasks, auth, rbac, httpapi, config, log, metrics |
| core `shared/` | domain logic with ≥2 consumers across apps, or needed by a core contract | text normalization (`titleutil`, `personname`) once TV/Movies parsers need them |
| app (ABO) | everything audiobook-specific | scanner, organizer, dedup, itunes, ABS compatibility surface, narrators |
| delete/merge | dead or duplicated mechanisms | v3/v2/v1 op duplication collapsed into `tasks`; legacy `internal/plugin` |

## 3.5 Dependency direction rules and their mechanical enforcement

| # | Rule | Enforced by |
|---|---|---|
| R1 | Core never imports app code. | (a) `core/go.mod` must not `require` any app module — checked by `core/archtest.TestNoAppRequire` reading `core/go.mod`; (b) `core/archtest.TestCoreImportsOnlyCore`: `go list -deps` of every core package contains no path outside `github.com/falkcorp/mediacore/...`, stdlib, and the allowlisted third-party set; runs in core's own `go test ./...`. Note that Go *does* allow module dependency cycles, so (a) is required, not redundant. |
| R2 | Apps import only core's public packages. | Go's `internal/` rule for `core/internal/...`; `archtest.NoKernelImports(t, "github.com/falkcorp/audiobook-organizer/...")` for `kernel/...` (called from the root module's `internal/arch`). |
| R3 | A module never imports another module's packages; modules talk through core contracts (events, outbox, tasks, capability lookups). | `archtest.ModulesIsolated(t, map[moduleID]pkgPrefix)`; for ABO there is one module, so the rule is vacuous until a second exists, but the test is wired from wave 0 so the second module cannot land without it. |
| R4 | Core layers: `log`,`metrics` → `config`,`secrets`,`storage` → `events`,`outbox`,`tasks`,`library`,`auth`,`rbac`,`audit`,`integrations/*`,`backup`,`updater`,`health` → `httpapi`,`realtime`,`arrfacade`,`ui` → `module` → `kernel` → `app`. A package may import only its own or lower layers. | `core/internal/archtest_layers_test.go` — the redesigned successor of `internal/arch/layering_test.go` for core (§3.5.1). |
| R5 | No process-global mutable registries in core. No `func init()` in core except allowlisted pure ones (e.g. none at start); no exported package-level variables of map, slice, func or pointer type; no use of `prometheus.DefaultRegisterer`, `otel.SetMeterProvider`/`SetTracerProvider`, `slog.SetDefault`, `http.DefaultServeMux`, `http.DefaultClient` inside core packages. | `core/archtest.TestNoGlobals` (AST scan). Rationale: several apps in one binary (D-lowest), and test isolation (`raw/07-plugin-registry-sdk.md` §6, `raw/12-cross-cutting.md` §9). |
| R6 | Every route declares a permission (or explicit `rbac.Public` / `rbac.AnyAuthenticated`). | Registration-time error in `httpapi` (fail-closed) **and** `archtest.RoutesHavePermissions` over the built router. |
| R7 | Every library-scale `RunItems` declares parallelism. | The `tasks.Parallelism` type has no usable zero value (§05-tasks); construction fails at registration. Replaces the repo-level lint `internal/plugins/runitems_concurrency_lint_test.go`. |
| R8 | Every module storage key lives in the module's keyspace or a declared legacy claim. | `storage` refuses writes outside the handed `Keyspace`; boot fails on overlapping claims (§05-storage). |

### 3.5.1 Redesign of `internal/arch/layering_test.go`

Verified assumptions that the extraction breaks (`raw/03-layering-guard.md` §2–§4):
the `>= 150` package floor (213 today; it fails once 64 packages leave the root module), the
`internal/server -> internal/database` positive-control edge (both packages are split by the
extraction), `layerOf` completeness keyed on root-module paths, and the `allowed` shrink-only map
(5 of its 12 edges are `config` reaching up).

The guard is **redesigned, not deleted**:

1. **Root module (ABO) guard** keeps its rule and its `layerOf` map, but:
   - the loader-health floor becomes a *ratio* check: `len(packages) >= 0.8 * len(layerOf)`
     instead of a literal 150, so moving packages out cannot trip it while a broken loader still does;
   - the positive control becomes "the root `main` package (key `.`) imports `internal/server`" —
     an edge that exists before, during and after the extraction (fallback chosen in the plan if it
     ever moves: the composition root `cmd/...` → `github.com/falkcorp/mediacore/app`);
   - imports of `github.com/falkcorp/mediacore/...` are classified as **layer −1 (external
     platform)**: legal from every ABO layer, never counted as an in-module edge;
   - a new assertion: no root-module package's computed layer is below the layer of an ABO package it
     re-exports to core through a callback (prevents "pass a func up" from being used to smuggle
     audiobook types into core contracts — checked by R1 on the core side anyway).
2. **Core guard** (`core/internal/archtest_layers_test.go`) is a new instance of the same algorithm
   with the R4 layer table, its own `layerOf`, its own ratio floor, and its own positive control
   (`app` imports `kernel`).
3. **Shared engine**: the classification/compute code moves to `core/archtest` as a library
   (`archtest.Layering(t, archtest.LayerSpec{...})`) so both modules — and every future app repo —
   run the same algorithm. This is what "reusable guard" in R2/R3 means.

## 3.6 Naming conventions

| Thing | Convention | Example |
|---|---|---|
| Module ID | lowercase `[a-z][a-z0-9]{1,15}` | `books`, `tv`, `movies` |
| Core namespace | the reserved module ID `core` | `core:admin` |
| Permission | `<moduleID>:<action>[.<sub>]` | `books:edit`, `tv:request`, `core:users.manage` |
| Task kind | `<moduleID>.<name>` | `books.scan`, `core.backup` |
| Event topic | `<moduleID>.<noun>.<verb-past>` with a version suffix | `books.book.updated.v1` |
| Storage keyspace | `m/<moduleID>/` for modules, `c/<subsystem>/` for core | `m/tv/series/…`, `c/tasks/run/…` |
| Native API path | `/api/core/v1/...`, `/api/<moduleID>/v1/...` | `/api/books/v1/books/{id}` |
| Config section | `core.*`, `modules.<moduleID>.*` | `modules.books.scan.interval` |
| Metric name | `mediacore_<subsystem>_*`, `<app>_<moduleID>_*` | `mediacore_tasks_runs_total` |
| UI route | `/<moduleID>/...` for module pages; `/settings`, `/system/...`, `/users` for shell pages | `/books/library` |

ABO's existing un-namespaced names (13 permission atoms, 258 key families, `/api/v1/...` paths,
flat config keys, `/api/v3/ops`) are mapped onto these conventions with explicit compatibility
windows in [09-abo-on-core](09-abo-on-core.md).

## 3.7 Process shape

One `main` per app calls `app.New` with the modules it ships and the adapters it enables, then
`Run`. The kernel validates the module graph (duplicate IDs, permission/route/task-kind/keyspace
collisions, migration order, declared dependencies) **before** opening listeners, runs core then
module migrations, constructs services, starts the task executor and scheduler, mounts routes, and
serves. Module enablement is **restart-bound** (kept from old decision 10): a disabled module
contributes no route, task, schedule, worker, nav entry or filesystem mutation, and its data is
retained.

```
main ──► app.New(opts, books.Module(), …)
           │  kernel: validate manifests → open storage → core migrations → module migrations
           │          → build services (container, explicit, no init()) → register tasks/schedules
           │          → mount httpapi (core routes, module routes, optional arrfacade) → ui.Handler
           ▼
         App.Run(ctx): start executor, scheduler, outbox relay, realtime hub, listeners
                       shutdown in reverse order on ctx cancel (bounded drain, §05-tasks)
```
