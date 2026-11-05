<!-- file: docs/specs/platform-core/README.md -->
<!-- version: 1.13.0 -->
<!-- guid: 4c00242a-5629-4c0e-b66f-e81b2fb9fa7a -->
<!-- last-edited: 2026-11-05 -->

# mediacore — platform-core design spec

**Status:** draft. Twelve review rounds, the advisor review and a plan-phase consistency pass are dispositioned and
applied (`docs/plans/platform-core/reviews/`, `spec-*-dispositions.md`), and round 13 is applied on top. The owner decisions **D10–D13**
(2026-11-04; 02 §2.6) then removed backward compatibility and soaks: they override every earlier resolution that
kept a compatibility window, an alias, HEAD's stored representation, binary-swap rollback past a migration, or a
soak gate, and the files were revised to match; D13 then fixed core's module path, public repository and Apache-2.0
licence, made an unconvertible identity row or an unfinished account merge stop the upgrade, and removed old-URL
redirects from the UI shell. The resolutions that do not depend on compatibility (R2-n…R11-n,
A-1…A-17, FIN12-n) stay binding.

The design, in brief:

- **Four releases** (03 §3.8): **R0** hotfixes (the six early security fixes, the toolchain bump, the updater
  hotfix); **R1** the core runtime with no user-data change (nested module, composition root, perimeter, logging,
  core storage with strict claims, the one journaled swap, the first UI steps: react-router 8 and the api-client); **R2** the one-way cutover of every core-service
  family plus the UI work, opened by F2, the dark foundation PRs; **R3** cleanup and the one upgrade floor. Code lands
  dark on `main`, each subsystem is wired in one flip PR, and every merge passes `make ci` and e2e; there are no
  soaks, and auto-promotion pauses from the first R2 flip until R2 ships, and by a lead extension during R1's PR
  series too (D11, D12 (a); D12's answers are cited as (a)–(d)).
- **Core services own their state natively** (03 §3.3 "Store interfaces and importers"): each persists in its own
  `c/<subsystem>/` keyspace. In R2 books' migration steps decode ABO's HEAD rows and hand them to each subsystem's
  importer, so core still holds no audiobook key name; books' own domain families stay in place.
- **One-way, checkpointed migration** (03 §3.7, §3.8): the boot takes the pre-migration checkpoint after a disk
  headroom check, runs M1–M8, quarantines rows it cannot decode (an unconvertible identity row stops the upgrade
  instead, D13), and raises the levels so older binaries refuse the store. Rollback is an offline restore of the
  checkpoint or an off-host archive (keyring and state files included) plus the pre-freeze release; every checkpoint
  is deleted 7 days after it stops being needed (D12 (d)).
- **What survives R2** (D12 (b)): API keys and the Audiobookshelf phone apps' sessions; task-run and log history,
  converted. Web sessions and one-time tokens are reset; throttle holds, indexes and caches are rebuilt.
- **The hardened design lands directly in R2:** hashed sessions and one-time tokens, `mck_` keys, native scopes,
  encrypted secrets, and operator-locked config with one fail-closed rule for the auth and Basic-gate keys.
- **One frozen external contract** (D12 (c)): the Audiobookshelf-compatible API of the phone apps, pinned by its
  conformance suite on the migrated fixture. Config keys, env names, metric names and HTTP paths may change; the
  dashboards, rules, scripts and skills change in the same PR, and a set retired security or DB-path env name
  refuses boot.
- **One owner per route-conflict set and per metric series** (R9-2, R9-3, R10-1): core's perimeter records every
  HTTP request, and a test checks that every series the shipped dashboards and rules name is served.
- **No predictive stage machinery** (A-4): Go's `internal/` rule, the layering and SDK guards and
  `TestCoreHoldsNoAppKeyNames` stay; 04 is an informative map. Every (GOOS, tag set) that ships is vetted on every
  PR (R9-10, R10-9).
- **Boot and operations safety:** HEAD's gin routes register with a same-owner collision rule (A-1); one crash
  matrix covers the whole boot and every migration step (A-8, R9-7); a factory reset only stages (offline only in R1), as a kind
  of the one journaled swap, under one confirmation contract, `storage.ResetRequest` (R7-2, R8-1, R9-6); concurrent
  feature work has move windows (A-11).
- Deferred-build contracts are **non-normative design sketches** (A-15).

**Evidence commit:** `748fd787a`. **Raw data:** [`docs/plans/platform-core/raw/`](../../plans/platform-core/raw/00-index.md).
**Plan:** `docs/plans/platform-core/implementation-plan.md` (written after this spec merges).

## Summary

audiobook-organizer's generic machinery — storage, durable tasks, events/outbox, config/secrets,
logging, metrics, HTTP API framework, realtime, authN, namespaced RBAC/audit, library roots,
AI/download/metadata/notification contracts and adapters, backup, updater, health and a React UI shell —
is extracted into **mediacore** (`github.com/falkcorp/mediacore`), a nested Go module at `core/` plus an
npm workspace, which a Sonarr-like TV app and a Radarr-like Movies app can share. ABO becomes
app = core + module `books`, in four releases R0–R3 with one data cutover (R2); its gin router stays
mounted behind core's HTTP perimeter permanently. Core moves to its own repository when the measurable
split criteria SC1–SC10 in §12 hold. Composition is explicit (`app.New(opts, modules...)`), with no `init()`
registration and no process globals in core, so several apps can share one binary later (lowest
priority). Details: [01-summary.md](01-summary.md).

## Build scope

Core builds only what ABO consumes during the extraction or what core itself needs
([03 §3.4](03-architecture.md)). These parts are described as **non-normative design sketches** and marked
**deferred-build** — the SDK freezes none of them and the extraction plan builds none of them; they are
designed and built when the first TV/Movies app starts (A-15):

| Deferred | Design sketch (non-normative) in |
|---|---|
| `acquire` (quality profiles, custom formats, decision engine, grab decisions, tracked downloads, import hand-off) | [10-tv-movies-proof](10-tv-movies-proof.md) |
| `integrations/indexer` (Newznab/Torznab) | [05-indexer](05-indexer.md) |
| `arrfacade` (*arr-v3-compatible API; only the `/api/v3/` path reservation exists meanwhile) | [08-arr-facade](08-arr-facade.md) |
| Download grab/monitor/pending-recovery pipeline and the adapters other than Deluge | [05-download](05-download.md) |
| AI usage ledger and monthly cap; notify inbox, per-user subscriptions, personal channels, templating | [05-ai](05-ai.md), [05-notify](05-notify.md) |
| Facade-only members of built packages: key and route machinery in `auth`/`httpapi` (`PurposeFacade`, query-key extraction, `FacadeGroup`) and the auth feed token | [05-authn](05-authn.md) §3.5, [05-http-api](05-http-api.md) §3.6 |
| Backup encryption (deferred for scope); the seed-role merge diff | [05-backup](05-backup.md) B3, [05-rbac](05-rbac.md) §4.8 |
| `Online` data migrations (read adapters, the kernel-synthesised `<ID>.datamigrate.<name>` kind): no consumer in R0–R3, whose data migrations, the R2 cutover steps included, are `BootBlocking` | [05-storage](05-storage.md) §5S.3.14, [03 §3.3](03-architecture.md) `storage/datamigrate` |
| Root read shares (`library.RootSpec.ReadShares`): no app of R0–R3 has two modules that share a root, and ABO has one module (SIMP8-5) | [05-library](05-library.md) `Deferred-build members`, [03 §3.4](03-architecture.md) |
| Updater hardening beyond the moved R0 updater: signed manifests, the boot-attempt counter, automatic rollback, the unit's binary-layout migration (A-6) | [05-updater](05-updater.md) |

The Build column of 03 §3.3 is the authoritative list of deferred packages and is re-checked for every
release. Deferral also applies to members of built packages (03 §3.4): each file lists them,
informatively, under a `Deferred-build members` heading; no generated list or absence test checks them (A-4).

## Decisions

The superseding decision record is [02-decision-record.md](02-decision-record.md) (replaces
`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`): all 13 old decisions marked
kept / changed / dropped (decision 13: books' domain families stay in place, and the core-service families move
once, one-way, into native keyspaces in R2), the new decisions N1–N15 with N13 deleted (N14: one upgrade floor, in R3; N15: the
cutover happens inside the extraction, in R2, with no storage grants), the eight inherited acceptance gates (G5
replaced by G5′: the store is migrated forward without loss, older builds refuse it, and rollback restores the
pre-migration copy), the five old open questions, and the owner decisions D10–D13 (no backward compatibility; no
soaks; the cutover scope; core's name, licence and the cutover's row policy) and D15 (the required checks enforced by a
no-bypass ruleset with the merge queue; the R1 freeze kept; the split thresholds; the dead v2 schedule strings; the freeze
as a repository variable; one M8). User decisions D1–D9 are fixed inputs. No owner question is open: those D13 and D15
answered (Q-A, Q-B, Q-H; Q-C, Q-G, Q-I), and those decided as adopted recommendations, are in
[14-risks-open-questions.md](14-risks-open-questions.md) §14.3.

## Reading order

0. For a release's rules without review provenance, read its normative digest under
   `docs/plans/platform-core/digests/` (produced as the first artefact of Phase 6; ADV-12); it cites these files
   for detail.
1. [01-summary](01-summary.md) → [02-decision-record](02-decision-record.md) → [03-architecture](03-architecture.md) (**normative** names, package tree, rules R1–R8)
2. [06-app-sdk](06-app-sdk.md) (what an app implements) and [04-package-mapping](04-package-mapping.md) (where every current package goes)
3. The capability contracts you need (§5 table below)
4. [09-abo-on-core](09-abo-on-core.md) (the releases R0–R3 and the R2 cutover; the release keys are defined in 03 §3.8)
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
| 5 | Indexers / search (deferred-build; design sketch, non-normative) | [05-indexer.md](05-indexer.md) |
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
| 8 | *arr facade (deferred-build; design sketch, non-normative) | [08-arr-facade.md](08-arr-facade.md) |
| 9 | ABO on core | [09-abo-on-core.md](09-abo-on-core.md) |
| 10 | TV and Movies proof and the `acquire` contract: design sketch (non-normative), deferred-build (+ ebooks note) | [10-tv-movies-proof.md](10-tv-movies-proof.md) |
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

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`; resolutions R8-1…R8-6):

- R8-1…R8-6 — status line: round 8 (80 findings, 0 BLOCKER) dispositioned and applied; the binding resolutions summarised; decisions paragraph: decision 13's round-8 amendment (R8-4).
- SIMP8-5 — Build scope: `library.RootSpec.ReadShares` is deferred with root suspension and confirmation.

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; resolutions A-1…A-17):

- A-1…A-17 — status line: the advisor review dispositioned and applied; the current design summarised with the superseded round resolutions named; decisions paragraph: decision 13's advisor amendments (A-2, A-3) replace the round-4 to round-8 extension, sentinel and narrowing amendments.
- A-2 — Build scope: row for the post-S10 hardening cutovers; the config basis and root read-share rows lose their extension suspension and confirmation.
- A-4 — Build scope: `Deferred-build members` headings are informative; `archtest.DeferredMembersAbsent` removed.
- A-6 — Build scope: row for the deferred updater hardening.
- A-15 (ADV-S9) — Build scope: deferred parts are non-normative design sketches.
- ADV-12 — reading order: per-stage normative digests under `docs/plans/platform-core/digests/`.
- integration pass (advisor): deferred table and file list cite 05-indexer, 08 and 10 as design sketches (non-normative).

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; resolutions R9-1…R9-11):

- R9-1…R9-11 — status line: round 9 (71 findings) dispositioned and applied; the design summary gains the commit-spanning legacy lock and one instance per family (R9-1), one owner per conflict set and metric family (R9-2, R9-3), HEAD parity for scoped keys, the Basic gate, file-only keys and H3's scope (R9-4, R9-5), the reset contract (R9-6), the public crash-matrix kit (R9-7), soaks bound to content (R9-8), done-check environments (R9-9), tag-set vetting and history (R9-10) and the hardening cutovers' reach (R9-11).

Round 10 (dispositions in `docs/plans/platform-core/reviews/spec-round-10-dispositions.md`; resolutions R10-1…R10-11):

- R10-1…R10-11 — status line: round 10 (52 findings) dispositioned and applied; design summary: operations routes stay books' (R10-1), ambient scopes never reach a confined key (R10-2), otelgin stays HEAD's recorder with `app.Options.MetricViews` (R10-3), the hold on the batch and one total lock order (R10-4), the gap-fill write inside the hold (R10-5), vet and guards on any tree and the fixed-sample history assertion (R10-9).
- OPS10-11, R10-7 — soak bullet aligned with 09 §9.2.1: the trailer covers every stage (`<stage> <n|flip>`), is required on not-yet-flipped packages, and soaks count from the release's first PR.
- integration pass (round 10): soak bullet matches 09 §9.2.1: every commit of a non-doc PR carries a `Stage-Release:` trailer (`none` allowed), soak rows per landing commit, R10-7's soak rules.

Round 11:

- R11-1…R11-5 — status line: round 11 (1 BLOCKER, 3 MAJOR and the listed notes) dispositioned and applied; design summary: no `/api/v1/operations*` alias (R11-3), one HTTP instrument set with `r.Pattern` cleared before gin (R11-1); soak bullet: bot commits (R11-2), UI-0 and H1–H3 in the grammar, declared-levels criterion (R11-4).

Final verification:

- FIN12-1…FIN12-10 — status line: round 12 final verification dispositioned and applied; soak bullet: five-ecosystem bot rule, no merge-commit exemption (FIN12-1, FIN12-9), levels written into the soak row and H-flip revert window (FIN12-2), `UI0` soaks in parallel (FIN12-5).

Owner decisions D10–D12:

- D10–D12 revision (2026-11-04): status line and design summary rewritten around the releases R0–R3, native ownership with importers, the one-way checkpointed R2 cutover, what survives it (D12 F2), the hardened design in R2 and the one frozen contract (D12 F3); the representation-neutral, lock-parity, otelgin, soak-gate and trailer bullets deleted. Build scope: the per-family cutover row, the cutover-only members row (grants, served-run preconditions) and the post-S10 hardening row deleted; backup encryption and the seed-role merge diff keep a row; the config basis row deleted (05-config no longer defines it). Decisions paragraph: decision 13, N14, N15, G5′ and D10–D12. Reading order item 4 cites 03 §3.8. Consistency pass: D12's answers cited as D12 (a)–(d), UI-0 renamed to R1's first UI steps, and the R1 reset noted as offline-only.

Round 13 (2026-11-04): status line names round 13 and D13; design summary: F2 and the R1 freeze (R1, R3), the pre-freeze release (R2), checkpoint retention (R4) and identity rows stopping the upgrade (D13); Decisions paragraph: D10–D13, Q-A, Q-B and Q-H answered.

Owner answers (2026-11-05): D15 — decisions paragraph: D15 recorded, no owner question open.
