<!-- file: docs/architecture/2026-09-18-pluggable-media-platform-adversarial-review.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5ae1b683-9a58-43f9-b96c-870a89e01926 -->
<!-- last-edited: 2026-09-18 -->

# Pluggable Media Platform Adversarial Review

## Purpose

This review tries to disprove the recommendations in the companion
[platform evaluation](2026-09-18-pluggable-media-platform-evaluation.md).
It uses current repository behavior and hostile operational scenarios rather
than treating architectural neatness as evidence.

## Outcome

The central recommendation survives: retain Go, use compiled-in first-party
modules, distinguish modules from integrations, and defer third-party code to
an out-of-process boundary. Five important parts required correction:

1. module enable/disable must initially be restart-bound, not hot;
2. the current event bus cannot carry correctness-critical workflows;
3. audiobooks and ebooks should initially link at Work, not Edition;
4. existing audiobook keys should be adapted in place, not renamed during
   extraction;
5. the original delivery ranges were too optimistic.

## Workflow 1 — “Disable means disabled” failure injection

### Attack

Enable or disable a plugin after startup while it owns HTTP routes, scheduled
operations, background goroutines, and dependencies required by another
module. Verify that the state transition removes or restores all effects.

### Evidence

- `internal/server/plugins_init.go` calls `InitAllScoped` once near the end of
  server construction.
- `internal/plugin.Registry.Enable` and `Disable` only mutate a boolean map.
- `PluginsHandler.EnablePlugin` and `DisablePlugin` mutate that map and the
  in-memory config map; neither calls `Init`, `Shutdown`, route removal, or
  configuration persistence.
- Gin routes are added to a live route tree with no matching unregister path.
- `internal/serviceregistry.Container` resolves, builds, post-initializes, and
  starts a fixed graph. It has no per-service runtime remove operation.
- `server.go` includes the entire `plugins` service group before the runtime
  plugin registry is initialized.

### Verdict

**Original conclusion weakened.** Runtime selection is feasible, but safe hot
enable/disable is not. Stage 1 should persist desired enablement and apply it at
the next restart. Hot transitions become a separate feature with operation
draining, reversible route dispatch, dependency accounting, and lifecycle
tests.

## Workflow 2 — event loss and partial failure

### Attack

Publish a cross-module “import completed” event, then crash the process before a
subscriber commits its database changes. Also make one subscriber return an
error while another succeeds.

### Evidence

`internal/plugin.EventBus.Publish` launches each subscriber in a goroutine,
recovers panics, logs errors, and returns immediately. There is no persisted
offset, acknowledgement, retry, ordering, backpressure, or replay.

### Verdict

**Original conclusion flawed.** The bus is suitable for notifications and
best-effort reactions, not state transitions. Cross-module correctness requires
a durable operation, a transactional outbox, or an explicit synchronous API.
The platform kernel must not advertise the current bus as durable integration
infrastructure.

## Workflow 3 — bibliographic counterexamples

### Attack

Try to link these pairs as one edition:

- an unabridged audiobook and an abridged audiobook;
- an ebook translated from a later revision and an audiobook of the original;
- an audiobook with a dramatized cast and an ebook;
- EPUB and PDF files whose publisher metadata disagree;
- an audiobook whose only identifier is an ASIN and an ebook with an ISBN.

### Evidence

The current `database.Work` explicitly spans editions, narrations, languages,
and publishers. It contains title, author, series, and alternate titles, while
`database.Book` carries audiobook-specific release, narration, playback, audio,
and external-ID fields. The repository does not currently have a reliable
cross-format edition identity.

### Verdict

**Original conclusion overturned.** Work is the only defensible initial link.
Edition/expression modeling should wait for observed ebook data and explicit
matching evidence. Adding it now would give uncertain data a falsely precise
schema.

## Workflow 4 — “SDK” compatibility attack

### Attack

Attempt to build an independently versioned plugin module against the public
SDK without importing repository-internal types.

### Evidence

`pkg/plugin/sdk/capability.go` aliases
`internal/operations/registry.Capability`. The other SDK interfaces focus on
operation registration and still describe the product as an audiobook
organizer. Meanwhile, lifecycle/routes/config live in `internal/plugin`, and
service construction lives in `internal/serviceregistry`.

### Verdict

**The current SDK is not a platform SDK.** It is an in-repository operation
facade. Stages 0–1 must define ownership and version-neutral DTOs before
anything is promised as an external contract. The useful operation definitions
can be adapted rather than discarded.

## Workflow 5 — compile-time module isolation attack

### Attack

Disable the audiobook module and build or run the host without its code,
database interfaces, routes, or UI imports. Check whether another package still
requires `database.Book` or the broad store.

### Evidence

- 843 Go files under `internal` refer to `database.Book` forms.
- 197 Go files under `internal` refer to `database.Store`.
- `internal/server` contains 630 Go files and `internal/database` contains 384.
- `server.go` has 1,379 lines, `database/store.go` has 1,441, and the principal
  React library page has 2,438.
- The UI routes and sidebar entries are statically declared in `App.tsx` and
  `Sidebar.tsx`.

These are rough coupling indicators, not a claim that every file needs manual
rewriting. They do show that “extract Audiobooks” is a strangler migration, not
a package move.

### Verdict

**Architecture survives; schedule does not.** Compiled-in modules remain the
lowest-risk target, but the host will still contain audiobook compatibility
adapters for several releases. The evaluation's estimates were widened.

## Workflow 6 — UI injection and version-skew attack

### Attack

Return a server module manifest containing an unknown component name, a route
collision, a stale frontend module version, or a malicious JavaScript URL.

### Evidence

The current React application compiles explicit component imports and route
elements. A JSON server manifest can select trusted compiled components, but it
cannot safely manufacture a React component or load untrusted code.

### Verdict

**Original explanation incomplete.** The client needs its own compile-time
registry keyed by module ID. At startup it intersects the trusted client
registry with the enabled server manifest. Unknown IDs and version mismatches
produce diagnostics. Remote JavaScript URLs are forbidden.

## Workflow 7 — storage migration rollback attack

### Attack

Rename every existing `book:*` key into an `audiobooks:*` namespace while the
server has large libraries, secondary indexes, backups, interrupted upgrades,
and a possible need to roll back to the prior binary.

### Evidence

The Pebble store has numerous hand-maintained book indexes and sidecars. Recent
repository comments document stale-index and data-loss regressions caused by
whole-row updates and incomplete index teardown. A mass key rewrite would
multiply those hazards and make binary rollback difficult.

### Verdict

**Original migration implication corrected.** New modules use namespaced keys.
The audiobook module initially owns an adapter over the legacy keyspace. A
future online migration needs dual-read/dual-write or a separately verified
offline process; it is not a prerequisite for modularity.

## Workflow 8 — Sonarr/Radarr parity reality check

### Attack

Treat “replace Sonarr and Radarr” as compatibility with mature personal media
automation: indexer protocols, release parsing, quality profiles, custom
formats, monitoring/wanted state, interactive search, download-client history,
remote path mapping, failed-download handling, import decisions, rename/upgrade
logic, calendar, notifications, and years of edge cases.

### Evidence

The current application has strong file, job, metadata, and Deluge foundations,
but its core domain is curated local audiobooks. It does not yet have a generic
release-candidate model, quality/custom-format scoring engine, episodic wanted
state, or a protocol ecosystem comparable to the Arr applications.

### Verdict

**Goal survives only when scoped.** A personal-workflow replacement is
plausible. Broad parity is a multi-year product program, not one module. The
evaluation now assigns 9–18 engineer-months to shared automation plus useful TV
and movie modules, with parity remaining an explicit product decision.

## Workflow 9 — Go plugin mechanism challenge

### Attack

Reconsider whether rejecting Go shared-object plugins was bias rather than
evidence.

### Evidence

The Go standard-library documentation warns that `plugin` is supported only on
Linux, FreeBSD, and macOS; race detector support is poor; deployment and
initialization are harder; untrusted libraries are dangerous; and runtime
crashes are likely unless host and plugin use exactly compatible toolchains,
build tags, flags, and environment. It also notes that generating imports and
building a static executable can be simpler.

Source: [Go `plugin` package documentation](https://pkg.go.dev/plugin).

### Verdict

**Original conclusion survives strongly.** Static first-party composition and
eventual out-of-process third-party integrations remain the safer split.

## Revised non-negotiable gates

Before implementation advances beyond the platform kernel, prove these cases:

1. A disabled module contributes no route, scheduled operation, background
   worker, navigation item, or filesystem mutation after restart.
2. A failed module migration prevents startup without corrupting its prior data.
3. The client rejects unknown or version-incompatible module descriptors.
4. No correctness-critical state transition depends solely on the in-memory
   event bus.
5. The existing audiobook database opens unchanged under the module adapter and
   remains readable by the previous release.
6. Adding a dummy second module requires no edit to audiobook packages.
7. The platform host can be tested with the audiobook module omitted from the
   selected startup graph, even if the binary still contains its code.
8. Resource/write-set identities are module-namespaced so unrelated media work
   can run concurrently without collisions.

## Residual risks

- A “small” core can still become a god platform package if it absorbs every
  abstraction used twice. Promotion into core should require two working module
  consumers.
- Compile-time module UI code remains present in the binary and browser assets;
  disablement is behavior isolation, not code removal or a hard security
  sandbox.
- Pebble key namespaces provide organization, not access control. Store APIs
  and tests must enforce module ownership.
- Transport-neutral DTOs can preserve a future RPC option, but pretending every
  internal call is remote-ready would add needless serialization and weaken Go
  types. Neutrality belongs at module boundaries only.
- The single process remains a shared failure domain. First-party modules are
  trusted code; only external processes provide crash isolation.

## Final adversarial assessment

Proceed with the architecture, but call it a **modular monolith with trusted
first-party modules**, not a general plugin platform yet. The first milestone
should prove startup selection, API/UI contribution, durable operation
registration, and legacy audiobook compatibility. If that milestone cannot
omit a dummy module cleanly, stop before adding ebooks—the boundary is not real.
