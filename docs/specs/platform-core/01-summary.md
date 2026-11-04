<!-- file: docs/specs/platform-core/01-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: d2956830-373c-4b12-9504-ccace49f7f6d -->
<!-- last-edited: 2026-11-04 -->

# 01 — Summary, goals, non-goals, glossary

## 1.1 Summary

audiobook-organizer (ABO) is a 213-package, ~705k-LOC (non-test) Go monolith with a React/TypeScript
UI (`raw/01-package-inventory.md`). About 59 packages (~19k LOC) are already generic, 104 (~379k LOC)
are audiobook-specific, and 50 (~307k LOC) mix the two. The mix is concentrated in a few keystones:
`internal/database` (the 445-method `Store` and `database.Book`, imported by 104 packages),
`internal/config` (a process-global 864-line struct that imports five domain packages),
`internal/server` (497 routes wired inline), and three overlapping registration mechanisms driven by
`init()` (`raw/02`, `raw/04`, `raw/07`, `raw/09`).

This spec defines **mediacore**: a Go module (`github.com/falkcorp/mediacore`) plus an npm workspace
(`@mediacore/*`) that provides everything an *arr-style media app needs that is not about one kind of
media — storage, durable tasks, events and outbox, config and secrets, logging, metrics, an HTTP API
framework with generated TypeScript clients, realtime, authentication, namespaced RBAC and audit,
media-library roots, contracts plus adapters for AI, download clients, indexers, metadata providers
and notifications, backup, updates, health, a React UI shell, and an extension point for an
*arr-v3-compatible API. Apps compose it explicitly — `app.New(opts, modules...)` — with no global
registration.

The extraction is a strangler fig: core is born as a nested module inside this repo (proven to build
and vet, `raw/00-baseline-build.md`), ABO is rewired onto it stage by stage while it keeps shipping,
and core moves to its own repository when measurable stability criteria hold (§12). TV and Movies
apps are specified only far enough to prove the contracts (§10); they reveal one large media-neutral
subsystem — acquisition (quality profiles, custom formats, the release decision engine, tracked
downloads) — which core gains as the `acquire` package when the first of those apps starts.

## 1.2 Goals

1. **One core, many apps.** ABO, a Sonarr-like TV app and a Radarr-like movies app build on the same
   core packages and the same UI shell, each in its own repository, each shipping its own binary.
2. **Self-contained modules.** A module brings its manifest, routes, tasks, schedules, migrations,
   permissions, settings schema, UI contribution and adapters; it never edits core or another module
   (inherited gate G6).
3. **ABO ships at every step.** Every stage of the extraction leaves `main` green under `make ci`
   and ABO deployable, with a rollback that opens the same database (gate G5).
4. **Correct by construction.** The boundaries are enforced mechanically (rules R1–R8 in §03): core
   cannot import app code, routes cannot be registered without a permission, library-scale loops
   cannot be written without declaring parallelism, modules cannot write outside their keyspace.
5. **Current toolchain.** Latest stable Go (go1.27.3) and frontend tooling, modern idioms, stdlib
   over third-party where equivalent (D8, §13).
6. **Free and open.** No paid service, tier or licence is required anywhere (D9).
7. **Don't rule out one binary with several apps** (lowest priority, §11): no process globals,
   namespaced everything, one shell with merged navigation.

## 1.3 Non-goals

- Full Sonarr/Radarr feature or API parity, or importing their databases (D2).
- Building the TV, Movies or Ebooks apps (D5): only proof sketches.
- Go `.so` plugins, out-of-process third-party plugins, or remote JavaScript (kept from 2026-09-18).
- Tenancy: no tenant IDs or tenant isolation (D7).
- Hot enable/disable of modules (restart-bound, kept decision 10).
- A universal media table (kept decision 6).
- Renaming ABO's existing 258 Pebble key families (kept decision 13); only small generic families are
  adopted into core keyspaces (§05-storage, §09).
- Fixing unrelated ABO defects found along the way: they are filed in
  `todo.d/2026-11-04_platform_core_followups.md`.

## 1.4 Glossary

| Term | Meaning |
|---|---|
| **core** / **mediacore** | The Go module `github.com/falkcorp/mediacore` (at `core/` until the split) and its npm workspace. |
| **SDK** | Core's public, versioned packages — everything outside `core/internal/` and `core/kernel/`. Measured by the API-stability gate (§12). |
| **kernel** | Core's composition machinery (`core/kernel/...`): module catalog, lifecycle supervisor. Imported only by `core/app`. |
| **app** | A shipped binary: one `main` that calls `app.New` with its modules. ABO is an app. |
| **module** | A self-contained, compiled-in domain implementing `module.Module` (e.g. `books`, `tv`). Identified by a module ID that namespaces its permissions, keys, routes, tasks, topics and config. |
| **adapter** | An implementation of a core capability contract (e.g. `integrations/download/deluge`). Configured per instance. |
| **capability contract** | A narrow Go interface in `core/integrations/*` (AI, download, indexer, metadata, notify). |
| **keyspace** | A prefix of the shared Pebble database handed to one owner: `c/<subsystem>/` for core, `m/<module>/` for modules. |
| **legacy claim** | A module's declaration that it owns pre-existing un-namespaced key families (ABO's 258), so they stay in place. |
| **adoption migration** | The one-time atomic migration that moves a small generic ABO key family (e.g. users) into a core keyspace. |
| **task** | A durable unit of background work in `core/tasks` (formerly "operation"/"op"/"job"); kind `<module>.<name>`. |
| **Parallelism** | The required declaration of how a library-scale loop runs (`tasks.CPU()`, `tasks.Fixed(n)`, `tasks.IO(n)`, `tasks.Sequential(reason)`). |
| **outbox** | A durable queue written in the same Pebble batch as the state change it announces; relayed at least once. |
| **permission scope** | `<module>:<action>[.<sub>]`, e.g. `books:edit`, `tv:request`, `core:admin`. |
| **shell** | The `@mediacore/shell` React package: auth, layout, navigation, settings frame, tasks, logs, users/roles, notifications, health. |
| ***arr facade** | An optional, per-app HTTP adapter exposing an *arr-v3-compatible subset for ecosystem clients (Overseerr/Jellyseerr, Prowlarr, Bazarr) through `core/arrfacade`. |
| **ABO** | audiobook-organizer, this repository. |
| **strangler stage** | One step of the extraction in §09, after which ABO is still shippable. |
| **gate G1–G8** | The eight acceptance gates inherited from the 2026-09-18 adversarial review (`02-decision-record.md` §2.4). |
| **R1–R8** | The mechanically enforced dependency/registration rules in `03-architecture.md` §3.5. |
