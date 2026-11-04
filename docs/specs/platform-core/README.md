<!-- file: docs/specs/platform-core/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4c00242a-5629-4c0e-b66f-e81b2fb9fa7a -->
<!-- last-edited: 2026-11-04 -->

# mediacore — platform-core design spec

**Status:** draft under antagonistic review (see `docs/plans/platform-core/reviews/`).
**Evidence commit:** `748fd787a`. **Raw data:** [`docs/plans/platform-core/raw/`](../../plans/platform-core/raw/00-index.md).
**Plan:** `docs/plans/platform-core/implementation-plan.md` (written after this spec merges).

## Summary

audiobook-organizer's generic machinery — storage, durable tasks, events/outbox, config/secrets,
logging, metrics, HTTP API framework, realtime, authN, namespaced RBAC/audit, library roots,
AI/download/indexer/metadata/notification contracts, backup, updater, health, a React UI shell and an
*arr-facade extension point — is extracted into **mediacore** (`github.com/falkcorp/mediacore`), a nested
Go module at `core/` plus an npm workspace, which a Sonarr-like TV app and a Radarr-like Movies app can
share. ABO becomes app = core + module `books`, migrated as a strangler fig that ships at every stage;
core moves to its own repository when the measurable criteria in §12 hold. Composition is explicit
(`app.New(opts, modules...)`), with no `init()` registration and no process globals, so several apps can
share one binary later (lowest priority). Details: [01-summary.md](01-summary.md).

## Decisions

The superseding decision record is [02-decision-record.md](02-decision-record.md) (replaces
`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`): all 13 old decisions marked
kept / changed / dropped, 12 new decisions (N1–N12), the eight inherited acceptance gates, and the five old
open questions. User decisions D1–D9 are fixed inputs.

## Reading order

1. [01-summary](01-summary.md) → [02-decision-record](02-decision-record.md) → [03-architecture](03-architecture.md) (**normative** names, package tree, rules R1–R8)
2. [06-app-sdk](06-app-sdk.md) (what an app implements) and [04-package-mapping](04-package-mapping.md) (where every current package goes)
3. The capability contracts you need (§5 table below)
4. [09-abo-on-core](09-abo-on-core.md) (the extraction stages and compatibility windows)
5. [10](10-tv-movies-proof.md), [11](11-multi-app.md), [12](12-repo-split.md), [13](13-toolchain.md), [14](14-risks-open-questions.md), [15](15-evidence-appendix.md)

## Section → file map

| § | Section | File |
|---|---|---|
| 1 | Summary, goals, non-goals, glossary | [01-summary.md](01-summary.md) |
| 2 | Superseding decision record | [02-decision-record.md](02-decision-record.md) |
| 3 | Architecture: layers, module mechanism, package tree, rules, naming | [03-architecture.md](03-architecture.md) |
| 4 | Package mapping (all 213 packages) | [04-package-mapping.md](04-package-mapping.md) |
| 5 | Storage, migrations, keyspaces | [05-storage.md](05-storage.md) |
| 5 | Library / media roots | [05-library.md](05-library.md) |
| 5 | Tasks, jobs, scheduler | [05-tasks.md](05-tasks.md) |
| 5 | Events (notify-only) and outbox | [05-events.md](05-events.md) |
| 5 | Logging | [05-logging.md](05-logging.md) |
| 5 | Metrics and telemetry | [05-metrics.md](05-metrics.md) |
| 5 | Config and secrets | [05-config.md](05-config.md) |
| 5 | AI providers | [05-ai.md](05-ai.md) |
| 5 | Download clients | [05-download.md](05-download.md) |
| 5 | Indexers / search | [05-indexer.md](05-indexer.md) |
| 5 | Metadata providers and outbound HTTP resilience | [05-metadata.md](05-metadata.md) |
| 5 | Notifications | [05-notify.md](05-notify.md) |
| 5 | HTTP API framework, versioning, generated TS client | [05-http-api.md](05-http-api.md) |
| 5 | Realtime | [05-realtime.md](05-realtime.md) |
| 5 | Authentication | [05-authn.md](05-authn.md) |
| 5 | RBAC and audit | [05-rbac.md](05-rbac.md) |
| 5 | Backup and restore | [05-backup.md](05-backup.md) |
| 5 | Updater | [05-updater.md](05-updater.md) |
| 5 | Health and diagnostics | [05-health.md](05-health.md) |
| 6 | App/module SDK and composition | [06-app-sdk.md](06-app-sdk.md) |
| 7 | UI shell | [07-ui-shell.md](07-ui-shell.md) |
| 8 | *arr facade | [08-arr-facade.md](08-arr-facade.md) |
| 9 | ABO on core | [09-abo-on-core.md](09-abo-on-core.md) |
| 10 | TV and Movies proof sketches (+ ebooks note) | [10-tv-movies-proof.md](10-tv-movies-proof.md) |
| 11 | Multi-app in one binary | [11-multi-app.md](11-multi-app.md) |
| 12 | Repo split | [12-repo-split.md](12-repo-split.md) |
| 13 | Toolchain and versions | [13-toolchain.md](13-toolchain.md) |
| 14 | Risks and open questions | [14-risks-open-questions.md](14-risks-open-questions.md) |
| 15 | Evidence appendix | [15-evidence-appendix.md](15-evidence-appendix.md) |
