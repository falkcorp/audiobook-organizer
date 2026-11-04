<!-- file: docs/specs/platform-core/14-risks-open-questions.md -->
<!-- version: 1.1.0 -->
<!-- guid: e270beb6-8eea-48b1-8397-4ef4ec709956 -->
<!-- last-edited: 2026-11-04 -->

# 14 — Risks, migration hazards, open questions

Every item has an owner section and a recommendation. "Owner" means the repository owner must decide;
everything else is resolved inside the named spec file during review. Round-1 review findings are
cited by ID (`docs/plans/platform-core/reviews/spec-round-1-*.md`; dispositions in
`spec-round-1-dispositions.md`).

## 14.1 Top risks

| # | Risk | Likelihood / impact | Evidence | Mitigation (where specified) |
|---|---|---|---|---|
| K1 | **Scale of the books-module residue.** ~620k LOC and 154 packages stay in ABO; 333 `Store` methods remain books-owned; 114 + ~22 test files use full-store mocks. The extraction can stall halfway with two half-systems. | High / High | [04](04-package-mapping.md), `raw/04-database-store.md` | Strangler stages each end shippable ([09](09-abo-on-core.md)); adapters keep old call sites compiling; ratchet tests stop regressions; the plan sequences the keystone (`database`, `config`, `server` wiring) first. Scope is smaller than v1.0.0 assumed: gin retirement and ABO-global removal are no longer stages (02 N7, 03 R5). |
| K2 | **Data compatibility during identity-family adoption** (users, roles, sessions, API keys copied into `c/...`). A rollback to the previous release must still open and read the DB (gate G5). | Medium / Critical | [05-storage](05-storage.md), [09](09-abo-on-core.md); DATA-1, DATA-3, SEC-1 | Legacy rows stay authoritative for the window; the new build writes legacy and core rows in one batch (including the `idx:` rows the old build resolves through) and rebuilds the core copy from legacy at every boot (02 decision 13). The settings families (`setting:`, `preference:`, `pref:`) are not adopted; core config overrides use `c/config/`. One store-level compatibility guard (stamped level + sidecar + flag + rollback-script check, owned by 05-storage); pre-migration checkpoint; over-cap families fall back to a resumable, checkpointed data migration with dry-run. |
| K3 | **Two auth stacks, permanently.** ABO's gin router stays mounted through `httpapi.MountLegacy` with gin's own auth chain inside (02 N7), next to core `httpapi`+`rbac`. What v1.0.0 treated as a transition is now the steady state. | Medium / High | [05-rbac](05-rbac.md), [05-http-api](05-http-api.md); SIMP-2, SEC-13, OPS-11 | Core perimeter middleware (peer allowlist, recovery, headers, CSRF, rate limit) wraps the legacy mount; the auth engine swap happens in exactly one stage (09 S4); the permission-equivalence test (allow/deny identical across all 572 routes for every seed role) becomes a **permanent** CI test; every legacy route is in a reviewed manifest with a permission (03 R6); fail-closed registration; live defects hot-fixed independently (`todo.d/2026-11-04_platform_core_followups.md`). |
| K4 | **Toolchain churn:** several "latest" releases are days old (bleve v2.6.2, @babel/core 8, vitest 5.1, plugin-react 6.2.1); TypeScript 7 has no JS API so lint and `openapi-typescript` stay on the TS 6 alias; react-router 8 removes `react-router-dom` (121 ABO files). | High / Medium | [13 §13.7](13-toolchain.md) | 14-day release-age rule, owned by 13 §13.6 and binding on every version target in the spec (FE-21); exceptions E1–E4 recorded with exit conditions; the react-router 8 migration is a separate ABO series that **precedes** shell adoption (13 E4, FE-2, SIMP-25). |
| K5 | **Single-process failure domain and module trust.** Modules are compiled-in, trusted code; keyspaces are organisation, not access control. | Certain / Medium | 2026-09-18 adversarial review residual risks (`raw/08-prior-art.md` §2) | Accepted. R8 prevents accidental cross-module writes; manifests cannot grant `core:*` ([05-rbac](05-rbac.md)); no third-party in-process code (decision 5). |
| K6 | **"Small core" grows into a god package.** | Medium / Medium | adversarial review; SIMP-1 | Non-circular promotion rule (03 §3.4): a package is built only if ABO consumes it during the extraction or core itself needs it; everything else is **deferred-build** (`acquire`, `arrfacade`, `integrations/indexer`, download grab/monitor). The Build column of 03 §3.3 is re-checked at every stage gate; the API-stability gate measures the SDK surface ([12](12-repo-split.md)). |
| K7 | **Replace directive and `go install`:** `go install github.com/falkcorp/audiobook-organizer@version` fails while the root `go.mod` carries a `replace` (or an `exclude`; `cmd/go` refuses either, MOD-3 X4). | Low / Low | 03 §3.2 item 5 | Not used by the release path; documented in the README of the release. The genproto exclude lives in `core/go.mod`, so the root becomes installable as soon as the split drops the `replace` (12 §12.8.2 step 7). |
| K8 | **Stale transitive `genproto`** reappears if `cockroachdb/errors` changes or another dependency pins the monolithic module, or the exclude outlives its reason. | Low / Medium | `raw/00-baseline-build.md` E7; MOD-4 X2 | Exclude in `core/go.mod` (applies to every workspace that includes core); `scripts/ci/check_genproto_exclude.py` fails on a missing **or** stale exclude; a throwaway-workspace CI step builds `go work init . ./core`; the build never depends on `go.work` (03 §3.2 item 1). |
| K9 | **Licence mismatch with D9.** The repo is PolyForm Noncommercial 1.0.0 (`LICENSE`), which is source-available, not OSI open source; core's history inherits it. | Certain / Medium | `LICENSE:1`, [12](12-repo-split.md) open item 1 | Owner decision Q-A below, before the split. |
| K10 | **Production unknowns** (key counts per family, session counts, op-history size) drive whether adoption rebuilds fit the batch cap or take the resumable path. | Certain / Low | [05-storage](05-storage.md), [09](09-abo-on-core.md) | Both branches designed; read-only census commands listed in 09 for the owner to run. |
| K11 | **The first core wave breaks the Docker and web builds.** Both Dockerfiles copy only `go.mod go.sum` before `go mod download` (`Dockerfile:54-55`, `Dockerfile.build-cgo:52-53`), which fails once the root `replace`s `./core` (MOD-1 X1); eleven-plus `cd web && npm ci` entry points break once `web/` depends on built `@mediacore/*` (MOD-9, FE-13). Prereleases do not build images, so only the production release would find out. | High (if unmitigated) / High | MOD-1, OPS-1, MOD-9, FE-13; `.github/workflows/prerelease.yml:48` (`docker-enabled: false`) | S0 updates both Dockerfiles (copy `core/go.mod`, `core/go.sum`), `.dockerignore`, and every `npm ci` entry point (one `scripts/ci/build_web.sh` behind `make web-deps`), and adds a PR job that builds the image (03 §3.2 item 4; 09 S0 owns the list and the done-check). |
| K12 | **Releases are skipped.** Installs jump from release N to the latest (the updater fetches the latest release, Docker users pull `:latest`, archives from old releases are restored), so a contract step can meet a store that never ran the matching expand/adopt and delete legacy rows that were never adopted; a build can also pass the guard yet be unable to read the config format a contract wrote. | Medium / Critical | OPS-2, OPS-3; `internal/updater/updater.go:295` (fetches `releases/latest`) | Every contract step requires the stamped level to prove the prior expand/adopt ran on **this** store; otherwise it adopts first or refuses to start with a clear message; legacy data is never deleted on an unadopted store; compatibility levels are defined by reader capability (09 §9.2.1, §9.2.2, §9.5). |
| K13 | **The identity adoption model has deliberate costs.** Rebuilding the core identity copy from legacy at every boot costs boot time that grows with the families; an API key whose scopes have no exact legacy equivalent is mirrored **disabled**, so it stops working after a rollback (never widened); sessions are not mirrored raw, so a rollback costs one re-login. | Certain / Medium | DATA-3, DATA-4, DATA-5, SEC-1, SEC-19, OPS-19 | Accepted trades of the round-1 resolution (dispositions, cross-cutting resolution 5). Boot-time budget covers the rebuild (09); over-cap falls back to the resumable path (K10); the disabled-key reason is recorded so the operator can see why a narrowed key stopped working; rollback runbooks state the re-login. |
| K14 | **Dependency skew between the binary and core CI.** The root build takes the MVS maximum over the root and `core/go.mod`; core CI tests only core's requirements. | Medium / Medium | MOD-11 | Dependabot gomod for `/` and `/core` in one group; `scripts/ci/check_module_skew.py` (12 S4(c), 03 §3.2 item 6). |
| K15 | **A dropped legacy env name silently points ABO at a new, empty database** (`DATABASE_PATH` is the only DB-path source in the shipped unit and plist), or turns basic auth off for compose users. | Low after mitigation / Critical | OPS-5 | Env names that are the only source of a required setting in a shipped deploy file are **permanent** (compat ledger "permanent" class); boot refuses to start on an empty store when a legacy DB path is set but unread (09, 05-config). |
| K16 | **The live restore path stays broken while contracts start deleting rows.** Today's restore extracts over the live store under a running engine; the staged restore arrives only with backup's stage. | Medium / Critical | OPS-15 | Staged restore pulled forward (or live-target extraction disabled with an offline runbook) and "staged restore shipped" made a precondition of the identity and config contract steps (09 §9.5, 05-backup). |

## 14.2 Migration hazards (each has a guard)

| Hazard | Guard |
|---|---|
| Core silently requires the app module via `go mod tidy` (`raw/00-baseline-build.md` E2) — including through a test file | `archtest.TestNoAppRequire` (R1a); `TestCoreImportsOnlyCore` over `go list -deps -test` (R1b, MOD-16) |
| ABO rewired onto a core `internal/` or `kernel/` package it may not import (stops compiling) | Visibility rule (03 §3.4) and R2: a package with an ABO importer moves to a public path or stays in ABO (MOD-2) |
| `examples/hello` imports core internals (the `internal/` rule is path-based) | `archtest.SDKOnly` (03 R2, MOD-7) |
| Repo-walking ratchet tests moved into core scan nothing | Per-move test classification step in every move brief; ratchets stay in the root |
| Root `./...` does not reach `core/`; `make ci` steps and CI jobs silently skip core or fail on moved packages | `GO_MODULE_DIRS := . core` for per-module targets plus the per-target classification and per-wave checklist in 09 S0 (03 §3.2 item 4, MOD-8) |
| Docker module-download layer lacks `core/go.mod`; web builds lack built `@mediacore/*` | S0 Dockerfile and entry-point changes plus a PR image job (K11) |
| Checkpoint tags take over `git describe` and the reported version | Checkpoints are `refs/core-checkpoints/v0.Y.Z`, not tags (12 §12.4.1, MOD-5) |
| `version.Version` drifts from checkpoints/tags | `scripts/ci/check_core_version.py`; release step 1 (12 §12.4.3, MOD-12) |
| Layering guard fails on the first move, or passes with a loader that sees nothing | Guard redesign (03 §3.5.1) is the first code task; ratio floor; in-module control `. → cmd`; classifier control for layer −1 (MOD-14) |
| Library-scale loop added without a worker pool | R7 (type + AST check) |
| Route added without permission, including on the legacy mount | R6 (registration error + `archtest.RoutesHavePermissions` over the router and the legacy manifest) |
| Two writers of one key family across owners | R8 claims + boot overlap check |
| Event bus used for correctness | Outbox ([05-events](05-events.md)); review checklist |
| Contract step on a store that skipped the matching expand/adopt | Stamped-level check before any legacy delete (K12) |
| Rollback widens an API key | Keys without an exact legacy scope equivalent are mirrored disabled (K13, SEC-1) |
| Restore into a live store | Staged restore applied at next boot ([05-backup](05-backup.md)); until it ships, live-target extraction is disabled and the contract steps wait for it (K16) |

## 14.3 Questions for the owner (with recommendations)

| ID | Question | Recommendation | Raised in |
|---|---|---|---|
| Q-A | Licence of the core repo: keep PolyForm Noncommercial (source-available) or relicense core under an OSI licence (Apache-2.0 or MIT) to satisfy D9 "open source only"? | **Relicense core as Apache-2.0** before the split (you are the copyright holder of the history; confirm no third-party contributions need consent). ABO may keep its licence. | [12](12-repo-split.md) |
| Q-B | Public or private core repository? | **Public** (free proxy/sum-db, free npm publishing, unlimited Actions minutes for public repos). | [12](12-repo-split.md) |
| Q-C | The 15 v2 cron strings that never ran (e.g. `deluge.protected-paths-sync` every 30 min): enable as real schedules or delete? | **Delete the declarations** (spec default); re-add individually as real `Schedule`s if wanted, because enabling 15 never-run jobs at once is an untested behaviour change. | [05-tasks](05-tasks.md) |
| Q-F | CLI flags and env names baked into shipped deploy files (unit, plist, compose, Dockerfile, example config) that are **not** the only source of a required setting: two-minor window or keep until core 1.0? (The sole-source case is decided, §14.3.1.) | **Keep until 1.0**, not usage-gated; two minors for everything else. | [09](09-abo-on-core.md) O7; OPS-5 |
| Q-G | Readiness thresholds for the split (S1: 3 consecutive API-stable checkpoints over 6 weeks; S5: coverage 70 % core / 60 % SDK)? | Accept as proposed; tune before wave 0 wires the jobs. | [12](12-repo-split.md) |
| Q-H | Core module name `github.com/falkcorp/mediacore`. | Accept; renaming is cheap only before the first core package lands. | [03](03-architecture.md) |

### 14.3.1 Decided in round 1 (adopted recommendation; the owner may override)

These were owner questions in v1.0.0. Round 1 found that leaving them open let spec files contradict
each other, so the lead adopted the recommendation and the files now implement it. Each stays
overridable by the owner; overriding one reopens the files named.

| ID | Former question | Decision | Implemented in |
|---|---|---|---|
| Q-D | Session copy during the rollback window: mirror raw sessions to legacy keys (no re-login on rollback) or not? | **No raw-session mirror** (SEC-19). Sessions are stored only hashed. A legacy token presented to the new build is looked up in `sess:` with epoch, status and revocation checks; a hit issues a hashed native session, sets the cookie and deletes the legacy row; that fallback code is removed 7 days after the session stage, independent of the contract step. Cost: one re-login, only after a rollback. | [05-authn](05-authn.md), [09](09-abo-on-core.md) S4 |
| Q-E | Plaintext secrets in `config_blob`: keep through the rollback window or scrub as soon as they are encrypted? | **One keyring, scrub at the config stage** (SEC-20). The design is 05-config's (§2.4/§2.10) and 09 S3 references it: the legacy key file is imported as the retired key `k0-legacy`; during the window `k0-legacy` stays readable and the mirror writer re-encrypts each changed secret in the legacy format into its legacy row in the same batch; plaintext blob fields are zeroed at S3; rotation is refused until the contract step. Condition from OPS-9: the scrub is rollback-safe only for secrets the previous build can read from an encrypted legacy row; 09 S3 owns making that true for every secret field (a reader in an earlier build if needed) before the scrub ships. | [05-config](05-config.md), [09](09-abo-on-core.md) S3 |
| Q-F (part) | Env names that are the **only** source of a required setting in a shipped deploy file (e.g. `DATABASE_PATH`) | **Permanent** (OPS-5): compat ledger "permanent" class; boot refuses to start on an empty store when a legacy DB path is set but unread. | [09](09-abo-on-core.md), [05-config](05-config.md) |

## 14.4 Old decision-record questions

Q1 resolved by D7, Q3 by D2; Q2 → [10 §ebooks](10-tv-movies-proof.md) (recommendation EPUB + PDF with an
OPDS feed, reusing core); Q4 → [05-library](05-library.md) (one root abstraction; `os.Root`-confined; room
for a remote driver later); Q5 → [09](09-abo-on-core.md) §9.2.1 (the single compatibility window, used by
every alias kind; removal only by a contract PR, never by a clock; 02 §2.5).

## 14.5 Remaining open items by file

These are design details each section resolves in review; none blocks the plan. Each file's "Open
items" section is authoritative; counts are not repeated here because round 1 revises those sections in
parallel, and a census taken mid-revision would be stale. Files with open items: 04, 05-ai, 05-authn,
05-backup, 05-config, 05-download, 05-events, 05-health, 05-http-api, 05-indexer, 05-library,
05-logging, 05-metadata, 05-metrics, 05-notify, 05-rbac, 05-realtime, 05-storage, 05-tasks,
05-updater, 06, 07, 08, 09, 10, 12. Round 2 re-counts them.

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- SEC-19 — Q-D moved to §14.3.1 as decided (no raw-session mirror).
- SEC-20, OPS-9 — Q-E moved to §14.3.1 as decided (one keyring, scrub at S3), with OPS-9's precondition.
- OPS-5 — Q-F split: sole-source env names decided permanent; K15 added; residual Q-F kept.
- MOD-1, OPS-1, MOD-9, FE-13 — K11 (Docker and web build break) added; hazards row.
- OPS-2, OPS-3 — K12 (skipped releases) added; hazards row.
- DATA-3, DATA-4, DATA-5, SEC-1, OPS-19 — K2 rewritten for the identity adoption model; K13 (its costs) added; hazards row.
- DATA-1 — K2: settings families not adopted; `c/config/`.
- OPS-15 — K16 added; "Restore into a live store" hazard updated.
- SIMP-1 — K6 mitigation: non-circular rule, Build column re-checked at stage gates.
- SIMP-2, SEC-13, OPS-11 — K3: permanent legacy mount, one-stage auth swap, permanent equivalence test.
- SIMP-3 — K1 notes reduced scope.
- SIMP-7 — §14.4 Q5 points at the single window in 09 §9.2.1.
- SIMP-18 — K2: one store-level compatibility guard.
- SIMP-25, FE-2, FE-21 — K4: react-router migration precedes the shell; 13 owns the age rule.
- MOD-2, MOD-5, MOD-7, MOD-8, MOD-12, MOD-14, MOD-16 — hazards rows added or updated.
- MOD-3, MOD-4 — K7 and K8 updated for the exclude in `core/go.mod`.
- MOD-11 — K14 added.
