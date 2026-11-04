<!-- file: docs/specs/platform-core/README.md -->
<!-- version: 1.3.0 -->
<!-- guid: 4c00242a-5629-4c0e-b66f-e81b2fb9fa7a -->
<!-- last-edited: 2026-11-04 -->

# mediacore — platform-core design spec

**Status:** draft under antagonistic review (see `docs/plans/platform-core/reviews/`). Round 1's 192
findings, round 2's 162 and round 3's 130 are dispositioned (`spec-round-{1,2,3}-dispositions.md`) and
applied to the files. Round 3's resolutions R3-1…R3-5 are binding and supersede round-2 R2-1…R2-3: during
the extraction every key family keeps one stored format (legacy-format adapters, no adoption copies or
mirrors), and per-family cutover to native keyspaces is deferred (02 decision 13, N15).
**Evidence commit:** `748fd787a`. **Raw data:** [`docs/plans/platform-core/raw/`](../../plans/platform-core/raw/00-index.md).
**Plan:** `docs/plans/platform-core/implementation-plan.md` (written after this spec merges).

## Summary

audiobook-organizer's generic machinery — storage, durable tasks, events/outbox, config/secrets,
logging, metrics, HTTP API framework, realtime, authN, namespaced RBAC/audit, library roots,
AI/download/metadata/notification contracts and adapters, backup, updater, health and a React UI shell —
is extracted into **mediacore** (`github.com/falkcorp/mediacore`), a nested Go module at `core/` plus an
npm workspace, which a Sonarr-like TV app and a Radarr-like Movies app can share. ABO becomes
app = core + module `books`, migrated as a strangler fig that ships at every stage; its gin router stays
mounted behind core's HTTP perimeter permanently. Core moves to its own repository when the measurable
split criteria SC1–SC10 in §12 hold. Composition is explicit (`app.New(opts, modules...)`), with no `init()`
registration and no process globals in core, so several apps can share one binary later (lowest
priority). Details: [01-summary.md](01-summary.md).

## Build scope

Core builds only what ABO consumes during the extraction or what core itself needs
([03 §3.4](03-architecture.md)). These parts are specified as **contracts only** and marked
**deferred-build** — the extraction plan builds none of them; they are built when the first TV/Movies
app starts:

| Deferred | Specified in |
|---|---|
| `acquire` (quality profiles, custom formats, decision engine, grab decisions, tracked downloads, import hand-off) | [10-tv-movies-proof](10-tv-movies-proof.md) |
| `integrations/indexer` (Newznab/Torznab) | [05-indexer](05-indexer.md) |
| `arrfacade` (*arr-v3-compatible API; only the `/api/v3/` path reservation exists meanwhile) | [08-arr-facade](08-arr-facade.md) |
| Download grab/monitor/pending-recovery pipeline and the adapters other than Deluge | [05-download](05-download.md) |
| AI usage ledger and monthly cap; notify inbox, per-user subscriptions, personal channels, templating | [05-ai](05-ai.md), [05-notify](05-notify.md) |
| Facade-only members of built packages: key and route machinery in `auth`/`httpapi` (`PurposeFacade`, query-key extraction, `FacadeGroup`) and the auth feed token | [05-authn](05-authn.md) §3.5, [05-http-api](05-http-api.md) §3.6 |
| Per-family cutover of ABO's generic families (users, roles, API keys, settings, ops history, activity) to native core keyspaces | [02](02-decision-record.md) N15, [05-storage](05-storage.md) |

The Build column of 03 §3.3 is the authoritative list of deferred packages and is re-checked at every
stage gate. Deferral also applies to members of built packages (03 §3.4); `archtest.DeferredMembersAbsent`
checks those from S2.

## Decisions

The superseding decision record is [02-decision-record.md](02-decision-record.md) (replaces
`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`): all 13 old decisions marked
kept / changed / dropped (decision 13 extended in round 3: one stored format per family during the
extraction), 15 new decisions (N1–N15; N13, the round-2 Teach-build rollback floor, is superseded; N14
adds the minimum-upgrade-from release, checked on the store; N15 defers per-family cutover), the eight
inherited acceptance gates, and the five old open questions. User decisions D1–D9 are fixed inputs.
Owner questions still open (including Q-I, how the one release-gating requirement — every PR passes
`make ci` with the extraction gates before merge — is enforced), and those decided as adopted
recommendations, are in [14-risks-open-questions.md](14-risks-open-questions.md) §14.3.

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
| 5 | Indexers / search (deferred-build) | [05-indexer.md](05-indexer.md) |
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
| 8 | *arr facade (deferred-build) | [08-arr-facade.md](08-arr-facade.md) |
| 9 | ABO on core | [09-abo-on-core.md](09-abo-on-core.md) |
| 10 | TV and Movies proof sketches, `acquire` contract (deferred-build) (+ ebooks note) | [10-tv-movies-proof.md](10-tv-movies-proof.md) |
| 11 | Multi-app in one binary | [11-multi-app.md](11-multi-app.md) |
| 12 | Repo split | [12-repo-split.md](12-repo-split.md) |
| 13 | Toolchain and versions | [13-toolchain.md](13-toolchain.md) |
| 14 | Risks and open questions | [14-risks-open-questions.md](14-risks-open-questions.md) |
| 15 | Evidence appendix | [15-evidence-appendix.md](15-evidence-appendix.md) |

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- SIMP-1 — "Build scope" section lists the deferred-build parts; section map marks them.
- SIMP-2 — summary: gin stays mounted permanently behind core's perimeter.
- SIMP-3 — summary: no process globals **in core**.
- SEC-19, SEC-20 — decisions paragraph points at 14 §14.3 for open and decided owner questions.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-1, R2-4 — decisions paragraph: 14 new decisions (N13 Teach-build rollback floor, N14 minimum-upgrade-from release). (N13 superseded in round 3 by R3-1.)
- R2-6 — decisions paragraph points at owner question Q-I (gates as required checks). (Restated in round 3 by R3-2.)
- MOD2-21 — summary names the split criteria SC1–SC10.
- Status line: round 2 dispositioned and applied.

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — status line and decisions paragraph: one stored format per family; N13 superseded; N15 cutover deferred (15 new decisions); "Build scope" lists the deferred cutover.
- R3-2 — decisions paragraph: Q-I asks only how the single requirement (every PR passes `make ci` with the extraction gates) is enforced.
- SIMP3-11 — "Build scope": row for the facade-only members of built packages (05-authn §3.5, 05-http-api §3.6); note that deferral covers members and is checked by `archtest.DeferredMembersAbsent`.
- Status line: round 3 dispositioned and applied.

