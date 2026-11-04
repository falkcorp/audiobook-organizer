<!-- file: docs/specs/platform-core/README.md -->
<!-- version: 1.11.0 -->
<!-- guid: 4c00242a-5629-4c0e-b66f-e81b2fb9fa7a -->
<!-- last-edited: 2026-11-04 -->

# mediacore — platform-core design spec

**Status:** draft under antagonistic review (see `docs/plans/platform-core/reviews/`). Round 1's 192
findings, round 2's 162, round 3's 130, round 4's 117, round 5's 115, round 6's 80, round 7's 96 and round 8's 80 are
dispositioned (`spec-round-{1,2,3,4,5,6,7,8}-dispositions.md`) and applied to the files, and so is the independent
advisor review (phase 4: 1 BLOCKER, 6 MAJOR, 5 MINOR, 9 simplification candidates, all accepted;
`spec-advisor-dispositions.md`). The round resolutions R2-n…R8-n stay binding except where the advisor's
resolutions **A-1…A-17** supersede them (notably R6-6, R7-1, R7-3, R7-6, R7-8, R8-4, R8-5 and R8-6). Round 9, the
confirmation review after the advisor pass (71 findings: 4 BLOCKER, 3 of them distinct, 27 MAJOR, 40 MINOR, all
accepted; `spec-round-9-dispositions.md`), is applied too; its resolutions **R9-1…R9-11** restore HEAD parity where
core now drives HEAD's code — the commit point, route ownership and metric ownership — and re-add none of the
deleted machinery. Round 10, the final confirmation review (52 findings: 2 BLOCKER, 27 MAJOR, 23 MINOR, all accepted;
`spec-round-10-dispositions.md`), is applied too; its resolutions **R10-1…R10-11** fix each defect by removing a
mechanism rather than adding one (R10-1 supersedes R9-2's route move; R10-3 supersedes R9-3's single HTTP emitter).
The design they fix, in brief:

- **One stored format per family during the extraction** (R3-1): core reaches ABO's data through SDK **store
  interfaces** whose legacy implementation the `books` module supplies, so core holds no audiobook key name and no
  storage grant (R4-1); legacy-only identifiers live in one package, `legacy`, outside the API promise (R6-3).
- **The extraction is representation-neutral** (A-2): ABO's sessions, one-time tokens, `abk_` keys, role atoms,
  plaintext blob secrets and auth-enable precedence stay exactly as at HEAD through S10, so a rollback costs no
  re-login or secret re-entry; the hardening becomes the post-S10 **identity, secrets and auth-precedence
  cutovers** on 02 N15's one-way machinery. Native stores (hello, TV, Movies) keep the hardened design.
- **Legacy rows keep HEAD's lock discipline** (A-3): storage-enforced write checks cover native keyspaces and
  core-written rows only; claims roll out shadow → strict. A legacy store's HEAD mutex spans core's commit
  (`legacy.WriteLock` through `storage.CommitLocked`), and each legacy store family has exactly one instance per
  process (R9-1); the hold is a mark on the batch (`storage.LocksHeld(b, l)`), every lock has a rank in one total
  order, and the gap-fill file write runs inside the hold (R10-4, R10-5).
- **One owner per route-conflict set and per metric series** (R10-1, R9-3, R10-3): the operations routes stay
  books' at every stage, and core's run route is a different path, not an alias; ABO's `opsmetrics` stops emitting
  what the executor records, checked against a HEAD golden with label values; the legacy gin mount keeps `otelgin`
  as HEAD's recorder for every request that reaches gin, core records only its own and module routes, and HEAD's
  bucket layouts arrive through `app.Options.MetricViews`. Scoped API keys keep HEAD's authenticated-only reach
  (R9-4), and ambient scopes never reach a worker-confined key, in H1 included (R10-2); the Basic gate and HEAD's file-only keys keep
  HEAD's storage, and H3 moves only auth-enable precedence (R9-5).
- **No predictive stage machinery** (A-4): Go's `internal/` rule, the layering and SDK guards and
  `TestCoreHoldsNoAppKeyNames` stay; 04 is an informative map, and the split's history uses git rename detection.
- **ABO keeps its metric names** (A-5); **S8 moves the updater** rather than rebuilding it (A-6). Every
  (GOOS, tag set) that ships is vetted on every PR, and the split's history keeps each moved file's pre-move
  commits, asserted with `git log --follow` on a real rewrite for a fixed sample (R9-10, R10-9); the vet and guard
  jobs run on any tree, built frontend or not (R10-9).
- **Boot and operations safety:** HEAD's routes register with a same-owner collision rule (A-1); legacy env names
  decode with HEAD's semantics (A-7); one crash matrix covers the whole boot (A-8), as the public kit
  `coretest/crashmatrix` that ABO also runs over its own boot (R9-7); 14-day soaks are owner checkpoints in
  `ci/soak.txt`, bound to the soaked binary; every PR that ships behaviour in a stage carries a `Stage-Release:
  <stage> <n|flip>` trailer, required on any PR that touches a not-yet-flipped stage's packages, and the gate
  admits it only after the predecessor release's soak, counted from the release's first PR (A-9, R9-8, R10-7;
  OPS10-11);
  every done-check names its environment (R9-9); `staging-scrub` needs a staging marker (A-10); concurrent feature
  work has move windows (A-11).
- A factory reset only stages, as a kind of the one journaled swap (R7-2, R8-1); the native factory-reset route
  deletes library files only when asked, under one confirmation contract, `storage.ResetRequest`, while the legacy
  alias keeps HEAD's wipe (A-17, R9-6).
- The post-S10 hardening cutovers state their real reach: H1 and H2 purge the old files from the store, and
  archives taken before them keep the old representation (R9-11).
- Deferred-build contracts are **non-normative design sketches** (A-15).

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
([03 §3.4](03-architecture.md)). These parts are described as **non-normative design sketches** and marked
**deferred-build** — the SDK freezes none of them and the extraction plan builds none of them; they are
designed and built when the first TV/Movies app starts (A-15), except the cutover rows, which follow S10:

| Deferred | Design sketch (non-normative) in |
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
| The config basis mechanism (`config.Basis`): no key of S0–S10 declares a basis | [05-config](05-config.md) §1.12.5, [03 §3.4](03-architecture.md) |
| Root read shares (`library.RootSpec.ReadShares`): no S0–S10 app has two modules that share a root, and ABO has one module (SIMP8-5) | [05-library](05-library.md) `Deferred-build members`, [03 §3.4](03-architecture.md) |
| The post-S10 hardening cutovers of ABO's identity, secrets and auth precedence (A-2) | [02](02-decision-record.md) N15, [12](12-repo-split.md) §12.4.4, [14](14-risks-open-questions.md) K25 |
| Updater hardening beyond the moved S0 updater: signed manifests, the boot-attempt counter, automatic rollback, the unit's binary-layout migration (A-6) | [05-updater](05-updater.md) |

The Build column of 03 §3.3 is the authoritative list of deferred packages and is re-checked at every
stage gate. Deferral also applies to members of built packages (03 §3.4): each file lists them,
informatively, under a `Deferred-build members` heading; no generated list or absence test checks them (A-4).

## Decisions

The superseding decision record is [02-decision-record.md](02-decision-record.md) (replaces
`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`): all 13 old decisions marked
kept / changed / dropped (decision 13 extended in round 3: one stored format per family during the
extraction; amended in round 4: store interfaces with books-supplied legacy implementations; amended in
round 5: HEAD's legacy-key precedence holds until cutover; amended in round 6: legacy-only identifiers in the
`legacy` package outside the API promise; amended by the advisor, A-2 and A-3: the extraction is
representation-neutral and legacy rows keep HEAD's lock discipline, superseding the round-4 to round-8
extension, sentinel and narrowing amendments), 15 new
decisions (N1–N15; N13, the round-2 Teach-build rollback floor, is superseded; N14 names the **floor
release** that deletes books' legacy migration code and the **stepping-stone release** a store must run
first, checked on the store; N15 defers per-family cutover, the storage grants and the three hardening
cutovers with it), the eight
inherited acceptance gates, and the five old open questions. User decisions D1–D9 are fixed inputs.
Owner questions still open (including Q-I, how the one release-gating requirement — every PR passes the
named required checks, `make ci`'s CI jobs, `extraction-gates`, the image job and `core-ci`, before merge —
is enforced), and those decided as adopted recommendations, are in
[14-risks-open-questions.md](14-risks-open-questions.md) §14.3.

## Reading order

0. For a stage's rules without review provenance, read its normative digest under
   `docs/plans/platform-core/digests/` (produced as the first artefact of Phase 6; ADV-12); it cites these files
   for detail.
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
