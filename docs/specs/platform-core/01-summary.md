<!-- file: docs/specs/platform-core/01-summary.md -->
<!-- version: 1.9.0 -->
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
grab/monitor pipeline are **deferred-build**: they are described as non-normative design sketches at proof
depth, which the SDK does not freeze, nothing in the extraction plan builds them, and they are designed and
built when the first TV/Movies app starts (A-15).

The extraction is a strangler fig: core is born as a nested module inside this repo (proven to build
and vet, `raw/00-baseline-build.md`), ABO is rewired onto it in four releases, R0–R3 (03 §3.8), while it keeps
shipping, and core moves to its own repository when measurable stability criteria hold (§12). TV and Movies
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
3. **ABO ships at every step, and its data is migrated forward, never dropped.** Every merge leaves `main`
   green under `make ci` and e2e, and every release leaves ABO deployable (03 §3.8): R0 hotfixes, R1 the core
   runtime with no user-data change, R2 the one cutover, R3 cleanup and the upgrade floor. In R2 the core-service
   families move once, one-way, into native keyspaces through in-binary migrations: books decodes its HEAD rows and
   hands them to core's importers, every ID is kept, and a row that does not decode is quarantined and reported,
   never dropped (gate G5′). Rollback is an offline restore of the pre-migration checkpoint or an off-host archive,
   keyring and state files included, plus the previous binary; an older binary refuses a migrated store. A store that
   has not run R2 installs R2 before R3 (02 N14).
4. **Correct by construction.** The boundaries are enforced mechanically (rules R1–R8 in §03): core
   cannot import app code, routes cannot be registered without a permission, library-scale loops
   cannot be written without declaring parallelism, modules cannot write outside their keyspace.
5. **Current toolchain.** Latest stable Go (go1.27.3) and frontend tooling, modern idioms, stdlib
   over third-party where equivalent (D8, §13).
6. **Nothing paid.** No paid service, tier or licence fee is required anywhere (D9; the repo's PolyForm
   Noncommercial licence costs nothing, and relicensing core is an optional reuse decision, 14 Q-A).
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
- Moving books' own domain families (about 240 of ABO's 258 key families) to new keys: they stay in place under
  books' legacy claims, because renaming them buys nothing. Only the core-service families (03 §3.7 names them)
  migrate, once, in R2 (02 decision 13, N15).
- Backward compatibility (D10): no alias, compatibility window, legacy store or previous-build parity is built.
  The one frozen external contract is the Audiobookshelf-compatible API of the phone apps (D12 F3).
- Soaks (D11): releases are verified by tests, the checkpoint and restore; a 1-day smoke run is optional.
- Retiring ABO's gin router: it stays mounted through `httpapi.MountLegacy` (02 N7). Its routes may change; a
  route group moves to native `httpapi` when it is rewritten, or when an R2 flip replaces the core subsystem's HEAD
  routes it carries.
- Building the deferred core packages (`acquire`, `arrfacade`, `integrations/indexer`, the download
  grab/monitor pipeline): their contracts are specified, their builds wait for the first TV/Movies app.
- Removing ABO's process globals (`config.AppConfig`, package-level loggers): optional ratchets, not
  part of any release.
- Fixing unrelated ABO defects found along the way: they are filed in
  `todo.d/2026-11-04_platform_core_followups.md`.

## 1.4 Glossary

| Term | Meaning |
|---|---|
| **core** / **mediacore** | The Go module `github.com/falkcorp/mediacore` (at `core/` until the split) and its npm workspace. |
| **SDK** | Core's public, versioned packages — everything outside `core/internal/` and `core/kernel/`. Measured by the API-stability gate (§12). |
| **kernel** | Core's composition machinery (`core/kernel/...`): module catalog, manifest validation, lifecycle supervisor and the typed services struct (no DI container). Imported only by `core/app`. |
| **deferred-build** | A core package described by a non-normative design sketch at proof depth, which the SDK does not freeze and the extraction plan does not build (03 §3.4; A-15): `acquire`, `arrfacade`, `integrations/indexer`, the download grab/monitor pipeline. Designed and built when the first TV/Movies app starts. |
| **legacy mount** | ABO's gin router mounted through `httpapi.MountLegacy` behind core's perimeter middleware; permanent (02 N7). |
| **entity.Ref** | The one polymorphic reference `{Module, Kind, ID}` to a module-owned entity (03 §3.3); `tasks.Resource` is this type. |
| **app** | A shipped binary: one `main` that calls `app.New` with its modules. ABO is an app. |
| **module** | A self-contained, compiled-in domain implementing `module.Module` (e.g. `books`, `tv`). Identified by a module ID that namespaces its permissions, keys, routes, tasks, topics and config. |
| **adapter** | An implementation of a core capability contract (e.g. `integrations/download/deluge`). Configured per instance. |
| **capability contract** | A narrow Go interface in `core/integrations/*` (AI, download, indexer, metadata, notify). |
| **keyspace** | A prefix of the shared Pebble database handed to one owner: `c/<subsystem>/` for core, `m/<module>/` for modules. |
| **legacy claim** | A module's declaration that it owns pre-existing un-namespaced key families, so they stay in place: books' domain families after R2 (and, until R2, the core-service families too). |
| **store interface** | The SDK interface through which a core service persists its state; the one list is 03 §3.3 "Store interfaces and importers" (SIMP5-8). Core ships the **native implementation** over its own keyspace, which every app uses, ABO included; `coretest/fakes` supplies an in-memory one. |
| **cutover** | R2's one-way migration of the core-service families from ABO's HEAD rows into native keyspaces (03 §3.8): one books migration step per family decodes the rows, writes them through the subsystem's **importer** and deletes them, behind the pre-migration checkpoint, followed by a level step that makes older binaries refuse the store. |
| **task** | A durable unit of background work in `core/tasks` (formerly "operation"/"op"/"job"); kind `<module>.<name>`. |
| **Parallelism** | The required declaration of how a library-scale loop runs (`tasks.CPU()`, `tasks.Fixed(n, reason)`, `tasks.Sequential(reason)`; §05-tasks). |
| **outbox** | A durable queue written in the same Pebble batch as the state change it announces; relayed at least once. |
| **permission scope** | `<module>:<action>[.<sub>]`, e.g. `books:edit`, `tv:request`, `core:admin`. |
| **shell** | The `@mediacore/shell` React package: auth, layout, navigation, settings frame, tasks, logs, users/roles, notification admin pages for the v1 scope (contract, mail and webhook channels, global routing) plus toasts (no inbox or personal channels), health. |
| ***arr facade** | An optional, per-app HTTP adapter exposing an *arr-v3-compatible subset for ecosystem clients (Overseerr/Jellyseerr, Prowlarr, Bazarr) through `core/arrfacade` (deferred-build). |
| **ABO** | audiobook-organizer, this repository. |
| **release R0–R3** | The four releases of the extraction (03 §3.8): R0 hotfixes, R1 core runtime, R2 the cutover, R3 cleanup and floor. "Stage" and "wave" are not release keys. |
| **flip** | The one PR that wires a subsystem in, after its code landed dark on `main` (03 §3.8: F3–F9 inside R2). |
| **importer** | `<pkg>.Importer`: the SDK interface through which an app hands a core subsystem its pre-core records, staged into the caller's batch with every ID kept (03 §3.3). |
| **pre-migration checkpoint** | The copy of the store a boot takes before its first shape-changing write; the rollback path, deleted automatically after 7 days (D12 F4; 05-storage). |
| **quarantine** | Where a migration step copies a row it cannot decode (`c/storage/quarantine/`), reported in health and never dropped (03 §3.3 `storage`). |
| **upgrade floor** | The one floor, set in R3: `UpgradeFloor{InstallFirst: R2}`. Boot refuses a store that has not run R2's level step, before any write, with exit 78 and a last log line naming R2 (02 N14). |
| **split criteria SC1–SC10** | The measurable readiness criteria for moving core to its own repository (§12.3); named `SC` so they never collide with other labels. |
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

Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`; R4-1 refines R3-1):

- SIMP4-2, SIMP4-3 (R4-1) — goal 3 and non-goals: core reaches ABO's data through store interfaces implemented by books code; glossary: "legacy-format adapter" replaced by "store interface" and "extension family"; "cutover" brings the storage grants.
- SEC4-1, OPS4-1 (R4-2) — goal 3: a moved secret leaves a never-matching sentinel, so a rollback fails closed.
- OPS4-11 — goal 3 refers to the one cost-and-consequence table of 09 §9.2.1 instead of restating two costs.
- OPS4-5, DATA4-18 — glossary: "minimum-upgrade-from release" replaced by "floor release" and "stepping-stone release", defined by what the store has applied; goal 3 uses both.
- DATA4-20 — non-goals list the families core services use (config blob and secret rows, clock and pause keys, system log rows), not "settings" and "activity".

Integration pass (round 4):

- integration pass (round 4): glossary "store interface" lists every interface of 03 §3.3 (dirty set, integration instances, notification rules) and names `module.Manifest.LegacyStores`.

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`; resolutions R5-1…R5-7):

- R5-1 (OPS5-2) — goal 3: only plaintext secrets move and are sentinelled; the secrets HEAD already encrypts, the Basic password among them, are not moved.
- R5-6, DATA5-4 — glossary "extension family": `legacy_ref.fp` over security-relevant fields; Widening records suspended, Derived fields reset; older-build revocations stay revoked.
- DATA5-16 — glossary "store interface": S2's `app.Options.LegacyConfigStore` is the one exception to `module.Manifest.LegacyStores`.
- SIMP5-8 — glossary "store interface" and non-goals cite 03 §3.3 and §3.7 instead of restating the store and family lists.

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`; resolutions R6-1…R6-7):

- SIMP6-2, SIMP6-3 (R6-3) — glossary: "SDK" excludes `core/legacy/`; new entry "legacy package"; "store interface" names `legacy.Stores`.

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; resolutions R7-1…R7-8):

- R7-2, SIMP7-8, COR7-5, R7-7 — glossary "legacy package": names `legacy.LibraryRootSource` and `legacy.Maintenance`, deletion in the first release after the cutover that no longer sets the member, and the mirror test of 03 §3.3.

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; resolutions A-1…A-17):

- A-2 (ADV-3, ADV-S1) — goal 3: the extraction is representation-neutral, so a rollback costs no re-login and no secret re-entry; non-goals: hardening ABO's representations is the post-S10 hardening cutovers; glossary: "extension family" removed, "hardening cutover" added.
- A-15 (ADV-S9) — summary and glossary "deferred-build": deferred packages are non-normative design sketches the SDK does not freeze.
- ADV-9 — goal 6: D9 is "nothing paid"; the PolyForm Noncommercial licence meets it.

Owner decisions D10–D12:

- D10–D12 revision (2026-11-04): summary names the releases R0–R3; goal 3 rewritten (data migrated forward one-way in R2, quarantine instead of loss, G5′, rollback by offline restore, the R3 floor); non-goals: the family non-goal narrowed to books' domain families, the "hardening during the extraction" non-goal deleted, non-goals for backward compatibility (D10) and soaks (D11) added, the gin non-goal lets routes change; glossary: "legacy package", "hardening cutover", "strangler stage", "floor release" and "stepping-stone release" deleted; "SDK", "legacy claim", "store interface" and "cutover" rewritten; "release R0–R3", "flip", "importer", "pre-migration checkpoint", "quarantine" and "upgrade floor" added.
