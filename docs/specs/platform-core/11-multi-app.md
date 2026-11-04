<!-- file: docs/specs/platform-core/11-multi-app.md -->
<!-- version: 1.11.2 -->
<!-- guid: 74ed9015-8ae0-4b4b-8049-4c09cfbba343 -->
<!-- last-edited: 2026-11-04 -->

# 11 — Several apps in one binary (lowest priority)

Evidence commit: `748fd787a`. The owner's priority order puts "ABO + TV + Movies modules in one
binary" last: **never ruled out, never over-invested** (02 §2.1). This file records which design
constraints keep the door open, what is deliberately not done, and what a later "suite" binary would
need. It is not a capability contract and adds no core API. One item in the checklist (§11.5 row 4)
does need a new core capability; it is recorded as such in 14.

## 11.1 What "multi-app" means here

One `main` calling `app.New(ctx, opts, books.Module(…), tv.New(…), movies.New(…))` (ABO's constructor name, 03 §3.7, MOD5-11): one process, one
data directory, one Pebble store, one user database, one HTTP listener, one SPA. It does **not** mean
several `App` values in one process sharing nothing (that is already possible once R5 holds, because
nothing in core is process-global) nor multi-tenancy (D7: no tenant IDs).

## 11.2 Constraints already honoured by the design

| Constraint | Where it is specified | What it guarantees for a suite binary |
|---|---|---|
| Module IDs unique, validated before storage opens | 06 §6.5.1 P1, 03 §3.6 | `books`, `tv`, `movies` cannot collide silently |
| Namespaced storage: `m/<id>/` per module, `c/<subsystem>/` for core, legacy claims checked for overlap; no core subsystem and no module reads another owner's rows; core subsystems have native stores only | 03 R8, 05-storage, 06 `Manifest.Storage storage.Spec`, 06 §6.5.1 P4b | ABO's domain families stay claimed by `books` in place; new modules never touch them. From R2 users, roles, API keys, settings, secrets, runs, the system log, integration instances, AI jobs, notify rules and library roots are core's native rows in `c/<subsystem>/`: books' R2 cutover steps moved HEAD's copies there once, through each subsystem's importer, so `tv` and `movies` share that one user database and one instance store with `books` without a copy and without any app-specific store implementation. Library roots are `core.library.roots` entries for every module, books' former import roots included (IDs kept). Root sharing between modules does not exist in R0–R3: `library.RootSpec.ReadShares` is a deferred-build member of 05-library ("Deferred-build members"), landing with the first app whose two modules share a root, and with it the read clause and the `core.library.roots` field (SIMP8-5). Until then each module reads only its own roots, so in a suite `tv` and `movies` cannot read a books root, nor books theirs |
| Route prefixes `/api/<id>/v1`, core at `/api/core/v1`; legacy subtree claims may not overlap | 03 §3.6, 06 `Registrar.HTTP`, 05-http-api §4.9 | no path overlap between modules by construction |
| Permission namespaces `<id>:<action>`, admin is `core:admin` | 03 §3.6, 06 `Manifest.Permissions []rbac.PermissionSpec`, 05-rbac | one role editor shows `books:*`, `tv:*`, `movies:*`; API keys can be scoped per module (roles and keys are native from R2 for every app, `books` included) |
| Task kinds `<id>.<name>`, resources as `entity.Ref{Module, Kind, ID}` | 03 §3.3, §3.6, G8, 05-tasks | one executor, no write-set identity collisions |
| Event topics `<id>.<noun>.<verb-past>.v<n>` in one catalog; outbox consumer names namespaced and declared in the manifest | 05-events, 06 `Registrar.Topics`, `Manifest.OutboxConsumers` | cross-module reactions without import edges (R3, raw subscriptions/consumers) |
| Config sections `modules.<id>.*` | 03 §3.6, 06 `Settings` | one config file, no key collisions |
| Module metrics under `app.Options.MetricNamespace` on a per-`App` registry (`<MetricNamespace>_*` while the app ships one module, `<MetricNamespace>_<id>_*` when it ships two or more; decided by the modules compiled in, never by which are enabled), core series under the same namespace as `<MetricNamespace>_<subsystem>_*` | 03 §3.6, 05-metrics, 06 `Deps.Metrics` | no duplicate-registration panic (today's default-registry hazard, raw/12 §9) |
| No process-global mutable state in core (R5, `TestNoGlobals`) | 03 R5 | the raw/12 §9 hazards (`config.AppConfig`, viper, `slog.SetDefault`, Prometheus default registry, OTel globals, `database.globalStore`, `plugin.Global()`, `serviceregistry.registered`, `aijobs`, throttle/quota maps) are absent from core |
| One shell with merged navigation from every enabled module | 07 §7.4–§7.5 | nav, settings panels and widgets of all modules render in one SPA; intersection per module |
| One task executor and scheduler | 05-tasks | a single queue with per-kind permissions and resources |
| Distinct *arr facades by URL base, collisions refused at boot (facade is deferred-build) | 08 §8.4 | `/tv/api/v3` and `/movies/api/v3` side by side |
| Restart-bound enablement per module, read before registration; no inter-module dependencies (`DependsOn` dropped, SIMP-22) | 06 §6.5.3 | a suite can ship all three and enable any subset; a disabled module's runs are held and its outbox messages retained |
| Module isolation guard wired from R1 | 03 R3 `archtest.ModulesIsolated` | a second module cannot import the first |
| One integration-instance store | 05-metadata M1 (SIMP-4) | download/indexer/AI/notify instances are shared data, not per-module copies |

## 11.3 Hazards found that a suite would trip over

| Hazard | Anchor | Required handling (owner) |
|---|---|---|
| ABO's catch-all `/api/*` → `/api/v1/*` 301 redirect is a gin global middleware with an exclusion list | `internal/server/server_lifecycle.go:1789-1816` | 05-http-api §4.9 re-implements it as the least-specific `/api/` handler of the `books` legacy mount, so every claimed prefix (`/api/core/`, `/api/<module>/`, facades) wins by `ServeMux` specificity. For an **unknown** path under another module (`/api/tv/v1/nope`) not to fall through to it, each module and core group must register a subtree 404 catch-all (open item for 05-http-api) |
| Un-namespaced ABO names that stay (books' `/api/v1/...` gin routes, the frozen ABS routes, HEAD's role ID `fp-worker`) | 03 §3.6 last paragraph | all are declared by `books`, so the kernel checks them against other modules: `books` declares `Manifest.LegacyPrefixes` (whose first segments are also reserved module IDs) and `Manifest.ImportedRoleIDs`, and `main` passes the ABS `/api/<seg>` names as `app.Options.ReservedAPINamespaces` (06 §6.4.5). There are no permission, task-kind or config aliases to carry over: R2's cutover steps renamed them once (D10), so a suite `main` carries only `ReservedAPINamespaces` over from ABO's |
| ABO-side globals (`config.AppConfig`, `database.globalStore`, viper, `slog.SetDefault` in `cmd/root.go:718`) | raw/12 §9 | R5 binds core only (cross-cutting resolution 3): ABO keeps `config.AppConfig` and its logger shim as books-module façades over core handles. They are harmless while one `App` runs `books`. A suite `main` must own logging/metrics setup through core, so `books` must not call `slog.SetDefault`/`otel.Set*` and its façades must be per-`App` values held by the module (§11.5 row 11) |
| Module metric names change when a second module is compiled in | 03 §3.6 metric row | with one shipped module ABO's series are `audiobook_organizer_*`; a suite binary that ships two or more makes them `<MetricNamespace>_books_*`. Core keeps no gather-time aliases, so a suite's dashboards query the suite's names from its first release and its release note says so (§11.5 row 12). Enabling or disabling a module at run time never renames a series (SIMP2-13) |

## 11.4 Deliberately left undone

* **No suite `main` and no combined SPA build.** Each app ships its own `main` and its own web
  workspace; nothing builds one bundle containing several apps' module UI packages.
* **One app identity per process.** `app.Options` has a single `AppID`/`MetricNamespace`/`ServiceName`/`EnvPrefix`/`StateDir`/`DisplayName`/`Version`/`Updater`
  (`ServiceName` is the OTel resource's `service.name`, 06 §6.4.5, MOD10-11);
  a suite would need its own identity, theme and update channel.
* **No per-module fairness in the task executor.** One module's CPU-bound library-scale task can use
  every worker; per-module concurrency quotas are not designed (05-tasks has global and per-kind
  limits only).
* **No per-module backup/restore granularity** beyond what 05-backup gives every module.
* **No combined OpenAPI client** for a suite; each module's generated client works, but there is no
  aggregate package.
* **Shared failure domain accepted.** A panic or memory blow-up in one module takes down all of them
  (adversarial review residual risk, raw/08 §2).
* **No cross-app UX** (unified search across books/series/movies, shared "requests" page).

## 11.5 Checklist for a future suite binary

| # | Item | Size |
|---|---|---|
| 1 | A suite repo or `cmd/suite` with `main` calling `app.New` with all modules; suite `app.Options` identity. Licences (12 §12.10 item 1, D13/D14): core is Apache-2.0, `books` stays PolyForm Noncommercial 1.0.0, so a suite artifact that ships `books` carries ABO's licence and its commercial use needs ABO's commercial licence; `tv` and `movies` choose their own | S |
| 2 | Suite web workspace: `createShell({ modules: [booksModule, tvModule, moviesModule] })`; each app's module UI must be publishable as its own package (today ABO keeps `modules/books/` as a directory, 07 §7.11 — promote it) | M |
| 3 | Embed the suite SPA with `embed_frontend` like any app (07 §7.6) | S |
| 4 | Merging existing single-app installs into one store is **not** "copy keyspaces" (DATA-30): each install holds its own core data, users, roles, API keys, config and run history in core's native `c/<subsystem>/` stores (ABO's since its R2 cutover), plus its own `c/secrets/` (different keyrings), `c/storage/install`, per-keyspace versions, per-owner reader levels, and `Sequence` high-water marks, which collide. It needs a **core storage export/import capability**: per keyspace **and per core store**, with version checks and ID-remap hooks for core subsystems (users by username, roles by ID, keyring merge, sequence rebase) that read and write through the same store interface and importer the subsystem uses, so core still never reads a module's rows; module keyspaces (`m/<id>/`) and a module's legacy claims copy as-is. This is a new core contract, recorded in 14. An install-wide level would also couple every app's rollback in a suite, which is why levels are per owner (05-storage per-owner reader levels, DATA-20) | L |
| 5 | Merge user databases of existing installs (users, roles, API keys) or start fresh — owner decision | M |
| 6 | Build the deferred `arrfacade` (08) if not already built; configure facade URL bases (`modules.tv.arrfacade.url_base=/tv`, `modules.movies.arrfacade.url_base=/movies`); two facades with one base fail boot (05-http-api §4.9) | S |
| 7 | Per-module executor quotas (05-tasks extension) if measurements show starvation | M |
| 8 | Suite e2e suite: boot with all modules, G1 for each disabled subset, nav merge, facade isolation | M |
| 9 | Release pipeline and updater source for the suite artifact | S |
| 10 | Re-run `archtest.ModulesIsolated` with all three modules and `TestNoGlobals` over every module package | S |
| 11 | Remove ABO's `config.AppConfig` and logger façades (or make them strictly per-`App` values with no package state), the optional 05-config/05-logging ratchets that the single-app plan does not need (SIMP-3) | M |
| 12 | Dashboard and alert queries moved to the `<MetricNamespace>_books_*` names a suite gives books' series, announced in the suite's release note; core keeps no metric aliases | S |
| 13 | Re-introduce inter-module dependencies (`Manifest.DependsOn`, additive) only if a suite module really needs another enabled; `MinCoreVersion` already exists (`version.Version`); generalise legacy prefix claims beyond "no overlap" only if two modules need one subtree (SIMP-22) | S |

Only row 4 requires a new core contract (storage export/import with ID remapping); it is listed in
[14](14-risks-open-questions.md). Every other row is additive or app-side; that is the test of whether
the door is still open. If a later spec change makes another row require a core API change, it must
say so in 14.

## Review history

Round 1 (2026-11-04), findings applied to this file:

- DATA-30 — §11.5 row 4: store merge needs a core export/import capability with ID remapping; flagged as a core contract change (14).
- SIMP-22 — `DependsOn` dropped from the enablement row; re-adding it, and generalising prefix claims, is §11.5 row 13.
- SIMP-3 — ABO's config/logger façades survive as per-`App` books façades; their removal is §11.5 row 11.
- SIMP-4 — one integration-instance store row.
- SIMP-5 / COR-12 / COR-13 — one topic catalog; declared consumers; held runs and retained messages for disabled modules.
- COR-16 / COR-17 / COR-26 — 06 vocabulary (`Manifest.Storage`, `Registrar.HTTP`, `PermissionSpec`), `entity.Ref`, `AppID`/`MetricPrefix`/`Updater`; metric-name change in a suite (§11.3, §11.5 row 12).
- Cross-cutting 1 — facade marked deferred-build.

Round 2 (2026-11-04), findings applied to this file:

- COR2-19 — §11.3: config `LegacyKeys` (no such field) → config `Aliases` (05-config).
- DATA2-29 — §11.5 row 4: "compat level and storage format" / "per-owner format" → "per-owner reader levels".
- SIMP2-13 — metric segment decided by the modules the app ships, not the enabled set (§11.2 row, §11.3 hazard).
- MOD2-21 — "wave 0" → S0.
- FE2-11 / COR2-8 (consistency with 06) — legacy prefixes' first segments are reserved IDs; `EnvPrefix`/`StateDir` listed in the single app identity.

Round 3 (2026-11-04), findings applied to this file:

- R3-1 — §11.2 storage row: core reaches legacy rows only through storage grants to core subsystems; a suite with `books` shares ABO's user, role and settings rows through them. §11.5 row 4: core data of an ABO install lives in books' granted legacy families until each family's cutover, so export/import works per granted family through the legacy-format adapters, and only ungranted legacy claims copy as-is.

Round 4 (2026-11-04), findings applied to this file:

- R4-1 — §11.2 storage row: no storage grants; books supplies its own legacy store implementations, at most one module per interface, and a suite without books runs on core's native stores. §11.5 row 4: export/import works per core store through the same interface (native or module-supplied), never by core reading a module's rows.

Round 5 (2026-11-04), findings applied to this file:

- MOD5-11 — §11.1: `books.Module(…)`, the one constructor name.
- COR5-5 — §11.3: ABO's 13 core-targeting atoms are app-level `app.Options.LegacyPermissionAliases`; books declares only own-namespace aliases and `LegacyRoleIDs`.
- SIMP5-1 — §11.2 storage row: exclusive vs routed stores; in a suite with `books`, routed stores serve `tv`/`movies` records natively.

Round 6 (2026-11-04), findings applied to this file:

- R6-3 / SIMP6-3 — §11.2 storage row and §11.5 row 4: `Manifest.LegacyStores` is a `legacy.Stores` value of core package `legacy`, outside the API promise; library roots are legacy-only beside config entries, not a routed store.
- MOD6-6 — §11.3 un-namespaced names hazard: a suite `main` carries ABO's `app.Options.LegacyTaskAliases` beside `LegacyPermissionAliases`.

Round 7 (2026-11-04), findings applied to this file:

- SIMP7-8 (consistency) — §11.2 storage row: `legacy.RootSource` is `legacy.LibraryRootSource`.
- SIMP7-2 (consistency) — §11.2 storage row: a legacy-served books root takes no `ReadShares`, so sharing one with `tv`/`movies` waits for 05-library's deferred root-suspension members; config-entry roots can share.

Round 8 (2026-11-04), findings applied to this file:

- SIMP8-5 (consistency) — §11.2 storage row: `ReadShares` is deferred-build on every root, config-entry roots included; a suite shares no root until the first app with two modules that share one.

Advisor (2026-11-04), resolutions applied to this file:

- A-2 / ADV-S1 — §11.2 storage and permission rows, §11.3 new hazard, §11.5 row 14: books' legacy stores keep HEAD's representation, so roles and keys hold legacy atoms only and a suite that ships `books` follows books' identity cutover.
- A-5 / ADV-S4 — §11.2 metric row, §11.3 metric hazard, §11.4, §11.5 row 12: core series under `app.Options.MetricNamespace`; no metric aliases, a suite moves its dashboards once.

Round 10 (2026-11-04), findings applied to this file:

- MOD10-11 (consistency) — §11.4 "One app identity per process": `app.Options.ServiceName` (06 §6.4.5) is part of the one identity.

- D10–D12 revision (2026-11-04): §11.2 storage row rewritten (no legacy stores or `legacy` package; books' core-service families are migrated into native `c/<subsystem>/` stores by the R2 cutover steps, so a suite shares one native user database and instance store); permission row no longer waits for an identity cutover; metric rows use `app.Options.MetricNamespace` (`MetricPrefix` deleted). §11.3: the compatibility-window row rewritten (no permission, task or config aliases; only `LegacyPrefixes`, `LegacyRoleIDs` and `ReservedAPINamespaces` remain) and the legacy-atom hazard deleted. §11.5 row 4 rewritten for native stores and importers; row 14 (books' identity cutover before a suite) deleted, since R2 does it. Stage references re-keyed to R1. Consistency pass: `Manifest.LegacyRoleIDs` is `Manifest.ImportedRoleIDs` (06 §6.4.2).

Round 13 (2026-11-04): D13, D14. §11.5 row 1 states the licences a suite inherits (core Apache-2.0, `books` PolyForm Noncommercial 1.0.0, 12 §12.10 item 1).
