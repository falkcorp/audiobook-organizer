<!-- file: docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md -->
<!-- version: 1.1.0 -->
<!-- guid: b0b3170b-f4c0-4030-b6ee-d69f47323db1 -->
<!-- last-edited: 2026-09-18 -->

# Pluggable Media Platform Decision Record

## Status

Proposed. This record accompanies
[`2026-09-18-pluggable-media-platform-evaluation.md`](2026-09-18-pluggable-media-platform-evaluation.md).
No application behavior changes are approved by this document.

## Decisions

1. **The host remains Go plus a bundled React/TypeScript UI.** A platform
   architecture is feasible without a language rewrite.
2. **First-party media domains are compiled-in modules.** Audiobooks, ebooks,
   comics, TV, and movies contribute a typed manifest and are selectable at
   runtime by enablement state.
3. **Integrations are capability adapters, not modules.** Metadata providers,
   indexers, download clients, media servers, and notifications get narrow
   contracts and host-owned secrets/health/permissions.
4. **Go shared-object plugins are rejected.** They do not meet portability,
   compatibility, safety, or UI requirements.
5. **External code is deferred and must be out of process.** Its eventual
   protocol is language-neutral and permissioned.
6. **Core owns platform concerns; modules own media-specific data and logic.**
   A generic nullable media-item table is rejected.
7. **Books and audiobooks initially link through shared works.** Links are
   explicit, provenance-backed, and reviewable rather than title-derived;
   edition/expression modeling is deferred until real data justifies it.
8. **The browser navigation comes from enabled module descriptors.** First-party
   UI bundles remain compiled into the web build; arbitrary remote JavaScript
   is not supported.
9. **Ebooks are the first new module.** TV and movies wait until shared
   acquisition/download contracts are real.
10. **Module enablement is restart-bound initially.** Hot enable/disable is
    deferred until routes, operations, and dependency lifecycle are reversible.
11. **The existing event bus is notification-only.** Correctness-critical
    cross-module work uses durable operations or an outbox.
12. **Audiobooks and ebooks initially link at Work, not Edition.** A richer
    bibliographic layer must be justified by real cross-format data.
13. **Existing audiobook Pebble keys remain in place during extraction.** New
    modules use namespaced keys; legacy migration is a later measured decision.

## Alternatives rejected or deferred

| Alternative | Decision | Reason |
| --- | --- | --- |
| Keep adding features to the existing audiobook model | Rejected | It compounds book-specific coupling and cannot express episodic/video domains cleanly. |
| Build a universal media schema first | Rejected | Premature abstraction would make all domains worse and harder to migrate. |
| Use Go `.so` plugins | Rejected | Poor portability and compatibility; no safe public extension boundary. |
| Start with external process plugins | Deferred | Necessary eventually for third parties, but too costly before contracts stabilise. |
| Start with TV/movie parity | Deferred | The acquisition/quality/release domain is substantially larger than ebook/comic library management. |

## Open questions to resolve before implementation planning

1. Is the first release single-user/home-server only, or must modules be scoped
   per user or tenant from the start?
2. Which ebook formats and reader integrations are required for the first
   useful ebook milestone?
3. Does “replace Sonarr/Radarr” mean personal workflow coverage or API/config
   compatibility with their existing ecosystems?
4. Should media modules share one physical storage-root abstraction only, or
   should a module be allowed to own a dedicated local/remote storage driver?
5. Which existing configuration formats and endpoints require a compatibility
   window when the audiobook module is extracted?
