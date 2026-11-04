<!-- file: docs/specs/platform-core/01-summary.md -->
<!-- version: 1.8.0 -->
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
   every key family keeps one stored format, written only by ABO code: core reaches ABO's data through
   store interfaces whose implementation the `books` module supplies, so the previous release reads
   every row the new build wrote (02 decision 13). The extraction is **representation-neutral**: sessions,
   one-time tokens, API keys, role scopes and secrets keep HEAD's stored form, so a rollback costs no
   re-login and no secret re-entry (A-2). A rollback's costs and consequences are listed once, in the table
   of 09 §9.2.1. An install
   whose store has not applied the books migrations that the floor release requires installs the
   stepping-stone release first (02 N14).
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
- Renaming or moving any of ABO's existing 258 Pebble key families during the extraction (02
  decision 13): core services use the generic ones (03 §3.7 names them) in place through the store
  interfaces of 03 §3.3, whose implementation over those keys is books code. Moving a family to a native keyspace is a later, one-way **cutover** (02 N15), not
  an extraction stage.
- Hardening ABO's stored representations during the extraction (hashed sessions, `mck_` keys and native
  scopes in ABO's stores, encrypted plaintext secrets, operator precedence for the auth switch): these are
  the post-S10 **hardening cutovers** of 02 N15 (A-2).
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
| **SDK** | Core's public, versioned packages — everything outside `core/internal/`, `core/kernel/` and `core/legacy/`. Measured by the API-stability gate (§12). |
| **legacy package** | `core/legacy` (with `legacy/legacytest`): the one core package holding every identifier that exists only to serve an app's legacy stores — the optional legacy-only capabilities, `legacy.Stores`, the legacy-only `legacy.LibraryRootSource`, the pre-core maintenance handle `legacy.Maintenance` (R7-2) and their conformance suites. Public, so ABO imports it, but outside the API promise like `kernel`, so the first release after a family's cutover that no longer sets its member deletes its identifiers without a breaking change (03 §3.3; R6-3, COR7-5). Owners never import it; each checks its unexported mirror of a capability in the mirror test of 03 §3.3 (R7-7). |
| **kernel** | Core's composition machinery (`core/kernel/...`): module catalog, manifest validation, lifecycle supervisor and the typed services struct (no DI container). Imported only by `core/app`. |
| **deferred-build** | A core package described by a non-normative design sketch at proof depth, which the SDK does not freeze and the extraction plan does not build (03 §3.4; A-15): `acquire`, `arrfacade`, `integrations/indexer`, the download grab/monitor pipeline. Designed and built when the first TV/Movies app starts. |
| **legacy mount** | ABO's gin router mounted through `httpapi.MountLegacy` behind core's perimeter middleware; permanent (02 N7). |
| **entity.Ref** | The one polymorphic reference `{Module, Kind, ID}` to a module-owned entity (03 §3.3); `tasks.Resource` is this type. |
| **app** | A shipped binary: one `main` that calls `app.New` with its modules. ABO is an app. |
| **module** | A self-contained, compiled-in domain implementing `module.Module` (e.g. `books`, `tv`). Identified by a module ID that namespaces its permissions, keys, routes, tasks, topics and config. |
| **adapter** | An implementation of a core capability contract (e.g. `integrations/download/deluge`). Configured per instance. |
| **capability contract** | A narrow Go interface in `core/integrations/*` (AI, download, indexer, metadata, notify). |
| **keyspace** | A prefix of the shared Pebble database handed to one owner: `c/<subsystem>/` for core, `m/<module>/` for modules. |
| **legacy claim** | A module's declaration that it owns pre-existing un-namespaced key families (ABO's 258), so they stay in place. |
| **store interface** | The SDK interface through which a core service persists state an app may already hold; the one list is 03 §3.3 "Store interfaces" (SIMP5-8). Core ships a **native implementation** over its own keyspace, used by new apps and `hello`; an app with pre-core data supplies a **legacy implementation** over its existing keys on `module.Manifest.LegacyStores` (a `legacy.Stores` value; for ABO, books code; S2's `app.Options.LegacyConfigStore` bridge is the one exception, DATA5-16), so core holds no app key name and no storage grant during the extraction (02 decision 13, R4-1). |
| **cutover** | The later, per-family, one-way step that moves a family to a native core keyspace and deletes the legacy rows; outside the extraction, checkpointed, behind the per-owner level guard; the only step in which core reads an app's rows, so storage grants land with it; its rollback floor is the cutover release itself (02 N15). |
| **hardening cutover** | One of the three named post-S10 cutovers on 02 N15's machinery that harden what the representation-neutral extraction keeps as at HEAD: **identity** (hashed sessions and one-time tokens, `mck_` keys, native scopes), **secrets** (encrypt the plaintext blob secrets) and **auth precedence** (A-2; 12 §12.4.4). |
| **task** | A durable unit of background work in `core/tasks` (formerly "operation"/"op"/"job"); kind `<module>.<name>`. |
| **Parallelism** | The required declaration of how a library-scale loop runs (`tasks.CPU()`, `tasks.Fixed(n, reason)`, `tasks.Sequential(reason)`; §05-tasks). |
| **outbox** | A durable queue written in the same Pebble batch as the state change it announces; relayed at least once. |
| **permission scope** | `<module>:<action>[.<sub>]`, e.g. `books:edit`, `tv:request`, `core:admin`. |
| **shell** | The `@mediacore/shell` React package: auth, layout, navigation, settings frame, tasks, logs, users/roles, notification admin pages for the v1 scope (contract, mail and webhook channels, global routing) plus toasts (no inbox or personal channels), health. |
| ***arr facade** | An optional, per-app HTTP adapter exposing an *arr-v3-compatible subset for ecosystem clients (Overseerr/Jellyseerr, Prowlarr, Bazarr) through `core/arrfacade` (deferred-build). |
| **ABO** | audiobook-organizer, this repository. |
| **strangler stage** | One step of the extraction in §09 (S0–S10, with S2b and UI-0), after which ABO is still shippable. "Wave" is not used. |
| **floor release** | The first release that deletes books' legacy migration code (and the code paths it calls; the 05-tasks T8 deletion) and declares books' upgrade floor. Whether a store may take it is a property of the **store** — the books migrations it has applied, recorded in its applied-steps sidecar — not of the binary being replaced (02 N14). |
| **stepping-stone release** | The release a floor release's manifest names to install first (`InstallFirst`): it carries books' legacy migrations and contains no deletion. A store that has not applied what the floor release requires runs it first; an updater from S0 on offers it instead of the floor release, and boot refuses with a message naming it (02 N14). Never the same release as the floor release (OPS4-5). |
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
