<!-- file: docs/specs/platform-core/03-architecture.md -->
<!-- version: 1.3.0 -->
<!-- guid: 8af6d935-7143-4d06-8562-b04a18504d81 -->
<!-- last-edited: 2026-11-04 -->

# 03 — Architecture

This section is **normative**: every other spec file uses the names, paths and rules defined
here. If another file disagrees with this one, this one wins until it is amended.

Evidence commit: `748fd787a`. Raw data: [`docs/plans/platform-core/raw/`](../../plans/platform-core/raw/00-index.md).
Round-1 review experiments X1–X8 are in
[`reviews/spec-round-1-modules-build.md`](../../plans/platform-core/reviews/spec-round-1-modules-build.md)
("Experiments run for this review"); they were run in scratch directories, never in the repo.

## 3.1 Three layers

| Layer | What it is | Where it lives (in-repo phase) | Where it lives (after split) |
|---|---|---|---|
| **Core** | Platform services every app needs: storage, tasks, events, config, secrets, logging, metrics, HTTP framework, realtime, authN, RBAC, audit, library roots, integration contracts and adapters, backup, updater, health, UI shell. | Nested Go module at `core/`, module path **`github.com/falkcorp/mediacore`**, plus npm workspace `core/web/` | Its own repo `falkcorp/mediacore` |
| **SDK** | The *public, versioned subset* of core that an app is allowed to import: `module`, the capability contracts, `tasks`, `storage`, `httpapi`, `rbac`, `config`, `events`, `log`, `metrics`, `coretest`, `archtest`, `entity`, `lifecycle`, `version`, `integrations/*`, `compat`, and the npm packages `@mediacore/shell`, `@mediacore/api-client` and `@mediacore/ui-kit`. The SDK is not a separate module; it is the set of core packages **outside** `core/internal/` and outside `core/kernel/`, and is what the API-stability gate (§12) measures. Packages marked **deferred** in §3.3 are SDK paths whose contract is specified but not yet built. | same module | same module |
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
   lands in **`core/go.mod`** in wave 0 and in no other `go.mod`, so the root — and every future app
   repo — keeps a `go.mod` that `go install …@version` accepts after the split (item 5). The
   supported developer workspace is **`go work init . ./core`**, uncommitted (`go.work` and
   `go.work.sum` are gitignored, `.gitignore:36-37`); a workspace that uses the root without
   `./core` is unsupported (it reproduces E1). No Makefile target or CI job builds through a
   `go.work`, except one throwaway step (below) that proves the supported workspace stays green.
   Two checks keep the exclude honest instead of relying on someone remembering it:
   `scripts/ci/check_genproto_exclude.py` (run in `core/`) fails if `go mod graph` lists
   `google.golang.org/genproto@` and the exclude is missing, **or** lists none while the exclude is
   still present (stale); and a CI step copies the checkout to a temp directory and runs
   `go work init . ./core && go build ./... ./core/...` there.
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
   genuinely per-module (build, vet, test, staticcheck, `fmt-check`, `go fix -diff`). It does **not**
   mean "run everything twice": each of the 13 `make ci` prerequisites (`Makefile:785`) and every CI
   job is classified as per-module, root-only or core-only, with per-module baseline files
   (`core/.errcheck-baseline`, `core/.interface-width-baseline`, `core/scripts/deadcode-baseline.txt`),
   a core coverage gate (12 S5), a core mockery config, and core gates registered in `ci/gates.txt`
   with a `core-job` value. The classification table and the per-wave checklist (shard weight files,
   `.mockery.yaml`, `tygo.yaml`, `ci/gates.txt` rows such as `store-width`/`store-references`) are
   owned by 09 S0. Build entry points outside Make must also learn about `core/`:
   - **Docker.** `Dockerfile:54-55` and `Dockerfile.build-cgo:52-53` copy only `go.mod go.sum` and run
     `go mod download && go mod verify`; with the `replace` and no `core/go.mod` in that layer both
     exit 1 (X1). Both Dockerfiles copy `core/go.mod core/go.sum` into `./core/` before
     `go mod download`; `.dockerignore` (today only `node_modules`, `web/node_modules`, `web/dist`,
     `.dockerignore:18-20`) adds `**/node_modules` and `core/web/packages/*/dist`; a PR job builds the
     image. These land in the same wave as the `replace` (09 S0).
   - **Web.** Once `web/` depends on `@mediacore/*` (whose `exports` point at built `dist/`), every
     `cd web && npm ci && npm run build` that does not first build `core/web` breaks. One
     implementation, `scripts/ci/build_web.sh` (core web build, then `web/`), is called by
     `make web-deps`, by every workflow and by both Dockerfiles; path filters gain `core/web/**`,
     `core/go.mod`, `core/go.sum`; Dependabot npm gains `/core/web`. The full list of entry points is
     owned by 09 S0 and 07 §7.3.2.
   - **Core's own Makefile.** `core/Makefile` (12 §12.3.11) carries core's `ci` target so that core
     CI runs from an export containing only `core/` (12 S4); its toolchain pin is checked by the
     toolchain guard (13 §13.2.2).
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
   (12 S4(c)).

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

E2c's package choice tested the mechanism, not a placement: [04](04-package-mapping.md) is
authoritative for where each package goes (it keeps `internal/keyedmutex` in ABO; the
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
extraction stages of [09](09-abo-on-core.md) (S0–S11); **core** = core itself needs it;
**deferred** = the contract is specified here and in its spec file at proof depth (D5), the plan
builds none of it, and it is built when the first TV/Movies app starts.

| Package | Visibility | Build | Purpose | Spec |
|---|---|---|---|---|
| `app` | public | ABO | Composition root. `app.New(ctx, app.Options, ...module.Module) (*app.App, error)`; `(*App).Run(ctx) error`. The **only** way to assemble a process. | [06-app-sdk](06-app-sdk.md) |
| `module` | public | ABO | `Module` interface, `Manifest`, `Registrar` (routes, tasks, migrations, permissions, settings, events, UI, adapters), `Deps` handed to a module; `module.ReservedIDs()` (§3.6). | [06-app-sdk](06-app-sdk.md) |
| `entity` | public | core | Leaf package (L0): `entity.Ref{Module, Kind, ID string}` — **the one** polymorphic reference to any module-owned entity. Every file uses this type: tasks resources (`tasks.Resource` is `entity.Ref`), outbox/notify payloads, download grabs (multi-entity grabs carry `[]entity.Ref`), acquisition history/blocklist/queue, audit targets. No other `EntityRef`/`Resource{Module, Kind, ID}` struct exists; an `archtest` check fails any core identifier named `EntityRef` outside `entity`. Never a shared media row (decision 6). | [10](10-tv-movies-proof.md), [05-tasks](05-tasks.md) |
| `lifecycle` | public | core | Leaf package (L0): `Component{Start, Stop}` ordering contract and shutdown budget types used by kernel, tasks, realtime, outbox relay. | [05-tasks](05-tasks.md), [05-health](05-health.md) |
| `version` | public | core | Leaf package (L0): `const Version = "0.Y.Z"`, the core version. **Never** derived from `debug.ReadBuildInfo` (under the `replace` that reports the zero pseudo-version, which is below every `MinCoreVersion`). Read by the kernel's `MinCoreVersion` check, `GET /api/core/v1/modules` and the backup manifest; CI keeps it equal to the newest checkpoint or release tag (12 §12.4.3). | [12](12-repo-split.md) |
| `storage` | public | ABO | Pebble-backed ordered KV: `DB`, `Keyspace`, `Batch`, `Snapshot`, `iter.Seq2` scans, per-keyspace migrations, legacy-prefix claims. | [05-storage](05-storage.md) |
| `storage/index` | public | ABO | Full-text search index contract (bleve implementation in `internal/bleveindex`). Depends on `storage`, never the reverse; index wiring lives in `kernel`. | [05-storage](05-storage.md) |
| `library` | public | ABO | Media roots, `os.Root`-confined filesystem access, path mapping, safe move/copy/hardlink, hashing, quarantine, free-space checks. | [05-library](05-library.md) |
| `tasks` | public | ABO | Durable task/job system: `Definition`, `Run`, `RunItems`, `Parallelism` (required), `Schedule` (the one schedule mechanism), `Resource` (= `entity.Ref`, namespaced write-sets), checkpoints, preview/dry-run, cancellation. | [05-tasks](05-tasks.md) |
| `events` | public | ABO | Notify-only in-process bus with typed, namespaced topics; the one topic catalog. | [05-events](05-events.md) |
| `outbox` | public | ABO | Durable, at-least-once, same-batch outbox for correctness-critical cross-module work. | [05-events](05-events.md) |
| `log` | public | ABO | `log/slog` construction, per-module child loggers, redaction, and the `Record`/`Tap` types of the **system log journal** (persisted, queryable operational log; implementation `internal/logjournal`; the books activity feed stays in the books module). | [05-logging](05-logging.md) |
| `metrics` | public | ABO | Per-`App` Prometheus registry and OTel providers (never the process globals). | [05-metrics](05-metrics.md) |
| `config` | public | ABO | Layered config: defaults → file → env → flags (operator-set values **lock** their key) → DB overrides (one row per key under `c/config/`, apply only to unlocked keys); per-module typed sections declared in the manifest; JSON Schema for the UI; change notification. Holds no integration instances (those are `integrations` records). | [05-config](05-config.md) |
| `secrets` | public | ABO | Encrypted-at-rest secret store with one keyring; config fields reference secrets by name. | [05-config](05-config.md) |
| `httpapi` | public | ABO | `net/http` (Go 1.22+ `ServeMux` patterns) API framework: typed routes, mandatory permission per route, error envelope, pagination, versioning, OpenAPI 3.1 generation, core perimeter middleware, and `MountLegacy` — a **permanent, supported** way to mount an app's existing router (ABO's gin engine) behind core's perimeter. | [05-http-api](05-http-api.md) |
| `realtime` | public | ABO | SSE hub with resumable, permission-filtered streams. | [05-realtime](05-realtime.md) |
| `auth` | public | ABO | Identity: users, sessions, API keys, bootstrap, OIDC/OAuth, credential epochs; middleware. | [05-authn](05-authn.md) |
| `rbac` | public | ABO | Permission scopes (`<ns>:<action>`), roles as data, module-declared permissions, enforcement helpers. | [05-rbac](05-rbac.md) |
| `audit` | public | ABO | Append-only audit log (auth events, permission denials, admin actions, task approvals); targets are `entity.Ref`. | [05-rbac](05-rbac.md) |
| `compat` | public | ABO | Compatibility primitives for strangler migrations: alias tables (paths, config keys, env vars, permission atoms, metric names) registered against one compatibility window (09 §9.2.1), Deprecation/Sunset headers, `mediacore_compat_legacy_use_total`; the store-level compatibility guard is the merged storage-format/compat-level mechanism owned by 05-storage. | [09-abo-on-core](09-abo-on-core.md) |
| `integrations` | public | ABO | Root of the integration contracts and **the one store for integration instances**: `integrations.Ref{Kind, Instance}`, instance records (kind, instance, settings, `secrets.Ref`s), the shared error taxonomy (retryable/permanent/auth/rate-limited), `Factory`/`Descriptor` types modules use to contribute adapters (`Registrar.Adapter`). Imports nothing in `module` (R4). | [05-metadata](05-metadata.md) |
| `integrations/ai` | public | ABO | Provider-neutral AI contract (text/structured generation, embeddings, transcription) plus routing/health. v1 records usage metrics only; the usage ledger and monthly cap are deferred. | [05-ai](05-ai.md) |
| `integrations/download` | public | ABO (contract, `download/deluge`, what ABO uses today); deferred (other adapters; grab/monitor/pending-recovery/`MarkImported`) | Download-client contract (`Client`, `GrabRequest`, `Item` status) + adapters `download/deluge`, `download/qbittorrent`, `download/transmission`, `download/sabnzbd`, `download/nzbget`. Owns client polling, item state and completion events; the grab pipeline belongs to `acquire`. | [05-download](05-download.md) |
| `integrations/indexer` | public | **deferred** | Indexer/search contract + Newznab/Torznab adapter. | [05-indexer](05-indexer.md) |
| `integrations/metadata` | public | ABO | Generic metadata-provider contract `Provider[Q, R any]` + resilience wiring. | [05-metadata](05-metadata.md) |
| `integrations/notify` | public | ABO | Notification contract + adapters (mail, webhook) with global routing. Inbox, per-user subscriptions, personal channels and templating are deferred. | [05-notify](05-notify.md) |
| `integrations/providerhttp` | public | ABO | Outbound HTTP for every adapter: per-provider throttle, daily quota, circuit breaker, retry, user agent — **instance-scoped**, not package globals. | [05-metadata](05-metadata.md) |
| `acquire` | public | **deferred** | Media-neutral acquisition engine (quality definitions, profiles, custom formats, decision engine, grab decisions, tracked-download records, import hand-off, history/blocklist/queue); consumes `integrations/download` events, never polls clients itself. | [10](10-tv-movies-proof.md) |
| `backup` | public | ABO | Consistent backup/restore of every core-owned store plus module-declared artifacts; one archive format. | [05-backup](05-backup.md) |
| `updater` | public | ABO | Self-update check/apply, parameterized by the app's release source. | [05-updater](05-updater.md) |
| `health` | public | ABO | Liveness/readiness, component health, diagnostics bundle. | [05-health](05-health.md) |
| `arrfacade` | public | **deferred** | Extension point for an *arr-v3-compatible API adapter (D2). During the extraction only the `/api/v3/` path reservation exists (05-http-api §4.1). | [08-arr-facade](08-arr-facade.md) |
| `ui` | public | ABO (from the stage in which ABO adopts the shell build) | Go side of the UI shell: serves an app's embedded **shell** SPA build with the core's headers/CSP/caching; `ui.ReservedSegments` (§3.6). Until an app ships a shell build, `ui.Handler` is not mounted and the legacy mount owns `/`. | [07-ui-shell](07-ui-shell.md) |
| `coretest` | public | core | Test kit: in-memory `App` harness, fake clock, fake adapters, permission assertions. | [06-app-sdk](06-app-sdk.md) |
| `archtest` | public | core | Reusable architecture guards an app runs in its own tests (§3.5). | §3.5 |
| `shared/...` | public | none yet | Cross-app *domain* packages that are not platform concerns but are used by ≥2 apps (§3.4). Zero promoted (04 §9). | [04-package-mapping](04-package-mapping.md) |
| `kernel/...` | public-but-unstable | core | Module catalog, manifest validation, lifecycle supervisor, and the **typed services struct** that builds core services in a fixed order with explicit constructors. There is **no generic DI container** (no `Get[T]`-of-`any`). Imported only by `app`. Excluded from the API-stability gate; apps that import it fail `archtest`. | [06-app-sdk](06-app-sdk.md) |
| `examples/hello` | public (example) | core | The minimal "hello" app from 06 §hello: a **package in the core module** (not a separate module), built and tested in core CI and importable by ABO's G6 test; the in-tree second consumer for the split criteria (12 S3). | [06-app-sdk](06-app-sdk.md), [12](12-repo-split.md) |
| `internal/<capability>http` | internal | ABO (`internal/acquirehttp`: deferred) | **Core capability HTTP routes** (`/api/core/v1/...`): one package per capability — e.g. `internal/taskhttp`, `internal/authhttp`, `internal/rbachttp`, `internal/audithttp`, `internal/confighttp`, `internal/libraryhttp`, `internal/backuphttp`, `internal/updaterhttp`, `internal/healthhttp`, `internal/eventhttp`. Each imports its capability package(s) and `httpapi` (and `realtime` for stream routes), sits at L6 (R4), registers on `httpapi`'s core group, and is imported **only by `kernel`**. | per capability |
| `internal/...` | internal | per contract | Implementations: `internal/pebble`, `internal/executor`, `internal/scheduler`, `internal/sse`, `internal/bleveindex`, `internal/openapi`, `internal/storagemigrate`, `internal/logjournal`, … | per contract |

npm workspace `core/web/` (D6):

| Package | Purpose |
|---|---|
| `@mediacore/shell` | React shell: auth, layout/nav, settings frame, tasks/jobs, logs, users/roles, notifications, system health; the `createShell({ modules })` extension API. |
| `@mediacore/api-client` | Generated TypeScript client for **core** routes (from the core OpenAPI document). Apps generate their own client package the same way. |
| `@mediacore/ui-kit` | Shared presentational components and theme tokens (MUI-based) used by shell and apps. |

## 3.4 What is core, what is shared, what is app

**Promotion rule (non-circular).** Being listed in §3.3 is not a reason to build a package. A
package is **built** in core only if

- (a) ABO consumes it during the extraction stages of [09](09-abo-on-core.md) (S0–S11), or
- (b) a core package built under (a) or (b) needs it — the kernel, the composition root, the
  architecture guards and the in-tree second consumer `examples/hello` count as core.

A package that meets neither keeps its contract in the spec at proof depth (D5) and is marked
**deferred** in §3.3: the plan builds none of it, and it is built, against that contract, when the
first TV/Movies app starts. Today that is `acquire`, `arrfacade`, `integrations/indexer`, the
download grab/monitor pipeline, the download adapters other than Deluge, and the AI usage ledger.
Cross-app domain helpers enter `shared/` only when they have **two real consumers**; until a second
app exists they stay in ABO unless a built core package needs them. The guard against the 2026-09-18
adversarial review's residual risk "small core becoming a god package" (`raw/08-prior-art.md` §2) is
therefore the consumer test, not the list. The Build column is re-checked at every stage gate
(14 K6). The package mapping ([04](04-package-mapping.md)) applies the rule to all 213 packages.

**Visibility rule.** A package that still has an ABO importer moves to a **public** core path, or
stays in ABO; it never moves to `internal/` or `kernel/`, which ABO cannot import (Go's `internal/`
rule; R2). It may move to `internal/`/`kernel/` only in the wave in which its **last** ABO importer has
been rewired to a public SDK surface (for example `sdnotify` behind `health`/`lifecycle` readiness
hooks, `compactprogress` behind a `storage` statistics type, `oauth` behind the `auth` OIDC API,
`scheduler`/`executor` behind `tasks`, startup steps only through `app`). 04 derives its
internal/public column from the import graph under this rule.

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
| R2 | Apps import only core's public packages. No wave rewires an ABO import onto `internal/` or `kernel/` (visibility rule, §3.4). | Go's `internal/` rule for `core/internal/...`; `archtest.NoKernelImports(t, "github.com/falkcorp/audiobook-organizer/...")` for `kernel/...` (called from the root module's `internal/arch`); `archtest.SDKOnly(t, "github.com/falkcorp/mediacore/examples/...")` for the examples, which the path-based `internal/` rule does not cover (X5). |
| R3 | A module never imports another module's packages; modules talk through core contracts (events, outbox, tasks, capability lookups). | `archtest.ModulesIsolated(t, map[moduleID]pkgPrefix)`; for ABO there is one module, so the rule is vacuous until a second exists, but the test is wired from wave 0 so the second module cannot land without it. |
| R4 | Core layers (a package may import only its own or lower layers): **L0** `entity`, `lifecycle`, `version`, `log`, `metrics` → **L1** `storage`, `storage/index` → **L2** `secrets` → **L3** `config` → **L4** `events`, `outbox`, `tasks`, `library`, `auth`, `rbac`, `audit`, `compat`, `integrations`, `integrations/*`, `backup`, `updater`, `health` → **L5** `acquire` → **L6** `httpapi`, `realtime`, `arrfacade`, `ui`, `internal/<capability>http` → **L7** `module` → **L8** `kernel` → **L9** `app`, `examples/*`. Deferred packages keep their layer so their contracts are checked when built. `coretest` and `archtest` are test-support packages (may import any core package; no core non-test package imports them). An `internal/*` package takes the layer of the public package it implements; an `internal/*` package that **bridges** capabilities (imports a package above the one it implements) takes the layer of its **highest import**, is never imported by a lower-layer package, and is wired by `kernel` — e.g. `internal/storagemigrate` (storage migrations run as tasks: L4), `internal/logjournal` (persisted journal over storage and tasks: L4), `internal/<capability>http` (L6). | `core/internal/archtest_layers_test.go` — the redesigned successor of `internal/arch/layering_test.go` for core (§3.5.1). |
| R5 | No process-global mutable state **in core**. No `func init()` in core except allowlisted pure ones (none at start); no exported package-level variables of map, slice, func or pointer type; core packages never call `prometheus.DefaultRegisterer`/`MustRegister` on the default registry, `otel.SetMeterProvider`, `otel.SetTracerProvider`, `otel.SetLogger`, `otel.SetTextMapPropagator`, `otel.SetErrorHandler`, `slog.SetDefault`, `log.SetOutput`, `http.DefaultServeMux`, `http.DefaultClient`/`http.DefaultTransport` mutation, or `signal.Notify`. The single exception is `app.InstallProcessDefaults`, an opt-in helper a `main` may call. R5 binds core only: an app may keep façades over core handles (ABO keeps `config.AppConfig` and its logger shim as books-module values); they are per-`App` values, so several apps in one binary stay possible, and removing them is an optional ratchet, not an extraction stage. | `core/archtest.TestNoGlobals` (AST scan). Rationale: several apps in one binary, and test isolation (`raw/07-plugin-registry-sdk.md` §6, `raw/12-cross-cutting.md` §9). |
| R6 | Every route declares a permission (or explicit `rbac.Public` / `rbac.AnyAuthenticated`; `AnyAuthenticated` never admits a confined credential, 05-rbac). Routes served through `httpapi.MountLegacy` (ABO's gin engine, mounted permanently behind core's perimeter middleware) are covered by a reviewed legacy route manifest in which every route names its permission. | Registration-time error in `httpapi` (fail-closed) **and** `archtest.RoutesHavePermissions` over the built router and the legacy manifest — a permanent test, not a migration test. |
| R7 | Every library-scale loop declares parallelism. | `tasks.Parallelism` has no valid zero value. For `Batch`/`Fixer` definitions the parallelism is a definition field and a missing value **fails registration**. For a `tasks.RunItems` call inside a task body, a zero value returns `tasks.ErrNoParallelism` before any item runs (fail-closed), and `archtest.RunItemsDeclareParallelism` (AST) fails any call whose parallelism argument is neither a call to a `tasks` constructor nor a `tasks.Parallelism`-typed parameter or required struct field passed through (pass-through is allowed; its zero value still fails at call time). Replaces `internal/plugins/runitems_concurrency_lint_test.go`. |
| R8 | Every module storage key lives in the module's keyspace or a declared legacy claim. | `storage` refuses writes outside the handed `Keyspace`; claims are of three kinds — prefix, exact key, and prefix-with-exceptions (sub-prefixes carved out for another owner, e.g. the shared `idx:`/`pref:` families) — resolved by longest match; boot fails on any overlap between owners (§05-storage). |

### 3.5.1 Redesign of `internal/arch/layering_test.go`

Verified assumptions that the extraction breaks (`raw/03-layering-guard.md` §2–§4):

- the **package floor** `minModulePackages = 150` (`internal/arch/layering_test.go:38`). The root has
  213 packages today. Under 04's mapping 59 packages leave and 2 new paths appear
  (`internal/books`, `internal/cfaccess`), so 213 − 59 + 2 = **156** remain: a margin of 6 that any
  further move consumes (04 §8 note 6; `raw/03-layering-guard.md` §3's 154 omits the 2 new paths).
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
   - imports of `github.com/falkcorp/mediacore/...` are classified as **layer −1 (external
     platform)**: legal from every ABO layer, never counted as an in-module edge;
   - a **classifier positive control**, effective from the first wave in which ABO imports core:
     at least one root package imports a `github.com/falkcorp/mediacore/...` path, and the guard
     classified that import as layer −1. (A cross-module edge cannot serve as the in-module control,
     because by the previous bullet it is never an in-module edge.)

   A rule against "smuggling audiobook types into core through a callback" was considered and
   dropped: callbacks are values, not import edges, so no import-graph check can see them, and
   R1(a)/(b) already stop core from naming any ABO type.
2. **Core guard** (`core/internal/archtest_layers_test.go`) is a new instance of the same algorithm
   with the R4 layer table, its own `layerOf`, its own ratio floor, and its own in-module positive
   control (`app` imports `kernel`). It is registered in `ci/gates.txt` as a core gate (§3.2
   item 4).
3. **Shared engine**: the classification/compute code moves to `core/archtest` as a library
   (`archtest.Layering(t, archtest.LayerSpec{...})`) so both modules — and every future app repo —
   run the same algorithm. This is what "reusable guard" in R2/R3 means.

## 3.6 Naming conventions

| Thing | Convention | Example |
|---|---|---|
| App ID | `[a-z][a-z0-9]{1,15}`, field `app.Options.AppID` | `abo` |
| Module ID | lowercase `[a-z][a-z0-9]{1,15}`; **reserved** (the first path segment of every route or page core serves, and every namespace core owns): `core`, `api`, `v` followed by digits, `auth`, `admin`, `events`, `health`, `healthz`, `readyz`, `metrics`, `system`, `settings`, `users`, `login`, `logout`, `setup`, `invite`, `activity`, `tasks`, `static`, `assets`, `ui`, `custom`; plus every top-level path segment an app declares for a compatibility surface in `app.Options.ReservedAPINamespaces` (ABO declares the ABS-compatible unversioned `/api/*` segments, so no future module may use those IDs). The kernel validates against one function, `module.ReservedIDs()`, which returns the union of `httpapi.ReservedSegments` (server paths), `ui.ReservedSegments` (shell routes) and `rbac.ReservedNamespaces` (`core`, `custom`); each list is owned by the package that serves those names, and a test asserts the union equals this row. | `books`, `tv`, `movies` |
| Core namespace | the reserved module ID `core` | `core:admin` |
| Permission | `<moduleID>:<action>[.<sub>]` | `books:edit`, `tv:request`, `core:users.manage` |
| Task kind | `<moduleID>.<segment>(.<segment>)*`, every segment matching `[a-z][a-z0-9-]*` (dots allowed after the module ID; no `_` — none of ABO's 269 op IDs has one, `internal/server/testdata/op_ids.golden`) | `books.scan`, `books.dedup.full-scan`, `core.backup` |
| Event topic | `<moduleID>.<noun>.<verb-past>` with a version suffix | `books.book.updated.v1` |
| Storage keyspace | `m/<moduleID>/` for modules, `c/<subsystem>/` for core | `m/tv/series/…`, `c/tasks/run/…`, `c/config/…` |
| Native API path | `/api/core/v1/...`, `/api/<moduleID>/v1/...` | `/api/books/v1/books/{id}` |
| Config section | `core.*`, `modules.<moduleID>.*` | `modules.books.scan.interval` |
| Role ID | core seed roles: bare names (`admin`, `editor`, `viewer`, kept from ABO); module seed roles: `<moduleID>.<name>`; custom roles: `custom.<slug>` | `admin`, `tv.admin`, `books.editor`, `custom.night-shift` |
| Audit action | `<moduleID>.<noun>.<verb>` (no version suffix: records are stored, not consumed) | `core.apikey.create`, `books.book.merge` |
| Metric name | core: `mediacore_<subsystem>_*`. Module series: `<MetricPrefix>_*` while the app hosts one module, `<MetricPrefix>_<moduleID>_*` when it hosts two or more. `MetricPrefix` is `app.Options.MetricPrefix` (`[a-z][a-z0-9_]*`, defaults to `AppID`); ABO sets `audiobook_organizer`, so its 73 existing families keep their names (`internal/telemetry/contract/testdata/series.golden`) and need no alias. Standard OTel semantic-convention names (e.g. `http_server_*`) are kept unchanged. | `mediacore_tasks_runs_total`, `audiobook_organizer_books_total` |
| UI route | `/<moduleID>/...` for module pages; shell pages under the segments of `ui.ReservedSegments` | `/books/library`, `/settings`, `/system/tasks`, `/invite/{token}` |

ABO's existing un-namespaced names (13 permission atoms, 258 key families, `/api/v1/...` paths,
flat config keys, `/api/v3/ops`) are mapped onto these conventions with explicit compatibility
windows in [09-abo-on-core](09-abo-on-core.md); the window itself is defined once, in 09 §9.2.1.
ABO's routes stay on its gin engine under `MountLegacy` unless a route group is rewritten for its
own reasons, so `/api/v1/...` is a permanent books legacy prefix, not a surface with a sunset.

## 3.7 Process shape

One `main` per app calls `app.New` with the modules it ships and the adapters it enables, then
`Run`. The kernel validates the module graph (duplicate or reserved IDs, `MinCoreVersion` against
`version.Version`, permission/route/task-kind/keyspace collisions, migration order) **before**
opening listeners, runs core then module migrations, constructs services, starts the task executor
and scheduler, mounts routes, and serves. Module enablement is **restart-bound** (kept from old
decision 10) and is read before module registration (06): a disabled module contributes no route,
task, schedule, worker, nav entry or filesystem mutation, and its data is retained.

```
main ──► app.New(opts, books.Module(), …)
           │  kernel: validate manifests → open storage → core migrations → module migrations
           │          → build the typed services struct (explicit constructors, no init())
           │          → register tasks/schedules
           │          → mount httpapi: core routes (internal/<capability>http), module routes,
           │            legacy mount (httpapi.MountLegacy, permanent for ABO's gin engine),
           │            arrfacade (deferred) → ui.Handler (only for an app that ships a shell
           │            build; until then the legacy mount owns `/`)
           ▼
         App.Run(ctx): start executor, scheduler, outbox relay, realtime hub, listeners
                       shutdown in reverse order on ctx cancel (bounded drain, §05-tasks)
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
- DATA-1 — config overrides live under `c/config/` (§3.3 `config`, §3.6 keyspace example).
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
