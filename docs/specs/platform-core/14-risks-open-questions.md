<!-- file: docs/specs/platform-core/14-risks-open-questions.md -->
<!-- version: 1.3.1 -->
<!-- guid: e270beb6-8eea-48b1-8397-4ef4ec709956 -->
<!-- last-edited: 2026-11-04 -->

# 14 — Risks, migration hazards, open questions

Every item has an owner section and a recommendation. "Owner" means the repository owner must decide;
everything else is resolved inside the named spec file during review. Review findings are cited by
ID (`docs/plans/platform-core/reviews/spec-round-{1,2}-*.md`; dispositions in
`spec-round-{1,2}-dispositions.md`; round-2 resolutions are cited as R2-n).

## 14.1 Top risks

| # | Risk | Likelihood / impact | Evidence | Mitigation (where specified) |
|---|---|---|---|---|
| K1 | **Scale of the books-module residue.** ~620k LOC and 160 packages stay in ABO (04 §8 note 6: 55 leave, 2 new paths); 333 `Store` methods remain books-owned; 114 + ~22 test files use full-store mocks. The extraction can stall halfway with two half-systems. | High / High | [04](04-package-mapping.md), `raw/04-database-store.md` | Strangler stages each end shippable ([09](09-abo-on-core.md)); adapters keep old call sites compiling; ratchet tests stop regressions; the plan sequences the keystone (`database`, `config`, `server` wiring) first. Scope is smaller than v1.0.0 assumed: gin retirement and ABO-global removal are no longer stages (02 N7, 03 R5). |
| K2 | **Data compatibility during identity-family adoption** (users, roles, sessions, API keys copied into `c/...`). A rollback to the previous release must still open and read the DB (gate G5). | Medium / Critical | [05-storage](05-storage.md), [09](09-abo-on-core.md); DATA-1, DATA-3, SEC-1 | Legacy rows stay authoritative for the window; the new build writes legacy and core rows in one batch (including the `idx:` rows the old build resolves through, and every revocation, also one re-applied after a restore, R2-2) and rebuilds the core copy from legacy at every boot (02 decision 13). A rollback lands on the sequence's Teach build or later, which carries the legacy-side guards (02 N13, K19). The settings families (`setting:`, `preference:`, `pref:`) are not adopted; core config overrides use `c/config/`. One store-level compatibility guard (stamped level + sidecar + flag + rollback-script check, owned by 05-storage); pre-migration checkpoint; over-cap families fall back to a resumable, checkpointed data migration with dry-run. |
| K3 | **Two auth stacks, permanently.** ABO's gin router stays mounted through `httpapi.MountLegacy` with gin's own auth chain inside (02 N7), next to core `httpapi`+`rbac`. What v1.0.0 treated as a transition is now the steady state. | Medium / High | [05-rbac](05-rbac.md), [05-http-api](05-http-api.md); SIMP-2, SEC-13, OPS-11 | Core perimeter middleware (peer allowlist, recovery, headers, CSRF, rate limit) wraps the legacy mount; the auth engine swap happens in exactly one stage (09 S4); the permission-equivalence test (allow/deny identical across all 572 routes for every seed role) becomes a **permanent** CI test; every legacy route is in a reviewed manifest with a permission (03 R6); fail-closed registration; live defects hot-fixed independently (`todo.d/2026-11-04_platform_core_followups.md`). |
| K4 | **Toolchain churn:** several "latest" releases are days old (bleve v2.6.2, @babel/core 8, vitest 5.1, plugin-react 6.2.1); TypeScript 7 has no JS API so lint and `openapi-typescript` stay on the TS 6 alias; react-router 8 removes `react-router-dom` (122 ABO files: 52 source, 70 test; 07 §7.3.3's grep). | High / Medium | [13 §13.7](13-toolchain.md) | 14-day release-age rule, owned by 13 §13.6 and binding on every version target in the spec (FE-21); exceptions E1–E4 recorded with exit conditions; the react-router 8 migration is a separate ABO series that **precedes** shell adoption (13 E4, FE-2, SIMP-25). |
| K5 | **Single-process failure domain and module trust.** Modules are compiled-in, trusted code; keyspaces are organisation, not access control. | Certain / Medium | 2026-09-18 adversarial review residual risks (`raw/08-prior-art.md` §2) | Accepted. R8 prevents accidental cross-module writes; manifests cannot grant `core:*` ([05-rbac](05-rbac.md)); no third-party in-process code (decision 5). |
| K6 | **"Small core" grows into a god package.** | Medium / Medium | adversarial review; SIMP-1 | Non-circular promotion rule (03 §3.4): a package is built only if ABO consumes it during the extraction or core itself needs it; everything else is **deferred-build** (`acquire`, `arrfacade`, `integrations/indexer`, download grab/monitor). The Build column of 03 §3.3 is re-checked at every stage gate; the API-stability gate measures the SDK surface ([12](12-repo-split.md)). |
| K7 | **Replace directive and `go install`:** `go install github.com/falkcorp/audiobook-organizer@version` fails while the root `go.mod` carries a `replace` (or an `exclude`; `cmd/go` refuses either, MOD-3 X4). | Low / Low | 03 §3.2 item 5 | Not used by the release path; documented in the README of the release. The genproto exclude lives in `core/go.mod`, so the root becomes installable as soon as the split drops the `replace` (12 §12.8.2 step 7). |
| K8 | **Stale transitive `genproto`** reappears if `cockroachdb/errors` changes or another dependency pins the monolithic module, or the exclude outlives its reason. | Low / Medium | `raw/00-baseline-build.md` E7; MOD-4 X2 | Exclude in `core/go.mod` from S0 (applies to every workspace that includes core); `core/scripts/ci/check_genproto_exclude.py` runs in a temp copy and fails unless the line is present, the workspaces build with it, and without it `go mod graph` still shows the requirer **and** the build fails with `ambiguous import` (stale otherwise; the live graph never lists an excluded version, MOD2-5 X10/X16); the build never depends on `go.work` (03 §3.2 item 1). |
| K9 | **Licence mismatch with D9.** The repo is PolyForm Noncommercial 1.0.0 (`LICENSE`), which is source-available, not OSI open source; core's history inherits it. | Certain / Medium | `LICENSE:1`, [12](12-repo-split.md) open item 1 | Owner decision Q-A below, before the split. |
| K10 | **Production unknowns** (key counts per family, session counts, op-history size) drive whether adoption rebuilds fit the batch cap or take the resumable path. | Certain / Low | [05-storage](05-storage.md), [09](09-abo-on-core.md) | Both branches designed; read-only census commands listed in 09 for the owner to run. |
| K11 | **The first core stage (S0) breaks the Docker and web builds.** Both Dockerfiles copy only `go.mod go.sum` before `go mod download` (`Dockerfile:54-55`, `Dockerfile.build-cgo:52-53`), which fails once the root `replace`s `./core` (MOD-1 X1); eleven-plus `cd web && npm ci` entry points break once `web/` depends on built `@mediacore/*` (MOD-9, FE-13). Prereleases do not build images, so only the production release would find out. | High (if unmitigated) / High | MOD-1, OPS-1, MOD-9, FE-13; `.github/workflows/prerelease.yml:48` (`docker-enabled: false`) | S0 updates both Dockerfiles (copy `core/go.mod`, `core/go.sum`), `.dockerignore`, and every `npm ci` entry point (one `scripts/ci/build_web.sh` behind `make web-deps`), and adds a PR job that builds the image (03 §3.2 item 4; 09 S0 owns the list and the done-check). |
| K12 | **Releases are skipped.** Installs jump from release N to the latest (the updater fetches the latest release, Docker users pull `:latest`, archives from old releases are restored), so a contract step can meet a store that never ran the matching expand/adopt and delete legacy rows that were never adopted; a build can also pass the guard yet be unable to read the config format a contract wrote. | Medium / Critical | OPS-2, OPS-3; `internal/updater/updater.go:295` (fetches `releases/latest`) | Every contract step lists its adopt step (`Adopts`) and needs that step's applied record on **this** store; otherwise it runs the adopt step first, and only a build that no longer registers it refuses to start (`ErrAdoptMissing`, naming the release to install). Skipping is bounded below by the minimum-upgrade-from release (K18, 02 N14). A contract whose expand level has not served the store long enough (`RequiresServed`) is **deferred** — the build boots and retries later, never fails boot (05-storage §5S.3.5). Legacy data is never deleted on an unadopted store; compatibility levels are defined by reader capability (09 §9.2.1, §9.2.2, §9.5). |
| K13 | **The identity adoption model has deliberate costs.** Rebuilding the core identity copy from legacy at every boot costs boot time that grows with the families; an API key whose scopes have no exact legacy equivalent is mirrored **disabled** — it keeps working natively while the side record is `Fresh` (05-storage §5S.3.11) and stops working only after a rollback (never widened); sessions are not mirrored raw, so a rollback costs one re-login. | Certain / Medium | DATA-3, DATA-4, DATA-5, SEC-1, SEC-19, OPS-19; SEC2-1, DATA2-3 | Accepted trades of the round-1 resolution (dispositions, cross-cutting resolution 5). The rollback that would turn a mirror-disabled key back on lands on the Teach build or later, whose legacy handlers refuse status changes on revoked or mirror-disabled keys (02 N13). Boot-time budget covers the rebuild (09); over-cap falls back to the resumable path (K10); the disabled-key reason is recorded so the operator can see why a narrowed key stopped working; rollback runbooks state the re-login. |
| K14 | **Dependency skew between the binary and core CI**, and its twin, **tidy coupling**: the root build takes the MVS maximum over the root and `core/go.mod`, core CI tests only core's requirements, and a core-only `go mod tidy` leaves the root without the `go.sum` entries core now needs (MOD2-6 X11). | Medium / Medium | MOD-11, MOD2-6 | Dependabot gomod for `/` and `/core` in one group, verified to yield one PR; `scripts/ci/check_module_skew.py` (12 SC4(c), 03 §3.2 item 6) with `--pin` for pinned modules (13 §13.5.2); a PR that changes `core/go.mod` carries the root tidy result, enforced by per-module `tidy-check` and by `core-ci.yml` building the root (03 §3.2 item 4). |
| K15 | **A dropped legacy env name silently points ABO at a new, empty database** (`DATABASE_PATH` is the only DB-path source in the shipped unit and plist), or turns basic auth off for compose users. | Low after mitigation / Critical | OPS-5 | Env names that are the only source of a required setting in a shipped deploy file are **permanent** (compat ledger "permanent" class); boot refuses to start on an empty store when a legacy DB path is set but unread (09, 05-config). |
| K16 | **The live restore path stays broken while contracts start deleting rows.** Today's restore extracts over the live store under a running engine; the staged restore arrives only with backup's stage. | Medium / Critical | OPS-15 | Staged restore pulled forward (or live-target extraction disabled with an offline runbook) and "staged restore shipped" made a precondition of the identity and config contract steps (09 §9.5, 05-backup). |
| K18 | **The stepping stone is real.** Books' legacy migrations 59–66 write families that core claims later and call code that 05-tasks T8 deletes, so an install older than the declared **minimum-upgrade-from release** cannot jump straight to a build that no longer carries that code. The updater and Docker `:latest` users would otherwise meet a refusal. | Medium / High | DATA2-2, OPS2-11; R2-4 | Legacy migrations run before core claims are enforced, under a logged legacy-migration grant, and their code is kept until the minimum-upgrade-from release (02 N14). Release manifests carry `min_upgrade_from`; the updater offers that release first instead of the latest (05-updater); boot refuses an older store with a message naming the release to install (09, 05-storage). The rule is stated honestly in 09 instead of "no stepping-stone release". |
| K19 | **The rollback floor costs rollback reach.** Once an adopt step has run on a store, the level guard refuses every build older than the sequence's Teach build (02 N13), so `make rollback` to a `.prev` older than the Teach build fails and only a pre-adoption backup goes further back. | Certain / Medium | SEC2-1, SEC2-2, DATA2-3, DATA2-5; R2-1 | Accepted: it is what removes seven blockers (a rollback that re-activates a disabled key, widens a bounded `users.manage` role, or revokes without journaling). The Teach build ships before every adopt step and serves production under normal deploys first (09); its legacy-side guards are the live-defect hotfixes of `todo.d/2026-11-04_platform_core_followups.md`, which can ship earlier still; `make rollback` and the updater's `ReaderCheck` name the floor when they refuse. |
| K20 | **Gates that gate nothing.** Main merges without CI gating (`auto-revert.yml`), `prerelease.yml` cuts an RC on every push and auto-promotes a production minor every 10 RCs, so the extraction's flip-PR, contract, ratchet, MOD-2 and Docker gates run after the code ships unless made real. | High (if unmitigated) / Critical | OPS2-1; R2-6; `.github/workflows/auto-revert.yml:8-11`, `prerelease.yml:59-131` | S0 makes the gates real — owner choice Q-I below (recommended: required checks via branch protection on gated paths, plus promotion gating as a second layer). W (a) counts production minors **and** 30 days (09 §9.2.1). |
| K17 | **Merging single-app installs into one store needs a new core contract** (DATA-30). Two installs each hold `c/auth/`, `c/rbac/`, `c/tasks/`, `c/config/`, `c/secrets/` under different keyrings, plus their own `c/storage/install`, bookkeeping, levels and `Sequence` high-water marks, so "copy the keyspaces" collides. | Low (only when a multi-app suite merges existing installs) / High | DATA-30; [05-storage](05-storage.md) §5S.11 item 6; [11](11-multi-app.md) §11.5 row 4 | A core **storage export/import** capability (per keyspace, version and level check, ID-remap hooks for users, roles and keys, keyring merge, sequence rebase; module keyspaces copy as-is). It changes a core contract, is not built until multi-app exists (lowest priority), and needs no change now: per-owner levels already keep one app's raise from blocking another's rollback. |

## 14.2 Migration hazards (each has a guard)

| Hazard | Guard |
|---|---|
| Core silently requires the app module via `go mod tidy` (`raw/00-baseline-build.md` E2) — including through a test file | `archtest.TestNoAppRequire` (R1a); `TestCoreImportsOnlyCore` over `go list -deps -test` (R1b, MOD-16) |
| ABO rewired onto a core `internal/` or `kernel/` package it may not import (stops compiling) | Visibility rule (03 §3.4) and R2: a package with an ABO importer moves to a public path or stays in ABO (MOD-2) |
| `examples/hello` imports core internals (the `internal/` rule is path-based) | `archtest.SDKOnly` (03 R2, MOD-7) |
| Repo-walking ratchet tests moved into core scan nothing | Per-move test classification step in every move brief; ratchets stay in the root |
| Root `./...` does not reach `core/`; `make ci` steps and CI jobs silently skip core or fail on moved packages | `GO_MODULE_DIRS := . core` for per-module targets plus the per-target classification and per-stage checklist in 09 S0 (03 §3.2 item 4, MOD-8) |
| Docker module-download layer lacks `core/go.mod`; web builds lack built `@mediacore/*` | S0 Dockerfile and entry-point changes plus a PR image job (K11) |
| Checkpoint tags take over `git describe` and the reported version | Checkpoints are `refs/core-checkpoints/v0.Y.Z`, not tags (12 §12.4.1, MOD-5) |
| `version.Version` drifts from checkpoints/tags | `scripts/ci/check_core_version.py`; release step 1 (12 §12.4.3, MOD-12) |
| Layering guard fails on the first move, or passes with a loader that sees nothing | Guard redesign (03 §3.5.1) is the first code task; ratio floor; in-module control `. → cmd`; classifier control (MOD-14) |
| Core imports hide wrong-way edges in ABO (every core package legal from every ABO layer) | Core layers mapped onto ABO's scale (L0–L5 → −1, L6 → transport 5, L7–L9 → entry 6), `loadGraph` keeps mediacore imports, `allowed` edges re-evaluated when their target moves (03 §3.5.1, MOD2-7) |
| A core dependency change breaks the root build (`missing go.sum entry`) | Tidy-coupling rule, per-module `tidy-check`, `core-ci.yml` builds the root on `core/go.mod` changes (K14, MOD2-6) |
| Rollback lands on a build that widens access (re-activates a disabled key, unbounded `users.manage`, no revocation journal) | Rollback floor = the Teach build; the level guard refuses older builds once adoption ran (02 N13, K19) |
| An old install skips the books legacy migrations, or a release deletes code they call | Legacy migrations before claims; minimum-upgrade-from release; boot refusal naming it (02 N14, K18) |
| A gate runs only after the commit is already in an RC or an auto-promoted production release | Extraction gates made real in S0 (Q-I, K20) |
| Library-scale loop added without a worker pool | R7 (type + AST check) |
| Route added without permission, including on the legacy mount | R6 (registration error + `archtest.RoutesHavePermissions` over the router and the legacy manifest) |
| Two writers of one key family across owners | R8 claims + boot overlap check |
| Event bus used for correctness | Outbox ([05-events](05-events.md)); review checklist |
| Contract step on a store that skipped the matching expand/adopt | `Adopts` applied-record check before any legacy delete; `RequiresServed` defers, never fails boot (K12) |
| Rollback widens an API key | Keys without an exact legacy scope equivalent are mirrored disabled and stay active natively while `Fresh` (K13, SEC-1) |
| Books' legacy reads/writes outside its claims go unnoticed in shadow mode | Violations are persisted (`c/storage/claimviol/`), so 09 S2b's "zero persisted violations ≥ 14 days" survives restarts (05-storage §5S.9.4) |
| Restore into a live store | Staged restore applied at next boot ([05-backup](05-backup.md)); until it ships, live-target extraction is disabled and the contract steps wait for it (K16) |

## 14.3 Questions for the owner (with recommendations)

| ID | Question | Recommendation | Raised in |
|---|---|---|---|
| Q-A | Licence of the core repo: keep PolyForm Noncommercial (source-available) or relicense core under an OSI licence (Apache-2.0 or MIT) to satisfy D9 "open source only"? | **Relicense core as Apache-2.0** before the split (you are the copyright holder of the history; confirm no third-party contributions need consent). ABO may keep its licence. | [12](12-repo-split.md) |
| Q-B | Public or private core repository? | **Public** (free proxy/sum-db, free npm publishing, unlimited Actions minutes for public repos). | [12](12-repo-split.md) |
| Q-C | The 15 v2 cron strings that never ran (e.g. `deluge.protected-paths-sync` every 30 min): enable as real schedules or delete? | **Delete the declarations** (spec default); re-add individually as real `Schedule`s if wanted, because enabling 15 never-run jobs at once is an untested behaviour change. | [05-tasks](05-tasks.md) |
| Q-F | CLI flags and env names baked into shipped deploy files (unit, plist, compose, Dockerfile, example config) that are **not** the only source of a required setting: two-minor window or keep until core 1.0? (The sole-source case is decided, §14.3.1.) | **Keep until 1.0**, not usage-gated; two minors for everything else. | [09](09-abo-on-core.md) O7; OPS-5 |
| Q-G | Readiness thresholds for the split (SC1: 3 consecutive API-stable checkpoints over 6 weeks; SC5: coverage 70 % core / 60 % SDK)? They are readiness reports, not merge gates (12 §12.3.11). | Accept as proposed; tune before S0 wires the readiness report. | [12](12-repo-split.md) |
| Q-H | Core module name `github.com/falkcorp/mediacore`. | Accept; renaming is cheap only before the first core package lands. | [03](03-architecture.md) |
| Q-I | How are the extraction's gates made real (R2-6)? The gates are the flip-PR checks (`TestAdoptedPrefixWritesGoThroughMirror`, `TestCompatLedgerMatchesCode`), the contract checks (`TestNoContractBeforeSunset`, P8), the ratchets, the MOD-2 gate (generated from `go list -deps -test -json`, R2-5) and the Docker image build. **(a)** Required status checks via branch protection, or **(b)** keep today's ungated merges and run the gates in the RC pipeline, where they block promotion (`check-rc-ordinal` refuses to auto-promote when the RC commit's CI is not green, or while the compat ledger has a stage in "dark PRs landed, flip not landed")? | **(a) for gated paths, plus (b) as a second layer.** One always-reporting required check, `extraction-gates`, runs the gate set when a PR touches `docs/compat-surfaces.md`, a `RaisesLevel`/adopt step, a `Mirror` declaration, `core/`, `go.mod`/`go.sum` or a Dockerfile (label `compat-gate`), and passes trivially otherwise, so the rest of the repo keeps ungated merges. Reason: (b) alone still publishes the change — `prerelease.yml` cuts an RC on every push, so an early contract or an unmirrored write reaches RC installs before any promotion check runs, and contracts delete data irreversibly. Cost: merges on gated paths wait for CI. | [09](09-abo-on-core.md) §9.1, §9.2.1; OPS2-1 |

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
items" section is authoritative. Census re-taken in the round-2 integration pass (2026-11-04), once every
file had settled, from the working tree with the round-1 method: an item is a numbered list entry or an
ID'd table row (`O1`, `1`, …) under a heading containing "Open items"; entries the file itself marks closed
or resolved ("(Closed …)", "(closed by …)", "**Resolved**") are counted separately. Files without an "Open
items" section (01, 02, 03, 11, 13, 15, README) and this file are not listed. **115** items remain open
across 26 files (round-2 revision snapshot: 117. Changes since: 09 settled at 6 open rows with 4 marked
closed (was 8 open); 05-http-api item 3 resolved (06 uses `Registrar.HTTP()` and `MountLegacy`); 05-rbac
item 6 resolved by the lead decision and item 7 opened (`core:library.browse` reach)).

| File | Open | Closed in place |
|---|---|---|
| [04-package-mapping](04-package-mapping.md) | 5 | 0 |
| [05-ai](05-ai.md) | 4 | 0 |
| [05-authn](05-authn.md) | 4 | 0 |
| [05-backup](05-backup.md) | 3 | 0 |
| [05-config](05-config.md) | 7 | 0 |
| [05-download](05-download.md) | 3 | 0 |
| [05-events](05-events.md) | 2 | 3 |
| [05-health](05-health.md) | 3 | 2 |
| [05-http-api](05-http-api.md) | 4 | 1 |
| [05-indexer](05-indexer.md) | 3 | 0 |
| [05-library](05-library.md) | 4 | 0 |
| [05-logging](05-logging.md) | 2 | 0 |
| [05-metadata](05-metadata.md) | 6 | 0 |
| [05-metrics](05-metrics.md) | 3 | 0 |
| [05-notify](05-notify.md) | 2 | 0 |
| [05-rbac](05-rbac.md) | 6 | 1 |
| [05-realtime](05-realtime.md) | 2 | 0 |
| [05-storage](05-storage.md) | 6 | 0 |
| [05-tasks](05-tasks.md) | 8 | 0 |
| [05-updater](05-updater.md) | 6 | 1 |
| [06-app-sdk](06-app-sdk.md) | 6 | 1 |
| [07-ui-shell](07-ui-shell.md) | 4 | 0 |
| [08-arr-facade](08-arr-facade.md) | 8 | 0 |
| [09-abo-on-core](09-abo-on-core.md) | 6 | 4 |
| [10-tv-movies-proof](10-tv-movies-proof.md) | 5 | 0 |
| [12-repo-split](12-repo-split.md) | 3 | 0 |

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

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-G request (DATA-30) — K17 added: store merge needs a core storage export/import contract (deferred, lowest priority).
- integration pass: Post-revision state — K1 package count (160 remain); K12 and hazards: `Adopts` check, `RequiresServed` defers instead of failing boot; K13 and hazards: mirror-disabled keys stay active natively while `Fresh`; shadow-mode claim violations persisted.
- integration pass: §14.5 re-counted per file (118 open items in 26 files) with the method stated there.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-1 (SEC2-1, SEC2-2, DATA2-3, DATA2-5) — K2, K13 point at the Teach-build rollback floor; K19 added (its cost); hazards row.
- R2-2 (SEC2-3, DATA2-4) — K2: every revocation, also after a restore, reaches the legacy row in the same batch.
- R2-4 (DATA2-2, OPS2-11) — K18 added (minimum-upgrade-from release, `min_upgrade_from`); K12 bounded by it; hazards row.
- R2-6 (OPS2-1) — Q-I added (required checks via branch protection vs RC-promotion gating; recommendation: required checks on gated paths plus promotion gating); K20 added; hazards row.
- MOD2-5 — K8: redesigned exclude check (temp copy; present, green with it, edge plus `ambiguous import` without it).
- MOD2-6 — K14: tidy coupling, one-PR Dependabot, `--pin`; hazards row.
- MOD2-7 — hazards row: core layers mapped onto ABO's scale.
- MOD2-8, MOD2-21 — Q-G: SC1/SC5, readiness report not merge gate, "before S0"; K11 "first core stage".
- FE2-19 — K4: 122 files (52 source, 70 test).
- §14.5 re-counted with the round-1 method (117 open items in 26 files).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): §14.5 re-counted after the integration pass: 115 open items in 26 files.
