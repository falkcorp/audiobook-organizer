<!-- file: docs/specs/platform-core/README.md -->
<!-- version: 1.7.0 -->
<!-- guid: 4c00242a-5629-4c0e-b66f-e81b2fb9fa7a -->
<!-- last-edited: 2026-11-04 -->

# mediacore — platform-core design spec

**Status:** draft under antagonistic review (see `docs/plans/platform-core/reviews/`). Round 1's 192
findings, round 2's 162, round 3's 130, round 4's 117, round 5's 115, round 6's 80 and round 7's 96 are
dispositioned (`spec-round-{1,2,3,4,5,6,7}-dispositions.md`) and applied to the files. Round 3's resolutions R3-1…R3-5 are
binding and supersede round-2 R2-1…R2-3: during the extraction every key family keeps one stored format
(no adoption copies or mirrors), and per-family cutover to native keyspaces is deferred (02 decision 13,
N15). Round 4's resolutions R4-1…R4-6 are binding; R4-1 refines R3-1: core reaches ABO's data through SDK
**store interfaces** whose legacy implementation the `books` module supplies, so core holds no audiobook
key name and no storage grant during the extraction, and a moved secret leaves a never-matching sentinel
(R4-2). Round 5's resolutions R5-1…R5-7 are binding and close local consistency and timing defects: only
plaintext secrets move (the four HEAD already encrypts, the Basic password among them, stay in place) and
HEAD's precedence for legacy keys holds until cutover (R5-1); legacy store packages import downward only
(R5-2); extension fingerprints cover security-relevant fields only and older-build revocations stay revoked
(R5-6); the UI build ID and the binary ID are separate values (R5-7). Round 6's resolutions R6-1…R6-7 are
binding: every write to a serialized family, progress ticks and ledger commits included, is checked at
commit (R6-1); `internal/operations/state` stays in ABO (49 packages leave, 5 new, 169 remain; R6-2); every
identifier that exists only for an app's legacy stores lives in one package, `legacy`, outside the API
promise like `kernel`, so a cutover removes it without a breaking change (R6-3); a staged restore is not a
foreign open (R6-4); API-key writes never race, shipped early as the sixth early fix (R6-5); an operator
value that enables auth always takes effect (R6-6); staging copies are scrubbed and inert before they boot
(R6-7). Round 7's resolutions R7-1…R7-8 are binding: operator-enabled auth survives a rollback, because
the legacy config store writes the narrowing legacy value (R7-1); a factory reset only stages, nothing
destructive runs before the restart, its core route lands at S4 r2 in `internal/storagehttp`, and whether
it keeps HEAD's library and Open Library wipes is owner question Q-K (R7-2); run-store write checking is
per guard row (R7-3); a restore boot or offline command never yields a false "replaced store" verdict
(R7-4); the S4 r2 upgrade signs no one out (R7-5); staging has one inert, measurable procedure (R7-6);
mirror tests live in the owner's external test package (R7-7); a file whose ABO imports have no
import-path-only replacement is a re-implementation, not a move (R7-8).
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
| Per-family cutover of the ABO families core services use (03 §3.7 names them; the store interfaces are 03 §3.3's one list) to native core keyspaces | [02](02-decision-record.md) N15, [05-storage](05-storage.md) |
| Members of built packages that only that cutover can use: storage grants (`Grant`, `GrantMode`) and the served-run preconditions; backup encryption; the seed-role merge diff | [05-storage](05-storage.md) §5S.3.13, [05-backup](05-backup.md) B3, [05-rbac](05-rbac.md) §4.8 |
| `Online` data migrations (read adapters, the kernel-synthesised `<ID>.datamigrate.<name>` kind): no S0–S10 consumer; extraction data migrations are `BootBlocking` | [05-storage](05-storage.md) §5S.3.14, [03 §3.3](03-architecture.md) `storage/datamigrate` |
| The config basis mechanism (`config.Basis` and the suspension and confirmation of config extension records it drives): no key of S0–S10 declares a basis | [05-config](05-config.md) §1.12.5, [03 §3.4](03-architecture.md) |
| Import-root extension suspension and confirmation (`ReadShares` on a legacy-served root, its `confirm` route and health token): a legacy-served root takes no shares and ABO has one module | [05-library](05-library.md) `Deferred-build members`, [03 §3.4](03-architecture.md) |

The Build column of 03 §3.3 is the authoritative list of deferred packages and is re-checked at every
stage gate. Deferral also applies to members of built packages (03 §3.4): each file lists them under a
`Deferred-build members` heading (an optional section number may precede it), whose first column holds only
package-qualified Go identifiers, and `archtest.DeferredMembersAbsent` checks the list generated from every
such heading from S2.

## Decisions

The superseding decision record is [02-decision-record.md](02-decision-record.md) (replaces
`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`): all 13 old decisions marked
kept / changed / dropped (decision 13 extended in round 3: one stored format per family during the
extraction; amended in round 4: store interfaces with books-supplied legacy implementations, native-only
data in a books-owned extension family, moved secrets replaced by a never-matching sentinel; amended in
round 5: only plaintext secrets move, HEAD's legacy-key precedence holds until cutover, extension
fingerprints cover security-relevant fields only; amended in round 6: legacy-only identifiers in the
`legacy` package outside the API promise, and an operator value that enables auth always takes effect;
amended in round 7: that operator tightening survives a rollback through one narrowing legacy write), 15 new
decisions (N1–N15; N13, the round-2 Teach-build rollback floor, is superseded; N14 names the **floor
release** that deletes books' legacy migration code and the **stepping-stone release** a store must run
first, checked on the store; N15 defers per-family cutover and the storage grants with it), the eight
inherited acceptance gates, and the five old open questions. User decisions D1–D9 are fixed inputs.
Owner questions still open (including Q-I, how the one release-gating requirement — every PR passes the
named required checks, `make ci`'s CI jobs, `extraction-gates`, the image job and `core-ci`, before merge —
is enforced), and those decided as adopted recommendations, are in
[14-risks-open-questions.md](14-risks-open-questions.md) §14.3.

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


Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`; R4-1 refines R3-1):

- SIMP4-2 (R4-1), SEC4-1 (R4-2) — status line: round 4 dispositioned and applied; store interfaces with books-supplied legacy implementations; sentinel for moved secrets.
- DATA4-20 — "Build scope": the cutover row lists the families core services actually use (config blob and secret rows, clock and pause keys, system log rows), not "settings" and "activity".
- SIMP4-4, SIMP4-5, SIMP4-6 (with R4-1) — "Build scope": row for the members only the cutover can use (storage grants, served-run preconditions), backup encryption and the seed-role merge diff.
- SIMP4-1 — "Build scope": deferred members listed under `Deferred-build members` headings; the check's list is generated from every heading.
- OPS4-5 — decisions paragraph: N14's floor release and stepping-stone release named distinctly.
- OPS4-7, MOD4-8 — decisions paragraph: Q-I's requirement is a set of named required checks.

Integration pass (round 4):

- integration pass (round 4): "Build scope": deferred cutover members are `Grant` and `GrantMode` (`Exclusive` deleted).

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`; resolutions R5-1…R5-7):

- R5-1, R5-2, R5-6, R5-7 — status line: round 5 dispositioned and applied; the binding resolutions summarised; decisions paragraph: decision 13's round-5 amendment (plaintext secrets only, legacy-key precedence, security-relevant fingerprints).
- SIMP5-7 — "Build scope": `Online` data migrations are deferred-build members.
- SIMP5-6, COR5-16 — "Build scope": `Deferred-build members` headings may carry a section number; first column package-qualified identifiers only.
- SIMP5-8 — "Build scope": the cutover row cites 03 §3.7 and §3.3 instead of restating the family list.

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`; resolutions R6-1…R6-7):

- R6-1…R6-7 (DATA6-1, MOD6-1, SIMP6-2, SIMP6-3, DATA6-2, SEC6-3, SEC6-1, OPS6-4) — status line: round 6 dispositioned and applied; the binding resolutions summarised; decisions paragraph: decision 13's round-6 amendment (`legacy` package, auth-enable exemption).
- SIMP6-1 — "Build scope": the config basis mechanism is deferred-build (no consumer in S0–S10).

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; resolutions R7-1…R7-8):

- R7-1…R7-8 — status line: round 7 (96 findings) dispositioned and applied; the binding resolutions summarised; decisions paragraph: decision 13's round-7 amendment (R7-1).
- SIMP7-9 — Build scope: the config basis deferral cites 05-config §1.12.5.
- SIMP7-2 — Build scope: new row for import-root extension suspension and confirmation (05-library deferred-build members).
