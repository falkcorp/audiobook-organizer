<!-- file: docs/specs/platform-core/01-summary.md -->
<!-- version: 1.3.0 -->
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
media-library roots, contracts plus adapters for AI, download clients, metadata providers and
notifications, backup, updates, health and a React UI shell. Apps compose it explicitly —
`app.New(opts, modules...)` — with no global registration in core.

Core **builds** only what ABO consumes during the extraction or what core itself needs (03 §3.4). The
indexer contract, the *arr-v3 facade extension point, the acquisition engine and the download
grab/monitor pipeline are specified as contracts at proof depth but are **deferred-build**: nothing in
the extraction plan builds them; they are built when the first TV/Movies app starts.

The extraction is a strangler fig: core is born as a nested module inside this repo (proven to build
and vet, `raw/00-baseline-build.md`), ABO is rewired onto it stage by stage while it keeps shipping,
and core moves to its own repository when measurable stability criteria hold (§12). TV and Movies
apps are specified only far enough to prove the contracts (§10); they reveal one large media-neutral
subsystem — acquisition (quality profiles, custom formats, the release decision engine, tracked
downloads) — which core gains as the `acquire` package when the first of those apps starts.
ABO keeps its gin router, mounted permanently behind core's HTTP perimeter (02 N7), so the extraction
moves platform machinery, not every route.

## 1.2 Goals

1. **One core, many apps.** ABO, a Sonarr-like TV app and a Radarr-like movies app build on the same
   core packages and the same UI shell, each in its own repository, each shipping its own binary.
2. **Self-contained modules.** A module brings its manifest, routes, tasks, schedules, migrations,
   permissions, settings schema, UI contribution and adapters; it never edits core or another module
   (inherited gate G6).
3. **ABO ships at every step.** Every stage of the extraction leaves `main` green under `make ci`
   and ABO deployable, with a rollback that opens the same database (gate G5). During the extraction
   every key family keeps one stored format, so the previous release reads every row the new build
   wrote (02 decision 13); a rollback's only stated costs are one re-login and re-entering secrets the
   new build moved to the encrypted store. An install whose store has not applied what the declared
   minimum-upgrade-from release requires upgrades through it first (02 N14).
4. **Correct by construction.** The boundaries are enforced mechanically (rules R1–R8 in §03): core
   cannot import app code, routes cannot be registered without a permission, library-scale loops
   cannot be written without declaring parallelism, modules cannot write outside their keyspace.
5. **Current toolchain.** Latest stable Go (go1.27.3) and frontend tooling, modern idioms, stdlib
   over third-party where equivalent (D8, §13).
6. **Free and open.** No paid service, tier or licence is required anywhere (D9).
7. **Don't rule out one binary with several apps** (lowest priority, §11): no process globals in
   core (R5 binds core only; ABO's remaining globals become per-App façades), namespaced everything,
   one shell with merged navigation.

## 1.3 Non-goals

- Full Sonarr/Radarr feature or API parity, or importing their databases (D2).
- Building the TV, Movies or Ebooks apps (D5): only proof sketches.
- Go `.so` plugins, out-of-process third-party plugins, or remote JavaScript (kept from 2026-09-18).
- Tenancy: no tenant IDs or tenant isolation (D7).
- Hot enable/disable of modules (restart-bound, kept decision 10).
- A universal media table (kept decision 6).
- Renaming or moving any of ABO's existing 258 Pebble key families during the extraction (02
  decision 13): core services read and write the generic ones (users, roles, API keys, settings and
  the config blob, ops history, activity) in place through legacy-format adapters. Moving a family to
  a native keyspace is a later, one-way **cutover** (02 N15), not an extraction stage.
- Retiring ABO's gin router or renaming its `/api/v1/...` routes: the router stays mounted through
  `httpapi.MountLegacy` (02 N7); a route group moves to native `httpapi` only when rewritten for its
  own reasons.
- Building the deferred core packages (`acquire`, `arrfacade`, `integrations/indexer`, the download
  grab/monitor pipeline): their contracts are specified, their builds wait for the first TV/Movies app.
- Removing ABO's process globals (`config.AppConfig`, package-level loggers): optional ratchets, not
  extraction stages.
- Fixing unrelated ABO defects found along the way: they are filed in
  `todo.d/2026-11-04_platform_core_followups.md`.

## 1.4 Glossary

| Term | Meaning |
|---|---|
| **core** / **mediacore** | The Go module `github.com/falkcorp/mediacore` (at `core/` until the split) and its npm workspace. |
| **SDK** | Core's public, versioned packages — everything outside `core/internal/` and `core/kernel/`. Measured by the API-stability gate (§12). |
| **kernel** | Core's composition machinery (`core/kernel/...`): module catalog, manifest validation, lifecycle supervisor and the typed services struct (no DI container). Imported only by `core/app`. |
| **deferred-build** | A core package whose contract is specified at proof depth but which the extraction plan does not build (03 §3.4): `acquire`, `arrfacade`, `integrations/indexer`, the download grab/monitor pipeline. Built when the first TV/Movies app starts. |
| **legacy mount** | ABO's gin router mounted through `httpapi.MountLegacy` behind core's perimeter middleware; permanent (02 N7). |
| **entity.Ref** | The one polymorphic reference `{Module, Kind, ID}` to a module-owned entity (03 §3.3); `tasks.Resource` is this type. |
| **app** | A shipped binary: one `main` that calls `app.New` with its modules. ABO is an app. |
| **module** | A self-contained, compiled-in domain implementing `module.Module` (e.g. `books`, `tv`). Identified by a module ID that namespaces its permissions, keys, routes, tasks, topics and config. |
| **adapter** | An implementation of a core capability contract (e.g. `integrations/download/deluge`). Configured per instance. |
| **capability contract** | A narrow Go interface in `core/integrations/*` (AI, download, indexer, metadata, notify). |
| **keyspace** | A prefix of the shared Pebble database handed to one owner: `c/<subsystem>/` for core, `m/<module>/` for modules. |
| **legacy claim** | A module's declaration that it owns pre-existing un-namespaced key families (ABO's 258), so they stay in place. |
| **legacy-format adapter** | How a core service uses an ABO key family during the extraction: it reads and writes the existing un-namespaced rows in their stored format through an explicit, logged **storage grant** (05-storage), so each family keeps one stored format and the previous release reads every row. Native-only data is added where the legacy format tolerates it (namespaced scope strings, which older builds ignore) or kept in a core-only family older builds never read (02 decision 13). |
| **cutover** | The later, per-family, one-way step that moves a family to a native core keyspace and deletes the legacy rows; outside the extraction, checkpointed, behind the per-owner level guard; its rollback floor is the cutover release itself (02 N15). |
| **task** | A durable unit of background work in `core/tasks` (formerly "operation"/"op"/"job"); kind `<module>.<name>`. |
| **Parallelism** | The required declaration of how a library-scale loop runs (`tasks.CPU()`, `tasks.Fixed(n, reason)`, `tasks.Sequential(reason)`; §05-tasks). |
| **outbox** | A durable queue written in the same Pebble batch as the state change it announces; relayed at least once. |
| **permission scope** | `<module>:<action>[.<sub>]`, e.g. `books:edit`, `tv:request`, `core:admin`. |
| **shell** | The `@mediacore/shell` React package: auth, layout, navigation, settings frame, tasks, logs, users/roles, notification admin pages for the v1 scope (contract, mail and webhook channels, global routing) plus toasts (no inbox or personal channels), health. |
| ***arr facade** | An optional, per-app HTTP adapter exposing an *arr-v3-compatible subset for ecosystem clients (Overseerr/Jellyseerr, Prowlarr, Bazarr) through `core/arrfacade` (deferred-build). |
| **ABO** | audiobook-organizer, this repository. |
| **strangler stage** | One step of the extraction in §09 (S0–S10, with S2b and UI-0), after which ABO is still shippable. "Wave" is not used. |
| **minimum-upgrade-from release** | The oldest release from which an install may upgrade directly to a given build; older installs must pass through it first (`min_upgrade_from` in the release manifest; boot refuses with a message naming it) (02 N14). |
| **split criteria SC1–SC10** | The measurable readiness criteria for moving core to its own repository (§12.3); named `SC` so they never collide with the 09 stages S0–S10. |
| **gate G1–G8** | The eight acceptance gates inherited from the 2026-09-18 adversarial review (`02-decision-record.md` §2.4). |
| **R1–R8** | The mechanically enforced dependency/registration rules in `03-architecture.md` §3.5. |

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- SIMP-1 — summary, non-goals and glossary state the deferred-build packages.
- SIMP-2 — summary and non-goals: gin stays under a permanent legacy mount.
- SIMP-3 — goal 7 and non-goals: R5 binds core only; ABO globals are optional ratchets.
- SIMP-17 — Parallelism glossary: `Fixed(n, reason)`, no `IO`.
- COR-17 — `entity.Ref` glossary entry.
- DATA-1, DATA-3 — non-goals and glossary: identity-family adoption model; settings families stay books legacy. (Adoption model superseded in round 3 by R3-1.)
- SIMP-26 — kernel glossary: typed services struct, no container.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-1 — goal 3 and glossary: Teach build is the rollback floor (02 N13). (Superseded in round 3 by R3-1.)
- R2-4 — goal 3 and glossary: minimum-upgrade-from release (02 N14).
- MOD2-21 — glossary: strangler stages are S0–S10 (no "wave"); split criteria are SC1–SC10.
- SIMP2-11 — shell glossary: notification admin pages and toasts; inbox/bell deferred.

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — goal 3: one stored format per family, rollback costs (re-login, re-enter moved secrets), no Teach-build floor; non-goals: no family moves during the extraction, cutover is later (02 N15); glossary: "identity-family adoption" and "Teach build" removed, "legacy-format adapter" and "cutover" added.
- DATA3-17 — superseded by R3-1: the glossary entry with the old contract precondition is removed with the adoption model.
- OPS3-7 — goal 3: the minimum-upgrade-from requirement is what the store has applied (02 N14).
